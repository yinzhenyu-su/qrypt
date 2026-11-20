package control

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type fsStub struct {
	statPath   string
	removed    string
	removedDir bool
	renamed    [2]string
}

func (s *fsStub) Stat(ctx context.Context, path string) (FSEntry, error) {
	s.statPath = path
	return FSEntry{Name: "file.txt", Path: path, Size: 3}, nil
}

func (s *fsStub) List(ctx context.Context, path string) ([]FSEntry, error) {
	return []FSEntry{{Name: "a", Path: path + "/a"}}, nil
}

func (s *fsStub) Read(ctx context.Context, path string, offset, size int64) ([]byte, error) {
	return []byte("abc"), nil
}

func (s *fsStub) Create(ctx context.Context, path string) error { return nil }
func (s *fsStub) WriteAt(ctx context.Context, path string, offset int64, data []byte) (int, error) {
	return len(data), nil
}
func (s *fsStub) Flush(ctx context.Context, path string) error { return nil }
func (s *fsStub) Mkdir(ctx context.Context, path string) (FSEntry, error) {
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
func (s *fsStub) WaitPath(ctx context.Context, path string, timeout time.Duration) error {
	return nil
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
