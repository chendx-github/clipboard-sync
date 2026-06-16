package transfer

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"clipboard-sync/protocol"
)

func BuildFileMetadata(paths []string) ([]protocol.FileMeta, error) {
	var files []protocol.FileMeta
	for _, path := range paths {
		items, err := buildPathMetadata(path)
		if err != nil {
			return nil, err
		}
		files = append(files, items...)
	}
	return files, nil
}

func buildPathMetadata(path string) ([]protocol.FileMeta, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("stat %s: %w", path, err)
	}
	if !info.IsDir() {
		meta, err := buildSingleFileMetadata(path, filepath.Base(path))
		if err != nil {
			return nil, err
		}
		return []protocol.FileMeta{meta}, nil
	}
	root := filepath.Clean(path)
	rootName := filepath.Base(root)
	var files []protocol.FileMeta
	err = filepath.WalkDir(root, func(current string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, current)
		if err != nil {
			return err
		}
		name := filepath.ToSlash(filepath.Join(rootName, rel))
		meta, err := buildSingleFileMetadata(current, name)
		if err != nil {
			return err
		}
		files = append(files, meta)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk %s: %w", path, err)
	}
	return files, nil
}

func buildSingleFileMetadata(path string, name string) (protocol.FileMeta, error) {
	info, err := os.Stat(path)
	if err != nil {
		return protocol.FileMeta{}, fmt.Errorf("stat %s: %w", path, err)
	}
	sha, err := sha256File(path)
	if err != nil {
		return protocol.FileMeta{}, err
	}
	return protocol.FileMeta{
		ID:       fileID(path, info),
		Name:     name,
		Path:     path,
		Size:     info.Size(),
		SHA256:   sha,
		Modified: info.ModTime().Unix(),
	}, nil
}

func sha256File(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open %s: %w", path, err)
	}
	defer file.Close()
	h := sha256.New()
	if _, err := io.Copy(h, file); err != nil {
		return "", fmt.Errorf("hash %s: %w", path, err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func fileID(path string, info os.FileInfo) string {
	seed := fmt.Sprintf("%s|%d|%d", path, info.Size(), info.ModTime().UnixNano())
	sum := sha256.Sum256([]byte(seed))
	return hex.EncodeToString(sum[:8])
}

func ChunkTotal(size int64, chunkSize int) int {
	if size == 0 {
		return 1
	}
	total := size / int64(chunkSize)
	if size%int64(chunkSize) != 0 {
		total++
	}
	return int(total)
}

func NewChunkMessage(token string, file protocol.FileMeta, sourceDevice string, targetDevice string, seq int, total int, data string, size int) protocol.FileChunkMessage {
	return protocol.FileChunkMessage{
		Event:        protocol.TopicFileChunk,
		Token:        token,
		FileID:       file.ID,
		FileName:     file.Name,
		SourceDevice: sourceDevice,
		TargetDevice: targetDevice,
		Seq:          seq,
		Total:        total,
		Data:         data,
		Size:         size,
		SentAt:       time.Now().Unix(),
	}
}

func NewCompleteMessage(token string, file protocol.FileMeta, sourceDevice string, targetDevice string, totalChunks int) protocol.FileCompleteMessage {
	return protocol.FileCompleteMessage{
		Event:        protocol.TopicFileComplete,
		Token:        token,
		FileID:       file.ID,
		FileName:     file.Name,
		SourceDevice: sourceDevice,
		TargetDevice: targetDevice,
		SHA256:       file.SHA256,
		Size:         file.Size,
		TotalChunks:  totalChunks,
		CompletedAt:  time.Now().Unix(),
	}
}
