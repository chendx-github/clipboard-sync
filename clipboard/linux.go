//go:build linux

package clipboard

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"clipboard-sync/protocol"
)

type linuxClipboard struct {
	pollInterval time.Duration
	backend      linuxBackend
	gtkWriter    *gtkFileWriter
	last         string
}

type linuxBackend int

const (
	backendXclip linuxBackend = iota
	backendWayland
)

func New(pollInterval time.Duration, fileWriter string) (Clipboard, error) {
	backend, err := detectLinuxBackend()
	if err != nil {
		return nil, err
	}
	clip := &linuxClipboard{pollInterval: pollInterval, backend: backend}
	// Older file managers (Nautilus 3.x) only honour file paste from a
	// GTK-owned clipboard. Optionally route file writes through a GTK helper.
	switch fileWriter {
	case "gtk":
		writer, gerr := detectGTKFileWriter()
		if gerr != nil {
			return nil, fmt.Errorf("gtk file writer requested but unavailable: %w", gerr)
		}
		clip.gtkWriter = writer
	case "auto":
		if writer, gerr := detectGTKFileWriter(); gerr == nil {
			clip.gtkWriter = writer
		}
	}
	return clip, nil
}

func (c *linuxClipboard) Read() (Data, error) {
	hasText, hasImage := c.detectClipboardKinds()
	if hasText {
		text, err := c.readText()
		if err == nil {
			if marker, ok := protocol.DecodeRemoteMarker(text); ok {
				data := Data{Type: DataTypeRemote, Text: text, RemoteMarker: &marker}
				data.Fingerprint = Fingerprint(data)
				return data, nil
			}
		}
	}
	files, err := c.readFiles()
	if err == nil && len(files) > 0 {
		data := Data{Type: DataTypeFiles, Files: files}
		data.Fingerprint = Fingerprint(data)
		return data, nil
	}
	image, mime, err := c.readImage()
	if err == nil && len(image) > 0 {
		data := Data{Type: DataTypeImage, Image: image, ImageMIME: mime}
		data.Fingerprint = Fingerprint(data)
		return data, nil
	}
	if !hasText && hasImage {
		return Data{}, err
	}
	text, err := c.readText()
	if err != nil {
		return Data{}, err
	}
	data := Data{Type: DataTypeText, Text: text}
	data.Fingerprint = Fingerprint(data)
	return data, nil
}

func (c *linuxClipboard) Write(data Data) error {
	switch data.Type {
	case DataTypeText:
		return c.writeText(data.Text)
	case DataTypeRemote:
		if data.RemoteMarker == nil {
			return fmt.Errorf("remote marker is nil")
		}
		encoded, err := protocol.EncodeRemoteMarker(*data.RemoteMarker)
		if err != nil {
			return err
		}
		return c.writeText(encoded)
	case DataTypeFiles:
		return c.writeFiles(data.Files)
	case DataTypeImage:
		return c.writeImage(data.Image, data.ImageMIME)
	default:
		return fmt.Errorf("unsupported clipboard data type: %s", data.Type)
	}
}

func (c *linuxClipboard) Watch(out chan Data) {
	go func() {
		for {
			data, err := c.Read()
			if err == nil && !data.Empty() && data.Fingerprint != "" && data.Fingerprint != c.last {
				c.last = data.Fingerprint
				out <- data
			}
			time.Sleep(c.pollInterval)
		}
	}()
}

func detectLinuxBackend() (linuxBackend, error) {
	if _, err := exec.LookPath("wl-paste"); err == nil {
		if _, err := exec.LookPath("wl-copy"); err == nil {
			return backendWayland, nil
		}
	}
	if _, err := exec.LookPath("xclip"); err == nil {
		return backendXclip, nil
	}
	return 0, fmt.Errorf("no clipboard backend found; install wl-clipboard or xclip")
}

func (c *linuxClipboard) readText() (string, error) {
	var cmd *exec.Cmd
	if c.backend == backendWayland {
		cmd = exec.Command("wl-paste", "--no-newline")
	} else {
		cmd = exec.Command("xclip", "-selection", "clipboard", "-o")
	}
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("read clipboard text: %w", err)
	}
	return string(out), nil
}

func (c *linuxClipboard) readFiles() ([]string, error) {
	content, err := c.readFileListPayload()
	if err != nil {
		return nil, err
	}
	return parseClipboardFileList(content)
}

func (c *linuxClipboard) readFileListPayload() (string, error) {
	if c.backend == backendWayland {
		mimeTypes, err := exec.Command("wl-paste", "--list-types").Output()
		if err != nil {
			return "", fmt.Errorf("list clipboard mime types: %w", err)
		}
		for _, mime := range []string{"text/uri-list", "x-special/gnome-copied-files"} {
			if bytes.Contains(mimeTypes, []byte(mime)) {
				out, err := exec.Command("wl-paste", "--type", mime, "--no-newline").Output()
				if err != nil {
					return "", fmt.Errorf("read clipboard mime %s: %w", mime, err)
				}
				return string(out), nil
			}
		}
		if payload, ok := c.readNautilusClipboardText(); ok {
			return payload, nil
		}
		return "", errors.New("no file mime type in clipboard")
	}

	for _, target := range []string{"x-special/gnome-copied-files", "text/uri-list"} {
		cmd := exec.Command("xclip", "-selection", "clipboard", "-t", target, "-o")
		out, err := cmd.Output()
		if err == nil && len(out) > 0 {
			return string(out), nil
		}
	}
	if payload, ok := c.readNautilusClipboardText(); ok {
		return payload, nil
	}
	return "", errors.New("no file payload in clipboard")
}

// readNautilusClipboardText reads a text target and returns it when it carries
// an "x-special/" file list (the nautilus-clipboard / gnome-copied-files text
// form). This is a fallback so that clipboards owned by GTK programs — which
// only expose text targets — are still recognised as file lists, letting our
// own GTK file writes round-trip back as files and avoid an echo loop.
func (c *linuxClipboard) readNautilusClipboardText() (string, bool) {
	var candidates []string
	if c.backend == backendWayland {
		mimeTypes, err := exec.Command("wl-paste", "--list-types").Output()
		if err != nil {
			return "", false
		}
		for _, target := range []string{"text/plain;charset=utf-8", "text/plain", "UTF8_STRING"} {
			if bytes.Contains(mimeTypes, []byte(target)) {
				candidates = append(candidates, target)
			}
		}
	} else {
		candidates = []string{"text/plain;charset=utf-8", "text/plain", "UTF8_STRING"}
	}
	for _, target := range candidates {
		var (
			out []byte
			err error
		)
		if c.backend == backendWayland {
			out, err = exec.Command("wl-paste", "--type", target, "--no-newline").Output()
		} else {
			out, err = exec.Command("xclip", "-selection", "clipboard", "-t", target, "-o").Output()
		}
		if err != nil || len(out) == 0 {
			continue
		}
		if s := string(out); strings.Contains(s, "x-special/") {
			return s, true
		}
	}
	return "", false
}

func parseClipboardFileList(payload string) ([]string, error) {
	var files []string
	scanner := bufio.NewScanner(strings.NewReader(payload))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.EqualFold(line, "copy") || strings.EqualFold(line, "cut") {
			continue
		}
		if strings.HasPrefix(line, "file://") {
			path, err := fileURIToPath(line)
			if err != nil {
				return nil, err
			}
			files = append(files, path)
			continue
		}
		if filepath.IsAbs(line) {
			files = append(files, line)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan file list: %w", err)
	}
	for _, path := range files {
		if _, err := os.Stat(path); err != nil {
			return nil, fmt.Errorf("stat %s: %w", path, err)
		}
	}
	return files, nil
}

func (c *linuxClipboard) writeText(text string) error {
	var cmd *exec.Cmd
	if c.backend == backendWayland {
		cmd = exec.Command("wl-copy")
	} else {
		cmd = exec.Command("xclip", "-selection", "clipboard")
	}
	cmd.Stdin = strings.NewReader(text)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("write clipboard text: %w", err)
	}
	return nil
}

func (c *linuxClipboard) writeFiles(files []string) error {
	if len(files) == 0 {
		return fmt.Errorf("no files to write")
	}
	for _, path := range files {
		if _, err := os.Stat(path); err != nil {
			return fmt.Errorf("stat %s: %w", path, err)
		}
	}
	if c.gtkWriter != nil {
		uris := make([]string, 0, len(files))
		for _, path := range files {
			uris = append(uris, pathToFileURI(path))
		}
		payload := "x-special/nautilus-clipboard\ncopy\n" + strings.Join(uris, "\n") + "\n"
		if err := c.gtkWriter.writeFiles(payload); err != nil {
			return fmt.Errorf("write gtk files: %w", err)
		}
		return nil
	}
	lines := make([]string, 0, len(files)+1)
	lines = append(lines, "copy")
	for _, path := range files {
		lines = append(lines, pathToFileURI(path))
	}
	payload := strings.Join(lines, "\n")
	if c.backend == backendWayland {
		cmd := exec.Command("wl-copy", "--type", "x-special/gnome-copied-files")
		cmd.Stdin = strings.NewReader(payload)
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("write gnome copied files: %w", err)
		}
		return nil
	}
	cmd := exec.Command("xclip", "-selection", "clipboard", "-t", "x-special/gnome-copied-files")
	cmd.Stdin = strings.NewReader(payload)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("write xclip files: %w", err)
	}
	return nil
}

func pathToFileURI(path string) string {
	return (&url.URL{Scheme: "file", Path: path}).String()
}

func fileURIToPath(uri string) (string, error) {
	parsed, err := url.Parse(uri)
	if err != nil {
		return "", fmt.Errorf("parse file uri %q: %w", uri, err)
	}
	if parsed.Scheme != "file" {
		return "", fmt.Errorf("unsupported uri scheme: %s", parsed.Scheme)
	}
	if parsed.Host != "" && parsed.Host != "localhost" {
		return "", fmt.Errorf("unsupported file uri host: %s", parsed.Host)
	}
	return parsed.Path, nil
}

func (c *linuxClipboard) readImage() ([]byte, string, error) {
	if c.backend == backendWayland {
		mimeTypes, err := exec.Command("wl-paste", "--list-types").Output()
		if err != nil {
			return nil, "", fmt.Errorf("list clipboard mime types: %w", err)
		}
		for _, mime := range []string{"image/png", "image/bmp"} {
			if bytes.Contains(mimeTypes, []byte(mime)) {
				out, err := exec.Command("wl-paste", "--type", mime).Output()
				if err != nil {
					return nil, "", fmt.Errorf("read clipboard image %s: %w", mime, err)
				}
				return out, mime, nil
			}
		}
		return nil, "", errors.New("no image mime type in clipboard")
	}
	for _, mime := range []string{"image/png", "image/bmp"} {
		out, err := exec.Command("xclip", "-selection", "clipboard", "-t", mime, "-o").Output()
		if err == nil && len(out) > 0 {
			return out, mime, nil
		}
	}
	return nil, "", errors.New("no image payload in clipboard")
}

func (c *linuxClipboard) writeImage(image []byte, mime string) error {
	if len(image) == 0 {
		return fmt.Errorf("image payload is empty")
	}
	if mime == "" {
		mime = "image/png"
	}
	if c.backend == backendWayland {
		cmd := exec.Command("wl-copy", "--type", mime)
		cmd.Stdin = bytes.NewReader(image)
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("write wayland image: %w", err)
		}
		return nil
	}
	cmd := exec.Command("xclip", "-selection", "clipboard", "-t", mime)
	cmd.Stdin = bytes.NewReader(image)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("write xclip image: %w", err)
	}
	return nil
}

func (c *linuxClipboard) detectClipboardKinds() (hasText bool, hasImage bool) {
	if c.backend == backendWayland {
		mimeTypes, err := exec.Command("wl-paste", "--list-types").Output()
		if err != nil {
			return true, false
		}
		mimeText := string(mimeTypes)
		return strings.Contains(mimeText, "text/plain") || strings.Contains(mimeText, "UTF8_STRING") || strings.Contains(mimeText, "STRING"),
			strings.Contains(mimeText, "image/png") || strings.Contains(mimeText, "image/bmp")
	}
	out, err := exec.Command("xclip", "-selection", "clipboard", "-t", "TARGETS", "-o").Output()
	if err != nil {
		return true, false
	}
	targets := string(out)
	return strings.Contains(targets, "text/plain") || strings.Contains(targets, "UTF8_STRING") || strings.Contains(targets, "STRING"),
		strings.Contains(targets, "image/png") || strings.Contains(targets, "image/bmp")
}
