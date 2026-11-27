package transfer

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"testing"
)

func TestValidateImagePayloadRejectsGarbage(t *testing.T) {
	// Some apps advertise an image/png target but answer with a handful of bytes.
	if err := ValidateImagePayload([]byte("this is not an image payload")); err == nil {
		t.Fatal("expected garbage payload to be rejected")
	}
	if err := ValidateImagePayload([]byte{0x89, 'P', 'N', 'G'}); err == nil {
		t.Fatal("expected truncated png header to be rejected")
	}
}

func TestValidateImagePayloadAcceptsPNG(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 2, 2))
	src.Set(0, 0, color.RGBA{R: 255, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, src); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	if err := ValidateImagePayload(buf.Bytes()); err != nil {
		t.Fatalf("expected valid png to be accepted: %v", err)
	}
}
