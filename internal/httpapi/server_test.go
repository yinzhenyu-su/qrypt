package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/control"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

const testToken = "test-token"

type fakeCore struct {
	files map[string]string
	dirs  map[string]bool
}

func newFakeCore() *fakeCore {
	return &fakeCore{
		files: map[string]string{
			"/local/hello.txt": "hello world",
			"/local/empty.txt": "",
		},
		dirs: map[string]bool{"/": true, "/local": true},
	}
}

func (c *fakeCore) RuntimeStatus(context.Context) (control.RuntimeStatus, error) {
	return control.RuntimeStatus{PID: 4242, Version: "test", WorkDir: "/tmp/work"}, nil
}

func (c *fakeCore) ListMounts(context.Context) ([]control.MountStatus, error) {
	return []control.MountStatus{{ID: "mnt-1", Name: "local", MountPoint: "/tmp/mnt", StartedAt: time.Unix(1, 0).UTC()}}, nil
}

func (c *fakeCore) FSStat(_ context.Context, path string) (control.FSEntry, error) {
	if c.dirs[path] {
		return control.FSEntry{ID: path, Name: pathBase(path), Path: path, IsDir: true}, nil
	}
	content, ok := c.files[path]
	if !ok {
		return control.FSEntry{}, fmt.Errorf("%w: %s", drive.ErrNotFound, path)
	}
	return control.FSEntry{
		ID:      path,
		Name:    pathBase(path),
		Path:    path,
		Size:    int64(len(content)),
		ModTime: time.Unix(1700000000, 0).UTC(),
	}, nil
}

func (c *fakeCore) FSList(_ context.Context, path string) ([]control.FSEntry, error) {
	if !c.dirs[path] {
		return nil, fmt.Errorf("%w: %s", drive.ErrNotFound, path)
	}
	entries := make([]control.FSEntry, 0)
	for file, content := range c.files {
		if parentPath(file) != path {
			continue
		}
		entries = append(entries, control.FSEntry{ID: file, Name: pathBase(file), Path: file, Size: int64(len(content))})
	}
	return entries, nil
}

func (c *fakeCore) FSReadStream(_ context.Context, path string, offset, size int64, _ bool) (io.ReadCloser, error) {
	content, ok := c.files[path]
	if !ok {
		return nil, fmt.Errorf("%w: %s", drive.ErrNotFound, path)
	}
	if offset > int64(len(content)) {
		return nil, fmt.Errorf("offset out of range")
	}
	end := int64(len(content))
	if size > 0 && offset+size < end {
		end = offset + size
	}
	return io.NopCloser(bytes.NewReader([]byte(content)[offset:end])), nil
}

func (c *fakeCore) FSPendingUploads(context.Context) ([]vfs.PendingUpload, error) {
	return []vfs.PendingUpload{{Path: "/local/pending.txt", Size: 3}}, nil
}

func pathBase(value string) string {
	if value == "/" {
		return "/"
	}
	index := strings.LastIndexByte(value, '/')
	return value[index+1:]
}

func parentPath(value string) string {
	index := strings.LastIndexByte(value, '/')
	if index <= 0 {
		return "/"
	}
	return value[:index]
}

func newTestServer(t *testing.T, opts Options) (*httptest.Server, *fakeCore) {
	t.Helper()
	core := newFakeCore()
	if opts.Token == "" {
		opts.Token = testToken
	}
	server := httptest.NewServer(New(core, opts).Handler())
	t.Cleanup(server.Close)
	return server, core
}

func doRequest(t *testing.T, server *httptest.Server, method, target string, body string, headers map[string]string) *http.Response {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, server.URL+target, reader)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+testToken)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for key, value := range headers {
		if key == "Host" {
			req.Host = value
			continue
		}
		req.Header.Set(key, value)
	}
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, target, err)
	}
	return resp
}

func readBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return string(payload)
}

func TestHealthNeedsNoToken(t *testing.T) {
	server, _ := newTestServer(t, Options{})
	req, err := http.NewRequest(http.MethodGet, server.URL+"/v1/health", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body := readBody(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}
	if !strings.Contains(body, `"status":"ok"`) || !strings.Contains(body, `"pid":4242`) {
		t.Fatalf("unexpected health body: %s", body)
	}
}

func TestPrivateEndpointsRequireToken(t *testing.T) {
	server, _ := newTestServer(t, Options{})
	req, err := http.NewRequest(http.MethodGet, server.URL+"/v1/session", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body := readBody(t, resp)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}
	if resp.Header.Get("WWW-Authenticate") == "" {
		t.Fatal("missing WWW-Authenticate header")
	}
}

func TestSessionAndMounts(t *testing.T) {
	server, _ := newTestServer(t, Options{})
	resp := doRequest(t, server, http.MethodGet, "/v1/session", "", nil)
	body := readBody(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("session status = %d, body = %s", resp.StatusCode, body)
	}
	if !strings.Contains(body, `"pid":4242`) || !strings.Contains(body, `"mount_names":["local"]`) {
		t.Fatalf("unexpected session body: %s", body)
	}
	resp = doRequest(t, server, http.MethodGet, "/v1/mounts", "", nil)
	body = readBody(t, resp)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `"name":"local"`) {
		t.Fatalf("mounts status = %d, body = %s", resp.StatusCode, body)
	}
}

func TestHostAndOriginGuards(t *testing.T) {
	server, _ := newTestServer(t, Options{AllowOrigins: []string{"http://app.example"}})
	resp := doRequest(t, server, http.MethodGet, "/v1/session", "", map[string]string{"Host": "evil.example"})
	body := readBody(t, resp)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("host guard status = %d, body = %s", resp.StatusCode, body)
	}
	resp = doRequest(t, server, http.MethodGet, "/v1/session", "", map[string]string{"Origin": "http://evil.example"})
	body = readBody(t, resp)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("origin guard status = %d, body = %s", resp.StatusCode, body)
	}
	resp = doRequest(t, server, http.MethodOptions, "/v1/fs/list", "", map[string]string{"Origin": "http://app.example"})
	readBody(t, resp)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("preflight status = %d", resp.StatusCode)
	}
	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "http://app.example" {
		t.Fatalf("allow-origin = %q", got)
	}
}

func TestListAndStat(t *testing.T) {
	server, _ := newTestServer(t, Options{})
	resp := doRequest(t, server, http.MethodGet, "/v1/fs/list?path=/local", "", nil)
	body := readBody(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list status = %d, body = %s", resp.StatusCode, body)
	}
	var listed struct {
		Data struct {
			Path    string            `json:"path"`
			Entries []control.FSEntry `json:"entries"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &listed); err != nil {
		t.Fatalf("decode list: %v (%s)", err, body)
	}
	if listed.Data.Path != "/local" || len(listed.Data.Entries) != 2 {
		t.Fatalf("unexpected list: %s", body)
	}
	resp = doRequest(t, server, http.MethodPost, "/v1/fs/stat", `{"path":"/local/hello.txt"}`, nil)
	body = readBody(t, resp)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `"name":"hello.txt"`) {
		t.Fatalf("stat status = %d, body = %s", resp.StatusCode, body)
	}
	resp = doRequest(t, server, http.MethodGet, "/v1/fs/stat?path=/local/missing", "", nil)
	body = readBody(t, resp)
	if resp.StatusCode != http.StatusNotFound || !strings.Contains(body, `"code":"not_found"`) {
		t.Fatalf("missing stat status = %d, body = %s", resp.StatusCode, body)
	}
}

func TestReadHeadRangeAndConditional(t *testing.T) {
	server, _ := newTestServer(t, Options{})
	resp := doRequest(t, server, http.MethodGet, "/v1/fs/read?path=/local/hello.txt", "", nil)
	body := readBody(t, resp)
	if resp.StatusCode != http.StatusOK || body != "hello world" {
		t.Fatalf("read status = %d, body = %q", resp.StatusCode, body)
	}
	etag := resp.Header.Get("ETag")
	if etag == "" {
		t.Fatal("missing ETag")
	}
	if got := resp.Header.Get("Accept-Ranges"); got != "bytes" {
		t.Fatalf("accept-ranges = %q", got)
	}

	resp = doRequest(t, server, http.MethodGet, "/v1/fs/read?path=/local/hello.txt", "", map[string]string{"Range": "bytes=2-6"})
	body = readBody(t, resp)
	if resp.StatusCode != http.StatusPartialContent || body != "llo w" {
		t.Fatalf("range status = %d, body = %q", resp.StatusCode, body)
	}
	if got := resp.Header.Get("Content-Range"); got != "bytes 2-6/11" {
		t.Fatalf("content-range = %q", got)
	}

	resp = doRequest(t, server, http.MethodGet, "/v1/fs/read?path=/local/hello.txt", "", map[string]string{"Range": "bytes=-3"})
	body = readBody(t, resp)
	if resp.StatusCode != http.StatusPartialContent || body != "rld" {
		t.Fatalf("suffix range status = %d, body = %q", resp.StatusCode, body)
	}

	resp = doRequest(t, server, http.MethodGet, "/v1/fs/read?path=/local/hello.txt", "", map[string]string{"Range": "bytes=0-1,3-4"})
	body = readBody(t, resp)
	if resp.StatusCode != http.StatusRequestedRangeNotSatisfiable {
		t.Fatalf("multi range status = %d, body = %s", resp.StatusCode, body)
	}
	if got := resp.Header.Get("Content-Range"); got != "bytes */11" {
		t.Fatalf("416 content-range = %q", got)
	}

	resp = doRequest(t, server, http.MethodGet, "/v1/fs/read?path=/local/hello.txt", "", map[string]string{"Range": "bytes=20-30"})
	readBody(t, resp)
	if resp.StatusCode != http.StatusRequestedRangeNotSatisfiable {
		t.Fatalf("out of range status = %d", resp.StatusCode)
	}

	resp = doRequest(t, server, http.MethodHead, "/v1/fs/read?path=/local/hello.txt", "", nil)
	readBody(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("head status = %d", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Length"); got != "11" {
		t.Fatalf("head content-length = %q", got)
	}

	resp = doRequest(t, server, http.MethodGet, "/v1/fs/read?path=/local/hello.txt", "", map[string]string{"If-None-Match": etag})
	readBody(t, resp)
	if resp.StatusCode != http.StatusNotModified {
		t.Fatalf("conditional status = %d", resp.StatusCode)
	}
}

func TestWriteEndpointsRejected(t *testing.T) {
	server, _ := newTestServer(t, Options{})
	for _, target := range []string{"/v1/fs/mkdir", "/v1/fs/remove", "/v1/fs/copy"} {
		resp := doRequest(t, server, http.MethodPost, target, `{"path":"/local/x"}`, nil)
		body := readBody(t, resp)
		if resp.StatusCode != http.StatusMethodNotAllowed || !strings.Contains(body, "read-only") {
			t.Fatalf("%s status = %d, body = %s", target, resp.StatusCode, body)
		}
	}
	resp := doRequest(t, server, http.MethodGet, "/v1/fs/nope", "", nil)
	readBody(t, resp)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown op status = %d", resp.StatusCode)
	}
}

func TestTasksAndEvents(t *testing.T) {
	server, _ := newTestServer(t, Options{EventInterval: 50 * time.Millisecond})
	resp := doRequest(t, server, http.MethodGet, "/v1/tasks", "", nil)
	body := readBody(t, resp)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `"path":"/local/pending.txt"`) {
		t.Fatalf("tasks status = %d, body = %s", resp.StatusCode, body)
	}

	req, err := http.NewRequest(http.MethodGet, server.URL+"/v1/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+testToken)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	req = req.WithContext(ctx)
	resp, err = server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	buffer := make([]byte, 128)
	read, err := resp.Body.Read(buffer)
	if err != nil && read == 0 {
		t.Fatalf("read event stream: %v", err)
	}
	if !strings.Contains(string(buffer[:read]), "event: status") {
		t.Fatalf("unexpected event stream: %q", buffer[:read])
	}
}
