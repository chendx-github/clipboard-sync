package cache

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"clipboard-sync/protocol"
)

const currentRemoteClipboardFile = "current_remote_clipboard.json"

type RemoteClipboardState struct {
	Token        string              `json:"token"`
	GroupID      string              `json:"group_id"`
	SourceDevice string              `json:"source_device"`
	Files        []protocol.FileMeta `json:"files"`
	CreatedAt    int64               `json:"created_at"`
	Status       string              `json:"status"`
	LocalPaths   map[string]string   `json:"local_paths,omitempty"`
	Error        string              `json:"error,omitempty"`
}

type Store struct {
	root string
	mu   sync.Mutex
}

func New(root string) (*Store, error) {
	if err := os.MkdirAll(filepath.Join(root, "transfers"), 0o755); err != nil {
		return nil, fmt.Errorf("create transfers dir: %w", err)
	}
	return &Store{root: root}, nil
}

func (s *Store) Root() string {
	return s.root
}

func (s *Store) SaveRemoteClipboard(state RemoteClipboardState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.writeJSON(filepath.Join(s.root, currentRemoteClipboardFile), state)
}

func (s *Store) LoadRemoteClipboard() (RemoteClipboardState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var state RemoteClipboardState
	err := s.readJSON(filepath.Join(s.root, currentRemoteClipboardFile), &state)
	return state, err
}

func (s *Store) SaveTransferState(state RemoteClipboardState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.writeJSON(s.transferPath(state.Token), state)
}

func (s *Store) LoadTransferState(token string) (RemoteClipboardState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var state RemoteClipboardState
	err := s.readJSON(s.transferPath(token), &state)
	return state, err
}

func (s *Store) MarkRequested(token string) error {
	state, err := s.LoadTransferState(token)
	if err != nil {
		return err
	}
	state.Status = "requested"
	return s.SaveTransferState(state)
}

func (s *Store) MarkReceiving(token string) error {
	state, err := s.LoadTransferState(token)
	if err != nil {
		return err
	}
	state.Status = "receiving"
	return s.SaveTransferState(state)
}

func (s *Store) MarkFileCompleted(token string, fileID string, localPath string) error {
	state, err := s.LoadTransferState(token)
	if err != nil {
		return err
	}
	if state.LocalPaths == nil {
		state.LocalPaths = make(map[string]string)
	}
	state.LocalPaths[fileID] = localPath
	state.Status = "receiving"
	if len(state.LocalPaths) == len(state.Files) {
		state.Status = "completed"
	}
	return s.SaveTransferState(state)
}

func (s *Store) MarkFailed(token string, err error) error {
	state, loadErr := s.LoadTransferState(token)
	if loadErr != nil {
		return loadErr
	}
	state.Status = "failed"
	state.Error = err.Error()
	return s.SaveTransferState(state)
}

func (s *Store) PrepareTransferFromRemote() (RemoteClipboardState, error) {
	state, err := s.LoadRemoteClipboard()
	if err != nil {
		return RemoteClipboardState{}, err
	}
	state.Status = "pending"
	state.Error = ""
	state.LocalPaths = map[string]string{}
	if err := s.SaveTransferState(state); err != nil {
		return RemoteClipboardState{}, err
	}
	return state, nil
}

func (s *Store) WaitForCompletion(token string, timeout time.Duration) (RemoteClipboardState, error) {
	deadline := time.Now().Add(timeout)
	for {
		state, err := s.LoadTransferState(token)
		if err == nil {
			switch state.Status {
			case "completed":
				return state, nil
			case "failed":
				return state, fmt.Errorf(state.Error)
			}
		}
		if time.Now().After(deadline) {
			return RemoteClipboardState{}, fmt.Errorf("wait transfer timeout for token %s", token)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

func (s *Store) transferPath(token string) string {
	return filepath.Join(s.root, "transfers", token+".json")
}

func (s *Store) writeJSON(path string, value any) error {
	payload, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal json: %w", err)
	}
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, payload, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", tmpPath, err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("rename %s: %w", path, err)
	}
	return nil
}

func (s *Store) readJSON(path string, target any) error {
	payload, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	if err := json.Unmarshal(payload, target); err != nil {
		return fmt.Errorf("decode %s: %w", path, err)
	}
	return nil
}
