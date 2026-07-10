package webdav

import (
	"context"
	"encoding/xml"
	"errors"
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
	isDir   bool
	data    []byte
	modTime time.Time
}

type testWebDAV struct {
	mu        sync.RWMutex
	files     map[string]*testFile // key = cleaned path
	lastRange string
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
	s.mu.Lock()
	defer s.mu.Unlock()

	p = cleanPath(p)
	file, ok := s.files[p]
	if !ok || file.isDir {
		http.Error(w, "Not Found", http.StatusNotFound)
		return
	}

	data := file.data
	status := http.StatusOK

	rangeHeader := r.Header.Get("Range")
	s.lastRange = rangeHeader
	if rangeHeader != "" {
		var start, end int64
		n, _ := fmt.Sscanf(rangeHeader, "bytes=%d-%d", &start, &end)
		if n >= 1 && start >= int64(len(data)) {
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
			return
		}
		if n >= 1 {
			if n == 1 {
				// Open-ended range: bytes=N- → rest of file
				end = int64(len(data)) - 1
			}
			if end >= int64(len(data)) || end == 0 {
				end = int64(len(data)) - 1
			}
			if start <= end {
				data = data[start : end+1]
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

func setupTestWithOptions(t *testing.T, opts Options) (*Driver, *testWebDAV, string) {
	t.Helper()
	ts := newTestWebDAV()
	srv := httptest.NewServer(ts)
	t.Cleanup(srv.Close)

	opts.URL = srv.URL + "/"
	if opts.Username == "" {
		opts.Username = "test"
	}
	if opts.Password == "" {
		opts.Password = "test"
	}
	return New(opts), ts, srv.URL
}

func TestWebDAV_Init(t *testing.T) {
	drv, _, _ := setupTest(t)
	ctx := context.Background()
	if err := drv.Init(ctx); err != nil {
		t.Fatalf("Init failed: %v", err)
	}
}

func TestWebDAV_DebugSnapshot(t *testing.T) {
	drv, _, _ := setupTestWithOptions(t, Options{RootPath: "/qrypt"})
	snapshot, err := drv.DebugSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Driver != "webdav" {
		t.Fatalf("driver = %q, want webdav", snapshot.Driver)
	}
	if snapshot.Health != "ok" {
		t.Fatalf("health = %q, want ok", snapshot.Health)
	}
	if snapshot.Stats[drive.DebugStatRootPath] != "/qrypt" {
		t.Fatalf("unexpected stats: %+v", snapshot.Stats)
	}
	if snapshot.Stats["username"] != "test" {
		t.Fatalf("unexpected username: %+v", snapshot.Stats)
	}
	if snapshot.Extra[drive.DebugExtraCredentialSource] != "config" {
		t.Fatalf("credential_source = %v, want config", snapshot.Extra[drive.DebugExtraCredentialSource])
	}
}

func TestWebDAVInstallBandwidthLimiter(t *testing.T) {
	drv, _, _ := setupTest(t)
	handled := drv.InstallBandwidthLimiter(drive.NewBandwidthLimiter(drive.BandwidthLimits{
		DownloadBytesPerSecond: 1,
		UploadBytesPerSecond:   1,
	}))
	if handled != drive.BandwidthLimitDownload|drive.BandwidthLimitUpload {
		t.Fatalf("handled directions = %v, want download|upload", handled)
	}
	if drv.limiter == nil {
		t.Fatal("limiter was not installed")
	}
}

func TestWebDAVReadUsesBandwidthLimiter(t *testing.T) {
	drv, ts, _ := setupTest(t)
	ts.mu.Lock()
	ts.files["/slow.txt"] = &testFile{data: []byte("slow"), modTime: time.Now()}
	ts.mu.Unlock()
	drv.InstallBandwidthLimiter(drive.NewBandwidthLimiter(drive.BandwidthLimits{DownloadBytesPerSecond: 1}))

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	rc, err := drv.Read(ctx, drive.Entry{ID: "/slow.txt", Size: 4}, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()

	_, err = io.ReadAll(rc)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("read error = %v, want context deadline exceeded", err)
	}
}

func TestWebDAVPutSourceUsesBandwidthLimiter(t *testing.T) {
	drv, _, _ := setupTest(t)
	drv.InstallBandwidthLimiter(drive.NewBandwidthLimiter(drive.BandwidthLimits{UploadBytesPerSecond: 1}))

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := drv.PutSource(ctx, drive.UploadRequest{
		ParentID: "/",
		Name:     "slow.txt",
		Source:   drive.NewBytesReadOnlyFileSource([]byte("slow")),
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("put error = %v, want context deadline exceeded", err)
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
	entry, err := drv.PutSource(ctx, drive.UploadRequest{
		ParentID: "/",
		Name:     "uploaded.txt",
		Source:   drive.NewBytesReadOnlyFileSource([]byte(content)),
	})
	if err != nil {
		t.Fatalf("PutSource failed: %v", err)
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

func TestWebDAV_RootPath(t *testing.T) {
	drv, ts, _ := setupTestWithOptions(t, Options{RootPath: "/qrypt/sub#root"})
	ctx := context.Background()

	ts.mu.Lock()
	ts.files["/qrypt"] = &testFile{isDir: true, modTime: time.Now()}
	ts.files["/qrypt/sub#root"] = &testFile{isDir: true, modTime: time.Now()}
	ts.files["/qrypt/sub#root/existing.txt"] = &testFile{data: []byte("existing"), modTime: time.Now()}
	ts.mu.Unlock()

	if err := drv.Init(ctx); err != nil {
		t.Fatal(err)
	}

	entries, err := drv.List(ctx, "/")
	if err != nil {
		t.Fatalf("List root_path failed: %v", err)
	}
	if len(entries) != 1 || entries[0].Name != "existing.txt" || entries[0].ID != "/existing.txt" {
		t.Fatalf("root_path entries = %+v, want existing.txt at virtual root", entries)
	}

	content := "new under root"
	if _, err := drv.PutSource(ctx, drive.UploadRequest{
		ParentID: "/",
		Name:     "new?file.txt",
		Source:   drive.NewBytesReadOnlyFileSource([]byte(content)),
	}); err != nil {
		t.Fatalf("PutSource under root_path failed: %v", err)
	}
	rc, err := drv.Read(ctx, drive.Entry{ID: "/new?file.txt"}, 0, 0)
	if err != nil {
		t.Fatalf("Read under root_path failed: %v", err)
	}
	data, err := io.ReadAll(rc)
	rc.Close()
	if err != nil {
		t.Fatalf("ReadAll failed: %v", err)
	}
	if string(data) != content {
		t.Fatalf("root_path read = %q, want %q", string(data), content)
	}

	ts.mu.RLock()
	_, existsAtRoot := ts.files["/new?file.txt"]
	_, existsUnderRootPath := ts.files["/qrypt/sub#root/new?file.txt"]
	ts.mu.RUnlock()
	if existsAtRoot || !existsUnderRootPath {
		t.Fatalf("root_path put location root=%t under_root_path=%t", existsAtRoot, existsUnderRootPath)
	}
}

func TestWebDAV_RootPathMissingReportsRootPath(t *testing.T) {
	drv, _, _ := setupTestWithOptions(t, Options{RootPath: "/missing"})
	err := drv.Init(context.Background())
	if err == nil {
		t.Fatal("expected missing root_path to fail")
	}
	if !strings.Contains(err.Error(), `root_path "/missing"`) {
		t.Fatalf("error = %v, want root_path context", err)
	}
}

func TestWebDAV_InitAllowsTemporaryPropfindStatus(t *testing.T) {
	withoutWebDAVRetryWait(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "PROPFIND" {
			t.Fatalf("method = %s, want PROPFIND", r.Method)
		}
		http.Error(w, "temporary unavailable", http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	drv := New(Options{URL: srv.URL + "/", Username: "test", Password: "test", RootPath: "/temporarily-blocked"})
	if err := drv.Init(context.Background()); err != nil {
		t.Fatalf("Init should allow temporary status, got %v", err)
	}
}

func TestWebDAV_InitRetriesTemporaryPropfindStatus(t *testing.T) {
	withoutWebDAVRetryWait(t)
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "PROPFIND" {
			t.Fatalf("method = %s, want PROPFIND", r.Method)
		}
		calls++
		if calls == 1 {
			http.Error(w, "temporary unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/xml; charset=utf-8")
		w.WriteHeader(http.StatusMultiStatus)
		if err := xml.NewEncoder(w).Encode(multistatus{Responses: []propfindResponse{
			{Href: "/", Propstat: []propstat{{Status: "HTTP/1.1 200 OK", Prop: prop{ResourceType: &resourceType{Collection: &struct{}{}}}}}},
		}}); err != nil {
			t.Fatal(err)
		}
	}))
	defer srv.Close()

	drv := New(Options{URL: srv.URL + "/", Username: "test", Password: "test"})
	if err := drv.Init(context.Background()); err != nil {
		t.Fatalf("Init failed after temporary status: %v", err)
	}
	if calls != 2 {
		t.Fatalf("PROPFIND calls = %d, want 2", calls)
	}
}

func TestWebDAV_SpaceRetriesTemporaryPropfindStatus(t *testing.T) {
	withoutWebDAVRetryWait(t)
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "PROPFIND" {
			t.Fatalf("method = %s, want PROPFIND", r.Method)
		}
		calls++
		if calls == 1 {
			http.Error(w, "temporary unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/xml; charset=utf-8")
		w.WriteHeader(http.StatusMultiStatus)
		if err := xml.NewEncoder(w).Encode(multistatus{Responses: []propfindResponse{
			{Href: "/", Propstat: []propstat{{Status: "HTTP/1.1 200 OK", Prop: prop{
				QuotaAvailableBytes: "200",
				QuotaUsedBytes:      "100",
			}}}},
		}}); err != nil {
			t.Fatal(err)
		}
	}))
	defer srv.Close()

	drv := New(Options{URL: srv.URL + "/", Username: "test", Password: "test"})
	space, err := drv.Space(context.Background())
	if err != nil {
		t.Fatalf("Space failed after temporary status: %v", err)
	}
	if calls != 2 {
		t.Fatalf("PROPFIND calls = %d, want 2", calls)
	}
	if space.Total != 300 || space.Free != 200 {
		t.Fatalf("space = %+v, want total=300 free=200", space)
	}
}

func TestWebDAV_SpaceUnsupportedWhenQuotaPropertiesMissing(t *testing.T) {
	withoutWebDAVRetryWait(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "PROPFIND" {
			t.Fatalf("method = %s, want PROPFIND", r.Method)
		}
		w.Header().Set("Content-Type", "application/xml; charset=utf-8")
		w.WriteHeader(http.StatusMultiStatus)
		if err := xml.NewEncoder(w).Encode(multistatus{Responses: []propfindResponse{
			{Href: "/", Propstat: []propstat{{Status: "HTTP/1.1 404 Not Found", Prop: prop{}}}},
		}}); err != nil {
			t.Fatal(err)
		}
	}))
	defer srv.Close()

	drv := New(Options{URL: srv.URL + "/", Username: "test", Password: "test"})
	_, err := drv.Space(context.Background())
	if !errors.Is(err, drive.ErrSpaceUnsupported) {
		t.Fatalf("Space error = %v, want ErrSpaceUnsupported", err)
	}
}

func withoutWebDAVRetryWait(t *testing.T) {
	t.Helper()
	original := webdavRetryWait
	webdavRetryWait = func(context.Context, int) error { return nil }
	t.Cleanup(func() { webdavRetryWait = original })
}

// ─── additional read offset tests ─────────────────────────────────────────

func TestWebDAV_ReadOffsetToEOF(t *testing.T) {
	drv, ts, _ := setupTest(t)
	ctx := context.Background()
	if err := drv.Init(ctx); err != nil {
		t.Fatal(err)
	}

	ts.mu.Lock()
	ts.files["/data.bin"] = &testFile{data: []byte("0123456789ABCDEF"), modTime: time.Now()}
	ts.mu.Unlock()

	entry := drive.Entry{ID: "/data.bin", Name: "data.bin", Size: 16}

	// offset only, no size — read from offset 5 to EOF.
	rc, err := drv.Read(ctx, entry, 5, 0)
	if err != nil {
		t.Fatalf("Read(offset=5, size=0) failed: %v", err)
	}
	defer rc.Close()

	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll failed: %v", err)
	}
	want := "56789ABCDEF"
	if string(data) != want {
		t.Errorf("Read(offset=5, size=0) = %q, want %q", string(data), want)
	}
}

func TestWebDAV_ReadOffsetZeroSizeZero(t *testing.T) {
	drv, ts, _ := setupTest(t)
	ctx := context.Background()
	if err := drv.Init(ctx); err != nil {
		t.Fatal(err)
	}

	ts.mu.Lock()
	ts.files["/full.txt"] = &testFile{data: []byte("entire file content"), modTime: time.Now()}
	ts.mu.Unlock()

	entry := drive.Entry{ID: "/full.txt", Name: "full.txt"}

	// offset=0, size=0 — read the whole file (no Range header).
	rc, err := drv.Read(ctx, entry, 0, 0)
	if err != nil {
		t.Fatalf("Read(offset=0, size=0) failed: %v", err)
	}
	defer rc.Close()

	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll failed: %v", err)
	}
	want := "entire file content"
	if string(data) != want {
		t.Errorf("Read(offset=0, size=0) = %q, want %q", string(data), want)
	}
}

func TestWebDAV_ReadRangePastEOFReturnsEmpty(t *testing.T) {
	drv, ts, _ := setupTest(t)
	ctx := context.Background()
	if err := drv.Init(ctx); err != nil {
		t.Fatal(err)
	}

	ts.mu.Lock()
	ts.files["/small.txt"] = &testFile{data: []byte("small"), modTime: time.Now()}
	ts.mu.Unlock()

	rc, err := drv.Read(ctx, drive.Entry{ID: "/small.txt", Name: "small.txt", Size: 5}, 4096, 1024)
	if err != nil {
		t.Fatalf("Read past EOF failed: %v", err)
	}
	defer rc.Close()

	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != 0 {
		t.Fatalf("Read past EOF returned %q, want empty", data)
	}
}

func TestWebDAV_ReadRangeIsClampedToEntrySize(t *testing.T) {
	drv, ts, _ := setupTest(t)
	ctx := context.Background()
	if err := drv.Init(ctx); err != nil {
		t.Fatal(err)
	}

	ts.mu.Lock()
	ts.files["/small.txt"] = &testFile{data: []byte("small"), modTime: time.Now()}
	ts.mu.Unlock()

	rc, err := drv.Read(ctx, drive.Entry{ID: "/small.txt", Name: "small.txt", Size: 5}, 0, 1024)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(rc)
	closeErr := rc.Close()
	if err != nil {
		t.Fatal(err)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	if string(data) != "small" {
		t.Fatalf("Read returned %q, want small", data)
	}

	ts.mu.RLock()
	lastRange := ts.lastRange
	ts.mu.RUnlock()
	if lastRange != "bytes=0-4" {
		t.Fatalf("Range = %q, want bytes=0-4", lastRange)
	}
}

// ─── special character tests ──────────────────────────────────────────────

func TestWebDAV_SpecialCharsInName(t *testing.T) {
	drv, _, _ := setupTest(t)
	ctx := context.Background()
	if err := drv.Init(ctx); err != nil {
		t.Fatal(err)
	}

	// Create a directory with a special character
	_, err := drv.Mkdir(ctx, "/", "sub#dir")
	if err != nil {
		t.Fatalf("Mkdir(sub#dir) failed: %v", err)
	}

	// Verify it appears in listing
	entries, err := drv.List(ctx, "/")
	if err != nil {
		t.Fatalf("List after Mkdir(sub#dir) failed: %v", err)
	}
	found := false
	for _, e := range entries {
		if e.Name == "sub#dir" && e.IsDir {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("sub#dir not found in listing")
	}

	// Upload a file with special characters into the subdirectory
	content := "special chars test"
	entry, err := drv.PutSource(ctx, drive.UploadRequest{
		ParentID: "/sub#dir",
		Name:     "file?name.txt",
		Source:   drive.NewBytesReadOnlyFileSource([]byte(content)),
	})
	if err != nil {
		t.Fatalf("PutSource(file?name.txt) into sub#dir failed: %v", err)
	}
	if entry.Name != "file?name.txt" {
		t.Errorf("Put returned name = %q, want %q", entry.Name, "file?name.txt")
	}

	// List the sub#dir
	entries, err = drv.List(ctx, "/sub#dir")
	if err != nil {
		t.Fatalf("List sub#dir failed: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry in sub#dir, got %d", len(entries))
	}
	if entries[0].Name != "file?name.txt" {
		t.Errorf("listed file name = %q, want %q", entries[0].Name, "file?name.txt")
	}

	// Read the file back
	rc, err := drv.Read(ctx, drive.Entry{ID: "/sub#dir/file?name.txt"}, 0, 0)
	if err != nil {
		t.Fatalf("Read /sub#dir/file?name.txt failed: %v", err)
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll failed: %v", err)
	}
	if string(data) != content {
		t.Errorf("Read back content = %q, want %q", string(data), content)
	}

	// Remove the file
	err = drv.Remove(ctx, drive.Entry{ID: "/sub#dir/file?name.txt", Name: "file?name.txt"})
	if err != nil {
		t.Fatalf("Remove file?name.txt failed: %v", err)
	}
}

func TestWebDAV_EscapePath(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"", ""},
		{"simple.txt", "simple.txt"},
		{"file#name", "file%23name"},
		{"file?query", "file%3Fquery"},
		{"100% done", "100%25%20done"},
		{"a/b/c", "a/b/c"},
		{"has space/file.txt", "has%20space/file.txt"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := escapePath(tt.input)
			if got != tt.want {
				t.Errorf("escapePath(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestWebDAV_BaseURLWithRootPath(t *testing.T) {
	tests := []struct {
		name     string
		rawURL   string
		rootPath string
		want     string
	}{
		{
			name:     "empty root",
			rawURL:   "https://example.com/dav",
			rootPath: "",
			want:     "https://example.com/dav/",
		},
		{
			name:     "normal root",
			rawURL:   "https://example.com/dav/",
			rootPath: "/qrypt/docs",
			want:     "https://example.com/dav/qrypt/docs/",
		},
		{
			name:     "special chars",
			rawURL:   "https://example.com/dav",
			rootPath: "/qrypt/sub#root/100% done",
			want:     "https://example.com/dav/qrypt/sub%23root/100%25%20done/",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := webdavBaseURL(tt.rawURL, tt.rootPath)
			if got != tt.want {
				t.Fatalf("webdavBaseURL(%q, %q) = %q, want %q", tt.rawURL, tt.rootPath, got, tt.want)
			}
		})
	}
}

func TestWebDAV_ToPathAbsoluteEncodedHref(t *testing.T) {
	drv := New(Options{URL: "https://example.com/remote.php/dav/files/user"})

	tests := []struct {
		name string
		href string
		want string
	}{
		{
			name: "hash and query bytes",
			href: "https://example.com/remote.php/dav/files/user/sub%23dir/file%3Fname.txt",
			want: "/sub#dir/file?name.txt",
		},
		{
			name: "percent and space bytes",
			href: "https://example.com/remote.php/dav/files/user/100%25%20done.txt",
			want: "/100% done.txt",
		},
		{
			name: "base directory",
			href: "https://example.com/remote.php/dav/files/user/",
			want: "/",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := drv.toPath(tt.href)
			if got != tt.want {
				t.Fatalf("toPath(%q) = %q, want %q", tt.href, got, tt.want)
			}
		})
	}
}
