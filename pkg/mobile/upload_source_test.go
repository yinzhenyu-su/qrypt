package mobile

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeOpener is a test implementation of UploadSourceOpener backed by an
// in-memory byte pool. It mirrors the Android SAF bridge: Open(token, offset)
// creates a fresh reader positioned at offset, Read advances it, Close
// releases it. rawFD, when set, makes OpenRaw hand over a raw fd (first
// element) starting at the given offset (second element).
type fakeOpener struct {
	mu      sync.Mutex
	pool    map[string][]byte
	handles map[int64]*fakeSource
	next    atomic.Int64
	opens   atomic.Int64
	rawFD   func(token string) (*RawSource, error)
}

type fakeSource struct {
	data   []byte
	offset int64
}

func newFakeOpener() *fakeOpener {
	return &fakeOpener{
		pool:    map[string][]byte{},
		handles: map[int64]*fakeSource{},
	}
}

func (f *fakeOpener) add(token string, data []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pool[token] = data
}

func (f *fakeOpener) Open(token string, offset int64) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	data, ok := f.pool[token]
	if !ok {
		return 0, fmt.Errorf("fake opener: unknown token %q", token)
	}
	if offset < 0 || offset > int64(len(data)) {
		return 0, fmt.Errorf("fake opener: offset %d out of range for %q (len %d)", offset, token, len(data))
	}
	f.opens.Add(1)
	handle := f.next.Add(1)
	f.handles[handle] = &fakeSource{data: data, offset: offset}
	return handle, nil
}

func (f *fakeOpener) Read(handle int64, size int) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	src, ok := f.handles[handle]
	if !ok {
		return nil, fmt.Errorf("fake opener: unknown handle %d", handle)
	}
	if src.offset >= int64(len(src.data)) {
		return []byte{}, nil
	}
	end := src.offset + int64(size)
	if end > int64(len(src.data)) {
		end = int64(len(src.data))
	}
	data := append([]byte(nil), src.data[src.offset:end]...)
	src.offset = end
	return data, nil
}

func (f *fakeOpener) Close(handle int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.handles[handle]; !ok {
		return fmt.Errorf("fake opener: unknown handle %d", handle)
	}
	delete(f.handles, handle)
	return nil
}

func (f *fakeOpener) OpenRawSource(token string) (*RawSource, error) {
	f.mu.Lock()
	raw := f.rawFD
	f.mu.Unlock()
	if raw == nil {
		return nil, fmt.Errorf("fake opener: raw fd unsupported for %q", token)
	}
	return raw(token)
}

func TestMobileSourceReaderRejectsOversizedRead(t *testing.T) {
	reader := &mobileSourceReader{opener: oversizedReadOpener{}, handle: 1}
	buf := make([]byte, 4)
	n, err := reader.Read(buf)
	if err == nil {
		t.Fatal("Read succeeded, want oversized read error")
	}
	if n != 0 {
		t.Fatalf("Read n = %d, want 0", n)
	}
}

type oversizedReadOpener struct{}

func (oversizedReadOpener) Open(string, int64) (int64, error) { return 1, nil }
func (oversizedReadOpener) Read(int64, int) ([]byte, error) {
	return []byte("too-large"), nil
}
func (oversizedReadOpener) Close(int64) error { return nil }
func (oversizedReadOpener) OpenRawSource(string) (*RawSource, error) {
	return nil, fmt.Errorf("oversized opener: raw fd unsupported")
}

// directUploadTestConfig returns a localfs config plus runtime for direct
// upload tests.
func directUploadTestConfig(t *testing.T, tmp, remote string) string {
	t.Helper()
	configPath := filepath.Join(tmp, "qrypt.toml")
	content := fmt.Sprintf(`
[[mounts]]
name = "quark"
type = "localfs"
[mounts.params]
root_path = %q

[upload]
upload_delay = "10ms"
`, remote)
	if err := os.WriteFile(configPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return configPath
}

func TestMobileSetUploadSourceOpenerDirectUpload(t *testing.T) {
	tmp := t.TempDir()
	remote := filepath.Join(tmp, "remote")
	if err := os.MkdirAll(remote, 0o755); err != nil {
		t.Fatal(err)
	}
	configPath := directUploadTestConfig(t, tmp, remote)

	opener := newFakeOpener()
	content := []byte("direct source upload through the app opener")
	opener.add("content://fake/download/photo.jpg", content)

	coreID, err := openCore(configPath, testRuntimeJSON(tmp))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = closeCore(coreID) }()

	// The opener must be installable after the session is open.
	if raw := SetUploadSourceOpenerJSON(opener); !jsonOK(raw) {
		t.Fatalf("SetUploadSourceOpenerJSON = %s", raw)
	}
	defer ClearUploadSourceOpenerJSON()

	raw := CreateDirectUploadTaskJSON(coreID, `{"items":[{"item_id":"direct-1","source_path":"content://fake/download/photo.jpg","dest_path":"/quark/photo.jpg","size":`+fmt.Sprintf("%d", len(content))+`}]}`, 0)
	var created struct {
		OK   bool       `json:"ok"`
		Data mobileTask `json:"data"`
	}
	if err := json.Unmarshal([]byte(raw), &created); err != nil {
		t.Fatal(err)
	}
	if !created.OK {
		t.Fatalf("CreateDirectUploadTaskJSON = %s", raw)
	}

	item := waitMobileTaskState(t, coreID, created.Data.ID, "succeeded")
	if len(item.Result.Items) != 1 || item.Result.Items[0].Phase != "direct" {
		t.Fatalf("task result = %+v, want direct result", item.Result.Items)
	}
	if data, err := os.ReadFile(filepath.Join(remote, "photo.jpg")); err != nil || string(data) != string(content) {
		t.Fatalf("remote data = %q err=%v, want %q", data, err, content)
	}
	// Hashing pass + upload pass must each reopen the source.
	if opens := opener.opens.Load(); opens < 2 {
		t.Fatalf("source opened %d times, want >= 2 (hash + upload)", opens)
	}
}

func TestMobileDirectUploadWithoutOpenerFails(t *testing.T) {
	tmp := t.TempDir()
	remote := filepath.Join(tmp, "remote")
	if err := os.MkdirAll(remote, 0o755); err != nil {
		t.Fatal(err)
	}
	configPath := directUploadTestConfig(t, tmp, remote)
	coreID, err := openCore(configPath, testRuntimeJSON(tmp))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = closeCore(coreID) }()
	SetUploadSourceOpenerJSON(nilOpener{})
	defer ClearUploadSourceOpenerJSON()

	raw := CreateDirectUploadTaskJSON(coreID, `{"items":[{"item_id":"direct-1","source_path":"content://unregistered/any","dest_path":"/quark/x.bin","size":3}]}`, 0)
	var created struct {
		OK   bool       `json:"ok"`
		Data mobileTask `json:"data"`
	}
	if err := json.Unmarshal([]byte(raw), &created); err != nil {
		t.Fatal(err)
	}
	if !created.OK {
		t.Fatalf("CreateDirectUploadTaskJSON = %s", raw)
	}
	// The task must fail cleanly (hashing cannot open the source).
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		var got struct {
			OK   bool       `json:"ok"`
			Data mobileTask `json:"data"`
		}
		if err := json.Unmarshal([]byte(GetTaskJSON(coreID, created.Data.ID)), &got); err != nil {
			t.Fatal(err)
		}
		if got.Data.State == "failed" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("direct upload task did not fail without an opener")
}

type nilOpener struct{}

func (nilOpener) Open(string, int64) (int64, error) { return 0, io.EOF }
func (nilOpener) Read(int64, int) ([]byte, error)   { return nil, io.EOF }
func (nilOpener) Close(int64) error                 { return nil }
func (nilOpener) OpenRawSource(string) (*RawSource, error) {
	return nil, fmt.Errorf("nil opener: raw fd unsupported")
}

func jsonOK(raw string) bool {
	var envelope struct {
		OK bool `json:"ok"`
	}
	return json.Unmarshal([]byte(raw), &envelope) == nil && envelope.OK
}

func TestMobileProviderPrefersRawFD(t *testing.T) {
	path := filepath.Join(t.TempDir(), "src.bin")
	content := []byte("raw fd direct read payload with enough length to offset")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	fd := dupFD(t, f)
	// Close the original now: the handed-over fd (a copy) is owned by the
	// provider reader, and a lingering os.File finalizer must never close a
	// possibly-reused descriptor number after the reader closes it.
	f.Close()

	var calls atomic.Int64
	opener := newFakeOpener()
	opener.rawFD = func(token string) (*RawSource, error) {
		calls.Add(1)
		if token != "fd://src" {
			return nil, fmt.Errorf("unknown token %q", token)
		}
		// fd starts at offset 3 in "file" terms; the requested read offset is
		// relative to the source, so the combined seek lands at 3+7.
		return &RawSource{FD: int64(fd), StartOffset: 3}, nil
	}
	provider := &mobileUploadSourceProvider{opener: opener}

	reader, err := provider.OpenUploadSource(context.Background(), "fd://src", 7)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("OpenRaw calls = %d, want 1 (no per-read JNI)", calls.Load())
	}
	if string(got) != string(content[10:]) {
		t.Fatalf("fd read = %q, want %q", got, content[10:])
	}
}

// dupFD is implemented per-platform: syscall.Dup on Unix, DuplicateHandle on
// Windows (which has no syscall.Dup). See dupfd_unix_test.go / dupfd_windows_test.go.

func TestMobileProviderFallsBackWhenRawFDUnsupported(t *testing.T) {
	opener := newFakeOpener()
	content := []byte("fallback content")
	opener.add("tok", content)
	opener.rawFD = func(string) (*RawSource, error) {
		return nil, fmt.Errorf("provider does not support raw fd")
	}
	provider := &mobileUploadSourceProvider{opener: opener}

	reader, err := provider.OpenUploadSource(context.Background(), "tok", 3)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	got, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(content[3:]) {
		t.Fatalf("fallback read = %q, want %q", got, content[3:])
	}
	if opens := opener.opens.Load(); opens != 1 {
		t.Fatalf("handle opens = %d, want 1 (fallback path used)", opens)
	}
}

func TestMobileProviderFallsBackWhenRawFDNotSeekable(t *testing.T) {
	r, _, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	opener := newFakeOpener()
	content := []byte("pipe fd fallback payload")
	opener.add("pipe", content)
	opener.rawFD = func(token string) (*RawSource, error) {
		return &RawSource{FD: int64(r.Fd())}, nil
	}
	provider := &mobileUploadSourceProvider{opener: opener}

	// A pipe cannot be seeked to the requested offset, so the provider must
	// close it and use the handle-based opener at the same offset.
	reader, err := provider.OpenUploadSource(context.Background(), "pipe", 5)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	got, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(content[5:]) {
		t.Fatalf("fallback read = %q, want %q", got, content[5:])
	}
}

func TestMobileDirectUploadViaRawFD(t *testing.T) {
	tmp := t.TempDir()
	remote := filepath.Join(tmp, "remote")
	if err := os.MkdirAll(remote, 0o755); err != nil {
		t.Fatal(err)
	}
	configPath := directUploadTestConfig(t, tmp, remote)

	content := []byte("direct upload through the raw fd channel: " + strings.Repeat("z", 300*1024))
	srcPath := filepath.Join(tmp, "src.bin")
	if err := os.WriteFile(srcPath, content, 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(srcPath)
	if err != nil {
		t.Fatal(err)
	}
	fd := dupFD(t, f)
	f.Close()

	opener := newFakeOpener()
	opener.rawFD = func(token string) (*RawSource, error) {
		if token != "content://fake/fd.bin" {
			return nil, fmt.Errorf("unknown token %q", token)
		}
		return &RawSource{FD: int64(fd)}, nil
	}

	coreID, err := openCore(configPath, testRuntimeJSON(tmp))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = closeCore(coreID) }()
	if raw := SetUploadSourceOpenerJSON(opener); !jsonOK(raw) {
		t.Fatalf("SetUploadSourceOpenerJSON = %s", raw)
	}
	defer ClearUploadSourceOpenerJSON()

	raw := CreateDirectUploadTaskJSON(coreID, `{"items":[{"item_id":"fd-1","source_path":"content://fake/fd.bin","dest_path":"/quark/fd.bin","size":`+fmt.Sprintf("%d", len(content))+`}]}`, 0)
	var created struct {
		OK   bool       `json:"ok"`
		Data mobileTask `json:"data"`
	}
	if err := json.Unmarshal([]byte(raw), &created); err != nil {
		t.Fatal(err)
	}
	if !created.OK {
		t.Fatalf("CreateDirectUploadTaskJSON = %s", raw)
	}

	waitMobileTaskState(t, coreID, created.Data.ID, "succeeded")
	if data, err := os.ReadFile(filepath.Join(remote, "fd.bin")); err != nil || string(data) != string(content) {
		t.Fatalf("remote data = %q err=%v, want source content", data, err)
	}
}

// TestMobileDirectUploadEncryptedReadBack verifies that a direct upload into
// an encrypted mount produces the same ciphertext format as the staging path:
// the file must decrypt back to the original plaintext.
func TestMobileDirectUploadEncryptedReadBack(t *testing.T) {
	tmp := t.TempDir()
	remote := filepath.Join(tmp, "remote")
	if err := os.MkdirAll(remote, 0o755); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(tmp, "qrypt.toml")
	config := fmt.Sprintf(`
[[mounts]]
name = "quark"
type = "localfs"
[mounts.params]
root_path = %q

[mounts.encryption]
password = "test-password"
filename_encryption = "standard"
filename_encoding = "base32"

[upload]
upload_delay = "10ms"
`, remote)
	if err := os.WriteFile(configPath, []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}

	opener := newFakeOpener()
	content := []byte("encrypted direct upload round trip: " + string(make([]byte, 300*1024)))
	opener.add("content://fake/photo.jpg", content)

	coreID, err := openCore(configPath, testRuntimeJSON(tmp))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = closeCore(coreID) }()
	if raw := SetUploadSourceOpenerJSON(opener); !jsonOK(raw) {
		t.Fatalf("SetUploadSourceOpenerJSON = %s", raw)
	}
	defer ClearUploadSourceOpenerJSON()

	// Direct upload.
	raw := CreateDirectUploadTaskJSON(coreID, `{"items":[{"item_id":"d1","source_path":"content://fake/photo.jpg","dest_path":"/quark/photo.jpg","size":`+fmt.Sprintf("%d", len(content))+`}]}`, 0)
	var created struct {
		OK   bool       `json:"ok"`
		Data mobileTask `json:"data"`
	}
	if err := json.Unmarshal([]byte(raw), &created); err != nil {
		t.Fatal(err)
	}
	if !created.OK {
		t.Fatalf("CreateDirectUploadTaskJSON = %s", raw)
	}
	waitMobileTaskState(t, coreID, created.Data.ID, "succeeded")

	// Staging upload of the same content as a control group.
	raw2 := CreateUploadTaskJSON(coreID, `{"items":[{"item_id":"s1","dest_path":"/quark/staging.jpg","size":`+fmt.Sprintf("%d", len(content))+`}]}`, 0)
	if err := json.Unmarshal([]byte(raw2), &created); err != nil {
		t.Fatal(err)
	}
	if !created.OK {
		t.Fatalf("CreateUploadTaskJSON = %s", raw2)
	}
	itemID := "s1"
	openRaw := OpenUploadItemJSON(coreID, created.Data.ID, itemID, 5000)
	var opened struct {
		OK   bool   `json:"ok"`
		Data string `json:"data"`
	}
	if err := json.Unmarshal([]byte(openRaw), &opened); err != nil {
		t.Fatal(err)
	}
	if !opened.OK {
		t.Fatalf("OpenUploadItemJSON = %s", openRaw)
	}
	for off := 0; off < len(content); off += 64 * 1024 {
		end := min(off+64*1024, len(content))
		n, err := WriteUploadItem(opened.Data, content[off:end], 5000)
		if err != nil {
			t.Fatal(err)
		}
		if n != end-off {
			t.Fatalf("short write: %d/%d", n, end-off)
		}
	}
	if raw := CommitUploadItemJSON(opened.Data, 5000); !jsonOK(raw) {
		t.Fatalf("CommitUploadItemJSON = %s", raw)
	}
	waitMobileTaskState(t, coreID, created.Data.ID, "succeeded")

	// Read both files back through the decryption path.
	for _, name := range []string{"photo.jpg", "staging.jpg"} {
		openRaw := OpenFileJSON(coreID, "/quark/"+name, "")
		var file struct {
			OK   bool   `json:"ok"`
			Data string `json:"data"`
		}
		if err := json.Unmarshal([]byte(openRaw), &file); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !file.OK {
			t.Fatalf("%s: OpenFileJSON = %s", name, openRaw)
		}
		buf := make([]byte, len(content))
		read, err := ReadAtInto(file.Data, 0, buf, 5000)
		if err != nil {
			t.Fatalf("%s: ReadAtInto: %v", name, err)
		}
		_ = CloseFileJSON(file.Data)
		if read != len(content) {
			t.Fatalf("%s: read %d bytes, want %d", name, read, len(content))
		}
		if string(buf) != string(content) {
			t.Fatalf("%s: read back differs from source (%d bytes mismatch)", name, len(content))
		}
	}
}
