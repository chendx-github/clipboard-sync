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
