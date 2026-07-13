//go:build linux

package clipboard

import "testing"

func TestGNOMECopiedFilesPayloadMatchesHistoricalFormat(t *testing.T) {
	mime, payload := fileClipboardPayload("gnome", []string{"/tmp/one.txt", "/tmp/folder"})
	if mime != "x-special/gnome-copied-files" {
		t.Fatalf("MIME = %q", mime)
	}
	want := "copy\nfile:///tmp/one.txt\nfile:///tmp/folder"
	if payload != want {
		t.Fatalf("GNOME payload = %q, want %q", payload, want)
	}

	mime, payload = fileClipboardPayload("uri-list", []string{"/tmp/one.txt", "/tmp/folder"})
	if mime != "text/uri-list" {
		t.Fatalf("MIME = %q", mime)
	}
	want = "file:///tmp/one.txt\nfile:///tmp/folder\n"
	if payload != want {
		t.Fatalf("URI list payload = %q, want %q", payload, want)
	}
}
