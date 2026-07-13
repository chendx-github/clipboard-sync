package protocol

import "testing"

func TestValidateClipboardUpdateRejectsEmptyFileList(t *testing.T) {
	message := NewFileUpdate("group", "device", "token", nil)
	if err := ValidateClipboardUpdate(message); err == nil {
		t.Fatal("expected empty file list to be rejected")
	}
}

func TestValidateClipboardUpdateAcceptsFileList(t *testing.T) {
	message := NewFileUpdate("group", "device", "token", []FileMeta{{Name: "file.txt", Size: 1}})
	if err := ValidateClipboardUpdate(message); err != nil {
		t.Fatalf("expected valid file update: %v", err)
	}
}
