package transfer

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"clipboard-sync/protocol"
)

type ImageTokenManager struct {
	ttl    time.Duration
	mu     sync.RWMutex
	items  map[string]imageEntry
	stopCh chan struct{}
}

type imageEntry struct {
	Meta      protocol.ImageMeta
	Path      string
	CreatedAt time.Time
	ExpiresAt time.Time
}

func NewImageTokenManager(ttl time.Duration) *ImageTokenManager {
	m := &ImageTokenManager{ttl: ttl, items: map[string]imageEntry{}, stopCh: make(chan struct{})}
	go m.gcLoop()
	return m
}

func (m *ImageTokenManager) Close() {
	close(m.stopCh)
}

func (m *ImageTokenManager) Issue(meta protocol.ImageMeta, path string) (string, error) {
	token, err := newToken()
	if err != nil {
		return "", err
	}
	now := time.Now()
	m.mu.Lock()
	m.items[token] = imageEntry{Meta: meta, Path: path, CreatedAt: now, ExpiresAt: now.Add(m.ttl)}
	m.mu.Unlock()
	return token, nil
}

func (m *ImageTokenManager) Lookup(token string) (protocol.ImageMeta, string, bool) {
	m.mu.RLock()
	entry, ok := m.items[token]
	m.mu.RUnlock()
	if !ok || time.Now().After(entry.ExpiresAt) {
		return protocol.ImageMeta{}, "", false
	}
	return entry.Meta, entry.Path, true
}

func (m *ImageTokenManager) gcLoop() {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			now := time.Now()
			m.mu.Lock()
			for token, entry := range m.items {
				if now.After(entry.ExpiresAt) {
					delete(m.items, token)
					_ = os.Remove(entry.Path)
				}
			}
			m.mu.Unlock()
		case <-m.stopCh:
			return
		}
	}
}

func StoreImageTemp(root string, data []byte, mime string) (protocol.ImageMeta, string, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return protocol.ImageMeta{}, "", fmt.Errorf("create image cache dir: %w", err)
	}
	h := sha256.Sum256(data)
	sha := hex.EncodeToString(h[:])
	path := filepath.Join(root, sha+imageExtension(mime))
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return protocol.ImageMeta{}, "", fmt.Errorf("write image temp file: %w", err)
	}
	meta := protocol.ImageMeta{Name: filepath.Base(path), MIME: mime, Size: int64(len(data)), SHA256: sha}
	return meta, path, nil
}

func imageExtension(mime string) string {
	switch mime {
	case "image/png":
		return ".png"
	case "image/bmp":
		return ".bmp"
	default:
		return ".img"
	}
}

func NewImageChunkMessage(token string, sourceDevice string, targetDevice string, seq int, total int, data []byte) protocol.ImageChunkMessage {
	return protocol.ImageChunkMessage{
		Event:        protocol.TopicImageChunk,
		Token:        token,
		SourceDevice: sourceDevice,
		TargetDevice: targetDevice,
		Seq:          seq,
		Total:        total,
		Data:         base64.StdEncoding.EncodeToString(data),
		Size:         len(data),
		SentAt:       time.Now().Unix(),
	}
}

func NewImageCompleteMessage(token string, sourceDevice string, targetDevice string, image protocol.ImageMeta, totalChunks int) protocol.ImageCompleteMessage {
	return protocol.ImageCompleteMessage{
		Event:        protocol.TopicImageComplete,
		Token:        token,
		SourceDevice: sourceDevice,
		TargetDevice: targetDevice,
		Image:        image,
		TotalChunks:  totalChunks,
		CompletedAt:  time.Now().Unix(),
	}
}

type ImageSender struct {
	chunkSize int
	publisher publisher
	deviceID  string
}

type publisher interface {
	Publish(subject string, message any) error
}

func NewImageSender(chunkSize int, publisher publisher, deviceID string) *ImageSender {
	return &ImageSender{chunkSize: chunkSize, publisher: publisher, deviceID: deviceID}
}

func (s *ImageSender) Send(token string, meta protocol.ImageMeta, path string, targetDevice string) error {
	handle, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open image %s: %w", path, err)
	}
	defer handle.Close()
	buffer := make([]byte, s.chunkSize)
	total := ChunkTotal(meta.Size, s.chunkSize)
	seq := 1
	for {
		n, readErr := handle.Read(buffer)
		if n > 0 {
			msg := NewImageChunkMessage(token, s.deviceID, targetDevice, seq, total, buffer[:n])
			if err := s.publisher.Publish(protocol.TopicImageChunk, msg); err != nil {
				return err
			}
			seq++
		}
		if readErr != nil {
			if readErr == io.EOF {
				break
			}
			return fmt.Errorf("read image %s: %w", path, readErr)
		}
	}
	return s.publisher.Publish(protocol.TopicImageComplete, NewImageCompleteMessage(token, s.deviceID, targetDevice, meta, total))
}

func (s *ImageSender) Broadcast(token string, meta protocol.ImageMeta, path string) error {
	return s.Send(token, meta, path, "")
}
