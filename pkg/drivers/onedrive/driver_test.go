package onedrive

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/drive/session"
)

type mockItem struct {
	id       string
	parentID string
	name     string
	data     []byte
	isDir    bool
	modTime  time.Time
	sha1     string
}

type mockOneDrive struct {
	mu                      sync.RWMutex
	items                   map[string]*mockItem
	children                map[string][]string
	nextID                  int
	uploads                 map[string]*mockUploadSession
	copyJobs                []string
	failCreateUploadSession bool
}

type mockUploadSession struct {
	parentID string
	name     string
	data     []byte
	puts     int
}

func newMockOneDrive() *mockOneDrive {
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	m := &mockOneDrive{
		items:    map[string]*mockItem{},
		children: map[string][]string{},
		uploads:  map[string]*mockUploadSession{},
	}
	m.items["root-id"] = &mockItem{id: "root-id", name: "root", isDir: true, modTime: now}
	m.items["docs-id"] = &mockItem{id: "docs-id", parentID: "root-id", name: "docs", isDir: true, modTime: now}
	m.items["file-id"] = &mockItem{id: "file-id", parentID: "docs-id", name: "hello #1.txt", data: []byte("hello world"), modTime: now}
	m.children["root-id"] = []string{"docs-id"}
	m.children["docs-id"] = []string{"file-id"}
	m.nextID = 10
	return m
}

func (m *mockOneDrive) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case strings.HasPrefix(r.URL.Path, "/copy-job/") && r.Method == http.MethodGet:
		m.mu.RLock()
		defer m.mu.RUnlock()
		jobID := strings.TrimPrefix(r.URL.Path, "/copy-job/")
		if _, ok := m.items[jobID]; ok {
			writeJSON(w, map[string]any{"status": "completed"})
			return
		}
		http.NotFound(w, r)
		return
	case strings.HasPrefix(r.URL.Path, "/online"):
		writeJSON(w, map[string]string{"access_token": "access-token", "refresh_token": "refresh-token-2"})
	case strings.HasSuffix(r.URL.Path, "/oauth2/token"):
		if err := r.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if r.Form.Get("grant_type") != "client_credentials" {
			writeJSON(w, map[string]string{"error": "invalid_grant"})
			return
		}
		writeJSON(w, map[string]string{"access_token": "access-token"})
	case strings.HasPrefix(r.URL.Path, "/download/"):
		m.handleDownload(w, r)
	case strings.HasPrefix(r.URL.Path, "/upload/"):
		m.handleUploadSession(w, r)
	case strings.HasPrefix(r.URL.Path, "/v1.0/me/drive"):
		m.handleGraph(w, r)
	case strings.HasPrefix(r.URL.Path, "/v1.0/users/"):
		m.handleGraph(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (m *mockOneDrive) handleGraph(w http.ResponseWriter, r *http.Request) {
	if auth := r.Header.Get("Authorization"); auth != "Bearer access-token" {
		writeGraphError(w, http.StatusUnauthorized, "InvalidAuthenticationToken", "invalid token")
		return
	}
	suffix := strings.TrimPrefix(r.URL.Path, "/v1.0/me/drive")
	if strings.HasPrefix(r.URL.Path, "/v1.0/users/") {
		_, suffix, _ = strings.Cut(r.URL.Path, "/drive")
	}
	if suffix == "" {
		writeJSON(w, map[string]any{"id": "drive-id", "quota": map[string]any{"total": 1000, "remaining": 750, "used": 250}})
		return
	}
	if suffix == "/root" {
		writeJSON(w, m.itemResp("root-id", r))
		return
	}
	if strings.HasPrefix(suffix, "/root:") {
		id, ok := m.findByPath(strings.TrimSuffix(strings.TrimPrefix(suffix, "/root:"), ":"))
		if !ok {
			writeGraphError(w, http.StatusNotFound, "itemNotFound", "not found")
			return
		}
		writeJSON(w, m.itemResp(id, r))
		return
	}
	if strings.HasPrefix(suffix, "/items/") {
		m.handleItem(w, r, strings.TrimPrefix(suffix, "/items/"))
		return
	}
	http.NotFound(w, r)
}

func (m *mockOneDrive) handleItem(w http.ResponseWriter, r *http.Request, rest string) {
	if idx := strings.Index(rest, ":/"); idx >= 0 {
		parentID, _ := url.PathUnescape(rest[:idx])
		nameTail := rest[idx+2:]
		namePart, op, _ := strings.Cut(nameTail, ":/")
		name, _ := url.PathUnescape(namePart)
		switch {
		case op == "content" && r.Method == http.MethodPut:
			data, _ := io.ReadAll(r.Body)
			id := m.createItem(parentID, name, false, data)
			writeJSON(w, m.itemResp(id, r))
			return
		case op == "createUploadSession" && r.Method == http.MethodPost:
			m.mu.RLock()
			fail := m.failCreateUploadSession
			m.mu.RUnlock()
			if fail {
				writeGraphError(w, http.StatusInternalServerError, "serverError", "injected create failure")
				return
			}
			uploadID := "session-" + name
			m.mu.Lock()
			m.uploads[uploadID] = &mockUploadSession{parentID: parentID, name: name}
			m.mu.Unlock()
			writeJSON(w, map[string]string{"uploadUrl": "http://" + r.Host + "/upload/" + url.PathEscape(uploadID)})
			return
		case strings.HasSuffix(nameTail, ":") && r.Method == http.MethodGet:
			name = strings.TrimSuffix(nameTail, ":")
			name, _ = url.PathUnescape(name)
			id, ok := m.childByName(parentID, name)
			if !ok {
				writeGraphError(w, http.StatusNotFound, "itemNotFound", "not found")
				return
			}
			writeJSON(w, m.itemResp(id, r))
			return
		}
	}

	itemID, tail, _ := strings.Cut(rest, "/")
	itemID, _ = url.PathUnescape(itemID)
	if tail == "children" && r.Method == http.MethodGet {
		m.mu.RLock()
		ids := append([]string(nil), m.children[itemID]...)
		m.mu.RUnlock()
		var values []map[string]any
		for _, id := range ids {
			values = append(values, m.itemResp(id, r))
		}
		writeJSON(w, map[string]any{"value": values})
		return
	}
	if tail == "children" && r.Method == http.MethodPost {
		var body struct {
			Name   string         `json:"name"`
			Folder map[string]any `json:"folder"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		id := m.createItem(itemID, body.Name, true, nil)
		writeJSON(w, m.itemResp(id, r))
		return
	}
	if tail == "copy" && r.Method == http.MethodPost {
		var body struct {
			ParentReference struct {
				ID string `json:"id"`
			} `json:"parentReference"`
			Name string `json:"name"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		src := m.items[itemID]
		if src == nil {
			writeGraphError(w, http.StatusNotFound, "itemNotFound", "not found")
			return
		}
		newID := m.createItem(body.ParentReference.ID, body.Name, src.isDir, src.data)
		m.copyJobs = append(m.copyJobs, newID)
		w.Header().Set("Location", "http://"+r.Host+"/copy-job/"+url.PathEscape(newID))
		w.WriteHeader(http.StatusAccepted)
		return
	}
	if tail == "" {
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, m.itemResp(itemID, r))
		case http.MethodPatch:
			var body struct {
				Name            string `json:"name"`
				ParentReference struct {
					ID string `json:"id"`
				} `json:"parentReference"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			m.mu.Lock()
			item := m.items[itemID]
			if body.Name != "" {
				item.name = body.Name
			}
			if body.ParentReference.ID != "" {
				m.removeChildLocked(item.parentID, itemID)
				item.parentID = body.ParentReference.ID
				m.children[item.parentID] = append(m.children[item.parentID], itemID)
			}
			m.mu.Unlock()
			writeJSON(w, m.itemResp(itemID, r))
		case http.MethodDelete:
			m.mu.Lock()
			if item := m.items[itemID]; item != nil {
				m.removeChildLocked(item.parentID, itemID)
			}
			delete(m.items, itemID)
			m.mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
		return
	}
	http.NotFound(w, r)
}

func (m *mockOneDrive) handleDownload(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/download/")
	m.mu.RLock()
	item := m.items[id]
	m.mu.RUnlock()
	if item == nil {
		http.NotFound(w, r)
		return
	}
	data := item.data
	if rangeHeader := r.Header.Get("Range"); rangeHeader != "" {
		var start, end int64
		if n, _ := fmt.Sscanf(rangeHeader, "bytes=%d-%d", &start, &end); n == 2 {
			if end >= int64(len(data)) {
				end = int64(len(data) - 1)
			}
			data = data[start : end+1]
			w.WriteHeader(http.StatusPartialContent)
		}
	}
	_, _ = w.Write(data)
}

func (m *mockOneDrive) handleUploadSession(w http.ResponseWriter, r *http.Request) {
	uploadID, _ := url.PathUnescape(strings.TrimPrefix(r.URL.Path, "/upload/"))
	m.mu.Lock()
	defer m.mu.Unlock()
	session := m.uploads[uploadID]
	if session == nil {
		http.NotFound(w, r)
		return
	}
	if r.Method == http.MethodGet {
		// upload session 状态查询：nextExpectedRanges 反映已传字节。
		writeJSON(w, map[string]any{"nextExpectedRanges": []string{fmt.Sprintf("bytes %d-/", len(session.data))}})
		return
	}
	if r.Method == http.MethodDelete {
		// cancel upload session。
		delete(m.uploads, uploadID)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	data, _ := io.ReadAll(r.Body)
	session.puts++
	start := int64(0)
	if n, _ := fmt.Sscanf(r.Header.Get("Content-Range"), "bytes %d-", &start); n == 1 {
		// Graph 语义：Content-Range 指定起始偏移，分片写入固定位置（支持覆盖重传）。
		if int64(len(session.data)) < start+int64(len(data)) {
			session.data = append(session.data, make([]byte, start+int64(len(data))-int64(len(session.data)))...)
		}
		copy(session.data[start:], data)
		expected := int64(0)
		if _, after, ok := strings.Cut(r.Header.Get("Content-Range"), "/"); ok {
			expected, _ = strconv.ParseInt(after, 10, 64)
		}
		if expected > 0 && int64(len(session.data)) >= expected {
			m.createItemLocked(session.parentID, session.name, false, session.data)
			delete(m.uploads, uploadID)
			w.WriteHeader(http.StatusCreated)
			writeJSON(w, map[string]string{"id": "complete"})
			return
		}
		w.WriteHeader(http.StatusAccepted)
		return
	}
	// 无 Content-Range 起始偏移的请求：顺序追加（兼容旧路径）。
	session.data = append(session.data, data...)
	total := int64(len(session.data))
	expected := int64(0)
	if _, after, ok := strings.Cut(r.Header.Get("Content-Range"), "/"); ok {
		expected, _ = strconv.ParseInt(after, 10, 64)
	}
	if expected > 0 && total >= expected {
		m.createItemLocked(session.parentID, session.name, false, session.data)
		delete(m.uploads, uploadID)
		w.WriteHeader(http.StatusCreated)
		writeJSON(w, map[string]string{"id": "complete"})
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func (m *mockOneDrive) findByPath(escapedPath string) (string, bool) {
	p, _ := url.PathUnescape(escapedPath)
	parts := strings.Split(strings.Trim(p, "/"), "/")
	id := "root-id"
	if len(parts) == 1 && parts[0] == "" {
		return id, true
	}
	for _, part := range parts {
		next, ok := m.childByName(id, part)
		if !ok {
			return "", false
		}
		id = next
	}
	return id, true
}

func (m *mockOneDrive) childByName(parentID, name string) (string, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, id := range m.children[parentID] {
		if item := m.items[id]; item != nil && item.name == name {
			return id, true
		}
	}
	return "", false
}

func (m *mockOneDrive) createItem(parentID, name string, isDir bool, data []byte) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.createItemLocked(parentID, name, isDir, data)
}

func (m *mockOneDrive) createItemLocked(parentID, name string, isDir bool, data []byte) string {
	if existing, ok := m.childByNameLocked(parentID, name); ok {
		item := m.items[existing]
		item.isDir = isDir
		item.data = append([]byte(nil), data...)
		return existing
	}
	m.nextID++
	id := fmt.Sprintf("item-%d", m.nextID)
	m.items[id] = &mockItem{id: id, parentID: parentID, name: name, isDir: isDir, data: append([]byte(nil), data...), modTime: time.Now().UTC()}
	m.children[parentID] = append(m.children[parentID], id)
	return id
}

func (m *mockOneDrive) childByNameLocked(parentID, name string) (string, bool) {
	for _, id := range m.children[parentID] {
		if item := m.items[id]; item != nil && item.name == name {
			return id, true
		}
	}
	return "", false
}

func (m *mockOneDrive) removeChildLocked(parentID, id string) {
	children := m.children[parentID]
	m.children[parentID] = slices.DeleteFunc(children, func(child string) bool { return child == id })
}

func (m *mockOneDrive) itemResp(id string, r *http.Request) map[string]any {
	m.mu.RLock()
	item := m.items[id]
	m.mu.RUnlock()
	if item == nil {
		return nil
	}
	resp := map[string]any{
		"id":   item.id,
		"name": item.name,
		"size": int64(len(item.data)),
		"fileSystemInfo": map[string]string{
			"lastModifiedDateTime": item.modTime.Format(time.RFC3339),
		},
		"parentReference": map[string]string{"id": item.parentID},
	}
	if item.isDir {
		resp["folder"] = map[string]int{"childCount": len(m.children[id])}
	} else {
		resp["file"] = map[string]any{"mimeType": "application/octet-stream"}
		if item.sha1 != "" {
			resp["file"] = map[string]any{
				"mimeType": "application/octet-stream",
				"hashes":   map[string]string{"sha1Hash": item.sha1},
			}
		}
		resp["@microsoft.graph.downloadUrl"] = "http://" + r.Host + "/download/" + item.id
	}
	return resp
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func writeGraphError(w http.ResponseWriter, status int, code, message string) {
	w.WriteHeader(status)
	writeJSON(w, map[string]any{"error": map[string]string{"code": code, "message": message}})
}

func newTestDriver(t *testing.T) (*Driver, *mockOneDrive) {
	t.Helper()
	mock := newMockOneDrive()
	srv := httptest.NewServer(mock)
	t.Cleanup(srv.Close)
	return New(Options{
		Region:       "global",
		APIBaseURL:   srv.URL,
		OAuthBaseURL: srv.URL,
		OnlineAPI:    srv.URL + "/online",
		RootPath:     "/docs",
		RefreshToken: "refresh-token",
		UseOnlineAPI: true,
		HTTPClient:   srv.Client(),
		ChunkSize:    defaultChunkSize,
	}), mock
}

func TestFactoryMissingRefreshToken(t *testing.T) {
	_, err := drive.New("onedrive", drive.Params{})
	if err == nil || !strings.Contains(err.Error(), "refresh_token") {
		t.Fatalf("expected refresh_token error, got %v", err)
	}
}

func TestAppFactoryRequiresCredentials(t *testing.T) {
	_, err := drive.New("onedrive_app", drive.Params{})
	if err == nil || !strings.Contains(err.Error(), "client_id") {
		t.Fatalf("expected client_id error, got %v", err)
	}
	_, err = drive.New("onedrive_app", drive.Params{"client_id": "id", "client_secret": "secret", "tenant_id": "tenant"})
	if err == nil || !strings.Contains(err.Error(), "email") {
		t.Fatalf("expected email error, got %v", err)
	}
}

func TestCleanAndEscapePath(t *testing.T) {
	if got := cleanOneDrivePath("docs/../docs/a b"); got != "/docs/a b" {
		t.Fatalf("clean path = %q", got)
	}
	if got := escapeDrivePath("/docs/a #?.txt"); got != "/docs/a%20%23%3F.txt" {
		t.Fatalf("escape path = %q", got)
	}
}

func TestAppModeInitListAndDebug(t *testing.T) {
	ctx := context.Background()
	mock := newMockOneDrive()
	srv := httptest.NewServer(mock)
	t.Cleanup(srv.Close)
	d := New(Options{
		Region:       "global",
		APIBaseURL:   srv.URL,
		OAuthBaseURL: srv.URL,
		RootPath:     "/docs",
		AppMode:      true,
		TenantID:     "tenant-id",
		Email:        "user@example.com",
		ClientID:     "client-id",
		ClientSecret: "client-secret",
		HTTPClient:   srv.Client(),
	})
	if err := d.Init(ctx); err != nil {
		t.Fatal(err)
	}
	entries, err := d.List(ctx, "0")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name != "hello #1.txt" {
		t.Fatalf("entries = %+v", entries)
	}
	snapshot, err := d.DebugSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Driver != "onedrive_app" {
		t.Fatalf("driver = %q, want onedrive_app", snapshot.Driver)
	}
	if snapshot.Stats["app_mode"] != true {
		t.Fatalf("stats = %+v", snapshot.Stats)
	}
}

func TestInitListReadAndWrite(t *testing.T) {
	ctx := context.Background()
	d, mock := newTestDriver(t)
	if err := d.Init(ctx); err != nil {
		t.Fatal(err)
	}
	if d.rootID != "docs-id" {
		t.Fatalf("rootID = %q, want docs-id", d.rootID)
	}
	entries, err := d.List(ctx, "0")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name != "hello #1.txt" {
		t.Fatalf("entries = %+v", entries)
	}
	rc, err := d.Read(ctx, entries[0], 6, 5)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(rc)
	rc.Close()
	if string(data) != "world" {
		t.Fatalf("read = %q", data)
	}
	dir, err := d.Mkdir(ctx, "0", "new dir")
	if err != nil {
		t.Fatal(err)
	}
	if !dir.IsDir || dir.ParentID != "docs-id" {
		t.Fatalf("mkdir entry = %+v", dir)
	}
	entry, err := d.PutSource(ctx, drive.UploadRequest{
		ParentID: "0",
		Name:     "small.txt",
		Source:   drive.NewBytesReadOnlyFileSource([]byte("small")),
	})
	if err != nil {
		t.Fatal(err)
	}
	if entry.Size != 5 {
		t.Fatalf("entry = %+v", entry)
	}
	if err := d.Rename(ctx, entry, "renamed.txt"); err != nil {
		t.Fatal(err)
	}
	if err := d.Move(ctx, drive.Entry{ID: entry.ID, Name: "renamed.txt"}, dir.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := mock.childByName(dir.ID, "renamed.txt"); !ok {
		t.Fatal("expected moved file under new dir")
	}
	if err := d.Remove(ctx, drive.Entry{ID: entry.ID}); err != nil {
		t.Fatal(err)
	}
	if _, ok := mock.get(entry.ID); ok {
		t.Fatal("expected removed file")
	}
}

func TestPutSourceLarge(t *testing.T) {
	ctx := context.Background()
	d, mock := newTestDriver(t)
	if err := d.Init(ctx); err != nil {
		t.Fatal(err)
	}
	content := bytes.Repeat([]byte("x"), oneDriveSmallUploadLimit+3)
	entry, err := d.PutSource(ctx, drive.UploadRequest{
		ParentID: "0",
		Name:     "large.bin",
		Source:   drive.NewBytesReadOnlyFileSource(content),
	})
	if err != nil {
		t.Fatal(err)
	}
	item, ok := mock.get(entry.ID)
	if !ok {
		t.Fatal("expected uploaded item")
	}
	if !bytes.Equal(item.data, content) {
		t.Fatal("large upload data mismatch")
	}
}

// TestPutSourceLargeResumes 验证大文件中断后重试按 nextExpectedRanges 续传：
// 已完整上传的分片跳过，中断分片重新上传，最终内容一致，且复用的是同一个
// provider 会话而非新建。
func TestPutSourceLargeResumes(t *testing.T) {
	ctx := context.Background()
	d, mock := newTestDriver(t)
	d.InstallStateStore(drive.NewFileStateStore(t.TempDir()))
	d.chunkSize = 3 << 20
	// 在第 2 个分片的 PUT 发送前注入失败：请求不到达服务器，确定性可复现。
	failer := &failChunkRoundTripper{base: d.client.Transport}
	d.client.Transport = failer
	if err := d.Init(ctx); err != nil {
		t.Fatal(err)
	}

	// 总大小必须超过 oneDriveSmallUploadLimit，且恰好两个分片。
	content := bytes.Repeat([]byte("z"), 2*(3<<20))
	if _, err := d.PutSource(ctx, drive.UploadRequest{
		ParentID: "0",
		Name:     "resume-large.bin",
		Source:   drive.NewBytesReadOnlyFileSource(content),
	}); err == nil {
		t.Fatal("expected first upload to fail at the second chunk")
	}

	mock.mu.RLock()
	var session *mockUploadSession
	for _, s := range mock.uploads {
		session = s
	}
	mock.mu.RUnlock()
	if session == nil {
		t.Fatal("expected one live upload session after the failed attempt")
	}
	if session.puts != 1 || !bytes.Equal(session.data, content[:3<<20]) {
		t.Fatalf("after failed attempt: puts=%d data=%d bytes, want chunk 0 only", session.puts, len(session.data))
	}

	entry, err := d.PutSource(ctx, drive.UploadRequest{
		ParentID: "0",
		Name:     "resume-large.bin",
		Source:   drive.NewBytesReadOnlyFileSource(content),
	})
	if err != nil {
		t.Fatal(err)
	}
	item, ok := mock.get(entry.ID)
	if !ok {
		t.Fatal("expected uploaded item")
	}
	if !bytes.Equal(item.data, content) {
		t.Fatal("resumed large upload data mismatch")
	}
	// 恢复必须跳过已完整上传的分片：同一会话只有 chunk0(1 次) + chunk1(1 次)。
	if session.puts != 2 {
		t.Fatalf("session puts = %d, want 2 (chunk 0 + resumed chunk 1)", session.puts)
	}
}

func TestPutSourceLargeReserveCleansUpOnCreateFailure(t *testing.T) {
	ctx := context.Background()
	d, mock := newTestDriver(t)
	store := drive.NewFileStateStore(t.TempDir())
	d.InstallStateStore(store)
	d.chunkSize = 3 << 20
	if err := d.Init(ctx); err != nil {
		t.Fatal(err)
	}
	mock.mu.Lock()
	mock.failCreateUploadSession = true
	mock.mu.Unlock()

	content := bytes.Repeat([]byte("q"), 2*(3<<20))
	if _, err := d.PutSource(ctx, drive.UploadRequest{
		ParentID: "0",
		Name:     "reserve-fail.bin",
		Source:   drive.NewBytesReadOnlyFileSource(content),
	}); err == nil {
		t.Fatal("expected create upload session failure to surface")
	}
	if len(mock.uploads) != 0 {
		t.Fatalf("expected no provider session to survive a failed create, got %d", len(mock.uploads))
	}
	// 预留绑定必须随创建失败一起清理：内存和盘面都回到未预留状态，
	// 崩溃/umount 后重启不会看到指向不存在会话的陈旧记录。
	if bindings := d.sessions.List(); len(bindings) != 0 {
		t.Fatalf("expected no in-memory binding after failed create, got %d", len(bindings))
	}
	reloaded := session.NewIndex(store, oneDriveSessionFile, session.IndexOptions{})
	if bindings := reloaded.List(); len(bindings) != 0 {
		t.Fatalf("expected no persisted binding after failed create, got %d", len(bindings))
	}
}

// failChunkRoundTripper 在第一个非零起始偏移的分片 PUT 发送前失败一次，
// 模拟上传中断（请求不会到达服务器，避免 body 中止导致的连接悬挂）。
type failChunkRoundTripper struct {
	base   http.RoundTripper
	failed bool
}

func (f *failChunkRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method == http.MethodPut && !f.failed {
		var start int64
		if n, _ := fmt.Sscanf(req.Header.Get("Content-Range"), "bytes %d-", &start); n == 1 && start > 0 {
			f.failed = true
			return nil, errors.New("injected chunk failure")
		}
	}
	return f.base.RoundTrip(req)
}

func (m *mockOneDrive) get(id string) (*mockItem, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	item, ok := m.items[id]
	return item, ok
}

// TestDriverRemoteHashFromGraphHashes verifies RemoteHash reads file.hashes
// from the Graph API item (and degrades to unsupported when absent).
func TestDriverRemoteHashFromGraphHashes(t *testing.T) {
	driver, mock := newTestDriver(t)
	mock.mu.Lock()
	mock.items["file-id"].sha1 = "da39a3ee5e6b4b0d3255bfef95601890afd80709"
	mock.mu.Unlock()

	ctx := context.Background()
	entries, err := driver.List(ctx, "docs-id")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(entries))
	}
	algorithm, hash, err := driver.RemoteHash(ctx, entries[0])
	if err != nil {
		t.Fatal(err)
	}
	if algorithm != drive.HashSHA1 || hash != "da39a3ee5e6b4b0d3255bfef95601890afd80709" {
		t.Fatalf("RemoteHash = (%s, %s), want (sha1, graph sha1Hash)", algorithm, hash)
	}
	// A fresh file without hashes degrades cleanly.
	mock.mu.Lock()
	mock.items["file-id"].sha1 = ""
	mock.mu.Unlock()
	entries, err = driver.List(ctx, "docs-id")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := driver.RemoteHash(ctx, entries[0]); err != drive.ErrUnsupported {
		t.Fatalf("RemoteHash without hashes err = %v, want ErrUnsupported", err)
	}
}

func TestOneDriveServerSideCopy(t *testing.T) {
	mock := newMockOneDrive()
	srv := httptest.NewServer(mock)
	defer srv.Close()
	d, _ := newTestDriver(t)

	src := drive.Entry{ID: "file-id", ParentID: "docs-id", Name: "hello #1.txt", Size: 11}
	dst, err := d.Copy(context.Background(), src, "docs-id", "copied.txt")
	if err != nil {
		t.Fatal(err)
	}
	if dst.ID == "" || dst.ID == src.ID {
		t.Fatalf("copy entry id = %q, want a new item id", dst.ID)
	}
	if dst.Name != "copied.txt" || dst.ParentID != "docs-id" {
		t.Fatalf("copy entry = %+v", dst)
	}
	// The source still exists and the copy has identical content.
	rc, err := d.Read(context.Background(), src, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	srcBody, _ := io.ReadAll(rc)
	rc.Close()
	rc, err = d.Read(context.Background(), dst, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	dstBody, _ := io.ReadAll(rc)
	rc.Close()
	if string(srcBody) != "hello world" || string(dstBody) != "hello world" {
		t.Fatalf("content after copy: src=%q dst=%q", srcBody, dstBody)
	}
}

func TestOneDriveCopyRejectsDirectory(t *testing.T) {
	mock := newMockOneDrive()
	srv := httptest.NewServer(mock)
	defer srv.Close()
	d, _ := newTestDriver(t)
	if _, err := d.Copy(context.Background(), drive.Entry{ID: "docs-id", IsDir: true}, "root-id", "x"); err == nil {
		t.Fatal("directory copy should fail")
	}
}
