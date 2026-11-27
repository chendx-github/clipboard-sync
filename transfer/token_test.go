package transfer

import (
	"testing"
	"time"

	"clipboard-sync/protocol"
)

func testFiles() []protocol.FileMeta {
	return []protocol.FileMeta{{ID: "f1", Name: "file.bin", Size: 1}}
}

// The token for the current clipboard content must stay usable long after the
// configured ttl so a paste that happens minutes later still works.
func TestLatestTokenStaysValidPastTTL(t *testing.T) {
	manager := NewTokenManager(20 * time.Millisecond)
	defer manager.Close()

	token, err := manager.Issue(testFiles())
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}
	time.Sleep(60 * time.Millisecond)
	if _, ok := manager.Lookup(token); !ok {
		t.Fatal("expected the latest token to stay valid")
	}
}

// Once newer clipboard content is published, the previous token keeps only a
// ttl window and then expires.
func TestPreviousTokenExpiresAfterTTL(t *testing.T) {
	manager := NewTokenManager(30 * time.Millisecond)
	defer manager.Close()

	first, err := manager.Issue(testFiles())
	if err != nil {
		t.Fatalf("issue first token: %v", err)
	}
	second, err := manager.Issue(testFiles())
	if err != nil {
		t.Fatalf("issue second token: %v", err)
	}
	time.Sleep(80 * time.Millisecond)
	if _, ok := manager.Lookup(first); ok {
		t.Fatal("expected the previous token to expire")
	}
	if _, ok := manager.Lookup(second); !ok {
		t.Fatal("expected the latest token to stay valid")
	}
}

// A successful lookup must extend the validity window of a non-latest token.
func TestLookupRefreshesTokenWindow(t *testing.T) {
	manager := NewTokenManager(150 * time.Millisecond)
	defer manager.Close()

	first, err := manager.Issue(testFiles())
	if err != nil {
		t.Fatalf("issue first token: %v", err)
	}
	if _, err := manager.Issue(testFiles()); err != nil {
		t.Fatalf("issue second token: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	if _, ok := manager.Lookup(first); !ok {
		t.Fatal("expected first token to be valid before its ttl")
	}
	time.Sleep(100 * time.Millisecond)
	if _, ok := manager.Lookup(first); !ok {
		t.Fatal("expected lookup to refresh the token window")
	}
}
