//go:build linux

package clipboard

import (
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

//go:embed gtk_helper.py
var gtkHelperScript string

// gtkFileWriter writes a Nautilus-compatible file-copy payload to the clipboard
// via a transient GTK helper process that holds CLIPBOARD ownership. This is
// required for file paste to work in older file managers (Nautilus 3.x) that
// only honour clipboards owned by GTK programs.
type gtkFileWriter struct {
	python string
	helper string
}

// detectGTKFileWriter probes for a python3 interpreter that can import pygobject
// (gi + Gtk 3) and materialises the embedded helper script on disk.
func detectGTKFileWriter() (*gtkFileWriter, error) {
	python, err := findGTKPython()
	if err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "clipboard-sync-gtk-")
	if err != nil {
		return nil, err
	}
	helper := filepath.Join(dir, "gtk_helper.py")
	if err := os.WriteFile(helper, []byte(gtkHelperScript), 0o644); err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	return &gtkFileWriter{python: python, helper: helper}, nil
}

func findGTKPython() (string, error) {
	candidates := []string{"/usr/bin/python3", "python3"}
	for _, candidate := range candidates {
		path, err := exec.LookPath(candidate)
		if err != nil {
			continue
		}
		probe := exec.Command(path, "-c",
			"import gi; gi.require_version('Gtk','3.0'); gi.require_version('Gdk','3.0'); from gi.repository import Gtk, Gdk")
		if probe.Run() == nil {
			return path, nil
		}
	}
	return "", fmt.Errorf("no python3 with pygobject (gi/Gtk 3) available")
}

// writeFiles spawns a detached GTK helper that takes clipboard ownership with
// the given payload. The previous helper (if any) self-terminates because
// taking ownership fires its owner-change handler.
func (w *gtkFileWriter) writeFiles(payload string) error {
	tmp, err := os.CreateTemp("", "clipboard-sync-payload-*.txt")
	if err != nil {
		return fmt.Errorf("create payload file: %w", err)
	}
	if _, err := tmp.WriteString(payload); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return fmt.Errorf("write payload file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return fmt.Errorf("close payload file: %w", err)
	}

	cmd := exec.Command(w.python, w.helper, tmp.Name())
	// Fully detach so the helper survives as an independent session and keeps
	// holding clipboard ownership regardless of the agent's lifetime.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start gtk clipboard helper: %w", err)
	}
	// Reap adoption: we intentionally do not Wait; the helper self-exits on
	// ownership change. Release the handle so it does not become a zombie.
	if cmd.Process != nil {
		_ = cmd.Process.Release()
	}
	return nil
}
