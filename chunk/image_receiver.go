package chunk

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"clipboard-sync/protocol"
)

type CompletedImage struct {
	Token string
	Path  string
	Meta  protocol.ImageMeta
}

type ImageCallback interface {
	OnImageCompleted(image CompletedImage) error
	OnImageTransferError(token string, err error) error
}

type ImageReceiver struct {
	root     string
	callback ImageCallback
	mu       sync.Mutex
	sessions map[string]*imageSession
	stopCh   chan struct{}
}

type imageSession struct {
	meta             protocol.ImageMeta
	path             string
	writer           *os.File
	nextSeq          int
	total            int
	pending          map[int][]byte
	pendingBytes     int64
	size             int64
	lastSeen         time.Time
	completed        bool
	completeReceived bool
}

func NewImageReceiver(root string, callback ImageCallback) *ImageReceiver {
	receiver := &ImageReceiver{root: root, callback: callback, sessions: map[string]*imageSession{}, stopCh: make(chan struct{})}
	go receiver.gcLoop()
	return receiver
}

func (r *ImageReceiver) Close() {
	close(r.stopCh)
}

func (r *ImageReceiver) HandleChunk(msg protocol.ImageChunkMessage) error {
	decoded, err := base64.StdEncoding.DecodeString(msg.Data)
	if err != nil {
		return r.fail(msg.Token, fmt.Errorf("decode image chunk: %w", err))
	}
	if len(decoded) != msg.Size {
		return r.fail(msg.Token, fmt.Errorf("image chunk size mismatch token=%s seq=%d want=%d got=%d", msg.Token, msg.Seq, msg.Size, len(decoded)))
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	session, err := r.ensureSessionLocked(msg.Token)
	if err != nil {
		return err
	}
	if msg.Seq < session.nextSeq || session.completed {
		return nil
	}
	session.lastSeen = time.Now()
	if _, exists := session.pending[msg.Seq]; exists {
		return nil
	}
	if session.total == 0 {
		session.total = msg.Total
	}
	if msg.Seq == session.nextSeq {
		if err := r.writeLocked(session, decoded); err != nil {
			return r.failLocked(msg.Token, err)
		}
		session.nextSeq++
		for {
			buffered, ok := session.pending[session.nextSeq]
			if !ok {
				break
			}
			delete(session.pending, session.nextSeq)
			session.pendingBytes -= int64(len(buffered))
			if err := r.writeLocked(session, buffered); err != nil {
				return r.failLocked(msg.Token, err)
			}
			session.nextSeq++
		}
		if session.completeReceived {
			return r.finishLocked(msg.Token, session)
		}
		return nil
	}
	if len(session.pending) >= maxPendingChunks || session.pendingBytes+int64(len(decoded)) > maxPendingChunkBytes {
		return r.failLocked(msg.Token, fmt.Errorf("too many pending image chunks token=%s", msg.Token))
	}
	session.pending[msg.Seq] = decoded
	session.pendingBytes += int64(len(decoded))
	return nil
}

func (r *ImageReceiver) HandleComplete(msg protocol.ImageCompleteMessage) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	session, err := r.ensureSessionLocked(msg.Token)
	if err != nil {
		return err
	}
	session.meta = msg.Image
	session.lastSeen = time.Now()
	session.completeReceived = true
	if session.total == 0 {
		session.total = msg.TotalChunks
	}
	if session.nextSeq-1 != session.total || len(session.pending) != 0 {
		return nil
	}
	return r.finishLocked(msg.Token, session)
}

func (r *ImageReceiver) finishLocked(token string, session *imageSession) error {
	if err := session.writer.Close(); err != nil {
		return r.failLocked(token, fmt.Errorf("close image file: %w", err))
	}
	payload, err := os.ReadFile(session.path)
	if err != nil {
		return r.failLocked(token, fmt.Errorf("read image file: %w", err))
	}
	sum := sha256.Sum256(payload)
	if hex.EncodeToString(sum[:]) != session.meta.SHA256 {
		return r.failLocked(token, fmt.Errorf("image sha mismatch"))
	}
	if int64(len(payload)) != session.meta.Size {
		return r.failLocked(token, fmt.Errorf("image size mismatch"))
	}
	session.completed = true
	delete(r.sessions, token)
	return r.callback.OnImageCompleted(CompletedImage{Token: token, Path: session.path, Meta: session.meta})
}

func (r *ImageReceiver) ensureSessionLocked(token string) (*imageSession, error) {
	if session, ok := r.sessions[token]; ok {
		return session, nil
	}
	if err := os.MkdirAll(filepath.Join(r.root, "images"), 0o755); err != nil {
		return nil, fmt.Errorf("create image spool dir: %w", err)
	}
	path := filepath.Join(r.root, "images", token+".bin")
	writer, err := os.Create(path)
	if err != nil {
		return nil, fmt.Errorf("create image spool file: %w", err)
	}
	session := &imageSession{path: path, writer: writer, nextSeq: 1, pending: map[int][]byte{}, lastSeen: time.Now()}
	r.sessions[token] = session
	return session, nil
}

func (r *ImageReceiver) gcLoop() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			r.expireStaleSessions()
		case <-r.stopCh:
			return
		}
	}
}

func (r *ImageReceiver) expireStaleSessions() {
	now := time.Now()
	var expired []string
	r.mu.Lock()
	for token, session := range r.sessions {
		if !session.completed && now.Sub(session.lastSeen) > transferSessionTTL {
			expired = append(expired, token)
		}
	}
	r.mu.Unlock()
	for _, token := range expired {
		_ = r.fail(token, fmt.Errorf("image transfer session timed out after %s", transferSessionTTL))
	}
}

func (r *ImageReceiver) writeLocked(session *imageSession, data []byte) error {
	if _, err := session.writer.Write(data); err != nil {
		return fmt.Errorf("write image chunk: %w", err)
	}
	session.size += int64(len(data))
	return nil
}

func (r *ImageReceiver) fail(token string, err error) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.failLocked(token, err)
}

func (r *ImageReceiver) failLocked(token string, err error) error {
	if session, ok := r.sessions[token]; ok {
		if session.writer != nil {
			_ = session.writer.Close()
		}
		_ = os.Remove(session.path)
		delete(r.sessions, token)
	}
	if callbackErr := r.callback.OnImageTransferError(token, err); callbackErr != nil {
		return fmt.Errorf("%v; callback error: %w", err, callbackErr)
	}
	return err
}
