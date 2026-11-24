package control

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	pathpkg "path"
	"strings"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

type fsStub struct {
	statPath    string
	removed     string
	removedDir  bool
	renamed     [2]string
	invalidated []string
	modTimes    [][2]string
	mkdirCalls  int
	idleTimeout time.Duration
}

func (s *fsStub) Stat(ctx context.Context, path string) (FSEntry, error) {
	s.statPath = path
	return FSEntry{Name: "file.txt", Path: path, Size: 3}, nil
}

func (s *fsStub) List(ctx context.Context, path string) ([]FSEntry, error) {
	return []FSEntry{{Name: "a", Path: path + "/a"}}, nil
}

func (s *fsStub) ReadStream(ctx context.Context, path string, offset, size int64, raw bool) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("abc")), nil
}

func (s *fsStub) Space(ctx context.Context) (FSSpace, error) {
	return FSSpace{Total: 100, Free: 40}, nil
}

func (s *fsStub) MountSpaces(ctx context.Context) ([]FSMountSpace, error) {
	return []FSMountSpace{{Name: "local", Total: 100, Free: 40}}, nil
}

func (s *fsStub) PendingUploads(ctx context.Context) ([]vfs.PendingUpload, error) {
	return []vfs.PendingUpload{{Path: "/local/pending.txt", Size: 1}}, nil
}

func (s *fsStub) Create(ctx context.Context, path string) error { return nil }
func (s *fsStub) WriteAt(ctx context.Context, path string, offset int64, data []byte) (int, error) {
	return len(data), nil
}
func (s *fsStub) Flush(ctx context.Context, path string) error { return nil }
func (s *fsStub) Mkdir(ctx context.Context, path string) (FSEntry, error) {
	s.mkdirCalls++
	return FSEntry{Name: "dir", IsDir: true, Path: path}, nil
}
func (s *fsStub) Remove(ctx context.Context, path string, dir bool) error {
	s.removed, s.removedDir = path, dir
	return nil
}
func (s *fsStub) Rename(ctx context.Context, source, destination string) error {
	s.renamed = [2]string{source, destination}
	return nil
}
func (s *fsStub) Truncate(ctx context.Context, path string, size int64) error { return nil }

func (s *fsStub) SetModTime(ctx context.Context, path string, modTime time.Time) error {
	s.modTimes = append(s.modTimes, [2]string{path, modTime.UTC().Format(time.RFC3339Nano)})
	return nil
}
func (s *fsStub) InvalidateList(ctx context.Context, path string) error {
	s.invalidated = append(s.invalidated, path)
	return nil
}

func (s *fsStub) Resolve(ctx context.Context, path string) (FSResolveResult, error) {
	return FSResolveResult{Path: path, PlainName: pathpkg.Base(path)}, nil
}

func (s *fsStub) WaitIdle(ctx context.Context, timeout time.Duration) error {
	s.idleTimeout = timeout
	return nil
}

func (s *fsStub) WaitPath(ctx context.Context, path string, timeout time.Duration) error {
	return nil
}

func (s *fsStub) Copy(ctx context.Context, req FSCopyRequest) (json.RawMessage, error) {
	if req.Source == "/local/missing" {
		return nil, fmt.Errorf("%w: %s", drive.ErrNotFound, req.Source)
	}
	return json.RawMessage(`{"pass":true,"bytes":3}`), nil
}

func newFSTestServer(stub FSService) *CommandServer {
	return &CommandServer{fs: stub}
}

func TestFSHandlersStatListAndMutations(t *testing.T) {
	stub := &fsStub{}
	server := newFSTestServer(stub)

	recorder := httptest.NewRecorder()
	server.handleFS(recorder, httptest.NewRequest(http.MethodGet, "/v1/fs/stat?path=%2Flocal", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("stat status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	var entry FSEntry
	if err := json.Unmarshal(recorder.Body.Bytes(), &entry); err != nil {
		t.Fatal(err)
	}
	if entry.Name != "file.txt" || stub.statPath != "/local" {
		t.Fatalf("stat entry=%+v path=%q", entry, stub.statPath)
	}

	recorder = httptest.NewRecorder()
	server.handleFS(recorder, httptest.NewRequest(http.MethodGet, "/v1/fs/list?path=%2Flocal", nil))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"a"`) {
		t.Fatalf("list status=%d body=%s", recorder.Code, recorder.Body.String())
	}

	recorder = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/fs/remove", strings.NewReader(`{"path":"/local/a","dir":true}`))
	server.handleFS(recorder, req)
	if recorder.Code != http.StatusOK || stub.removed != "/local/a" || !stub.removedDir {
		t.Fatalf("remove status=%d body=%s stub=%+v", recorder.Code, recorder.Body.String(), stub)
	}

	recorder = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/v1/fs/rename", strings.NewReader(`{"source":"/a","destination":"/b"}`))
	server.handleFS(recorder, req)
	if recorder.Code != http.StatusOK || stub.renamed != [2]string{"/a", "/b"} {
		t.Fatalf("rename status=%d body=%s stub=%+v", recorder.Code, recorder.Body.String(), stub)
	}

	recorder = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/v1/fs/write?path=%2Flocal%2Ff&offset=4", strings.NewReader("hello"))
	server.handleFS(recorder, req)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"written": 5`) {
		t.Fatalf("write status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestFSSetModTimeHandler(t *testing.T) {
	stub := &fsStub{}
	server := newFSTestServer(stub)

	recorder := httptest.NewRecorder()
	server.handleFS(recorder, httptest.NewRequest(http.MethodPost, "/v1/fs/setmodtime", strings.NewReader(`{"path":"/local/f.txt","mod_time":"2026-11-24T10:00:00Z"}`)))
	if recorder.Code != http.StatusOK {
		t.Fatalf("setmodtime status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	if len(stub.modTimes) != 1 || stub.modTimes[0] != [2]string{"/local/f.txt", "2026-11-24T10:00:00Z"} {
		t.Fatalf("setmodtime calls = %v", stub.modTimes)
	}

	recorder = httptest.NewRecorder()
	server.handleFS(recorder, httptest.NewRequest(http.MethodPost, "/v1/fs/setmodtime", strings.NewReader(`{"path":"/local/f.txt"}`)))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("missing mod_time status = %d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestFSHandlerUnavailableAndUnknown(t *testing.T) {
	server := &CommandServer{}
	recorder := httptest.NewRecorder()
	server.handleFS(recorder, httptest.NewRequest(http.MethodGet, "/v1/fs/stat?path=/", nil))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("nil service status = %d", recorder.Code)
	}

	server = newFSTestServer(&fsStub{})
	recorder = httptest.NewRecorder()
	server.handleFS(recorder, httptest.NewRequest(http.MethodGet, "/v1/fs/nope", nil))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("unknown op status = %d", recorder.Code)
	}
}

func TestFSHandlersStreamAndQueries(t *testing.T) {
	server := newFSTestServer(&fsStub{})

	recorder := httptest.NewRecorder()
	server.handleFS(recorder, httptest.NewRequest(http.MethodGet, "/v1/fs/read?path=%2Flocal%2Ffile.txt&offset=0&size=0", nil))
	if recorder.Code != http.StatusOK || recorder.Body.String() != "abc" {
		t.Fatalf("read status=%d body=%q", recorder.Code, recorder.Body.String())
	}

	recorder = httptest.NewRecorder()
	server.handleFS(recorder, httptest.NewRequest(http.MethodGet, "/v1/fs/space", nil))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"total": 100`) {
		t.Fatalf("space status=%d body=%s", recorder.Code, recorder.Body.String())
	}

	recorder = httptest.NewRecorder()
	server.handleFS(recorder, httptest.NewRequest(http.MethodGet, "/v1/fs/spaces", nil))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"name": "local"`) {
		t.Fatalf("spaces status=%d body=%s", recorder.Code, recorder.Body.String())
	}

	recorder = httptest.NewRecorder()
	server.handleFS(recorder, httptest.NewRequest(http.MethodPost, "/v1/fs/copy", strings.NewReader(`{"source":"/local/a","destination":"/local/b"}`)))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"pass":true`) {
		t.Fatalf("copy status=%d body=%s", recorder.Code, recorder.Body.String())
	}

	recorder = httptest.NewRecorder()
	server.handleFS(recorder, httptest.NewRequest(http.MethodGet, "/v1/fs/pending", nil))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"path": "/local/pending.txt"`) {
		t.Fatalf("pending status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestFSCopyErrorMapsToNotFound(t *testing.T) {
	server := newFSTestServer(&fsStub{})
	recorder := httptest.NewRecorder()
	server.handleFS(recorder, httptest.NewRequest(http.MethodPost, "/v1/fs/copy", strings.NewReader(`{"source":"/local/missing","destination":"/local/b"}`)))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("copy status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

// TestFSMutationOperationIDReplaysResponse checks that a repeated idempotency
// key executes the mutation once and replays the first response.
func TestFSMutationOperationIDReplaysResponse(t *testing.T) {
	stub := &fsStub{}
	server := &CommandServer{fs: stub, operations: newOperationMemo()}

	send := func(id string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/v1/fs/mkdir", strings.NewReader(`{"path":"/local/dir"}`))
		if id != "" {
			request.Header.Set(OperationIDHeader, id)
		}
		server.handleFS(recorder, request)
		return recorder
	}

	first := send("op-1")
	second := send("op-1")
	if first.Code != http.StatusOK || second.Code != http.StatusOK {
		t.Fatalf("status first=%d second=%d", first.Code, second.Code)
	}
	if stub.mkdirCalls != 1 {
		t.Fatalf("mkdir calls = %d, want 1 for a repeated operation id", stub.mkdirCalls)
	}
	if first.Body.String() != second.Body.String() {
		t.Fatalf("replayed body = %q, want %q", second.Body.String(), first.Body.String())
	}

	third := send("op-2")
	if third.Code != http.StatusOK || stub.mkdirCalls != 2 {
		t.Fatalf("new operation id: status=%d calls=%d, want 200/2", third.Code, stub.mkdirCalls)
	}

	// No key means no dedup: the handler runs again.
	fourth := send("")
	if fourth.Code != http.StatusOK || stub.mkdirCalls != 3 {
		t.Fatalf("keyless: status=%d calls=%d, want 200/3", fourth.Code, stub.mkdirCalls)
	}
}

// TestFSHandlerWaitIdle covers the whole-filesystem settle endpoint used by
// `fs sync` when it runs through the resident Core.
func TestFSHandlerWaitIdle(t *testing.T) {
	stub := &fsStub{}
	server := newFSTestServer(stub)

	recorder := httptest.NewRecorder()
	server.handleFS(recorder, httptest.NewRequest(http.MethodPost, "/v1/fs/idle", strings.NewReader(`{"timeout_ms":1500}`)))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"idle": true`) {
		t.Fatalf("idle status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if stub.idleTimeout != 1500*time.Millisecond {
		t.Fatalf("idle timeout = %v, want 1.5s", stub.idleTimeout)
	}

	recorder = httptest.NewRecorder()
	server.handleFS(recorder, httptest.NewRequest(http.MethodGet, "/v1/fs/idle", nil))
	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET idle status = %d", recorder.Code)
	}
}
