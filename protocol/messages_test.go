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

func TestNewUpdatesCarryOriginalSource(t *testing.T) {
	for name, message := range map[string]ClipboardUpdateMessage{
		"text":  NewTextUpdate("g", "dev-a", "hello"),
		"file":  NewFileUpdate("g", "dev-a", "tok", []FileMeta{{Name: "f.txt", Size: 1}}),
		"image": NewImageUpdate("g", "dev-a", "tok", ImageMeta{Name: "i.png", MIME: "image/png", Size: 1, SHA256: "x"}),
	} {
		if message.DeviceID != "dev-a" {
			t.Fatalf("%s: DeviceID = %q", name, message.DeviceID)
		}
		if message.OriginalSource != "dev-a" {
			t.Fatalf("%s: OriginalSource = %q, want dev-a", name, message.OriginalSource)
		}
	}
}

func TestRemoteProvenanceMarkerRoundTrip(t *testing.T) {
	// Matches what windowsClipboard.stampRemoteMarker writes for remote
	// text/image: token + source device, no files.
	marker := RemoteClipboardMarker{Token: "tok-123", GroupID: "", SourceDevice: "dev-b", CreatedAt: 1700000000}
	encoded, err := EncodeRemoteMarker(marker)
	if err != nil {
		t.Fatalf("encode marker: %v", err)
	}
	decoded, ok := DecodeRemoteMarker(encoded)
	if !ok {
		t.Fatal("expected decoded marker to be recognised")
	}
	if decoded.Token != "tok-123" || decoded.SourceDevice != "dev-b" {
		t.Fatalf("decoded marker = %+v", decoded)
	}
	if len(decoded.Files) != 0 {
		t.Fatalf("provenance marker should have no files, got %+v", decoded.Files)
	}
}
