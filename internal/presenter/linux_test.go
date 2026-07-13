//go:build linux

package presenter

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureMountRootDirPreservesExistingContents(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "keep.txt")
	if err := os.WriteFile(path, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := ensureMountRootDir(root); err != nil {
		t.Fatalf("ensure mount root: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("existing mount-root content was removed: %v", err)
	}
}
