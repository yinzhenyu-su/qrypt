package mobile

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeOpener is a test implementation of UploadSourceOpener backed by an
// in-memory byte pool. It mirrors the Android SAF bridge: Open(token, offset)
// creates a fresh reader positioned at offset, Read advances it, Close
// releases it.
type fakeOpener struct {
	mu      sync.Mutex
	pool    map[string][]byte
	handles map[int64]*fakeSource
	next    atomic.Int64
	opens   atomic.Int64
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

func (f *fakeOpener) Read(handle int64, dst []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	src, ok := f.handles[handle]
	if !ok {
		return 0, fmt.Errorf("fake opener: unknown handle %d", handle)
	}
	if src.offset >= int64(len(src.data)) {
		return 0, nil
	}
	n := copy(dst, src.data[src.offset:])
	src.offset += int64(n)
	return n, nil
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
	defer SetUploadSourceOpenerJSON(nilOpener{})

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
func (nilOpener) Read(int64, []byte) (int, error)   { return 0, io.EOF }
func (nilOpener) Close(int64) error                 { return nil }

func jsonOK(raw string) bool {
	var envelope struct {
		OK bool `json:"ok"`
	}
	return json.Unmarshal([]byte(raw), &envelope) == nil && envelope.OK
}
