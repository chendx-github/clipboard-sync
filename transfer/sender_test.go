package transfer

import (
	"context"
	"strings"
	"testing"

	"clipboard-sync/protocol"
)

func TestSendSingleFileRejectsDirectory(t *testing.T) {
	sender := &Sender{}
	err := sender.sendSingleFile(context.Background(), "token", protocol.FileMeta{Path: t.TempDir()}, "target")
	if err == nil || !strings.Contains(err.Error(), "directory metadata") {
		t.Fatalf("expected directory metadata error, got %v", err)
	}
}
