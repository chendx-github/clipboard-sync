//go:build linux

package clipboard

import (
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

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

func TestTextFileList(t *testing.T) {
	first := t.TempDir() + "/one.txt"
	second := t.TempDir() + "/two.txt"
	if err := os.WriteFile(first, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(second, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	firstURI := pathToFileURI(first)
	secondURI := pathToFileURI(second)
	tests := []struct {
		name string
		text string
		want []string
		ok   bool
	}{
		{name: "gnome copy", text: "copy\n" + firstURI + "\n" + secondURI, want: []string{first, second}, ok: true},
		{name: "uri list", text: firstURI + "\n" + secondURI, want: []string{first, second}, ok: true},
		{name: "ordinary text", text: "file:///tmp/one.txt is an example", ok: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, ok := textFileList(test.text)
			if ok != test.ok {
				t.Fatalf("textFileList() ok = %t, want %t", ok, test.ok)
			}
			if strings.Join(got, "\n") != strings.Join(test.want, "\n") {
				t.Fatalf("textFileList() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestWriteTextDoesNotWaitForXclipOwnership(t *testing.T) {
	originalPath := os.Getenv("PATH")
	t.Cleanup(func() { t.Setenv("PATH", originalPath) })

	dir := t.TempDir()
	path := dir + "/xclip"
	if err := os.WriteFile(path, []byte("#!/bin/sh\n/bin/sleep 2\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	if _, err := exec.LookPath("xclip"); err != nil {
		t.Fatal(err)
	}

	started := time.Now()
	if err := (&linuxClipboard{backend: backendXclip}).writeText("text"); err != nil {
		t.Fatalf("write text: %v", err)
	}
	if elapsed := time.Since(started); elapsed >= time.Second {
		t.Fatalf("xclip write blocked for %s", elapsed)
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
