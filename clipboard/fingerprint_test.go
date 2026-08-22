package clipboard

import "testing"

// TestFingerprintIgnoresProvenance guards the invariant that OriginToken and
// OriginDevice are NOT part of the content fingerprint. On Linux (and Windows
// before the provenance marker is detected) the watcher dedups by fingerprint,
// so remote-origin writes must hash identically to their byte-preserved
// read-back regardless of provenance metadata.
func TestFingerprintIgnoresProvenance(t *testing.T) {
	base := Data{Type: DataTypeImage, Image: []byte{1, 2, 3, 4}, ImageMIME: "image/png"}
	remote := base
	remote.OriginToken = "tok-123"
	remote.OriginDevice = "dev-b"

	if Fingerprint(base) == "" {
		t.Fatal("expected non-empty fingerprint")
	}
	if Fingerprint(base) != Fingerprint(remote) {
		t.Fatalf("provenance must not change fingerprint: %s vs %s", Fingerprint(base), Fingerprint(remote))
	}
}

func TestFingerprintDifferentiatesContent(t *testing.T) {
	a := Data{Type: DataTypeText, Text: "hello"}
	b := Data{Type: DataTypeText, Text: "world"}
	if Fingerprint(a) == Fingerprint(b) {
		t.Fatal("different content must produce different fingerprints")
	}
}
