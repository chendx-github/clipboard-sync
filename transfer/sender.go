package transfer

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"os"
	"time"

	"clipboard-sync/mq"
	"clipboard-sync/protocol"
)

type Sender struct {
	groupID   string
	chunkSize int
	mq        *mq.Client
	logger    *slog.Logger
	deviceID  string
}

const (
	messageOverheadBytes = 4096
	base64ExpansionRatio = 4.0 / 3.0
	flushEveryChunks     = 16
	flushEveryBytes      = 32 * 1024 * 1024
	minFileChunkPayload  = 64 * 1024
)

func NewSender(groupID string, chunkSize int, client *mq.Client, logger *slog.Logger, deviceID string) *Sender {
	return &Sender{groupID: groupID, chunkSize: chunkSize, mq: client, logger: logger, deviceID: deviceID}
}

func (s *Sender) SendFiles(ctx context.Context, token string, files []protocol.FileMeta, targetDevice string) error {
	for _, file := range files {
		if err := s.sendSingleFile(ctx, token, file, targetDevice); err != nil {
			return err
		}
	}
	return nil
}

func (s *Sender) sendSingleFile(ctx context.Context, token string, file protocol.FileMeta, targetDevice string) error {
	info, err := os.Stat(file.Path)
	if err != nil {
		return fmt.Errorf("stat file %s: %w", file.Path, err)
	}
	if info.IsDir() {
		return fmt.Errorf("refuse to send directory metadata as a file: %s", file.Path)
	}
	handle, err := os.Open(file.Path)
	if err != nil {
		return fmt.Errorf("open file %s: %w", file.Path, err)
	}
	defer handle.Close()

	chunkSize := s.effectiveChunkSize()
	buffer := make([]byte, chunkSize)
	total := ChunkTotal(file.Size, chunkSize)
	hash := sha256.New()
	seq := 1
	unflushedChunks := 0
	unflushedBytes := 0
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		n, readErr := handle.Read(buffer)
		if n > 0 {
			if _, err := hash.Write(buffer[:n]); err != nil {
				return fmt.Errorf("hash file %s: %w", file.Path, err)
			}
			payload := base64.StdEncoding.EncodeToString(buffer[:n])
			chunkMessage := NewChunkMessage(s.groupID, token, file, s.deviceID, targetDevice, seq, total, payload, n)
			if err := s.mq.Publish(protocol.TopicFileChunk, chunkMessage); err != nil {
				return err
			}
			unflushedChunks++
			unflushedBytes += n
			if unflushedChunks >= flushEveryChunks || unflushedBytes >= flushEveryBytes {
				if err := s.flush(ctx); err != nil {
					return err
				}
				unflushedChunks = 0
				unflushedBytes = 0
			}
			seq++
		}
		if readErr != nil {
			if readErr == io.EOF {
				break
			}
			return fmt.Errorf("read file %s: %w", file.Path, readErr)
		}
	}
	sha256Hex := hex.EncodeToString(hash.Sum(nil))
	completeMessage := NewCompleteMessage(s.groupID, token, file, s.deviceID, targetDevice, total, sha256Hex)
	if err := s.mq.Publish(protocol.TopicFileComplete, completeMessage); err != nil {
		return err
	}
	if err := s.flush(ctx); err != nil {
		return err
	}
	s.logger.Info("file sent", "token", token, "file", file.Name, "chunks", total, "chunk_size", chunkSize, "target_device", targetDevice)
	return nil
}

func (s *Sender) effectiveChunkSize() int {
	maxPayload := s.mq.MaxPayload()
	safePayload := int64(minFileChunkPayload)
	if maxPayload > messageOverheadBytes {
		safePayload = int64(float64(maxPayload-messageOverheadBytes) / base64ExpansionRatio)
	}
	if safePayload < minFileChunkPayload {
		safePayload = minFileChunkPayload
	}
	chunkSize := s.chunkSize
	if chunkSize <= 0 || int64(chunkSize) > safePayload {
		chunkSize = int(safePayload)
	}
	return chunkSize
}

func (s *Sender) flush(ctx context.Context) error {
	flushCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return s.mq.Flush(flushCtx)
}
