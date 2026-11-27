package transfer

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	"clipboard-sync/protocol"
)

// TokenManager hands out tokens for the current local file clipboard content.
//
// Validity model:
//   - the token published for the *latest* clipboard content never expires while
//     it is still the latest one, so pasting minutes later still works;
//   - every other token keeps the configured ttl and is refreshed on each
//     successful lookup;
//   - when new content is published the previous latest token degrades to a
//     normal ttl window and is then garbage collected.
type TokenManager struct {
	ttl    time.Duration
	mu     sync.RWMutex
	items  map[string]tokenEntry
	latest string
	stopCh chan struct{}
}

type tokenEntry struct {
	Files     []protocol.FileMeta
	CreatedAt time.Time
	// ExpiresAt zero means "no expiry while this token is the latest one".
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
	if previous, ok := m.items[m.latest]; ok {
		previous.ExpiresAt = now.Add(m.ttl)
		m.items[m.latest] = previous
	}
	m.items[token] = tokenEntry{Files: files, CreatedAt: now}
	m.latest = token
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

// Lookup returns the files for a token and refreshes its validity window on a
// hit. A token that is still the current clipboard content is always valid.
func (m *TokenManager) Lookup(token string) ([]protocol.FileMeta, bool) {
	now := time.Now()
	m.mu.Lock()
	entry, ok := m.items[token]
	if !ok {
		m.mu.Unlock()
		return nil, false
	}
	if token != m.latest {
		if !entry.ExpiresAt.IsZero() && now.After(entry.ExpiresAt) {
			m.mu.Unlock()
			return nil, false
		}
		entry.ExpiresAt = now.Add(m.ttl)
		m.items[token] = entry
	}
	m.mu.Unlock()
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
				if token == m.latest {
					continue
				}
				if !entry.ExpiresAt.IsZero() && now.After(entry.ExpiresAt) {
					delete(m.items, token)
				}
			}
			m.mu.Unlock()
		case <-m.stopCh:
			return
		}
	}
}
