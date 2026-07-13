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
	GroupID    string     `json:"group_id"`
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
	GroupID      string `json:"group_id"`
	Token        string `json:"token"`
	Type         string `json:"type"`
	TargetDevice string `json:"target_device"`
	RequesterID  string `json:"requester_id"`
	RequestedAt  int64  `json:"requested_at"`
}

type FileChunkMessage struct {
	Event        string `json:"event"`
	GroupID      string `json:"group_id"`
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
	GroupID      string `json:"group_id"`
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
	GroupID      string `json:"group_id"`
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
	GroupID      string    `json:"group_id"`
	Token        string    `json:"token"`
	SourceDevice string    `json:"source_device"`
	TargetDevice string    `json:"target_device,omitempty"`
	Image        ImageMeta `json:"image"`
	TotalChunks  int       `json:"total_chunks"`
	CompletedAt  int64     `json:"completed_at"`
}

type RemoteClipboardMarker struct {
	Token        string     `json:"token"`
	GroupID      string     `json:"group_id"`
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

func NewTextUpdate(groupID string, deviceID string, text string) ClipboardUpdateMessage {
	return ClipboardUpdateMessage{
		Event:     TopicClipboardUpdate,
		GroupID:   groupID,
		DeviceID:  deviceID,
		Type:      TypeText,
		Text:      text,
		CreatedAt: time.Now().Unix(),
	}
}

func NewFileUpdate(groupID string, deviceID string, token string, files []FileMeta) ClipboardUpdateMessage {
	return ClipboardUpdateMessage{
		Event:     TopicClipboardUpdate,
		GroupID:   groupID,
		DeviceID:  deviceID,
		Type:      TypeFile,
		Files:     files,
		Token:     token,
		CreatedAt: time.Now().Unix(),
	}
}

func NewImageUpdate(groupID string, deviceID string, token string, image ImageMeta) ClipboardUpdateMessage {
	return ClipboardUpdateMessage{
		Event:     TopicClipboardUpdate,
		GroupID:   groupID,
		DeviceID:  deviceID,
		Type:      TypeImage,
		Image:     &image,
		Token:     token,
		CreatedAt: time.Now().Unix(),
	}
}

func NewRequest(groupID string, token string, dataType string, targetDevice string, requesterID string) ClipboardRequestMessage {
	return ClipboardRequestMessage{
		Event:        TopicClipboardRequest,
		GroupID:      groupID,
		Token:        token,
		Type:         dataType,
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
	return nil
}

func ValidateClipboardUpdate(message ClipboardUpdateMessage) error {
	if message.Event != TopicClipboardUpdate {
		return fmt.Errorf("invalid clipboard update event: %s", message.Event)
	}
	if message.GroupID == "" {
		return fmt.Errorf("clipboard update group_id is empty")
	}
	if message.DeviceID == "" {
		return fmt.Errorf("clipboard update device_id is empty")
	}
	switch message.Type {
	case TypeText:
		return nil
	case TypeFile:
		if message.Token == "" {
			return fmt.Errorf("file update token is empty")
		}
		for _, file := range message.Files {
			if err := ValidateFileMeta(file); err != nil {
				return err
			}
		}
		return nil
	case TypeImage:
		if message.Token == "" {
			return fmt.Errorf("image update token is empty")
		}
		if message.Image == nil {
			return fmt.Errorf("image update metadata is missing")
		}
		return ValidateImageMeta(*message.Image)
	default:
		return fmt.Errorf("unknown clipboard update type: %s", message.Type)
	}
}

func ValidateClipboardRequest(message ClipboardRequestMessage) error {
	if message.Event != TopicClipboardRequest {
		return fmt.Errorf("invalid clipboard request event: %s", message.Event)
	}
	if message.GroupID == "" || message.Token == "" || message.TargetDevice == "" || message.RequesterID == "" {
		return fmt.Errorf("clipboard request missing required routing fields")
	}
	if message.Type != TypeFile && message.Type != TypeImage {
		return fmt.Errorf("invalid clipboard request type: %s", message.Type)
	}
	return nil
}

func ValidateFileChunk(message FileChunkMessage) error {
	if message.Event != TopicFileChunk {
		return fmt.Errorf("invalid file chunk event: %s", message.Event)
	}
	if message.GroupID == "" || message.Token == "" || message.FileID == "" || message.FileName == "" || message.SourceDevice == "" || message.TargetDevice == "" {
		return fmt.Errorf("file chunk missing required routing fields")
	}
	if message.Seq < 1 || message.Total < 1 || message.Seq > message.Total || message.Size < 0 || message.Data == "" {
		return fmt.Errorf("invalid file chunk sequence or size")
	}
	return nil
}

func ValidateFileComplete(message FileCompleteMessage) error {
	if message.Event != TopicFileComplete {
		return fmt.Errorf("invalid file complete event: %s", message.Event)
	}
	if message.GroupID == "" || message.Token == "" || message.FileID == "" || message.FileName == "" || message.SourceDevice == "" || message.TargetDevice == "" || message.SHA256 == "" {
		return fmt.Errorf("file complete missing required routing fields")
	}
	if message.TotalChunks < 0 || message.Size < 0 {
		return fmt.Errorf("invalid file complete size")
	}
	return nil
}

func ValidateImageMeta(meta ImageMeta) error {
	if meta.Name == "" || meta.MIME == "" || meta.SHA256 == "" {
		return fmt.Errorf("image metadata missing required fields")
	}
	if meta.Size < 0 {
		return fmt.Errorf("image size is invalid")
	}
	return nil
}

func ValidateImageChunk(message ImageChunkMessage) error {
	if message.Event != TopicImageChunk {
		return fmt.Errorf("invalid image chunk event: %s", message.Event)
	}
	if message.GroupID == "" || message.Token == "" || message.SourceDevice == "" || message.TargetDevice == "" {
		return fmt.Errorf("image chunk missing required routing fields")
	}
	if message.Seq < 1 || message.Total < 1 || message.Seq > message.Total || message.Size < 0 || message.Data == "" {
		return fmt.Errorf("invalid image chunk sequence or size")
	}
	return nil
}

func ValidateImageComplete(message ImageCompleteMessage) error {
	if message.Event != TopicImageComplete {
		return fmt.Errorf("invalid image complete event: %s", message.Event)
	}
	if message.GroupID == "" || message.Token == "" || message.SourceDevice == "" || message.TargetDevice == "" {
		return fmt.Errorf("image complete missing required routing fields")
	}
	if message.TotalChunks < 1 {
		return fmt.Errorf("invalid image complete chunk count")
	}
	return ValidateImageMeta(message.Image)
}
