package presenter

import (
	"clipboard-sync/cache"
	"clipboard-sync/protocol"
)

type TransferAccessor interface {
	EnsureRemoteTransfer(state cache.RemoteClipboardState) error
	ReadRemoteFile(state cache.RemoteClipboardState, file protocol.FileMeta, offset int64, size int) ([]byte, error)
}

type Presentation struct {
	Paths   []string
	Handled bool
}

type Presenter interface {
	PresentRemoteFiles(state cache.RemoteClipboardState) (Presentation, error)
	IsManagedPaths(paths []string) bool
	Close() error
}
