package clipboard

import "clipboard-sync/protocol"

type DataType string

const (
	DataTypeText   DataType = protocol.TypeText
	DataTypeFiles  DataType = protocol.TypeFile
	DataTypeImage  DataType = protocol.TypeImage
	DataTypeRemote DataType = "remote-file"
)

type Data struct {
	Type         DataType
	Text         string
	Files        []string
	Image        []byte
	ImageMIME    string
	RemoteMarker *protocol.RemoteClipboardMarker
	Fingerprint  string
	// OriginToken and OriginDevice identify content that was written to the
	// local clipboard by this agent after receiving it from a remote device.
	// They are intentionally NOT part of Fingerprint (which stays content-only
	// so Linux byte-preserving dedup keeps working). Clipboard writers use them
	// to stamp a private provenance marker so the local watcher can recognise
	// agent-written remote content and never re-broadcast it.
	OriginToken  string
	OriginDevice string
}

type Clipboard interface {
	Read() (Data, error)
	Write(Data) error
	Watch(chan Data)
}

func (d Data) Empty() bool {
	switch d.Type {
	case DataTypeText:
		return d.Text == ""
	case DataTypeFiles:
		return len(d.Files) == 0
	case DataTypeImage:
		return len(d.Image) == 0
	case DataTypeRemote:
		return d.RemoteMarker == nil
	default:
		return true
	}
}
