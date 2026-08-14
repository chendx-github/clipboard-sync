//go:build linux

package clipboard

import "testing"

func TestShouldUseWayland(t *testing.T) {
	tests := []struct {
		name           string
		sessionType    string
		waylandDisplay string
		want           bool
	}{
		{name: "x11 session", sessionType: "x11", want: false},
		{name: "wayland session", sessionType: "wayland", want: true},
		{name: "wayland display", waylandDisplay: "wayland-0", want: true},
		{name: "no desktop session hint", want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := shouldUseWayland(test.sessionType, test.waylandDisplay); got != test.want {
				t.Fatalf("shouldUseWayland(%q, %q) = %t, want %t", test.sessionType, test.waylandDisplay, got, test.want)
			}
		})
	}
}

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
