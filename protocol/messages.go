package protocol

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const (
	TopicClipboardUpdate  = "clipboard.update"
	TopicClipboardRequest = "clipboard.request"
	TopicFileChunk        = "file.chunk"
	TopicFileComplete     = "file.complete"
	TopicImageChunk       = "image.chunk"
	TopicImageComplete    = "image.complete"

	TypeText  = "text"
	TypeFile  = "file"
	TypeImage = "image"

	RemoteMarkerPrefix = "CLIPBOARD_SYNC_REMOTE:"
)

type FileMeta struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Path     string `json:"path,omitempty"`
	Size     int64  `json:"size"`
	SHA256   string `json:"sha256"`
	Modified int64  `json:"modified,omitempty"`
}

type ImageMeta struct {
	Name   string `json:"name"`
	MIME   string `json:"mime"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type ClipboardUpdateMessage struct {
	Event      string     `json:"event"`
	DeviceID   string     `json:"device_id"`
	Type       string     `json:"type"`
	Text       string     `json:"text,omitempty"`
	Files      []FileMeta `json:"files,omitempty"`
	Image      *ImageMeta `json:"image,omitempty"`
	Token      string     `json:"token,omitempty"`
	CreatedAt  int64      `json:"created_at"`
	TargetHint string     `json:"target_hint,omitempty"`
}

type ClipboardRequestMessage struct {
	Event        string `json:"event"`
	Token        string `json:"token"`
	TargetDevice string `json:"target_device"`
	RequesterID  string `json:"requester_id"`
	RequestedAt  int64  `json:"requested_at"`
}

type FileChunkMessage struct {
	Event        string `json:"event"`
	Token        string `json:"token"`
	FileID       string `json:"file_id"`
	FileName     string `json:"file_name"`
	SourceDevice string `json:"source_device"`
	TargetDevice string `json:"target_device"`
	Seq          int    `json:"seq"`
	Total        int    `json:"total"`
	Data         string `json:"data"`
	Size         int    `json:"size"`
	SentAt       int64  `json:"sent_at"`
}

type FileCompleteMessage struct {
	Event        string `json:"event"`
	Token        string `json:"token"`
	FileID       string `json:"file_id"`
	FileName     string `json:"file_name"`
	SourceDevice string `json:"source_device"`
	TargetDevice string `json:"target_device"`
	SHA256       string `json:"sha256"`
	Size         int64  `json:"size"`
	TotalChunks  int    `json:"total_chunks"`
	CompletedAt  int64  `json:"completed_at"`
}

type ImageChunkMessage struct {
	Event        string `json:"event"`
	Token        string `json:"token"`
	SourceDevice string `json:"source_device"`
	TargetDevice string `json:"target_device,omitempty"`
	Seq          int    `json:"seq"`
	Total        int    `json:"total"`
	Data         string `json:"data"`
	Size         int    `json:"size"`
	SentAt       int64  `json:"sent_at"`
}

type ImageCompleteMessage struct {
	Event        string    `json:"event"`
	Token        string    `json:"token"`
	SourceDevice string    `json:"source_device"`
	TargetDevice string    `json:"target_device,omitempty"`
	Image        ImageMeta `json:"image"`
	TotalChunks  int       `json:"total_chunks"`
	CompletedAt  int64     `json:"completed_at"`
}

type RemoteClipboardMarker struct {
	Token        string     `json:"token"`
	SourceDevice string     `json:"source_device"`
	Files        []FileMeta `json:"files"`
	CreatedAt    int64      `json:"created_at"`
}

func Encode(v any) ([]byte, error) {
	return json.Marshal(v)
}

func Decode[T any](payload []byte) (T, error) {
	var result T
	err := json.Unmarshal(payload, &result)
	return result, err
}

func NewTextUpdate(deviceID string, text string) ClipboardUpdateMessage {
	return ClipboardUpdateMessage{
		Event:     TopicClipboardUpdate,
		DeviceID:  deviceID,
		Type:      TypeText,
		Text:      text,
		CreatedAt: time.Now().Unix(),
	}
}

func NewFileUpdate(deviceID string, token string, files []FileMeta) ClipboardUpdateMessage {
	return ClipboardUpdateMessage{
		Event:     TopicClipboardUpdate,
		DeviceID:  deviceID,
		Type:      TypeFile,
		Files:     files,
		Token:     token,
		CreatedAt: time.Now().Unix(),
	}
}

func NewImageUpdate(deviceID string, token string, image ImageMeta) ClipboardUpdateMessage {
	return ClipboardUpdateMessage{
		Event:     TopicClipboardUpdate,
		DeviceID:  deviceID,
		Type:      TypeImage,
		Image:     &image,
		Token:     token,
		CreatedAt: time.Now().Unix(),
	}
}

func NewRequest(token string, targetDevice string, requesterID string) ClipboardRequestMessage {
	return ClipboardRequestMessage{
		Event:        TopicClipboardRequest,
		Token:        token,
		TargetDevice: targetDevice,
		RequesterID:  requesterID,
		RequestedAt:  time.Now().Unix(),
	}
}

func EncodeRemoteMarker(marker RemoteClipboardMarker) (string, error) {
	payload, err := json.Marshal(marker)
	if err != nil {
		return "", err
	}
	return RemoteMarkerPrefix + string(payload), nil
}

func DecodeRemoteMarker(text string) (RemoteClipboardMarker, bool) {
	if !strings.HasPrefix(text, RemoteMarkerPrefix) {
		return RemoteClipboardMarker{}, false
	}
	var marker RemoteClipboardMarker
	err := json.Unmarshal([]byte(strings.TrimPrefix(text, RemoteMarkerPrefix)), &marker)
	if err != nil {
		return RemoteClipboardMarker{}, false
	}
	return marker, true
}

func ValidateFileMeta(meta FileMeta) error {
	if meta.Name == "" {
		return fmt.Errorf("file name is empty")
	}
	if meta.Size < 0 {
		return fmt.Errorf("file size is invalid")
	}
	if meta.SHA256 == "" {
		return fmt.Errorf("file sha256 is empty")
	}
	return nil
}
