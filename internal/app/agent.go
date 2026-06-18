package app

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"runtime"
	"sync"
	"time"

	"clipboard-sync/cache"
	"clipboard-sync/chunk"
	"clipboard-sync/clipboard"
	"clipboard-sync/internal/config"
	"clipboard-sync/internal/presenter"
	"clipboard-sync/mq"
	"clipboard-sync/protocol"
	"clipboard-sync/transfer"
)

type Agent struct {
	deviceID      string
	config        config.Config
	clipboard     clipboard.Clipboard
	mq            *mq.Client
	store         *cache.Store
	tokenManager  *transfer.TokenManager
	imageTokens   *transfer.ImageTokenManager
	sender        *transfer.Sender
	imageSender   *transfer.ImageSender
	receiver      *chunk.Receiver
	imageReceiver *chunk.ImageReceiver
	presenter     presenter.Presenter
	logger        *slog.Logger
	suppressUntil time.Time
	mu            sync.Mutex
	lastSeen      string
	requested     map[string]struct{}
}

func NewAgent(deviceID string, cfg config.Config, clip clipboard.Clipboard, mqClient *mq.Client, store *cache.Store, logger *slog.Logger) *Agent {
	agent := &Agent{
		deviceID:     deviceID,
		config:       cfg,
		clipboard:    clip,
		mq:           mqClient,
		store:        store,
		tokenManager: transfer.NewTokenManager(cfg.TokenTTLDuration()),
		imageTokens:  transfer.NewImageTokenManager(cfg.TokenTTLDuration()),
		logger:       logger,
		requested:    map[string]struct{}{},
	}
	agent.sender = transfer.NewSender(cfg.GroupID, cfg.ChunkSize, mqClient, logger, deviceID)
	agent.imageSender = transfer.NewImageSender(cfg.GroupID, cfg.ChunkSize, mqClient, deviceID)
	agent.receiver = chunk.NewReceiver(cfg.DownloadDir, agent)
	agent.imageReceiver = chunk.NewImageReceiver(cfg.DownloadDir, agent)
	var presenterErr error
	agent.presenter, presenterErr = presenter.New(cfg.MountDir, agent)
	if presenterErr != nil {
		logger.Warn("presenter disabled", "error", presenterErr)
	}
	return agent
}

func (a *Agent) Close() {
	a.tokenManager.Close()
	a.imageTokens.Close()
	a.receiver.Close()
	a.imageReceiver.Close()
	if a.presenter != nil {
		_ = a.presenter.Close()
	}
	a.mq.Close()
}

func (a *Agent) Start(ctx context.Context) error {
	if err := a.subscribe(); err != nil {
		return err
	}
	watchCh := make(chan clipboard.Data, 8)
	a.clipboard.Watch(watchCh)
	a.logger.Info("agent started", "device_id", a.deviceID)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case data := <-watchCh:
			if err := a.handleLocalClipboard(ctx, data); err != nil {
				a.logger.Error("handle local clipboard failed", "error", err)
			}
		}
	}
}

func (a *Agent) subscribe() error {
	if err := a.mq.Subscribe(protocol.TopicClipboardUpdate, a.onClipboardUpdate); err != nil {
		return err
	}
	if err := a.mq.Subscribe(protocol.TopicClipboardRequest, a.onClipboardRequest); err != nil {
		return err
	}
	if err := a.mq.Subscribe(protocol.TopicFileChunk, a.onFileChunk); err != nil {
		return err
	}
	if err := a.mq.Subscribe(protocol.TopicFileComplete, a.onFileComplete); err != nil {
		return err
	}
	if err := a.mq.Subscribe(protocol.TopicImageChunk, a.onImageChunk); err != nil {
		return err
	}
	if err := a.mq.Subscribe(protocol.TopicImageComplete, a.onImageComplete); err != nil {
		return err
	}
	return nil
}

func (a *Agent) handleLocalClipboard(ctx context.Context, data clipboard.Data) error {
	if data.Empty() {
		return nil
	}

	a.mu.Lock()
	if time.Now().Before(a.suppressUntil) {
		a.mu.Unlock()
		return nil
	}
	if data.Fingerprint != "" && data.Fingerprint == a.lastSeen {
		a.mu.Unlock()
		return nil
	}
	a.lastSeen = data.Fingerprint
	a.mu.Unlock()

	switch data.Type {
	case clipboard.DataTypeText:
		return a.publishText(data.Text)
	case clipboard.DataTypeFiles:
		if a.presenter != nil && a.presenter.IsManagedPaths(data.Files) {
			return nil
		}
		return a.publishFiles(data.Files)
	case clipboard.DataTypeImage:
		a.logger.Info("local image clipboard detected", "mime", data.ImageMIME, "size", len(data.Image))
		return a.publishImage(data.Image, data.ImageMIME)
	case clipboard.DataTypeRemote:
		return nil
	default:
		return fmt.Errorf("unsupported clipboard type: %s", data.Type)
	}
}

func (a *Agent) publishText(text string) error {
	message := protocol.NewTextUpdate(a.config.GroupID, a.deviceID, text)
	if err := a.mq.Publish(protocol.TopicClipboardUpdate, message); err != nil {
		return err
	}
	if err := a.flushMQ(5 * time.Second); err != nil {
		return err
	}
	a.logger.Info("clipboard text update published")
	return nil
}

func (a *Agent) publishFiles(paths []string) error {
	files, err := transfer.BuildFileMetadata(paths)
	if err != nil {
		return err
	}
	token, err := a.tokenManager.Issue(files)
	if err != nil {
		return err
	}
	message := protocol.NewFileUpdate(a.config.GroupID, a.deviceID, token, files)
	if err := a.mq.Publish(protocol.TopicClipboardUpdate, message); err != nil {
		return err
	}
	if err := a.flushMQ(5 * time.Second); err != nil {
		return err
	}
	a.logger.Info("clipboard file metadata published", "token", token, "files", len(files))
	return nil
}

func (a *Agent) publishImage(image []byte, mime string) error {
	meta, path, err := transfer.StoreImageTemp(a.config.CacheDir, image, mime)
	if err != nil {
		return err
	}
	token, err := a.imageTokens.Issue(meta, path)
	if err != nil {
		return err
	}
	message := protocol.NewImageUpdate(a.config.GroupID, a.deviceID, token, meta)
	if err := a.mq.Publish(protocol.TopicClipboardUpdate, message); err != nil {
		return err
	}
	if err := a.flushMQ(5 * time.Second); err != nil {
		return err
	}
	a.logger.Info("clipboard image update published", "token", token, "mime", meta.MIME, "size", meta.Size)
	return nil
}

func (a *Agent) onClipboardUpdate(_ string, payload []byte) error {
	message, err := protocol.Decode[protocol.ClipboardUpdateMessage](payload)
	if err != nil {
		return err
	}
	if err := protocol.ValidateClipboardUpdate(message); err != nil {
		return err
	}
	if message.GroupID != a.config.GroupID {
		return nil
	}
	if message.DeviceID == a.deviceID {
		return nil
	}
	switch message.Type {
	case protocol.TypeText:
		return a.applyRemoteText(message.Text)
	case protocol.TypeFile:
		return a.applyRemoteFiles(message)
	case protocol.TypeImage:
		return a.applyRemoteImage(message)
	default:
		return fmt.Errorf("unknown clipboard update type: %s", message.Type)
	}
}

func (a *Agent) applyRemoteText(text string) error {
	data := clipboard.Data{Type: clipboard.DataTypeText, Text: text}
	data.Fingerprint = clipboard.Fingerprint(data)
	a.setSuppressFingerprint(data.Fingerprint)
	if err := a.clipboard.Write(data); err != nil {
		return err
	}
	a.logger.Info("remote text applied to clipboard")
	return nil
}

func (a *Agent) applyRemoteFiles(message protocol.ClipboardUpdateMessage) error {
	state := cache.RemoteClipboardState{
		Token:        message.Token,
		GroupID:      message.GroupID,
		SourceDevice: message.DeviceID,
		Files:        message.Files,
		CreatedAt:    message.CreatedAt,
		Status:       "pending",
	}
	if err := a.store.SaveRemoteClipboard(state); err != nil {
		return err
	}
	if err := a.store.SaveTransferState(state); err != nil {
		return err
	}
	presentation, err := a.presentRemoteFiles(state)
	if err != nil {
		return err
	}
	if presentation.Handled {
		a.logger.Info("remote file metadata presented by platform", "token", message.Token, "source_device", message.DeviceID, "files", len(message.Files))
		return nil
	}
	data := clipboard.Data{Type: clipboard.DataTypeFiles, Files: presentation.Paths}
	data.Fingerprint = clipboard.Fingerprint(data)
	a.setSuppressFingerprint(data.Fingerprint)
	if err := a.clipboard.Write(data); err != nil {
		return err
	}
	a.logger.Info("remote file metadata cached", "token", message.Token, "source_device", message.DeviceID, "files", len(message.Files))
	return nil
}

func (a *Agent) applyRemoteImage(message protocol.ClipboardUpdateMessage) error {
	if message.Image == nil {
		return fmt.Errorf("remote image metadata is missing")
	}
	a.logger.Info("remote image metadata received", "token", message.Token, "source_device", message.DeviceID, "mime", message.Image.MIME)
	return a.requestImageTransfer(message.Token, message.DeviceID)
}

func (a *Agent) onClipboardRequest(_ string, payload []byte) error {
	message, err := protocol.Decode[protocol.ClipboardRequestMessage](payload)
	if err != nil {
		return err
	}
	if err := protocol.ValidateClipboardRequest(message); err != nil {
		return err
	}
	if message.GroupID != a.config.GroupID {
		return nil
	}
	if message.TargetDevice != a.deviceID {
		return nil
	}
	if message.Type == protocol.TypeImage {
		meta, path, ok := a.imageTokens.Lookup(message.Token)
		if !ok {
			return fmt.Errorf("image token expired or unknown: %s", message.Token)
		}
		a.logger.Info("image transfer request received", "token", message.Token, "requester", message.RequesterID)
		if err := a.imageSender.Send(message.Token, meta, path, message.RequesterID); err != nil {
			return err
		}
		return a.flushMQ(10 * time.Second)
	}
	files, ok := a.tokenManager.Lookup(message.Token)
	if ok {
		a.logger.Info("file transfer request received", "token", message.Token, "requester", message.RequesterID)
		return a.sender.SendFiles(context.Background(), message.Token, files, message.RequesterID)
	}
	return fmt.Errorf("token expired or unknown: %s", message.Token)
}

func (a *Agent) onFileChunk(_ string, payload []byte) error {
	message, err := protocol.Decode[protocol.FileChunkMessage](payload)
	if err != nil {
		return err
	}
	if err := protocol.ValidateFileChunk(message); err != nil {
		return err
	}
	if message.GroupID != a.config.GroupID {
		return nil
	}
	if message.TargetDevice != a.deviceID || message.SourceDevice == a.deviceID {
		return nil
	}
	if message.Seq == 1 {
		a.logger.Info("remote file chunk received", "token", message.Token, "file", message.FileName, "source_device", message.SourceDevice, "total", message.Total)
	}
	if err := a.store.MarkReceiving(message.Token); err != nil {
		return err
	}
	return a.receiver.HandleChunk(message)
}

func (a *Agent) onFileComplete(_ string, payload []byte) error {
	message, err := protocol.Decode[protocol.FileCompleteMessage](payload)
	if err != nil {
		return err
	}
	if err := protocol.ValidateFileComplete(message); err != nil {
		return err
	}
	if message.GroupID != a.config.GroupID {
		return nil
	}
	if message.TargetDevice != a.deviceID || message.SourceDevice == a.deviceID {
		return nil
	}
	a.logger.Info("remote file complete received", "token", message.Token, "file", message.FileName, "source_device", message.SourceDevice, "total_chunks", message.TotalChunks, "size", message.Size)
	return a.receiver.HandleComplete(message)
}

func (a *Agent) onImageChunk(_ string, payload []byte) error {
	message, err := protocol.Decode[protocol.ImageChunkMessage](payload)
	if err != nil {
		return err
	}
	if err := protocol.ValidateImageChunk(message); err != nil {
		return err
	}
	if message.GroupID != a.config.GroupID {
		return nil
	}
	if message.SourceDevice == a.deviceID {
		return nil
	}
	if message.TargetDevice != a.deviceID {
		return nil
	}
	if message.Seq == 1 {
		a.logger.Info("remote image chunk received", "token", message.Token, "source_device", message.SourceDevice, "total", message.Total)
	}
	return a.imageReceiver.HandleChunk(message)
}

func (a *Agent) onImageComplete(_ string, payload []byte) error {
	message, err := protocol.Decode[protocol.ImageCompleteMessage](payload)
	if err != nil {
		return err
	}
	if err := protocol.ValidateImageComplete(message); err != nil {
		return err
	}
	if message.GroupID != a.config.GroupID {
		return nil
	}
	if message.SourceDevice == a.deviceID {
		return nil
	}
	if message.TargetDevice != a.deviceID {
		return nil
	}
	a.logger.Info("remote image complete received", "token", message.Token, "source_device", message.SourceDevice, "mime", message.Image.MIME, "size", message.Image.Size, "total_chunks", message.TotalChunks)
	return a.imageReceiver.HandleComplete(message)
}

func (a *Agent) RequestRemotePaste(ctx context.Context, timeout time.Duration) error {
	_ = ctx
	current, err := a.clipboard.Read()
	if err != nil {
		return err
	}
	if current.Type != clipboard.DataTypeRemote || current.RemoteMarker == nil {
		return fmt.Errorf("clipboard does not contain remote files")
	}
	marker := current.RemoteMarker
	if marker.GroupID != "" && marker.GroupID != a.config.GroupID {
		return fmt.Errorf("remote clipboard group mismatch: %s", marker.GroupID)
	}
	state := cache.RemoteClipboardState{
		Token:        marker.Token,
		GroupID:      marker.GroupID,
		SourceDevice: marker.SourceDevice,
		Files:        marker.Files,
		CreatedAt:    marker.CreatedAt,
		Status:       "pending",
	}
	if err := a.store.SaveTransferState(state); err != nil {
		return err
	}
	request := protocol.NewRequest(a.config.GroupID, marker.Token, protocol.TypeFile, marker.SourceDevice, a.deviceID)
	if err := a.mq.Publish(protocol.TopicClipboardRequest, request); err != nil {
		return err
	}
	if err := a.mq.Flush(ctx); err != nil {
		return err
	}
	if err := a.store.MarkRequested(marker.Token); err != nil {
		return err
	}
	a.logger.Info("remote paste requested", "token", marker.Token, "source_device", marker.SourceDevice)
	result, err := a.store.WaitForCompletion(marker.Token, timeout)
	if err != nil {
		return err
	}
	var localPaths []string
	for _, file := range result.Files {
		localPath, ok := result.LocalPaths[file.ID]
		if !ok {
			return fmt.Errorf("local file missing for %s", file.Name)
		}
		localPaths = append(localPaths, localPath)
	}
	data := clipboard.Data{Type: clipboard.DataTypeFiles, Files: localPaths}
	data.Fingerprint = clipboard.Fingerprint(data)
	a.setSuppressFingerprint(data.Fingerprint)
	if err := a.clipboard.Write(data); err != nil {
		return err
	}
	a.logger.Info("remote files downloaded and written to clipboard", "token", marker.Token)
	return nil
}

func (a *Agent) EnsureRemoteTransfer(state cache.RemoteClipboardState) error {
	a.mu.Lock()
	if _, ok := a.requested[state.Token]; ok {
		a.mu.Unlock()
		return nil
	}
	a.requested[state.Token] = struct{}{}
	a.mu.Unlock()
	if err := a.receiver.PrepareFiles(state.Token, state.Files); err != nil {
		return err
	}
	if err := a.store.SaveTransferState(state); err != nil {
		return err
	}
	request := protocol.NewRequest(a.config.GroupID, state.Token, protocol.TypeFile, state.SourceDevice, a.deviceID)
	if err := a.mq.Publish(protocol.TopicClipboardRequest, request); err != nil {
		return err
	}
	flushCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := a.mq.Flush(flushCtx); err != nil {
		return err
	}
	if err := a.store.MarkRequested(state.Token); err != nil {
		return err
	}
	a.logger.Info("remote transfer requested", "token", state.Token, "source_device", state.SourceDevice)
	return nil
}

func (a *Agent) ReadRemoteFile(state cache.RemoteClipboardState, file protocol.FileMeta, offset int64, size int) ([]byte, error) {
	if err := a.EnsureRemoteTransfer(state); err != nil {
		return nil, err
	}
	chunk, err := a.receiver.ReadAt(state.Token, file, offset, size)
	if err != nil {
		return nil, err
	}
	return chunk, nil
}

func (a *Agent) requestImageTransfer(token string, sourceDevice string) error {
	a.mu.Lock()
	if _, ok := a.requested[token]; ok {
		a.mu.Unlock()
		return nil
	}
	a.requested[token] = struct{}{}
	a.mu.Unlock()
	request := protocol.NewRequest(a.config.GroupID, token, protocol.TypeImage, sourceDevice, a.deviceID)
	if err := a.mq.Publish(protocol.TopicClipboardRequest, request); err != nil {
		return err
	}
	if err := a.flushMQ(5 * time.Second); err != nil {
		return err
	}
	a.logger.Info("remote image transfer requested", "token", token, "source_device", sourceDevice)
	return nil
}

func (a *Agent) OnFileCompleted(file chunk.CompletedFile) error {
	a.logger.Info("file transfer completed", "token", file.Token, "file_id", file.FileID, "path", file.LocalPath)
	return a.store.MarkFileCompleted(file.Token, file.FileID, file.LocalPath)
}

func (a *Agent) OnTransferError(token string, err error) error {
	a.logger.Error("file transfer failed", "token", token, "error", err)
	return a.store.MarkFailed(token, err)
}

func (a *Agent) OnImageCompleted(image chunk.CompletedImage) error {
	payload, err := os.ReadFile(image.Path)
	if err != nil {
		return fmt.Errorf("read completed image: %w", err)
	}
	data := clipboard.Data{Type: clipboard.DataTypeImage, Image: payload, ImageMIME: image.Meta.MIME}
	data.Fingerprint = clipboard.Fingerprint(data)
	a.setSuppressFingerprintFor(data.Fingerprint, imageSuppressDuration(len(payload)))
	a.logger.Info("writing remote image to clipboard", "token", image.Token, "mime", image.Meta.MIME, "size", len(payload))
	if err := a.clipboard.Write(data); err != nil {
		a.logger.Error("write remote image to clipboard failed", "token", image.Token, "error", err)
		return err
	}
	a.logger.Info("image transfer completed", "token", image.Token, "path", image.Path, "mime", image.Meta.MIME)
	return nil
}

func (a *Agent) OnImageTransferError(token string, err error) error {
	a.logger.Error("image transfer failed", "token", token, "error", err)
	return nil
}

func (a *Agent) setSuppressWindow() {
	a.setSuppressFingerprint("")
	if data, err := a.clipboard.Read(); err == nil {
		a.mu.Lock()
		a.lastSeen = data.Fingerprint
		a.mu.Unlock()
	}
}

func (a *Agent) setSuppressFingerprint(fingerprint string) {
	a.setSuppressFingerprintFor(fingerprint, 1500*time.Millisecond)
}

func (a *Agent) setSuppressFingerprintFor(fingerprint string, duration time.Duration) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.suppressUntil = time.Now().Add(duration)
	if fingerprint != "" {
		a.lastSeen = fingerprint
	}
}

func (a *Agent) flushMQ(timeout time.Duration) error {
	flushCtx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return a.mq.Flush(flushCtx)
}

func imageSuppressDuration(size int) time.Duration {
	if size > 1024*1024 {
		return 10 * time.Second
	}
	return 1500 * time.Millisecond
}

func (a *Agent) presentRemoteFiles(state cache.RemoteClipboardState) (presenter.Presentation, error) {
	if a.presenter == nil {
		if runtime.GOOS == "linux" {
			return presenter.Presentation{}, fmt.Errorf("linux remote file presenter is unavailable; restart agent and check fuse mount %s", a.config.MountDir)
		}
		marker := protocol.RemoteClipboardMarker{
			Token:        state.Token,
			GroupID:      a.config.GroupID,
			SourceDevice: state.SourceDevice,
			Files:        state.Files,
			CreatedAt:    state.CreatedAt,
		}
		encoded, err := protocol.EncodeRemoteMarker(marker)
		if err != nil {
			return presenter.Presentation{}, err
		}
		return presenter.Presentation{Paths: []string{encoded}}, nil
	}
	presentation, err := a.presenter.PresentRemoteFiles(state)
	if err != nil {
		return presenter.Presentation{}, err
	}
	if presentation.Handled || len(presentation.Paths) != 0 {
		return presentation, nil
	}
	marker := protocol.RemoteClipboardMarker{
		Token:        state.Token,
		GroupID:      a.config.GroupID,
		SourceDevice: state.SourceDevice,
		Files:        state.Files,
		CreatedAt:    state.CreatedAt,
	}
	encoded, err := protocol.EncodeRemoteMarker(marker)
	if err != nil {
		return presenter.Presentation{}, err
	}
	return presenter.Presentation{Paths: []string{encoded}}, nil
}
