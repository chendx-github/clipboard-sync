package clipboard

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

func Fingerprint(data Data) string {
	h := sha256.New()
	h.Write([]byte(string(data.Type)))
	h.Write([]byte("|"))
	h.Write([]byte(data.Text))
	h.Write([]byte("|"))
	h.Write([]byte(strings.Join(data.Files, "\n")))
	h.Write([]byte("|" + data.ImageMIME + "|" + fmt.Sprintf("%d", len(data.Image))))
	if len(data.Image) > 0 {
		h.Write(data.Image)
	}
	if data.RemoteMarker != nil {
		h.Write([]byte("|" + data.RemoteMarker.Token + "|" + data.RemoteMarker.SourceDevice))
	}
	return hex.EncodeToString(h.Sum(nil))
}
