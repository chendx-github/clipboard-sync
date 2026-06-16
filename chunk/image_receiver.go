package chunk

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sync"

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
}

type imageSession struct {
	meta             protocol.ImageMeta
	path             string
	writer           *os.File
	nextSeq          int
	total            int
	pending          map[int][]byte
	size             int64
	completed        bool
	completeReceived bool
}

func NewImageReceiver(root string, callback ImageCallback) *ImageReceiver {
	return &ImageReceiver{root: root, callback: callback, sessions: map[string]*imageSession{}}
}

func (r *ImageReceiver) HandleChunk(msg protocol.ImageChunkMessage) error {
	decoded, err := base64.StdEncoding.DecodeString(msg.Data)
	if err != nil {
		return r.fail(msg.Token, fmt.Errorf("decode image chunk: %w", err))
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
	if _, exists := session.pending[msg.Seq]; exists {
		return nil
	}
	if session.total == 0 {
		session.total = msg.Total
	}
	if msg.Seq == session.nextSeq {
		if err := r.writeLocked(session, decoded); err != nil {
			return r.fail(msg.Token, err)
		}
		session.nextSeq++
		for {
			buffered, ok := session.pending[session.nextSeq]
			if !ok {
				break
			}
			delete(session.pending, session.nextSeq)
			if err := r.writeLocked(session, buffered); err != nil {
				return r.fail(msg.Token, err)
			}
			session.nextSeq++
		}
		if session.completeReceived {
			return r.finishLocked(msg.Token, session)
		}
		return nil
	}
	session.pending[msg.Seq] = decoded
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
		return r.fail(token, fmt.Errorf("close image file: %w", err))
	}
	payload, err := os.ReadFile(session.path)
	if err != nil {
		return r.fail(token, fmt.Errorf("read image file: %w", err))
	}
	sum := sha256.Sum256(payload)
	if hex.EncodeToString(sum[:]) != session.meta.SHA256 {
		return r.fail(token, fmt.Errorf("image sha mismatch"))
	}
	if int64(len(payload)) != session.meta.Size {
		return r.fail(token, fmt.Errorf("image size mismatch"))
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
	session := &imageSession{path: path, writer: writer, nextSeq: 1, pending: map[int][]byte{}}
	r.sessions[token] = session
	return session, nil
}

func (r *ImageReceiver) writeLocked(session *imageSession, data []byte) error {
	if _, err := session.writer.Write(data); err != nil {
		return fmt.Errorf("write image chunk: %w", err)
	}
	session.size += int64(len(data))
	return nil
}

func (r *ImageReceiver) fail(token string, err error) error {
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
