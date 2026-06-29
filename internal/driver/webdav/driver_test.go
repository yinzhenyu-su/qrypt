package webdav

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

// ─── in-memory test WebDAV server ─────────────────────────────────────────

type testFile struct {
	isDir    bool
	data     []byte
	modTime  time.Time
}

type testWebDAV struct {
	mu     sync.RWMutex
	files  map[string]*testFile // key = cleaned path
}

func newTestWebDAV() *testWebDAV {
	return &testWebDAV{
		files: map[string]*testFile{
			"/": {isDir: true, modTime: time.Now()},
		},
	}
}

func (s *testWebDAV) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Strip the mount path to get the internal path.
	p := r.URL.Path
	if p == "" {
		p = "/"
	}

	switch r.Method {
	case "PROPFIND":
		s.handlePropfind(w, r, p)
	case "GET", "HEAD":
		s.handleGet(w, r, p)
	case "PUT":
		s.handlePut(w, r, p)
	case "MKCOL":
		s.handleMkcol(w, r, p)
	case "DELETE":
		s.handleDelete(w, r, p)
	case "MOVE":
		s.handleMove(w, r, p)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *testWebDAV) handlePropfind(w http.ResponseWriter, r *http.Request, p string) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	depth := r.Header.Get("Depth")
	p = cleanPath(p)

	file, ok := s.files[p]
	if !ok || (!file.isDir && depth == "1") {
		http.Error(w, "Not Found", http.StatusNotFound)
		return
	}

	var responses []propfindResponse

	if depth == "1" && file.isDir {
		// Add the directory itself
		responses = append(responses, s.makeResponse(p, file))
		// Add children
		prefix := p
		if prefix != "/" {
			prefix += "/"
		}
		for cpath, cfile := range s.files {
			if cpath == p || cpath == "/" {
				continue
			}
			dir := path.Dir(cpath)
			if dir == "/" && p == "/" {
				responses = append(responses, s.makeResponse(cpath, cfile))
			} else if dir == p || (p != "/" && strings.HasPrefix(cpath, prefix) && !strings.Contains(strings.TrimPrefix(cpath, prefix), "/")) {
				responses = append(responses, s.makeResponse(cpath, cfile))
			}
		}
	} else {
		// PROPFIND depth 0 — just the resource itself
		responses = append(responses, s.makeResponse(p, file))
	}

	w.Header().Set("DAV", "1")
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(http.StatusMultiStatus)
	enc := xml.NewEncoder(w)
	enc.Encode(multistatus{Responses: responses})
}

func (s *testWebDAV) makeResponse(p string, f *testFile) propfindResponse {
	href := p
	if f.isDir && href != "/" {
		href += "/"
	}
	r := propfindResponse{Href: href}
	ps := propstat{Status: "HTTP/1.1 200 OK"}
	if f.isDir {
		ps.Prop.ResourceType = &resourceType{Collection: &struct{}{}}
	} else {
		ps.Prop.GetContentLen = fmt.Sprintf("%d", len(f.data))
	}
	ps.Prop.GetLastMod = f.modTime.UTC().Format(time.RFC1123)
	r.Propstat = []propstat{ps}
	return r
}

func (s *testWebDAV) handleGet(w http.ResponseWriter, r *http.Request, p string) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	p = cleanPath(p)
	file, ok := s.files[p]
	if !ok || file.isDir {
		http.Error(w, "Not Found", http.StatusNotFound)
		return
	}

	data := file.data
	status := http.StatusOK

	rangeHeader := r.Header.Get("Range")
	if rangeHeader != "" {
		var start, end int64
		if _, err := fmt.Sscanf(rangeHeader, "bytes=%d-%d", &start, &end); err == nil && start < int64(len(data)) {
			if end >= int64(len(data)) || end == 0 {
				end = int64(len(data)) - 1
			}
			if start <= end {
				data = data[start:end+1]
				status = http.StatusPartialContent
				w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(file.data)))
			}
		}
	}

	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(data)))
	w.WriteHeader(status)
	w.Write(data)
}

func (s *testWebDAV) handlePut(w http.ResponseWriter, r *http.Request, p string) {
	p = cleanPath(p)
	if p == "/" {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}

	data, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.files[p] = &testFile{
		data:    data,
		modTime: time.Now(),
	}

	// Ensure parent dir exists
	parent := path.Dir(p)
	if _, ok := s.files[parent]; !ok {
		s.files[parent] = &testFile{isDir: true, modTime: time.Now()}
	}

	w.WriteHeader(http.StatusCreated)
}

func (s *testWebDAV) handleMkcol(w http.ResponseWriter, r *http.Request, p string) {
	p = cleanPath(p)
	if p == "/" {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.files[p]; ok {
		http.Error(w, "Already exists", http.StatusMethodNotAllowed)
		return
	}

	s.files[p] = &testFile{isDir: true, modTime: time.Now()}
	w.WriteHeader(http.StatusCreated)
}

func (s *testWebDAV) handleDelete(w http.ResponseWriter, r *http.Request, p string) {
	p = cleanPath(p)
	if p == "/" {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.files[p]; !ok {
		http.Error(w, "Not Found", http.StatusNotFound)
		return
	}

	delete(s.files, p)
	w.WriteHeader(http.StatusNoContent)
}

func (s *testWebDAV) handleMove(w http.ResponseWriter, r *http.Request, p string) {
	destHeader := r.Header.Get("Destination")
	if destHeader == "" {
		http.Error(w, "Destination header required", http.StatusBadRequest)
		return
	}

	destURL, err := url.Parse(destHeader)
	if err != nil {
		http.Error(w, "Bad Destination header", http.StatusBadRequest)
		return
	}
	dest := cleanPath(destURL.Path)

	s.mu.Lock()
	defer s.mu.Unlock()

	file, ok := s.files[p]
	if !ok {
		http.Error(w, "Not Found", http.StatusNotFound)
		return
	}

	s.files[dest] = file
	delete(s.files, p)
	w.WriteHeader(http.StatusNoContent)
}

func cleanPath(p string) string {
	p = path.Clean(p)
	if p == "." {
		p = "/"
	}
	return p
}

// ─── tests ────────────────────────────────────────────────────────────────

func setupTest(t *testing.T) (*Driver, *testWebDAV, string) {
	t.Helper()
	ts := newTestWebDAV()
	srv := httptest.NewServer(ts)
	t.Cleanup(srv.Close)

	baseURL := srv.URL + "/"
	drv := New(Options{
		URL:      baseURL,
		Username: "test",
		Password: "test",
	})
	return drv, ts, srv.URL
}

func TestWebDAV_Init(t *testing.T) {
	drv, _, _ := setupTest(t)
	ctx := context.Background()
	if err := drv.Init(ctx); err != nil {
		t.Fatalf("Init failed: %v", err)
	}
}

func TestWebDAV_ListRoot(t *testing.T) {
	drv, ts, _ := setupTest(t)
	ctx := context.Background()
	if err := drv.Init(ctx); err != nil {
		t.Fatal(err)
	}

	ts.mu.Lock()
	ts.files["/hello.txt"] = &testFile{data: []byte("world"), modTime: time.Now()}
	ts.files["/subdir"] = &testFile{isDir: true, modTime: time.Now()}
	ts.mu.Unlock()

	entries, err := drv.List(ctx, "/")
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}

	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d: %+v", len(entries), entries)
	}

	// Find the file
	found := false
	for _, e := range entries {
		if e.Name == "hello.txt" {
			found = true
			if e.IsDir {
				t.Error("hello.txt should not be a directory")
			}
			if e.Size != 5 {
				t.Errorf("hello.txt size = %d, want 5", e.Size)
			}
		}
		if e.Name == "subdir" && !e.IsDir {
			t.Error("subdir should be a directory")
		}
	}
	if !found {
		t.Error("hello.txt not found in listing")
	}
}

func TestWebDAV_Read(t *testing.T) {
	drv, ts, _ := setupTest(t)
	ctx := context.Background()
	if err := drv.Init(ctx); err != nil {
		t.Fatal(err)
	}

	content := "Hello, WebDAV!"
	ts.mu.Lock()
	ts.files["/test.txt"] = &testFile{data: []byte(content), modTime: time.Now()}
	ts.mu.Unlock()

	entry := drive.Entry{ID: "/test.txt", Name: "test.txt", Size: int64(len(content))}
	rc, err := drv.Read(ctx, entry, 0, 0)
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}
	defer rc.Close()

	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll failed: %v", err)
	}
	if string(data) != content {
		t.Errorf("Read got %q, want %q", string(data), content)
	}

	// Partial read
	rc2, err := drv.Read(ctx, entry, 7, 6)
	if err != nil {
		t.Fatalf("Read (partial) failed: %v", err)
	}
	defer rc2.Close()

	data2, err := io.ReadAll(rc2)
	if err != nil {
		t.Fatalf("ReadAll (partial) failed: %v", err)
	}
	wantPartial := "WebDAV"
	if string(data2) != wantPartial {
		t.Errorf("Read (partial) got %q, want %q", string(data2), wantPartial)
	}
}

func TestWebDAV_Mkdir(t *testing.T) {
	drv, _, _ := setupTest(t)
	ctx := context.Background()
	if err := drv.Init(ctx); err != nil {
		t.Fatal(err)
	}

	entry, err := drv.Mkdir(ctx, "/", "newfolder")
	if err != nil {
		t.Fatalf("Mkdir failed: %v", err)
	}
	if !entry.IsDir {
		t.Error("Mkdir should return a directory entry")
	}
	if entry.Name != "newfolder" {
		t.Errorf("Mkdir name = %q, want %q", entry.Name, "newfolder")
	}

	// Verify it appears in listing
	entries, err := drv.List(ctx, "/")
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	found := false
	for _, e := range entries {
		if e.Name == "newfolder" && e.IsDir {
			found = true
			break
		}
	}
	if !found {
		t.Error("newfolder not found in listing after Mkdir")
	}
}

func TestWebDAV_Put(t *testing.T) {
	drv, _, _ := setupTest(t)
	ctx := context.Background()
	if err := drv.Init(ctx); err != nil {
		t.Fatal(err)
	}

	content := "uploaded content"
	body := strings.NewReader(content)
	entry, err := drv.Put(ctx, "/", "uploaded.txt", int64(len(content)), body)
	if err != nil {
		t.Fatalf("Put failed: %v", err)
	}
	if entry.Name != "uploaded.txt" {
		t.Errorf("Put name = %q, want %q", entry.Name, "uploaded.txt")
	}

	// Read it back
	rc, err := drv.Read(ctx, drive.Entry{ID: "/uploaded.txt"}, 0, 0)
	if err != nil {
		t.Fatalf("Read after Put failed: %v", err)
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll after Put failed: %v", err)
	}
	if string(data) != content {
		t.Errorf("Read after Put got %q, want %q", string(data), content)
	}
}

func TestWebDAV_Remove(t *testing.T) {
	drv, ts, _ := setupTest(t)
	ctx := context.Background()
	if err := drv.Init(ctx); err != nil {
		t.Fatal(err)
	}

	// Create a file first
	ts.mu.Lock()
	ts.files["/delete_me.txt"] = &testFile{data: []byte("bye"), modTime: time.Now()}
	ts.mu.Unlock()

	// Remove it
	err := drv.Remove(ctx, drive.Entry{ID: "/delete_me.txt", Name: "delete_me.txt"})
	if err != nil {
		t.Fatalf("Remove failed: %v", err)
	}

	// Verify it's gone
	ts.mu.RLock()
	_, exists := ts.files["/delete_me.txt"]
	ts.mu.RUnlock()
	if exists {
		t.Error("file still exists after Remove")
	}
}

func TestWebDAV_Move(t *testing.T) {
	drv, ts, _ := setupTest(t)
	ctx := context.Background()
	if err := drv.Init(ctx); err != nil {
		t.Fatal(err)
	}

	ts.mu.Lock()
	ts.files["/movable.txt"] = &testFile{data: []byte("move me"), modTime: time.Now()}
	ts.files["/destdir"] = &testFile{isDir: true, modTime: time.Now()}
	ts.mu.Unlock()

	// Move file to subdirectory
	err := drv.Move(ctx, drive.Entry{ID: "/movable.txt", Name: "movable.txt"}, "/destdir")
	if err != nil {
		t.Fatalf("Move failed: %v", err)
	}

	ts.mu.RLock()
	_, srcExists := ts.files["/movable.txt"]
	dstEntry, dstExists := ts.files["/destdir/movable.txt"]
	ts.mu.RUnlock()

	if srcExists {
		t.Error("source file still exists after Move")
	}
	if !dstExists {
		t.Error("destination file not found after Move")
	}
	if dstEntry != nil && string(dstEntry.data) != "move me" {
		t.Errorf("moved file content = %q, want %q", string(dstEntry.data), "move me")
	}
}

func TestWebDAV_Rename(t *testing.T) {
	drv, ts, _ := setupTest(t)
	ctx := context.Background()
	if err := drv.Init(ctx); err != nil {
		t.Fatal(err)
	}

	ts.mu.Lock()
	ts.files["/old_name.txt"] = &testFile{data: []byte("renamed"), modTime: time.Now()}
	ts.mu.Unlock()

	err := drv.Rename(ctx, drive.Entry{ID: "/old_name.txt", Name: "old_name.txt"}, "new_name.txt")
	if err != nil {
		t.Fatalf("Rename failed: %v", err)
	}

	ts.mu.RLock()
	_, srcExists := ts.files["/old_name.txt"]
	dstEntry, dstExists := ts.files["/new_name.txt"]
	ts.mu.RUnlock()

	if srcExists {
		t.Error("old file still exists after Rename")
	}
	if !dstExists {
		t.Error("new file not found after Rename")
	}
	if dstEntry != nil && string(dstEntry.data) != "renamed" {
		t.Errorf("renamed file content = %q, want %q", string(dstEntry.data), "renamed")
	}
}
