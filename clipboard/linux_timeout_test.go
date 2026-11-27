//go:build linux

package clipboard

import (
	"strings"
	"testing"
	"time"
)

// A wedged clipboard owner used to block the watcher forever; every helper call
// must be killed once its timeout expires.
func TestRunClipboardCommandTimesOut(t *testing.T) {
	start := time.Now()
	_, err := runClipboardCommand(150*time.Millisecond, "sleep", "30")
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("expected timeout error, got %v", err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("timeout was not enforced, took %s", elapsed)
	}
}

func TestRunClipboardWriteTimesOut(t *testing.T) {
	start := time.Now()
	err := runClipboardWrite(150*time.Millisecond, "sleep", []string{"30"}, strings.NewReader(""))
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("expected timeout error, got %v", err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("timeout was not enforced, took %s", elapsed)
	}
}
