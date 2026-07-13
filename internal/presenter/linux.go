//go:build linux

package presenter

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"bazil.org/fuse"
	"bazil.org/fuse/fs"
	"clipboard-sync/cache"
	"clipboard-sync/protocol"
)

type linuxPresenter struct {
	root     string
	accessor TransferAccessor
	conn     *fuse.Conn
	mu       sync.RWMutex
	state    cache.RemoteClipboardState
	names    map[string]protocol.FileMeta
}

func New(root string, accessor TransferAccessor) (Presenter, error) {
	p := &linuxPresenter{
		root:     root,
		accessor: accessor,
		names:    make(map[string]protocol.FileMeta),
	}
	if err := p.remount(); err != nil {
		return nil, err
	}
	return p, nil
}

func (p *linuxPresenter) Root() (fs.Node, error) {
	return &rootNode{presenter: p}, nil
}

func (p *linuxPresenter) PresentRemoteFiles(state cache.RemoteClipboardState) (Presentation, error) {
	if err := p.ensureMounted(); err != nil {
		return Presentation{}, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.state = state
	p.names = map[string]protocol.FileMeta{}
	used := map[string]int{}
	for _, file := range state.Files {
		base := sanitizeRelativeName(file.Name)
		name := base
		if count := used[base]; count > 0 {
			name = fmt.Sprintf("%d_%s", count, base)
		}
		used[base]++
		p.names[name] = file
	}
	paths := topLevelPaths(p.root, p.names)
	sort.Strings(paths)
	return Presentation{Paths: paths}, nil
}

func (p *linuxPresenter) IsManagedPaths(paths []string) bool {
	if len(paths) == 0 {
		return false
	}
	for _, path := range paths {
		clean := filepath.Clean(path)
		if clean == p.root {
			continue
		}
		if !strings.HasPrefix(clean, p.root+string(os.PathSeparator)) {
			return false
		}
	}
	return true
}

func (p *linuxPresenter) Close() error {
	if p.conn == nil {
		return nil
	}
	_ = fuse.Unmount(p.root)
	return p.conn.Close()
}

func (p *linuxPresenter) ensureMounted() error {
	if _, err := os.ReadDir(p.root); err == nil {
		return nil
	} else if !errors.Is(err, syscall.ENOTCONN) {
		return fmt.Errorf("check mount dir %s: %w", p.root, err)
	}
	if err := p.remount(); err != nil {
		return fmt.Errorf("recover broken fuse mount %s: %w", p.root, err)
	}
	return nil
}

func (p *linuxPresenter) remount() error {
	if err := forceUnmountIfStale(p.root); err != nil {
		return err
	}
	if err := ensureMountRootDir(p.root); err != nil {
		return err
	}
	conn, err := fuse.Mount(
		p.root,
		fuse.FSName("clipboard-sync"),
		fuse.Subtype("clipboard-sync"),
	)
	if err != nil {
		return fmt.Errorf("mount fuse at %s: %w", p.root, err)
	}
	p.mu.Lock()
	oldConn := p.conn
	p.conn = conn
	p.mu.Unlock()
	if oldConn != nil {
		_ = oldConn.Close()
	}
	go func() {
		_ = fs.Serve(conn, p)
	}()
	time.Sleep(150 * time.Millisecond)
	if _, err := os.ReadDir(p.root); err != nil {
		_ = fuse.Unmount(p.root)
		_ = conn.Close()
		return fmt.Errorf("verify fuse mount %s: %w", p.root, err)
	}
	return nil
}

func forceUnmountIfStale(root string) error {
	if info, err := os.Lstat(root); err == nil && info.Mode()&os.ModeDir == 0 {
		if err := os.Remove(root); err != nil {
			return fmt.Errorf("remove non-directory mount path %s: %w", root, err)
		}
	}
	if _, err := os.ReadDir(root); err == nil {
		return nil
	} else if !errors.Is(err, syscall.ENOTCONN) {
		return nil
	}
	if err := fuse.Unmount(root); err != nil {
		return fmt.Errorf("unmount stale fuse mount %s: %w", root, err)
	}
	return nil
}

func ensureMountRootDir(root string) error {
	if err := os.RemoveAll(root); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("reset mount dir %s: %w", root, err)
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return fmt.Errorf("create mount dir %s: %w", root, err)
	}
	return nil
}

func (p *linuxPresenter) snapshot() (cache.RemoteClipboardState, map[string]protocol.FileMeta) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	files := make(map[string]protocol.FileMeta, len(p.names))
	for name, meta := range p.names {
		files[name] = meta
	}
	return p.state, files
}

type rootNode struct {
	presenter *linuxPresenter
}

func (n *rootNode) Attr(_ context.Context, attr *fuse.Attr) error {
	attr.Mode = os.ModeDir | 0o755
	setCurrentOwner(attr)
	return nil
}

func (n *rootNode) ReadDirAll(_ context.Context) ([]fuse.Dirent, error) {
	_, files := n.presenter.snapshot()
	entries := dirEntriesForPrefix(files, "")
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	return entries, nil
}

func (n *rootNode) Lookup(_ context.Context, name string) (fs.Node, error) {
	state, files := n.presenter.snapshot()
	meta, ok := files[name]
	if !ok {
		if hasChildPrefix(files, name) {
			return &dirNode{presenter: n.presenter, prefix: name}, nil
		}
		return nil, syscall.ENOENT
	}
	return &fileNode{presenter: n.presenter, state: state, meta: meta}, nil
}

type dirNode struct {
	presenter *linuxPresenter
	prefix    string
}

func (n *dirNode) Attr(_ context.Context, attr *fuse.Attr) error {
	attr.Mode = os.ModeDir | 0o755
	setCurrentOwner(attr)
	return nil
}

func (n *dirNode) ReadDirAll(_ context.Context) ([]fuse.Dirent, error) {
	_, files := n.presenter.snapshot()
	entries := dirEntriesForPrefix(files, n.prefix)
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	return entries, nil
}

func (n *dirNode) Lookup(_ context.Context, name string) (fs.Node, error) {
	state, files := n.presenter.snapshot()
	child := n.prefix + "/" + name
	if meta, ok := files[child]; ok {
		return &fileNode{presenter: n.presenter, state: state, meta: meta}, nil
	}
	if hasChildPrefix(files, child) {
		return &dirNode{presenter: n.presenter, prefix: child}, nil
	}
	return nil, syscall.ENOENT
}

type fileNode struct {
	presenter *linuxPresenter
	state     cache.RemoteClipboardState
	meta      protocol.FileMeta
}

func (n *fileNode) Attr(_ context.Context, attr *fuse.Attr) error {
	attr.Mode = 0o644
	attr.Size = uint64(n.meta.Size)
	attr.Mtime = time.Unix(n.meta.Modified, 0)
	attr.Ctime = attr.Mtime
	setCurrentOwner(attr)
	return nil
}

func (n *fileNode) Open(_ context.Context, _ *fuse.OpenRequest, resp *fuse.OpenResponse) (fs.Handle, error) {
	resp.Flags |= fuse.OpenDirectIO
	return n, nil
}

func (n *fileNode) Read(_ context.Context, req *fuse.ReadRequest, resp *fuse.ReadResponse) error {
	if req.Offset >= n.meta.Size {
		resp.Data = nil
		return nil
	}
	chunk, err := n.presenter.accessor.ReadRemoteFile(n.state, n.meta, req.Offset, req.Size)
	if err != nil && err != io.EOF {
		return err
	}
	resp.Data = chunk
	return nil
}

func sanitizeRelativeName(name string) string {
	clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(name)))
	clean = strings.TrimPrefix(clean, "/")
	if strings.TrimSpace(clean) == "" || clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "clipboard-sync-file"
	}
	return clean
}

func dirEntriesForPrefix(files map[string]protocol.FileMeta, prefix string) []fuse.Dirent {
	seen := map[string]fuse.Dirent{}
	for name := range files {
		rest := name
		if prefix != "" {
			if !strings.HasPrefix(name, prefix+"/") {
				continue
			}
			rest = strings.TrimPrefix(name, prefix+"/")
		}
		part, _, hasChild := strings.Cut(rest, "/")
		if part == "" {
			continue
		}
		entryType := fuse.DT_File
		if hasChild {
			entryType = fuse.DT_Dir
		}
		seen[part] = fuse.Dirent{Name: part, Type: entryType}
	}
	entries := make([]fuse.Dirent, 0, len(seen))
	for _, entry := range seen {
		entries = append(entries, entry)
	}
	return entries
}

func hasChildPrefix(files map[string]protocol.FileMeta, prefix string) bool {
	for name := range files {
		if strings.HasPrefix(name, prefix+"/") {
			return true
		}
	}
	return false
}

func topLevelPaths(root string, files map[string]protocol.FileMeta) []string {
	seen := map[string]struct{}{}
	for name := range files {
		part, _, _ := strings.Cut(name, "/")
		if part != "" {
			seen[part] = struct{}{}
		}
	}
	paths := make([]string, 0, len(seen))
	for name := range seen {
		paths = append(paths, filepath.Join(root, name))
	}
	return paths
}

func setCurrentOwner(attr *fuse.Attr) {
	attr.Uid = uint32(os.Getuid())
	attr.Gid = uint32(os.Getgid())
}
