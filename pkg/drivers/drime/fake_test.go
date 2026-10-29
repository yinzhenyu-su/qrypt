package drime

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
)

type recordedRequest struct {
	method string
	path   string
	query  url.Values
	body   []byte
}

type fakeUpload struct {
	key   string
	parts map[int]string
}

type fakeProvider struct {
	t      *testing.T
	server *httptest.Server

	mu            sync.Mutex
	token         string
	workspaceID   string
	nextID        int64
	entries       map[int64]*apiEntry
	children      map[int64][]int64
	links         map[string]*apiShareLink
	uploads       map[string]*fakeUpload
	requests      []recordedRequest
	content       map[string][]byte
	ignoreRange   bool
	redirect      bool
	forceStatus   map[string]int
	childDelay    map[int64]int
	failPart      int
	repeatPage    bool
	neverEnding   bool
	plainPassword map[string]string
	presigned     map[string][]byte
	force416      bool
	malformed     map[string]bool
	retryOnce     bool
	verifyFalse   bool
	failImportAt  int
	importCalls   int
}

func newFakeProvider(t *testing.T) *fakeProvider {
	t.Helper()
	f := &fakeProvider{
		t:             t,
		token:         "test-token",
		workspaceID:   "0",
		nextID:        100,
		entries:       map[int64]*apiEntry{},
		children:      map[int64][]int64{},
		links:         map[string]*apiShareLink{},
		uploads:       map[string]*fakeUpload{},
		content:       map[string][]byte{},
		forceStatus:   map[string]int{},
		childDelay:    map[int64]int{},
		plainPassword: map[string]string{},
		presigned:     map[string][]byte{},
		malformed:     map[string]bool{},
	}
	f.server = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeProvider) apiBase() string { return f.server.URL + "/api/v1" }

func (f *fakeProvider) newDriver(t *testing.T, opts Options) *Driver {
	t.Helper()
	if opts.AccessToken == "" {
		opts.AccessToken = f.token
	}
	if opts.APIBaseURL == "" {
		opts.APIBaseURL = f.apiBase()
	}
	if opts.WorkspaceID == "" {
		opts.WorkspaceID = f.workspaceID
	}
	if opts.ChunkSize == 0 {
		opts.ChunkSize = minChunkSize
	}
	d := New(opts)
	d.InstallStateStore(&driveMemoryStore{data: map[string][]byte{}})
	return d
}

type driveMemoryStore struct {
	mu   sync.Mutex
	data map[string][]byte
}

func (s *driveMemoryStore) LoadJSON(name string, out any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, ok := s.data[name]
	if !ok {
		return fmt.Errorf("missing")
	}
	return json.Unmarshal(raw, out)
}

func (s *driveMemoryStore) SaveJSON(name string, value any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.data == nil {
		s.data = map[string][]byte{}
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	s.data[name] = raw
	return nil
}

func int64p(v int64) *int64 { return &v }

func (f *fakeProvider) addEntry(parent int64, name string, isDir bool, size int64, hash, fileHash string) *apiEntry {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.addEntryLocked(parent, name, isDir, size, hash, fileHash)
}

func (f *fakeProvider) addEntryLocked(parent int64, name string, isDir bool, size int64, hash, fileHash string) *apiEntry {
	id := f.nextID
	f.nextID++
	var parentPtr *int64
	if parent != 0 {
		parentPtr = int64p(parent)
	}
	kind := "file"
	if isDir {
		kind = "folder"
	}
	var fh *string
	if fileHash != "" {
		fh = &fileHash
	}
	entry := &apiEntry{
		ID:          id,
		Name:        name,
		Type:        kind,
		FileSize:    size,
		ParentID:    parentPtr,
		WorkspaceID: parseWorkspace(f.workspaceID),
		Hash:        fmt.Sprintf("hash-%d", id),
		FileHash:    fh,
		UpdatedAt:   "2026-10-29T15:00:00.000000Z",
		CreatedAt:   "2026-10-29T15:00:00.000000Z",
		URL:         fmt.Sprintf("api/v1/file-entries/%d", id),
		Permissions: map[string]bool{"files.download": true},
	}
	f.entries[id] = entry
	f.children[parent] = append(f.children[parent], id)
	return entry
}

func parseWorkspace(raw string) int64 {
	n, _ := strconv.ParseInt(raw, 10, 64)
	return n
}

func (f *fakeProvider) findChild(parent int64, name string) *apiEntry {
	for _, id := range f.children[parent] {
		if e := f.entries[id]; e != nil && e.Name == name {
			return e
		}
	}
	return nil
}

func (f *fakeProvider) handler(w http.ResponseWriter, r *http.Request) bool {
	if status, ok := f.forceStatus[r.URL.Path]; ok {
		writeStatus(w, status, `{"message":"forced"}`)
		return true
	}
	return false
}

func (f *fakeProvider) handle(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	_ = r.Body.Close()
	f.mu.Lock()
	f.requests = append(f.requests, recordedRequest{method: r.Method, path: r.URL.Path, query: r.URL.Query(), body: body})
	f.mu.Unlock()
	if f.handler(w, r) {
		return
	}
	f.mu.Lock()
	if f.retryOnce && r.URL.Path == "/api/v1/drive/file-entries" {
		f.retryOnce = false
		f.mu.Unlock()
		w.Header().Set("Retry-After", "0")
		writeStatus(w, 429, `{"message":"rate limited"}`)
		return
	}
	if f.malformed[r.URL.Path] {
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_, _ = w.Write([]byte("{not-json"))
		return
	}
	f.mu.Unlock()

	path := r.URL.Path
	switch {
	case path == "/api/v1/users/me":
		if !f.auth(w, r) {
			return
		}
		writeJSON(w, 200, map[string]any{"status": "success", "user": map[string]any{"id": 1, "email": "probe@example.com"}})
	case path == "/api/v1/me/workspaces":
		if !f.auth(w, r) {
			return
		}
		writeJSON(w, 200, map[string]any{"status": "success", "workspaces": []any{map[string]any{"id": 77, "name": "TestWorkSpace"}}})
	case path == "/api/v1/user/space-usage":
		if !f.auth(w, r) {
			return
		}
		writeJSON(w, 200, map[string]any{"status": "success", "used": 100, "available": 900})
	case path == "/api/v1/drive/file-entries":
		if !f.auth(w, r) {
			return
		}
		f.list(w, r)
	case path == "/api/v1/file-entries/hash/check":
		if !f.auth(w, r) {
			return
		}
		f.hashCheck(w, body)
	case path == "/api/v1/folders":
		if !f.auth(w, r) {
			return
		}
		f.mkdir(w, r, body)
	case strings.HasPrefix(path, "/api/v1/file-entries/download/"):
		f.downloadHash(w, r)
	case strings.HasPrefix(path, "/content/"):
		id, _ := strconv.ParseInt(strings.TrimPrefix(path, "/content/"), 10, 64)
		f.serveContent(w, r, id)
	case path != "" && strings.HasPrefix(path, "/api/v1/file-entries/") && r.Method == http.MethodGet && !strings.Contains(strings.TrimPrefix(path, "/api/v1/file-entries/"), "/"):
		id, _ := strconv.ParseInt(strings.TrimPrefix(path, "/api/v1/file-entries/"), 10, 64)
		f.downloadByID(w, r, id)
	case strings.HasPrefix(path, "/api/v1/file-entries/") && strings.HasSuffix(path, "/model"):
		if !f.auth(w, r) {
			return
		}
		f.model(w, r)
	case strings.HasPrefix(path, "/api/v1/file-entries/") && strings.HasSuffix(path, "/shareable-link"):
		if !f.auth(w, r) {
			return
		}
		f.shareLink(w, r, body)
	case strings.HasPrefix(path, "/api/v1/file-entries/") && strings.Contains(path, "/verify-integrity"):
		f.mu.Lock()
		verified := !f.verifyFalse
		f.mu.Unlock()
		writeJSON(w, 200, map[string]any{"status": "success", "verified": verified})
	case strings.HasPrefix(path, "/api/v1/file-entries/") && path != "/api/v1/file-entries/move" && path != "/api/v1/file-entries/duplicate" && path != "/api/v1/file-entries/delete":
		if !f.auth(w, r) {
			return
		}
		f.entryRoute(w, r, body)
	case path == "/api/v1/file-entries/move":
		if !f.auth(w, r) {
			return
		}
		f.move(w, r, body)
	case path == "/api/v1/file-entries/duplicate":
		if !f.auth(w, r) {
			return
		}
		f.duplicate(w, r, body)
	case path == "/api/v1/file-entries/delete":
		if !f.auth(w, r) {
			return
		}
		f.delete(w, r, body)
	case path == "/api/v1/uploads":
		if !f.auth(w, r) {
			return
		}
		f.upload(w, r, body)
	case path == "/api/v1/s3/simple/presign":
		if !f.auth(w, r) {
			return
		}
		f.presign(w, body)
	case strings.HasPrefix(path, "/r2/simple/"):
		f.mu.Lock()
		f.presigned[strings.TrimPrefix(path, "/r2/simple/")] = body
		f.mu.Unlock()
		w.WriteHeader(200)
	case path == "/api/v1/s3/multipart/create":
		if !f.auth(w, r) {
			return
		}
		f.multipartCreate(w, r, body)
	case path == "/api/v1/s3/multipart/batch-sign-part-urls":
		if !f.auth(w, r) {
			return
		}
		f.signParts(w, r, body)
	case strings.HasPrefix(path, "/part/"):
		f.putPart(w, r, body)
	case path == "/api/v1/s3/multipart/get-uploaded-parts":
		if !f.auth(w, r) {
			return
		}
		f.uploadedParts(w, r, body)
	case path == "/api/v1/s3/multipart/complete":
		if !f.auth(w, r) {
			return
		}
		f.complete(w, r, body)
	case path == "/api/v1/s3/multipart/abort":
		if !f.auth(w, r) {
			return
		}
		f.abort(w, r, body)
	case path == "/api/v1/s3/entries":
		if !f.auth(w, r) {
			return
		}
		f.createEntryFromUpload(w, r, body)
	case strings.HasPrefix(path, "/api/v1/shareable-links/") && strings.HasSuffix(path, "/import") && r.Method == http.MethodPost:
		if !f.auth(w, r) {
			return
		}
		f.importShare(w, r, body)
	case strings.HasPrefix(path, "/api/v1/shareable-links/"):
		f.publicShare(w, r)
	case strings.HasPrefix(path, "/api/v1/folders/") && strings.HasSuffix(path, "/path"):
		writeJSON(w, 200, map[string]any{"status": "success", "path": []any{}})
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeProvider) auth(w http.ResponseWriter, r *http.Request) bool {
	if r.Header.Get("Authorization") != "Bearer "+f.token {
		writeStatus(w, 401, `{"message":"Unauthenticated."}`)
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeStatus(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}

func (f *fakeProvider) list(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	workspace := r.URL.Query().Get("workspaceId")
	folderID := int64(0)
	if raw := r.URL.Query().Get("folderId"); raw != "" {
		folderID, _ = strconv.ParseInt(raw, 10, 64)
		if f.entries[folderID] == nil {
			writeStatus(w, 404, `{"message":"This folder does not exist or you do not have access to it."}`)
			return
		}
	}
	if workspace != f.workspaceID && folderID != 0 {
		writeJSON(w, 200, apiListing{Data: []apiEntry{}, CurrentPage: 1, LastPage: 1, PerPage: 100, Total: 0})
		return
	}
	ids := append([]int64(nil), f.children[folderID]...)
	if f.childDelay[folderID] > 0 {
		f.childDelay[folderID]--
		ids = nil
	}
	sort.Slice(ids, func(i, j int) bool { return f.entries[ids[i]].Name < f.entries[ids[j]].Name })
	perPage, _ := strconv.Atoi(r.URL.Query().Get("perPage"))
	if perPage <= 0 || perPage > 100 {
		perPage = 100
	}
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page <= 0 {
		page = 1
	}
	if f.repeatPage && page > 1 {
		page = 1
	}
	if f.neverEnding {
		data := make([]apiEntry, 0, 100)
		for i := 0; i < 100; i++ {
			data = append(data, apiEntry{
				ID:          int64(page*1000 + i + 1),
				Name:        fmt.Sprintf("p%03d-%03d", page, i),
				Type:        "file",
				FileSize:    1,
				Hash:        fmt.Sprintf("hash-%d-%d", page, i),
				UpdatedAt:   "2026-10-29T15:00:00.000000Z",
				Permissions: map[string]bool{"files.download": true},
			})
		}
		writeJSON(w, 200, apiListing{Data: data, CurrentPage: page, LastPage: page + 1, PerPage: 100, Total: 1 << 30})
		return
	}
	last := (len(ids) + perPage - 1) / perPage
	if last == 0 {
		last = 1
	}
	start := (page - 1) * perPage
	end := start + perPage
	if start > len(ids) {
		start = len(ids)
	}
	if end > len(ids) {
		end = len(ids)
	}
	data := make([]apiEntry, 0, end-start)
	for _, id := range ids[start:end] {
		data = append(data, *f.entries[id])
	}
	writeJSON(w, 200, apiListing{Data: data, CurrentPage: page, LastPage: last, PerPage: perPage, Total: len(ids)})
}

func (f *fakeProvider) mkdir(w http.ResponseWriter, r *http.Request, body []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var payload struct {
		Name     string `json:"name"`
		ParentID *int64 `json:"parentId"`
	}
	_ = json.Unmarshal(body, &payload)
	parent := int64(0)
	if payload.ParentID != nil {
		parent = *payload.ParentID
	}
	if existing := f.findChild(parent, payload.Name); existing != nil {
		if existing.isDir() {
			writeJSON(w, 200, apiFolderResponse{Status: "success", Folder: *existing})
			return
		}
		writeStatus(w, 422, `{"message":"name exists"}`)
		return
	}
	entry := f.addEntryLocked(parent, payload.Name, true, 0, "", "")
	writeJSON(w, 200, apiFolderResponse{Status: "success", Folder: *entry})
}

func (f *fakeProvider) hashCheck(w http.ResponseWriter, body []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var payload struct {
		FileHash string `json:"fileHash"`
	}
	_ = json.Unmarshal(body, &payload)
	for _, entry := range f.entries {
		if entry.FileHash != nil && *entry.FileHash == payload.FileHash && !entry.isDir() {
			writeJSON(w, 200, apiHashCheckResponse{Status: "success", Exists: true, Entry: entry})
			return
		}
	}
	writeJSON(w, 200, apiHashCheckResponse{Status: "success", Exists: false})
}

func (f *fakeProvider) model(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := pathID(r.URL.Path, "/api/v1/file-entries/", "/model")
	entry := f.entries[id]
	if entry == nil {
		writeStatus(w, 404, `{"message":"No query results for model"}`)
		return
	}
	writeJSON(w, 200, map[string]any{"status": "success", "fileEntry": entry})
}

func pathID(path, prefix, suffix string) int64 {
	raw := strings.TrimPrefix(path, prefix)
	raw = strings.TrimSuffix(raw, suffix)
	id, _ := strconv.ParseInt(strings.Trim(raw, "/"), 10, 64)
	return id
}

func (f *fakeProvider) entryRoute(w http.ResponseWriter, r *http.Request, body []byte) {
	id := pathID(r.URL.Path, "/api/v1/file-entries/", "")
	f.mu.Lock()
	defer f.mu.Unlock()
	entry := f.entries[id]
	if entry == nil {
		writeStatus(w, 404, `{"message":"not found"}`)
		return
	}
	switch r.Method {
	case http.MethodPut:
		var payload struct {
			Name string `json:"name"`
		}
		_ = json.Unmarshal(body, &payload)
		entry.Name = payload.Name
		writeJSON(w, 200, map[string]any{"status": "success", "fileEntry": entry})
	case http.MethodPost:
		// The rclone method-override workaround is not supported.
		writeStatus(w, 405, `{"message":"The POST method is not supported"}`)
	default:
		writeStatus(w, 405, `{"message":"method"}`)
	}
}

func (f *fakeProvider) move(w http.ResponseWriter, r *http.Request, body []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var payload struct {
		EntryIDs      []string `json:"entryIds"`
		DestinationID *int64   `json:"destinationId"`
	}
	_ = json.Unmarshal(body, &payload)
	dest := int64(0)
	if payload.DestinationID != nil {
		dest = *payload.DestinationID
	}
	var entries []apiEntry
	for _, raw := range payload.EntryIDs {
		id, _ := strconv.ParseInt(raw, 10, 64)
		entry := f.entries[id]
		if entry == nil {
			continue
		}
		f.removeChild(apiEntryParent(entry), id)
		entry.ParentID = int64p(dest)
		f.children[dest] = append(f.children[dest], id)
		entries = append(entries, *entry)
	}
	writeJSON(w, 200, apiMoveResponse{Status: "success", Entries: entries})
}

func apiEntryParent(entry *apiEntry) int64 {
	if entry.ParentID == nil {
		return 0
	}
	return *entry.ParentID
}

func (f *fakeProvider) removeChild(parent, id int64) {
	ids := f.children[parent]
	for i, existing := range ids {
		if existing == id {
			f.children[parent] = append(ids[:i], ids[i+1:]...)
			return
		}
	}
}

func (f *fakeProvider) duplicate(w http.ResponseWriter, r *http.Request, body []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var payload struct {
		EntryIDs      []string `json:"entryIds"`
		DestinationID *int64   `json:"destinationId"`
	}
	_ = json.Unmarshal(body, &payload)
	dest := int64(0)
	if payload.DestinationID != nil {
		dest = *payload.DestinationID
	}
	var entries []apiEntry
	for _, raw := range payload.EntryIDs {
		id, _ := strconv.ParseInt(raw, 10, 64)
		src := f.entries[id]
		if src == nil {
			continue
		}
		name := src.Name
		if existing := f.findChild(dest, name); existing != nil {
			name = duplicateName(src.Name, 1)
		}
		cloned := f.addEntryLocked(dest, name, src.isDir(), src.FileSize, src.Hash, "")
		if src.FileHash != nil {
			cloned.FileHash = src.FileHash
		}
		cloned.ClientLastModified = src.ClientLastModified
		entries = append(entries, *cloned)
	}
	writeJSON(w, 200, apiMoveResponse{Status: "success", Entries: entries})
}

func duplicateName(name string, n int) string {
	ext := ""
	base := name
	if i := strings.LastIndex(name, "."); i > 0 {
		base, ext = name[:i], name[i:]
	}
	return fmt.Sprintf("%s (%d)%s", base, n, ext)
}

func (f *fakeProvider) delete(w http.ResponseWriter, r *http.Request, body []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var payload struct {
		EntryIDs      []string `json:"entryIds"`
		DeleteForever bool     `json:"deleteForever"`
	}
	_ = json.Unmarshal(body, &payload)
	marked := 0
	var bytes int64
	for _, raw := range payload.EntryIDs {
		id, _ := strconv.ParseInt(raw, 10, 64)
		entry := f.entries[id]
		if entry == nil {
			writeStatus(w, 422, `{"message":"invalid","errors":{"entryIds":["invalid"]}}`)
			return
		}
		marked += 1 + len(f.children[id])
		bytes += entry.FileSize
		for _, child := range f.children[id] {
			delete(f.entries, child)
		}
		delete(f.children, id)
		f.removeChild(apiEntryParent(entry), id)
		delete(f.entries, id)
	}
	writeJSON(w, 200, apiDeleteResponse{Status: "success", Marked: marked, Bytes: bytes})
}

func (f *fakeProvider) upload(w http.ResponseWriter, r *http.Request, body []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, params, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	reader := multipart.NewReader(bytes.NewReader(body), params["boundary"])
	form, err := reader.ReadForm(32 << 20)
	if err != nil {
		writeStatus(w, 422, `{"message":"bad form"}`)
		return
	}
	defer func() { _ = form.RemoveAll() }()
	name := "upload.bin"
	size := int64(0)
	if files := form.File["file"]; len(files) > 0 {
		header := files[0]
		name = header.Filename
		if file, err := header.Open(); err == nil {
			data, _ := io.ReadAll(file)
			file.Close()
			size = int64(len(data))
		}
	}
	parent, _ := strconv.ParseInt(form.Value["parentId"][0], 10, 64)
	hash := ""
	if values := form.Value["fileHash"]; len(values) > 0 {
		hash = values[0]
	}
	entry := f.addEntryLocked(parent, name, false, size, "", hash)
	writeJSON(w, 201, apiUploadResponse{Status: "success", FileEntry: *entry})
}

func (f *fakeProvider) presign(w http.ResponseWriter, body []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var payload struct {
		Filename string `json:"filename"`
	}
	_ = json.Unmarshal(body, &payload)
	id := fmt.Sprintf("pre-%d", len(f.uploads)+len(f.presigned)+1)
	writeJSON(w, 200, apiPresignResponse{Status: "success", URL: f.server.URL + "/r2/simple/" + id, Key: "uploads/" + id + "/" + id})
}

func (f *fakeProvider) multipartCreate(w http.ResponseWriter, r *http.Request, body []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var payload struct {
		Filename string `json:"filename"`
		Size     int64  `json:"size"`
	}
	_ = json.Unmarshal(body, &payload)
	uploadID := fmt.Sprintf("upload-%d", len(f.uploads)+1)
	key := "uploads/" + uploadID + "/" + uploadID
	f.uploads[uploadID] = &fakeUpload{key: key, parts: map[int]string{}}
	writeJSON(w, 200, apiMultipartCreateResponse{Status: "success", Key: key, UploadID: uploadID})
}

func (f *fakeProvider) signParts(w http.ResponseWriter, r *http.Request, body []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var payload struct {
		UploadID    string `json:"uploadId"`
		PartNumbers []int  `json:"partNumbers"`
	}
	_ = json.Unmarshal(body, &payload)
	urls := make([]apiSignedPart, 0, len(payload.PartNumbers))
	for _, part := range payload.PartNumbers {
		urls = append(urls, apiSignedPart{PartNumber: part, URL: f.server.URL + "/part/" + payload.UploadID + "/" + strconv.Itoa(part)})
	}
	writeJSON(w, 200, apiSignPartsResponse{Status: "success", URLs: urls})
}

func (f *fakeProvider) putPart(w http.ResponseWriter, r *http.Request, body []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/part/"), "/")
	if len(parts) != 2 {
		writeStatus(w, 404, `{"message":"part"}`)
		return
	}
	upload := f.uploads[parts[0]]
	if upload == nil {
		writeStatus(w, 404, `{"message":"upload"}`)
		return
	}
	num, _ := strconv.Atoi(parts[1])
	if f.failPart == num {
		writeStatus(w, 500, `{"message":"part failed"}`)
		return
	}
	upload.parts[num] = fmt.Sprintf("\"etag-%d\"", num)
	w.Header().Set("ETag", upload.parts[num])
	w.WriteHeader(200)
}

func (f *fakeProvider) uploadedParts(w http.ResponseWriter, r *http.Request, body []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var payload struct {
		UploadID string `json:"uploadId"`
	}
	_ = json.Unmarshal(body, &payload)
	upload := f.uploads[payload.UploadID]
	if upload == nil {
		writeStatus(w, 404, `{"message":"upload not found"}`)
		return
	}
	var parts []apiUploadedPart
	for num, etag := range upload.parts {
		parts = append(parts, apiUploadedPart{PartNumber: num, ETag: etag, Size: 1})
	}
	sort.Slice(parts, func(i, j int) bool { return parts[i].PartNumber < parts[j].PartNumber })
	writeJSON(w, 200, apiUploadedPartsResponse{Status: "success", Parts: parts})
}

func (f *fakeProvider) complete(w http.ResponseWriter, r *http.Request, body []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var payload struct {
		UploadID string `json:"uploadId"`
	}
	_ = json.Unmarshal(body, &payload)
	if f.uploads[payload.UploadID] == nil {
		writeStatus(w, 404, `{"message":"upload not found"}`)
		return
	}
	writeJSON(w, 200, apiCompleteResponse{Status: "success", Location: "https://storage.example/object"})
}

func (f *fakeProvider) abort(w http.ResponseWriter, r *http.Request, body []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var payload struct {
		UploadID string `json:"uploadId"`
	}
	_ = json.Unmarshal(body, &payload)
	delete(f.uploads, payload.UploadID)
	writeJSON(w, 200, map[string]any{"status": "success"})
}

func (f *fakeProvider) createEntryFromUpload(w http.ResponseWriter, r *http.Request, body []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var payload struct {
		Filename        string `json:"filename"`
		Size            int64  `json:"size"`
		ClientName      string `json:"clientName"`
		ClientExtension string `json:"clientExtension"`
		ParentID        *int64 `json:"parentId"`
		FileHash        string `json:"fileHash"`
	}
	_ = json.Unmarshal(body, &payload)
	parent := int64(0)
	if payload.ParentID != nil {
		parent = *payload.ParentID
	}
	entry := f.addEntryLocked(parent, payload.ClientName, false, payload.Size, "", payload.FileHash)
	writeJSON(w, 200, apiUploadResponse{Status: "success", FileEntry: *entry})
}

func (f *fakeProvider) downloadHash(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	raw := strings.TrimPrefix(r.URL.Path, "/api/v1/file-entries/download/")
	hash := strings.Trim(raw, "/")
	id := int64(0)
	for _, entry := range f.entries {
		if entry.Hash == hash {
			id = entry.ID
			break
		}
	}
	if id == 0 {
		writeStatus(w, 404, `{"message":"not found"}`)
		return
	}
	f.serveContentLocked(w, r, id)
}

func (f *fakeProvider) downloadByID(w http.ResponseWriter, r *http.Request, id int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.redirect {
		w.Header().Set("Location", "/content/"+strconv.FormatInt(id, 10))
		w.WriteHeader(http.StatusFound)
		return
	}
	f.serveContentLocked(w, r, id)
}

func (f *fakeProvider) serveContent(w http.ResponseWriter, r *http.Request, id int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.serveContentLocked(w, r, id)
}

func (f *fakeProvider) serveContentLocked(w http.ResponseWriter, r *http.Request, id int64) {
	if f.force416 {
		w.Header().Set("Content-Range", "bytes */1")
		w.WriteHeader(416)
		return
	}
	entry := f.entries[id]
	if entry == nil {
		writeStatus(w, 404, `{"message":"not found"}`)
		return
	}
	data := f.content[strconv.FormatInt(id, 10)]
	if data == nil {
		data = []byte(strings.Repeat("x", int(entry.FileSize)))
	}
	rangeHeader := r.Header.Get("Range")
	if rangeHeader == "" || f.ignoreRange {
		w.WriteHeader(200)
		_, _ = w.Write(data)
		return
	}
	var start, end int64
	if _, err := fmt.Sscanf(rangeHeader, "bytes=%d-%d", &start, &end); err != nil {
		if _, err := fmt.Sscanf(rangeHeader, "bytes=%d-", &start); err != nil {
			writeStatus(w, 400, `{"message":"bad range"}`)
			return
		}
		end = int64(len(data)) - 1
	}
	if start >= int64(len(data)) {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", len(data)))
		w.WriteHeader(416)
		return
	}
	if end >= int64(len(data)) {
		end = int64(len(data)) - 1
	}
	w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(data)))
	w.WriteHeader(206)
	_, _ = w.Write(data[start : end+1])
}

func (f *fakeProvider) shareLink(w http.ResponseWriter, r *http.Request, body []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := pathID(r.URL.Path, "/api/v1/file-entries/", "/shareable-link")
	entry := f.entries[id]
	if entry == nil {
		writeStatus(w, 404, `{"message":"not found"}`)
		return
	}
	var existing *apiShareLink
	for _, link := range f.links {
		if link.EntryID == id {
			existing = link
			break
		}
	}
	switch r.Method {
	case http.MethodGet:
		if existing == nil {
			writeStatus(w, 404, `{"message":"not found"}`)
			return
		}
		writeJSON(w, 200, apiShareLinkResponse{Status: "success", Link: existing})
	case http.MethodPost:
		var payload struct {
			Password      string  `json:"password"`
			ExpiresAt     *string `json:"expires_at"`
			AllowDownload bool    `json:"allow_download"`
			AllowEdit     bool    `json:"allow_edit"`
		}
		_ = json.Unmarshal(body, &payload)
		link := &apiShareLink{
			ID:            1000 + id,
			Hash:          fmt.Sprintf("link-%d", id),
			EntryID:       id,
			AllowDownload: payload.AllowDownload,
			AllowEdit:     payload.AllowEdit,
			ExpiresAt:     payload.ExpiresAt,
			Entry:         entry,
		}
		if payload.Password != "" {
			hashed := "hashed"
			link.Password = &hashed
			f.plainPassword[link.Hash] = payload.Password
		}
		f.links[link.Hash] = link
		writeJSON(w, 200, apiShareLinkResponse{Status: "success", Link: link})
	case http.MethodPut:
		if existing == nil {
			writeStatus(w, 404, `{"message":"not found"}`)
			return
		}
		var payload struct {
			Password  *string `json:"password"`
			ExpiresAt *string `json:"expiresAt"`
		}
		_ = json.Unmarshal(body, &payload)
		if payload.Password != nil {
			hashed := "hashed"
			existing.Password = &hashed
		}
		existing.ExpiresAt = payload.ExpiresAt
		writeJSON(w, 200, apiShareLinkResponse{Status: "success", Link: existing})
	case http.MethodDelete:
		if existing == nil {
			writeStatus(w, 404, `{"message":"not found"}`)
			return
		}
		delete(f.links, existing.Hash)
		writeJSON(w, 200, map[string]any{"status": "success"})
	}
}

func (f *fakeProvider) importShare(w http.ResponseWriter, r *http.Request, body []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.importCalls++
	if f.failImportAt > 0 && f.importCalls == f.failImportAt {
		writeStatus(w, 500, `{"message":"import failed"}`)
		return
	}
	raw := strings.TrimPrefix(r.URL.Path, "/api/v1/shareable-links/")
	raw = strings.TrimSuffix(raw, "/import")
	link := f.links[raw]
	if link == nil {
		writeStatus(w, 404, `{"message":"link not found"}`)
		return
	}
	var payload struct {
		EntryID       string `json:"entryId"`
		DestinationID *int64 `json:"destinationId"`
	}
	_ = json.Unmarshal(body, &payload)
	sourceID := link.EntryID
	if payload.EntryID != "" {
		for _, entry := range f.entries {
			if entry.Hash == payload.EntryID {
				sourceID = entry.ID
				break
			}
		}
	}
	source := f.entries[sourceID]
	if source == nil {
		writeStatus(w, 404, `{"message":"entry not found"}`)
		return
	}
	dest := int64(0)
	if payload.DestinationID != nil {
		dest = *payload.DestinationID
	}
	name := source.Name
	if f.findChild(dest, name) != nil {
		name = duplicateName(name, 1)
	}
	copied := f.addEntryLocked(dest, name, source.isDir(), source.FileSize, source.Hash, "")
	writeJSON(w, 200, apiImportResponse{Status: "success", Entry: copied})
}

func (f *fakeProvider) publicShare(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	raw := strings.TrimPrefix(r.URL.Path, "/api/v1/shareable-links/")
	parts := strings.Split(raw, ":")
	link := f.links[parts[0]]
	if link == nil {
		writeStatus(w, 404, `{"message":"link not found"}`)
		return
	}
	if link.Password != nil && r.URL.Query().Get("password") != f.plainPassword[link.Hash] && r.Header.Get("Authorization") != "Bearer "+f.token {
		writeStatus(w, 403, `{"passwordInvalid":true,"message":"Password is not valid"}`)
		return
	}
	folderID := link.EntryID
	if len(parts) == 2 {
		for _, entry := range f.entries {
			if entry.Hash == parts[1] {
				folderID = entry.ID
				break
			}
		}
	}
	var children *apiListing
	if r.URL.Query().Get("withEntries") == "true" {
		ids := f.children[folderID]
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		if page <= 0 {
			page = 1
		}
		perPage := 50
		last := (len(ids) + perPage - 1) / perPage
		if last == 0 {
			last = 1
		}
		start := (page - 1) * perPage
		end := start + perPage
		if start > len(ids) {
			start = len(ids)
		}
		if end > len(ids) {
			end = len(ids)
		}
		data := make([]apiEntry, 0, end-start)
		for _, id := range ids[start:end] {
			data = append(data, *f.entries[id])
		}
		children = &apiListing{Data: data, CurrentPage: page, LastPage: last, PerPage: perPage, Total: len(ids)}
	}
	writeJSON(w, 200, apiShareLinkResponse{Status: "success", Link: link, FolderChildren: children})
}

func (f *fakeProvider) requestsFor(method, path string) []recordedRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []recordedRequest
	for _, req := range f.requests {
		if req.method == method && req.path == path {
			out = append(out, req)
		}
	}
	return out
}
