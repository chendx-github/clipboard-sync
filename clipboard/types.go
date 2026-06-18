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
