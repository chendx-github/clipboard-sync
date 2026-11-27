package chunk

import (
	"errors"
	"testing"
	"time"

	"clipboard-sync/protocol"
)

type nopCallback struct{}

func (nopCallback) OnFileCompleted(CompletedFile) error { return nil }
func (nopCallback) OnTransferError(string, error) error { return nil }

// A failure reported by the source device must surface immediately instead of
// waiting for the read timeout, even when no session was prepared yet.
func TestFailTokenSurfacesWithoutSession(t *testing.T) {
	receiver := NewReceiver(t.TempDir(), nopCallback{})
	defer receiver.Close()

	token := "expired-token"
	remoteErr := errors.New("token is gone")
	if err := receiver.FailToken(token, remoteErr); !errors.Is(err, remoteErr) {
		t.Fatalf("FailToken error = %v, want %v", err, remoteErr)
	}
	file := protocol.FileMeta{ID: "f1", Name: "file.bin", Size: 4}
	if err := receiver.PrepareFiles(token, []protocol.FileMeta{file}); !errors.Is(err, remoteErr) {
		t.Fatalf("PrepareFiles error = %v, want %v", err, remoteErr)
	}
	start := time.Now()
	if _, err := receiver.ReadAt(token, file, 0, 4); !errors.Is(err, remoteErr) {
		t.Fatalf("ReadAt error = %v, want %v", err, remoteErr)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("ReadAt should fail fast, took %s", elapsed)
	}
}
