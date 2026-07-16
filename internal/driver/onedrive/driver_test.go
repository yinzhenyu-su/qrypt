package onedrive

import (
	"bytes"
	"context"
	"encoding/json"
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
)

type mockItem struct {
	id       string
	parentID string
	name     string
	data     []byte
	isDir    bool
	modTime  time.Time
}

type mockOneDrive struct {
	mu       sync.RWMutex
	items    map[string]*mockItem
	children map[string][]string
	nextID   int
	uploads  map[string]*mockUploadSession
}

type mockUploadSession struct {
	parentID string
	name     string
	size     int64
	data     []byte
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
	w.Write(data)
}

func (m *mockOneDrive) handleUploadSession(w http.ResponseWriter, r *http.Request) {
	uploadID, _ := url.PathUnescape(strings.TrimPrefix(r.URL.Path, "/upload/"))
	data, _ := io.ReadAll(r.Body)
	m.mu.Lock()
	session := m.uploads[uploadID]
	if session == nil {
		m.mu.Unlock()
		http.NotFound(w, r)
		return
	}
	session.data = append(session.data, data...)
	total := int64(len(session.data))
	expected := int64(0)
	if _, after, ok := strings.Cut(r.Header.Get("Content-Range"), "/"); ok {
		expected, _ = strconv.ParseInt(after, 10, 64)
	}
	if expected > 0 && total >= expected {
		m.createItemLocked(session.parentID, session.name, false, session.data)
		delete(m.uploads, uploadID)
		m.mu.Unlock()
		w.WriteHeader(http.StatusCreated)
		writeJSON(w, map[string]string{"id": "complete"})
		return
	}
	m.mu.Unlock()
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
		resp["file"] = map[string]string{"mimeType": "application/octet-stream"}
		resp["@microsoft.graph.downloadUrl"] = "http://" + r.Host + "/download/" + item.id
	}
	return resp
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
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

func (m *mockOneDrive) get(id string) (*mockItem, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	item, ok := m.items[id]
	return item, ok
}
