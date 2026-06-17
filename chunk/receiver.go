package chunk

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"clipboard-sync/protocol"
)

type CompletedFile struct {
	Token     string
	FileID    string
	LocalPath string
}

type Callback interface {
	OnFileCompleted(file CompletedFile) error
	OnTransferError(token string, err error) error
}

type Receiver struct {
	root     string
	callback Callback
	mu       sync.Mutex
	sessions map[string]*fileSession
	stopCh   chan struct{}
}

const (
	readWaitTimeout      = 2 * time.Minute
	transferSessionTTL   = 10 * time.Minute
	maxPendingChunks     = 1024
	maxPendingChunkBytes = 1024 * 1024 * 1024
)

type fileSession struct {
	mu           sync.Mutex
	cond         *sync.Cond
	token        string
	file         protocol.FileMeta
	path         string
	writer       *os.File
	hash         hash.Hash
	nextSeq      int
	total        int
	pending      map[int][]byte
	pendingBytes int64
	available    int64
	createdAt    time.Time
	lastSeen     time.Time
	completed    bool
	completeSeen bool
	failed       error
	closedWriter bool
}

func NewReceiver(root string, callback Callback) *Receiver {
	receiver := &Receiver{root: root, callback: callback, sessions: map[string]*fileSession{}, stopCh: make(chan struct{})}
	go receiver.gcLoop()
	return receiver
}

func (r *Receiver) Close() {
	close(r.stopCh)
}

func (r *Receiver) PrepareFiles(token string, files []protocol.FileMeta) error {
	for _, file := range files {
		if err := r.prepareFile(token, file); err != nil {
			return err
		}
	}
	return nil
}

func (r *Receiver) HandleChunk(msg protocol.FileChunkMessage) error {
	decoded, err := base64.StdEncoding.DecodeString(msg.Data)
	if err != nil {
		return r.fail(msg.Token, fmt.Errorf("decode base64 chunk: %w", err))
	}
	if len(decoded) != msg.Size {
		return r.fail(msg.Token, fmt.Errorf("chunk size mismatch token=%s file=%s seq=%d want=%d got=%d", msg.Token, msg.FileName, msg.Seq, msg.Size, len(decoded)))
	}
	session, err := r.sessionForChunk(msg)
	if err != nil {
		return err
	}

	session.mu.Lock()
	defer session.mu.Unlock()
	if session.failed != nil || session.completed {
		return session.failed
	}
	session.lastSeen = time.Now()
	if session.total == 0 {
		session.total = msg.Total
	}
	if msg.Seq < session.nextSeq {
		return nil
	}
	if _, exists := session.pending[msg.Seq]; exists {
		return nil
	}
	if session.total > 0 && msg.Seq > session.total {
		return r.failSession(session, fmt.Errorf("chunk seq %d exceeds total %d", msg.Seq, session.total))
	}
	if msg.Seq == session.nextSeq {
		if err := session.write(decoded); err != nil {
			return r.failSession(session, err)
		}
		session.nextSeq++
		for {
			buffered, ok := session.pending[session.nextSeq]
			if !ok {
				break
			}
			delete(session.pending, session.nextSeq)
			session.pendingBytes -= int64(len(buffered))
			if err := session.write(buffered); err != nil {
				return r.failSession(session, err)
			}
			session.nextSeq++
		}
		session.cond.Broadcast()
		if session.completeSeen && session.nextSeq-1 == session.total && len(session.pending) == 0 {
			return r.finish(session, session.file.SHA256, session.file.Size, session.total)
		}
		return nil
	}
	if len(session.pending) >= maxPendingChunks || session.pendingBytes+int64(len(decoded)) > maxPendingChunkBytes {
		return r.failSession(session, fmt.Errorf("too many pending chunks token=%s file=%s", msg.Token, msg.FileName))
	}
	session.pending[msg.Seq] = decoded
	session.pendingBytes += int64(len(decoded))
	return nil
}

func (r *Receiver) HandleComplete(msg protocol.FileCompleteMessage) error {
	session, ok := r.lookup(msg.Token, msg.FileID)
	if !ok {
		return r.fail(msg.Token, fmt.Errorf("complete received before file preparation token=%s file=%s", msg.Token, msg.FileID))
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.completed {
		return nil
	}
	session.lastSeen = time.Now()
	session.completeSeen = true
	session.file.Name = msg.FileName
	session.file.Size = msg.Size
	session.file.SHA256 = msg.SHA256
	if session.total == 0 {
		session.total = msg.TotalChunks
	}
	if session.nextSeq-1 != session.total || len(session.pending) != 0 {
		session.cond.Broadcast()
		return nil
	}
	return r.finish(session, msg.SHA256, msg.Size, msg.TotalChunks)
}

func (r *Receiver) finish(session *fileSession, sha256Hex string, size int64, totalChunks int) error {
	if !session.closedWriter {
		if err := session.writer.Sync(); err != nil {
			return r.failSession(session, fmt.Errorf("sync file: %w", err))
		}
		if err := session.writer.Close(); err != nil {
			return r.failSession(session, fmt.Errorf("close file: %w", err))
		}
		session.closedWriter = true
	}
	computed := hex.EncodeToString(session.hash.Sum(nil))
	if computed != sha256Hex {
		return r.failSession(session, fmt.Errorf("sha mismatch for %s want=%s got=%s", session.file.Name, sha256Hex, computed))
	}
	if session.available != size {
		return r.failSession(session, fmt.Errorf("size mismatch for %s want=%d got=%d", session.file.Name, size, session.available))
	}
	if session.nextSeq-1 != totalChunks {
		return r.failSession(session, fmt.Errorf("chunk count mismatch for %s want=%d got=%d", session.file.Name, totalChunks, session.nextSeq-1))
	}
	session.completed = true
	session.cond.Broadcast()
	return r.callback.OnFileCompleted(CompletedFile{Token: session.token, FileID: session.file.ID, LocalPath: session.path})
}

func (r *Receiver) ReadAt(token string, file protocol.FileMeta, offset int64, size int) ([]byte, error) {
	session, ok := r.lookup(token, file.ID)
	if !ok {
		if err := r.prepareFile(token, file); err != nil {
			return nil, err
		}
		session, _ = r.lookup(token, file.ID)
	}
	session.mu.Lock()
	deadline := time.Now().Add(readWaitTimeout)
	for session.failed == nil && !session.completed && session.available <= offset {
		if time.Now().After(deadline) {
			session.mu.Unlock()
			return nil, fmt.Errorf("timeout waiting for file data token=%s file=%s offset=%d", token, file.Name, offset)
		}
		session.mu.Unlock()
		time.Sleep(100 * time.Millisecond)
		session.mu.Lock()
	}
	available := session.available
	completed := session.completed
	failed := session.failed
	path := session.path
	fileSize := session.file.Size
	session.mu.Unlock()
	if failed != nil {
		return nil, failed
	}
	if offset >= available {
		if completed {
			if offset < fileSize {
				return nil, fmt.Errorf("file completed before requested offset token=%s file=%s offset=%d available=%d size=%d", token, file.Name, offset, available, fileSize)
			}
			return nil, io.EOF
		}
		return nil, io.EOF
	}
	readSize := int(available - offset)
	if readSize > size {
		readSize = size
	}
	buf := make([]byte, readSize)
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open spool file: %w", err)
	}
	defer f.Close()
	n, err := f.ReadAt(buf, offset)
	if err != nil && err != io.EOF {
		return nil, fmt.Errorf("read spool file: %w", err)
	}
	return buf[:n], nil
}

func (r *Receiver) prepareFile(token string, file protocol.FileMeta) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := sessionKey(token, file.ID)
	if _, ok := r.sessions[key]; ok {
		return nil
	}
	dir := filepath.Join(r.root, token)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create spool dir: %w", err)
	}
	relPath, err := safeRelativePath(file.Name)
	if err != nil {
		return err
	}
	path := filepath.Join(dir, relPath)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create spool parent dir: %w", err)
	}
	writer, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("create spool file: %w", err)
	}
	now := time.Now()
	session := &fileSession{
		token:     token,
		file:      file,
		path:      path,
		writer:    writer,
		hash:      sha256.New(),
		nextSeq:   1,
		pending:   map[int][]byte{},
		createdAt: now,
		lastSeen:  now,
	}
	session.cond = sync.NewCond(&session.mu)
	r.sessions[key] = session
	return nil
}

func (r *Receiver) sessionForChunk(msg protocol.FileChunkMessage) (*fileSession, error) {
	if session, ok := r.lookup(msg.Token, msg.FileID); ok {
		return session, nil
	}
	if err := r.prepareFile(msg.Token, protocol.FileMeta{ID: msg.FileID, Name: msg.FileName}); err != nil {
		return nil, err
	}
	session, _ := r.lookup(msg.Token, msg.FileID)
	return session, nil
}

func (s *fileSession) write(chunk []byte) error {
	if _, err := s.writer.Write(chunk); err != nil {
		return fmt.Errorf("write chunk: %w", err)
	}
	if _, err := s.hash.Write(chunk); err != nil {
		return fmt.Errorf("update hash: %w", err)
	}
	s.available += int64(len(chunk))
	return nil
}

func (r *Receiver) lookup(token string, fileID string) (*fileSession, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	session, ok := r.sessions[sessionKey(token, fileID)]
	return session, ok
}

func (r *Receiver) gcLoop() {
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

func (r *Receiver) expireStaleSessions() {
	now := time.Now()
	expired := map[string]struct{}{}
	r.mu.Lock()
	sessions := make([]*fileSession, 0, len(r.sessions))
	for _, session := range r.sessions {
		sessions = append(sessions, session)
	}
	r.mu.Unlock()
	for _, session := range sessions {
		session.mu.Lock()
		stale := !session.completed && session.failed == nil && now.Sub(session.lastSeen) > transferSessionTTL
		token := session.token
		session.mu.Unlock()
		if stale {
			expired[token] = struct{}{}
		}
	}
	for token := range expired {
		_ = r.fail(token, fmt.Errorf("transfer session timed out after %s", transferSessionTTL))
	}
}

func (r *Receiver) fail(token string, err error) error {
	r.mu.Lock()
	var sessions []*fileSession
	for key, session := range r.sessions {
		if session.token == token {
			sessions = append(sessions, session)
			delete(r.sessions, key)
		}
	}
	r.mu.Unlock()
	for _, session := range sessions {
		session.mu.Lock()
		session.failed = err
		if session.writer != nil && !session.closedWriter {
			_ = session.writer.Close()
			session.closedWriter = true
		}
		session.cond.Broadcast()
		session.mu.Unlock()
	}
	if callbackErr := r.callback.OnTransferError(token, err); callbackErr != nil {
		return fmt.Errorf("%v; callback error: %w", err, callbackErr)
	}
	return err
}

func (r *Receiver) failSession(lockedSession *fileSession, err error) error {
	token := lockedSession.token
	r.mu.Lock()
	var sessions []*fileSession
	for key, session := range r.sessions {
		if session.token == token {
			sessions = append(sessions, session)
			delete(r.sessions, key)
		}
	}
	r.mu.Unlock()
	for _, session := range sessions {
		if session != lockedSession {
			session.mu.Lock()
		}
		session.failed = err
		if session.writer != nil && !session.closedWriter {
			_ = session.writer.Close()
			session.closedWriter = true
		}
		session.cond.Broadcast()
		if session != lockedSession {
			session.mu.Unlock()
		}
	}
	if callbackErr := r.callback.OnTransferError(token, err); callbackErr != nil {
		return fmt.Errorf("%v; callback error: %w", err, callbackErr)
	}
	return err
}

func sessionKey(token string, fileID string) string {
	return token + ":" + fileID
}

func safeRelativePath(name string) (string, error) {
	clean := filepath.Clean(filepath.FromSlash(name))
	if clean == "." || filepath.IsAbs(clean) || clean == ".." {
		return "", fmt.Errorf("invalid relative file name: %s", name)
	}
	if len(clean) > 2 && clean[:2] == ".." && os.IsPathSeparator(clean[2]) {
		return "", fmt.Errorf("invalid relative file name: %s", name)
	}
	return clean, nil
}
