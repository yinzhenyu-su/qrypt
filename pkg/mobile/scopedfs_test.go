package mobile

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

func TestMobileScopedFSBackendDirectUploadAndRead(t *testing.T) {
	tmp := t.TempDir()
	remote := filepath.Join(tmp, "scoped")
	if err := os.MkdirAll(remote, 0o755); err != nil {
		t.Fatal(err)
	}
	backend := newFakeScopedBackend(remote)
	if raw := SetScopedFSBackendJSON(backend); !jsonOK(raw) {
		t.Fatalf("SetScopedFSBackendJSON = %s", raw)
	}
	t.Cleanup(func() { ClearScopedFSBackendJSON() })

	configPath := filepath.Join(tmp, "qrypt.toml")
	if err := os.WriteFile(configPath, []byte(`
[[mounts]]
name = "phone"
type = "scopedfs"
[mounts.params]
root_token = "grant"
root_id = "root"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	var opened struct {
		OK   bool   `json:"ok"`
		Data string `json:"data"`
	}
	if err := json.Unmarshal([]byte(OpenJSON(configPath, testRuntimeJSON(tmp))), &opened); err != nil {
		t.Fatal(err)
	}
	if !opened.OK {
		t.Fatalf("OpenJSON = %+v, want ok", opened)
	}
	defer func() { _ = closeCore(opened.Data) }()

	mkdirRaw := MkdirJSON(opened.Data, "/phone/docs", 0)
	if !jsonOK(mkdirRaw) {
		t.Fatalf("MkdirJSON = %s", mkdirRaw)
	}
	source := filepath.Join(tmp, "source.txt")
	content := []byte("scopedfs mobile direct upload")
	if err := os.WriteFile(source, content, 0o644); err != nil {
		t.Fatal(err)
	}
	createRaw := CreateDirectUploadTaskJSON(opened.Data, `{"items":[{"item_id":"direct","source_path":`+fmt.Sprintf("%q", source)+`,"dest_path":"/phone/docs/uploaded.txt","size":`+fmt.Sprintf("%d", len(content))+`}]}`, 0)
	var created struct {
		OK   bool `json:"ok"`
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(createRaw), &created); err != nil {
		t.Fatal(err)
	}
	if !created.OK || created.Data.ID == "" {
		t.Fatalf("CreateDirectUploadTaskJSON = %s", createRaw)
	}
	task := waitMobileTaskState(t, opened.Data, created.Data.ID, "succeeded")
	if task.Type != "upload_stream_direct" || task.Progress.StagingBytesTotal != 0 {
		t.Fatalf("task = %+v, want direct scopedfs upload without staging bytes", task)
	}

	var openedFile struct {
		OK   bool   `json:"ok"`
		Data string `json:"data"`
	}
	if err := json.Unmarshal([]byte(OpenFileJSON(opened.Data, "/phone/docs/uploaded.txt", "")), &openedFile); err != nil {
		t.Fatal(err)
	}
	if !openedFile.OK {
		t.Fatalf("OpenFileJSON = %+v", openedFile)
	}
	defer CloseFileJSON(openedFile.Data)
	buf := make([]byte, len(content))
	n, err := ReadAtInto(openedFile.Data, 0, buf, 0)
	if err != nil {
		t.Fatalf("ReadAtInto: %v", err)
	}
	if n != len(content) || string(buf) != string(content) {
		t.Fatalf("read %d %q, want %q", n, buf, content)
	}
}

type fakeScopedBackend struct {
	root        string
	mu          sync.Mutex
	nextHandle  int64
	readHandles map[int64]*os.File
	writeFiles  map[int64]*os.File
	writePaths  map[int64]string
}

func newFakeScopedBackend(root string) *fakeScopedBackend {
	return &fakeScopedBackend{
		root:        root,
		readHandles: map[int64]*os.File{},
		writeFiles:  map[int64]*os.File{},
		writePaths:  map[int64]string{},
	}
}

func (b *fakeScopedBackend) Stat(rootToken, id string) (string, error) {
	info, err := os.Stat(b.path(id))
	if err != nil {
		return "", classifyFakeScopedError(err)
	}
	return b.entryJSON(b.path(id), info)
}

func (b *fakeScopedBackend) List(rootToken, parentID string) (string, error) {
	dir := b.path(parentID)
	items, err := os.ReadDir(dir)
	if err != nil {
		return "", classifyFakeScopedError(err)
	}
	out := make([]scopedEntryJSON, 0, len(items))
	for _, item := range items {
		info, err := item.Info()
		if err != nil {
			continue
		}
		out = append(out, b.entry(filepath.Join(dir, item.Name()), info))
	}
	raw, err := json.Marshal(out)
	return string(raw), err
}

func (b *fakeScopedBackend) OpenRead(rootToken, id string, offset int64) (int64, error) {
	file, err := os.Open(b.path(id))
	if err != nil {
		return 0, classifyFakeScopedError(err)
	}
	if offset > 0 {
		if _, err := file.Seek(offset, io.SeekStart); err != nil {
			file.Close()
			return 0, err
		}
	}
	handle := b.next()
	b.mu.Lock()
	b.readHandles[handle] = file
	b.mu.Unlock()
	return handle, nil
}

func (b *fakeScopedBackend) Read(handle int64, dst []byte) (int, error) {
	b.mu.Lock()
	file := b.readHandles[handle]
	b.mu.Unlock()
	if file == nil {
		return 0, os.ErrClosed
	}
	n, err := file.Read(dst)
	if errors.Is(err, io.EOF) {
		return n, nil
	}
	return n, err
}

func (b *fakeScopedBackend) CloseRead(handle int64) error {
	b.mu.Lock()
	file := b.readHandles[handle]
	delete(b.readHandles, handle)
	b.mu.Unlock()
	if file == nil {
		return nil
	}
	return file.Close()
}

func (b *fakeScopedBackend) Mkdir(rootToken, parentID, name string) (string, error) {
	path := filepath.Join(b.path(parentID), name)
	if err := os.Mkdir(path, 0o755); err != nil {
		return "", err
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	return b.entryJSON(path, info)
}

func (b *fakeScopedBackend) Move(rootToken, id, name string, isDir bool, dstParentID string) error {
	return os.Rename(b.path(id), filepath.Join(b.path(dstParentID), name))
}

func (b *fakeScopedBackend) Rename(rootToken, id string, isDir bool, newName string) error {
	return os.Rename(b.path(id), filepath.Join(filepath.Dir(b.path(id)), newName))
}

func (b *fakeScopedBackend) Remove(rootToken, id string, isDir bool) error {
	if isDir {
		return os.RemoveAll(b.path(id))
	}
	return os.Remove(b.path(id))
}

func (b *fakeScopedBackend) CreateWrite(rootToken, parentID, name string) (int64, error) {
	path := filepath.Join(b.path(parentID), name)
	file, err := os.Create(path)
	if err != nil {
		return 0, err
	}
	handle := b.next()
	b.mu.Lock()
	b.writeFiles[handle] = file
	b.writePaths[handle] = path
	b.mu.Unlock()
	return handle, nil
}

func (b *fakeScopedBackend) Write(handle int64, data []byte) (int, error) {
	b.mu.Lock()
	file := b.writeFiles[handle]
	b.mu.Unlock()
	if file == nil {
		return 0, os.ErrClosed
	}
	return file.Write(data)
}

func (b *fakeScopedBackend) CloseWrite(handle int64) (string, error) {
	b.mu.Lock()
	file := b.writeFiles[handle]
	path := b.writePaths[handle]
	delete(b.writeFiles, handle)
	delete(b.writePaths, handle)
	b.mu.Unlock()
	if file == nil {
		return "", os.ErrClosed
	}
	if err := file.Close(); err != nil {
		return "", err
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	return b.entryJSON(path, info)
}

func (b *fakeScopedBackend) AbortWrite(handle int64) error {
	b.mu.Lock()
	file := b.writeFiles[handle]
	path := b.writePaths[handle]
	delete(b.writeFiles, handle)
	delete(b.writePaths, handle)
	b.mu.Unlock()
	if file != nil {
		_ = file.Close()
	}
	if path != "" {
		return os.Remove(path)
	}
	return nil
}

func (b *fakeScopedBackend) next() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.nextHandle++
	return b.nextHandle
}

func (b *fakeScopedBackend) path(id string) string {
	if id == "" || id == "0" || id == "/" || id == "root" {
		return b.root
	}
	return id
}

func (b *fakeScopedBackend) entryJSON(path string, info os.FileInfo) (string, error) {
	raw, err := json.Marshal(b.entry(path, info))
	return string(raw), err
}

func (b *fakeScopedBackend) entry(path string, info os.FileInfo) scopedEntryJSON {
	parent := filepath.Dir(path)
	if path == b.root {
		parent = ""
	}
	return scopedEntryJSON{
		ID:        path,
		ParentID:  parent,
		Name:      filepath.Base(path),
		IsDir:     info.IsDir(),
		Size:      info.Size(),
		ModTime:   info.ModTime().Format(timeFormatRFC3339),
		UpdatedAt: info.ModTime().Format(timeFormatRFC3339),
	}
}

func classifyFakeScopedError(err error) error {
	if errors.Is(err, os.ErrNotExist) {
		return drive.ErrNotFound
	}
	return err
}

const timeFormatRFC3339 = "2006-01-02T15:04:05Z07:00"

var _ ScopedFSBackend = (*fakeScopedBackend)(nil)
