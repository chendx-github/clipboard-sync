//go:build windows

package clipboard

import (
	"image"
	"image/color"
	"testing"
)

func TestDecodeClipboardImagePayloadAcceptsDIB(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	img.Set(0, 0, color.RGBA{R: 0x12, G: 0x34, B: 0x56, A: 0xff})

	decoded, err := decodeClipboardImagePayload(imageToDIB24(img), "image/png")
	if err != nil {
		t.Fatalf("decode DIB payload: %v", err)
	}
	if decoded.Bounds() != img.Bounds() {
		t.Fatalf("decoded bounds = %v, want %v", decoded.Bounds(), img.Bounds())
	}
}
