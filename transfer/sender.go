package transfer

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"os"
	"time"

	"clipboard-sync/mq"
	"clipboard-sync/protocol"
)

type Sender struct {
	chunkSize int
	mq        *mq.Client
	logger    *slog.Logger
	deviceID  string
}

const (
	maxFileChunkPayload = 512 * 1024
	fileChunkPause      = 2 * time.Millisecond
)

func NewSender(chunkSize int, client *mq.Client, logger *slog.Logger, deviceID string) *Sender {
	return &Sender{chunkSize: chunkSize, mq: client, logger: logger, deviceID: deviceID}
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
	handle, err := os.Open(file.Path)
	if err != nil {
		return fmt.Errorf("open file %s: %w", file.Path, err)
	}
	defer handle.Close()

	chunkSize := s.chunkSize
	if chunkSize <= 0 || chunkSize > maxFileChunkPayload {
		chunkSize = maxFileChunkPayload
	}
	buffer := make([]byte, chunkSize)
	total := ChunkTotal(file.Size, chunkSize)
	seq := 1
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		n, readErr := handle.Read(buffer)
		if n > 0 {
			payload := base64.StdEncoding.EncodeToString(buffer[:n])
			chunkMessage := NewChunkMessage(token, file, s.deviceID, targetDevice, seq, total, payload, n)
			if err := s.mq.Publish(protocol.TopicFileChunk, chunkMessage); err != nil {
				return err
			}
			if err := s.flush(ctx); err != nil {
				return err
			}
			if err := pause(ctx, fileChunkPause); err != nil {
				return err
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
	completeMessage := NewCompleteMessage(token, file, s.deviceID, targetDevice, total)
	if err := s.mq.Publish(protocol.TopicFileComplete, completeMessage); err != nil {
		return err
	}
	if err := s.flush(ctx); err != nil {
		return err
	}
	s.logger.Info("file sent", "token", token, "file", file.Name, "chunks", total, "target_device", targetDevice)
	return nil
}

func (s *Sender) flush(ctx context.Context) error {
	flushCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return s.mq.Flush(flushCtx)
}

func pause(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
