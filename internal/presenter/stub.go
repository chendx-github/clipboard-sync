//go:build !linux && !windows

package presenter

import "clipboard-sync/cache"

type stubPresenter struct{}

func New(_ string, _ TransferAccessor) (Presenter, error) {
	return &stubPresenter{}, nil
}

func (s *stubPresenter) PresentRemoteFiles(_ cache.RemoteClipboardState) (Presentation, error) {
	return Presentation{}, nil
}

func (s *stubPresenter) IsManagedPaths(_ []string) bool {
	return false
}

func (s *stubPresenter) Close() error {
	return nil
}
