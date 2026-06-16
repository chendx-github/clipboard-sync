package transfer

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	"clipboard-sync/protocol"
)

type TokenManager struct {
	ttl    time.Duration
	mu     sync.RWMutex
	items  map[string]tokenEntry
	stopCh chan struct{}
}

type tokenEntry struct {
	Files     []protocol.FileMeta
	CreatedAt time.Time
	ExpiresAt time.Time
}

func NewTokenManager(ttl time.Duration) *TokenManager {
	m := &TokenManager{
		ttl:    ttl,
		items:  make(map[string]tokenEntry),
		stopCh: make(chan struct{}),
	}
	go m.gcLoop()
	return m
}

func (m *TokenManager) Close() {
	close(m.stopCh)
}

func (m *TokenManager) Issue(files []protocol.FileMeta) (string, error) {
	token, err := newToken()
	if err != nil {
		return "", err
	}
	now := time.Now()
	m.mu.Lock()
	m.items[token] = tokenEntry{Files: files, CreatedAt: now, ExpiresAt: now.Add(m.ttl)}
	m.mu.Unlock()
	return token, nil
}

func newToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}
	return hex.EncodeToString(b), nil
}

func (m *TokenManager) Lookup(token string) ([]protocol.FileMeta, bool) {
	m.mu.RLock()
	entry, ok := m.items[token]
	m.mu.RUnlock()
	if !ok || time.Now().After(entry.ExpiresAt) {
		return nil, false
	}
	return entry.Files, true
}

func (m *TokenManager) gcLoop() {
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
				}
			}
			m.mu.Unlock()
		case <-m.stopCh:
			return
		}
	}
}
