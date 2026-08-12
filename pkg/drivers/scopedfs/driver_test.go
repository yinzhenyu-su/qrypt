package scopedfs

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

func TestDriverUsesRegisteredBackend(t *testing.T) {
	backendName := "test-" + t.Name()
	backend := newLocalBackend(t.TempDir())
	if err := RegisterBackend(backendName, backend); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { UnregisterBackend(backendName) })

	driver, err := New(Options{Backend: backendName, RootToken: "grant", RootID: backend.rootID})
	if err != nil {
		t.Fatal(err)
	}
	if err := driver.Init(context.Background()); err != nil {
		t.Fatal(err)
	}

	dir, err := driver.Mkdir(context.Background(), "", "docs")
	if err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	entry, err := driver.PutSource(context.Background(), drive.UploadRequest{
		ParentID: dir.ID,
		Name:     "hello.txt",
		Source:   drive.NewBytesReadOnlyFileSource([]byte("hello scoped storage")),
	})
	if err != nil {
		t.Fatalf("PutSource: %v", err)
	}
	if entry.Name != "hello.txt" || entry.ParentID != dir.ID || entry.Size != int64(len("hello scoped storage")) {
		t.Fatalf("uploaded entry = %+v", entry)
	}

	resolved, err := driver.ResolvePath(context.Background(), "/docs/hello.txt")
	if err != nil {
		t.Fatalf("ResolvePath: %v", err)
	}
	if resolved != entry.ID {
		t.Fatalf("ResolvePath id = %q, want %q", resolved, entry.ID)
	}
	rc, err := driver.Read(context.Background(), entry, 6, 6)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	got, err := io.ReadAll(rc)
	_ = rc.Close()
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(got) != "scoped" {
		t.Fatalf("read = %q, want scoped", got)
	}
	rc, err = driver.Read(context.Background(), entry, 0, 0)
	if err != nil {
		t.Fatalf("Read full: %v", err)
	}
	got, err = io.ReadAll(rc)
	_ = rc.Close()
	if err != nil {
		t.Fatalf("ReadAll full: %v", err)
	}
	if string(got) != "hello scoped storage" {
		t.Fatalf("full read = %q, want full content", got)
	}
	if err := driver.Rename(context.Background(), entry, "renamed.txt"); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if _, err := driver.ResolvePath(context.Background(), "/docs/renamed.txt"); err != nil {
		t.Fatalf("ResolvePath after rename: %v", err)
	}
}

func TestDriverMoveAndRemove(t *testing.T) {
	backendName := "test-" + t.Name()
	backend := newLocalBackend(t.TempDir())
	if err := RegisterBackend(backendName, backend); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { UnregisterBackend(backendName) })
	driver, err := New(Options{Backend: backendName, RootToken: "grant", RootID: backend.rootID})
	if err != nil {
		t.Fatal(err)
	}

	srcDir, err := driver.Mkdir(context.Background(), "", "src")
	if err != nil {
		t.Fatal(err)
	}
	dstDir, err := driver.Mkdir(context.Background(), "", "dst")
	if err != nil {
		t.Fatal(err)
	}
	entry, err := driver.PutSource(context.Background(), drive.UploadRequest{
		ParentID: srcDir.ID,
		Name:     "file.txt",
		Source:   drive.NewBytesReadOnlyFileSource([]byte("move me")),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := driver.Move(context.Background(), entry, dstDir.ID); err != nil {
		t.Fatalf("Move: %v", err)
	}
	movedID, err := driver.ResolvePath(context.Background(), "/dst/file.txt")
	if err != nil {
		t.Fatalf("ResolvePath moved: %v", err)
	}
	moved, err := driver.Stat(context.Background(), drive.Entry{ID: movedID})
	if err != nil {
		t.Fatalf("Stat moved: %v", err)
	}
	if err := driver.Remove(context.Background(), moved); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := driver.ResolvePath(context.Background(), "/dst/file.txt"); !errors.Is(err, drive.ErrNotFound) {
		t.Fatalf("ResolvePath removed err = %v, want not found", err)
	}
}

func TestDriverCapabilities(t *testing.T) {
	backendName := "test-" + t.Name()
	backend := newLocalBackend(t.TempDir())
	if err := RegisterBackend(backendName, backend); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { UnregisterBackend(backendName) })
	driver, err := New(Options{Backend: backendName, RootToken: "grant", RootID: backend.rootID})
	if err != nil {
		t.Fatal(err)
	}
	if drive.HasCapability(driver, drive.CapabilityResumableUploader) {
		t.Fatalf("scopedfs must not claim resumable upload")
	}
	if drive.HasCapability(driver, drive.CapabilityMtime) {
		t.Fatalf("scopedfs must not claim mtime")
	}
	if drive.HasCapability(driver, drive.CapabilitySpace) {
		t.Fatalf("scopedfs must not claim space")
	}
	if !drive.HasCapability(driver, drive.CapabilitySourceUploader) || !drive.HasCapability(driver, drive.CapabilityWriter) {
		t.Fatalf("capabilities = %+v, want direct source upload and writer", driver.Capabilities())
	}
	if violations := drive.CheckUnsupportedCapabilities(context.Background(), driver); len(violations) != 0 {
		t.Fatalf("negative capability violations = %+v", violations)
	}
}

func TestMissingBackendFailsAtInit(t *testing.T) {
	driver, err := New(Options{Backend: "missing-" + t.Name(), RootToken: "grant"})
	if err != nil {
		t.Fatal(err)
	}
	if err := driver.Init(context.Background()); err == nil {
		t.Fatal("Init succeeded with missing backend, want error")
	}
}

type localBackend struct {
	root   string
	rootID string
}

func newLocalBackend(root string) *localBackend {
	return &localBackend{root: root, rootID: root}
}

func (b *localBackend) Stat(ctx context.Context, rootToken, id string) (drive.Entry, error) {
	path := b.path(id)
	info, err := os.Stat(path)
	if err != nil {
		return drive.Entry{}, classifyFakeLocalError(err)
	}
	return b.entry(path, info), nil
}

func (b *localBackend) List(ctx context.Context, rootToken, parentID string) ([]drive.Entry, error) {
	dir := b.path(parentID)
	items, err := os.ReadDir(dir)
	if err != nil {
		return nil, classifyFakeLocalError(err)
	}
	entries := make([]drive.Entry, 0, len(items))
	for _, item := range items {
		info, err := item.Info()
		if err != nil {
			continue
		}
		entries = append(entries, b.entry(filepath.Join(dir, item.Name()), info))
	}
	return entries, nil
}

func (b *localBackend) OpenRead(ctx context.Context, rootToken, id string, offset int64) (io.ReadCloser, error) {
	file, err := os.Open(b.path(id))
	if err != nil {
		return nil, classifyFakeLocalError(err)
	}
	if offset > 0 {
		if _, err := file.Seek(offset, io.SeekStart); err != nil {
			file.Close()
			return nil, err
		}
	}
	return file, nil
}

func (b *localBackend) Mkdir(ctx context.Context, rootToken, parentID, name string) (drive.Entry, error) {
	path := filepath.Join(b.path(parentID), name)
	if err := os.Mkdir(path, 0o755); err != nil {
		return drive.Entry{}, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return drive.Entry{}, err
	}
	return b.entry(path, info), nil
}

func (b *localBackend) Move(ctx context.Context, rootToken string, entry drive.Entry, dstParentID string) error {
	return os.Rename(b.path(entry.ID), filepath.Join(b.path(dstParentID), entry.Name))
}

func (b *localBackend) Rename(ctx context.Context, rootToken string, entry drive.Entry, newName string) error {
	return os.Rename(b.path(entry.ID), filepath.Join(filepath.Dir(b.path(entry.ID)), newName))
}

func (b *localBackend) Remove(ctx context.Context, rootToken string, entry drive.Entry) error {
	if entry.IsDir {
		return os.RemoveAll(b.path(entry.ID))
	}
	return os.Remove(b.path(entry.ID))
}

func (b *localBackend) CreateWrite(ctx context.Context, rootToken, parentID, name string) (WriteHandle, error) {
	path := filepath.Join(b.path(parentID), name)
	file, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	return &localWriteHandle{backend: b, path: path, file: file}, nil
}

func (b *localBackend) path(id string) string {
	if id == "" || id == "0" || id == "/" || id == "root" {
		return b.root
	}
	return id
}

func (b *localBackend) entry(path string, info os.FileInfo) drive.Entry {
	modTime := info.ModTime()
	parent := filepath.Dir(path)
	if path == b.root {
		parent = ""
	}
	return drive.Entry{
		ID:        path,
		ParentID:  parent,
		Name:      filepath.Base(path),
		IsDir:     info.IsDir(),
		Size:      info.Size(),
		ModTime:   modTime,
		CreatedAt: modTime,
		UpdatedAt: modTime,
	}
}

type localWriteHandle struct {
	backend *localBackend
	path    string
	file    *os.File
	closed  bool
}

func (h *localWriteHandle) Write(p []byte) (int, error) {
	if h.closed {
		return 0, os.ErrClosed
	}
	return h.file.Write(p)
}

func (h *localWriteHandle) Close() (drive.Entry, error) {
	if h.closed {
		return drive.Entry{}, os.ErrClosed
	}
	h.closed = true
	if err := h.file.Close(); err != nil {
		return drive.Entry{}, err
	}
	info, err := os.Stat(h.path)
	if err != nil {
		return drive.Entry{}, err
	}
	return h.backend.entry(h.path, info), nil
}

func (h *localWriteHandle) Abort() error {
	if h.closed {
		return nil
	}
	h.closed = true
	_ = h.file.Close()
	return os.Remove(h.path)
}

func classifyFakeLocalError(err error) error {
	if errors.Is(err, os.ErrNotExist) {
		return drive.ErrNotFound
	}
	return err
}

var _ Backend = (*localBackend)(nil)
var _ WriteHandle = (*localWriteHandle)(nil)
