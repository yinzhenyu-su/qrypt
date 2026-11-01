package koofr

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

// fakeProvider is an in-memory Koofr API served over httptest. It mirrors the
// probe-observed shapes: 409 AlreadyExists on mkdir, autorename upload
// defaults, 416 ranges, mount-scoped link endpoints, plaintext password echo
// on password/set, non-JSON error bodies, and fault injection.
type fakeProvider struct {
	t      *testing.T
	server *httptest.Server

	mu       sync.Mutex
	nodes    map[string]*fakeNode
	extNodes map[string]map[string]*fakeNode // per external mount id
	links    map[string]*fakeLink
	mounts   []apiMount
	// notSynced marks external mounts whose every file operation fails with
	// 500 FilesNotSynced (probe-observed out-of-sync backend).
	notSynced map[string]bool

	authEmail    string
	authPassword string
	rejectAuth   bool

	requests []recordedRequest
	faults   []injectedFault
	reqSeq   int
	linkSeq  int

	ignoreRange bool
	delay       time.Duration

	lastPutQuery url.Values
}

type fakeNode struct {
	name      string
	isDir     bool
	googleDoc bool
	data      []byte
	modMs     int64
}

type fakeLink struct {
	apiLink
	password string
}

type recordedRequest struct {
	Method string
	Path   string
	Query  url.Values
}

type injectedFault struct {
	match     func(r *http.Request) bool
	respond   func(w http.ResponseWriter, r *http.Request)
	remaining int
}

func newFakeProvider(t *testing.T) *fakeProvider {
	t.Helper()
	f := &fakeProvider{
		t:         t,
		nodes:     map[string]*fakeNode{"/": {name: "", isDir: true}},
		extNodes:  map[string]map[string]*fakeNode{},
		notSynced: map[string]bool{},
		links:     map[string]*fakeLink{},
		mounts: []apiMount{{
			ID:         "primary-mount",
			Name:       "Koofr",
			IsPrimary:  true,
			Online:     true,
			CanWrite:   true,
			CanUpload:  true,
			SpaceTotal: 1058816,
			SpaceUsed:  6302,
		}},
		authEmail:    "user@example.com",
		authPassword: "app-pass",
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", f.route)
	f.server = httptest.NewServer(mux)
	t.Cleanup(f.server.Close)
	return f
}

// newDriver wires a Driver to the fake and initializes it in locked mode
// (mount_id pinned to the primary mount).
func (f *fakeProvider) newDriver(t *testing.T, opts Options) *Driver {
	t.Helper()
	opts.Email = orDefault(opts.Email, f.authEmail)
	opts.Password = orDefault(opts.Password, f.authPassword)
	opts.APIBaseURL = f.server.URL
	if opts.MountID == "" {
		opts.MountID = "primary-mount"
	}
	return f.init(t, opts)
}

// newVirtualDriver wires a Driver with mount_id omitted, so Init serves the
// aggregated virtual root (primary storage plus online external mounts).
func (f *fakeProvider) newVirtualDriver(t *testing.T, opts Options) *Driver {
	t.Helper()
	opts.Email = orDefault(opts.Email, f.authEmail)
	opts.Password = orDefault(opts.Password, f.authPassword)
	opts.APIBaseURL = f.server.URL
	opts.MountID = ""
	return f.init(t, opts)
}

func (f *fakeProvider) init(t *testing.T, opts Options) *Driver {
	t.Helper()
	d := New(opts)
	d.cl.wait = func(ctx context.Context, _ int, _ time.Duration) error { return ctx.Err() }
	if err := d.Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	return d
}

// addExternalMount registers a non-primary mount with its own empty tree.
func (f *fakeProvider) addExternalMount(mount apiMount) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if mount.IsPrimary {
		f.t.Fatal("addExternalMount expects a non-primary mount")
	}
	f.mounts = append(f.mounts, mount)
	f.extNodes[mount.ID] = map[string]*fakeNode{"/": {name: "", isDir: true}}
}

// addStandardExternalMounts installs the virtual-root fixture: a writable
// external mount ("wgdrive"), an offline mount, and a read-only mount, each
// with a small tree.
func (f *fakeProvider) addStandardExternalMounts() {
	f.addExternalMount(apiMount{ID: "ext-drive", Name: "wgdrive", Online: true, CanWrite: true, CanUpload: true})
	f.putFileOn("ext-drive", "/existing.bin", []byte("ext-data"), 1700000000123)
	f.mkdirAllOn("ext-drive", "/nested")
	f.putFileOn("ext-drive", "/nested/leaf.bin", []byte("leaf"), 1700000000456)
	f.addExternalMount(apiMount{ID: "ext-offline", Name: "offline-mnt", Online: false, CanWrite: true, CanUpload: true})
	f.addExternalMount(apiMount{ID: "ext-ro", Name: "romount", Online: true, CanWrite: false, CanUpload: false})
	f.putFileOn("ext-ro", "/doc.bin", []byte("read-only"), 1700000000789)
}

func orDefault(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

// --- fixture helpers -----------------------------------------------------

// treeLocked returns the node tree of one provider mount (nil when the
// mount carries no tree). f.mu must be held.
func (f *fakeProvider) treeLocked(mountID string) map[string]*fakeNode {
	if mountID == "primary-mount" {
		return f.nodes
	}
	return f.extNodes[mountID]
}

func (f *fakeProvider) mkdirAll(path string) {
	f.mkdirAllOn("primary-mount", path)
}

func (f *fakeProvider) mkdirAllOn(mountID, path string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.mkdirInLocked(f.treeLocked(mountID), "/"+strings.Trim(path, "/"))
}

func (f *fakeProvider) mkdirInLocked(tree map[string]*fakeNode, path string) {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	current := ""
	for _, part := range parts {
		current = current + "/" + part
		if node, ok := tree[current]; ok {
			if !node.isDir {
				return
			}
			continue
		}
		tree[current] = &fakeNode{name: part, isDir: true, modMs: time.Now().UnixMilli()}
	}
}

func (f *fakeProvider) putFile(path string, data []byte, modMs int64) {
	f.putFileOn("primary-mount", path, data, modMs)
}

func (f *fakeProvider) putFileOn(mountID, path string, data []byte, modMs int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	path = "/" + strings.Trim(path, "/")
	tree := f.treeLocked(mountID)
	f.mkdirInLocked(tree, parentPath(path))
	tree[path] = &fakeNode{name: baseName(path), data: data, modMs: modMs}
}

// putGoogleDoc installs a placeholder entry mimicking Google Docs/shortcut
// files on external mounts: size=-1, a google-apps content type, and no hash.
func (f *fakeProvider) putGoogleDoc(path string, modMs int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	path = "/" + strings.Trim(path, "/")
	f.mkdirInLocked(f.nodes, parentPath(path))
	f.nodes[path] = &fakeNode{name: baseName(path), data: []byte("ignored"), modMs: modMs, googleDoc: true}
}

func (n *fakeNode) apiFile() apiFile {
	file := apiFile{Name: n.name, Type: "file", Size: int64(len(n.data)), Modified: n.modMs, ContentType: "application/octet-stream"}
	if n.googleDoc {
		file.Size = -1
		file.ContentType = "application/vnd.google-apps.document"
		return file
	}
	if n.isDir {
		file.Type = "dir"
		file.Size = 0
		file.ContentType = ""
	} else {
		sum := md5.Sum(n.data)
		file.Hash = hex.EncodeToString(sum[:])
	}
	return file
}

// --- fault injection ------------------------------------------------------

func (f *fakeProvider) addFault(match func(r *http.Request) bool, count int, respond func(w http.ResponseWriter, r *http.Request)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.faults = append(f.faults, injectedFault{match: match, respond: respond, remaining: count})
}

// faultStatus answers status with body for the next count requests whose path
// contains fragment.
func (f *fakeProvider) faultStatus(method, fragment string, count, status int, body string) {
	f.addFault(func(r *http.Request) bool {
		return r.Method == method && strings.Contains(r.URL.Path, fragment)
	}, count, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	})
}

func (f *fakeProvider) requestsMatching(method, fragment string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	count := 0
	for _, req := range f.requests {
		if req.Method == method && strings.Contains(req.Path, fragment) {
			count++
		}
	}
	return count
}

func (f *fakeProvider) lastPutParams() url.Values {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastPutQuery
}

// --- routing --------------------------------------------------------------

func (f *fakeProvider) route(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	if f.delay > 0 {
		delay := f.delay
		f.mu.Unlock()
		time.Sleep(delay)
		f.mu.Lock()
	}
	f.requests = append(f.requests, recordedRequest{Method: r.Method, Path: r.URL.Path, Query: r.URL.Query()})
	for i := range f.faults {
		fault := &f.faults[i]
		if fault.remaining > 0 && fault.match(r) {
			fault.remaining--
			respond := fault.respond
			f.mu.Unlock()
			respond(w, r)
			return
		}
	}
	f.mu.Unlock()

	if !strings.HasPrefix(r.URL.Path, "/content/links/") && !f.authorized(r) {
		f.writeError(w, r, http.StatusUnauthorized, "", "bad auth")
		return
	}

	path := r.URL.Path
	query := r.URL.Query()
	switch {
	case path == "/api/v2.1/user":
		f.handleUser(w)
	case path == "/api/v2.1/mounts":
		f.handleMounts(w)
	case path == "/api/v2.1/links":
		f.handleAllLinks(w)
	case strings.HasPrefix(path, "/api/v2.1/mounts/"):
		rest := strings.TrimPrefix(path, "/api/v2.1/mounts/")
		parts := strings.SplitN(rest, "/", 2)
		mountID := parts[0]
		sub := ""
		if len(parts) == 2 {
			sub = "/" + parts[1]
		}
		if mountID != "primary-mount" && !f.knownMount(mountID) {
			f.writeError(w, r, http.StatusNotFound, "NotFound", "mount not found")
			return
		}
		if sub != "" && f.mountNotSynced(mountID) {
			// Probe-observed: an out-of-sync external mount fails every
			// file operation with 500 FilesNotSynced.
			f.writeError(w, r, http.StatusInternalServerError, "FilesNotSynced", "Files not synced")
			return
		}
		switch {
		case sub == "":
			f.handleMount(w, mountID)
		case strings.HasPrefix(sub, "/files/list"):
			f.handleList(w, r, mountID, query)
		case strings.HasPrefix(sub, "/files/info"):
			f.handleInfo(w, r, mountID, query)
		case strings.HasPrefix(sub, "/files/folder"):
			f.handleFolder(w, r, mountID, query)
		case strings.HasPrefix(sub, "/files/rename"):
			f.handleRename(w, r, mountID, query)
		case strings.HasPrefix(sub, "/files/move"):
			f.handleMove(w, r, mountID, query)
		case strings.HasPrefix(sub, "/files/copy"):
			f.handleCopy(w, r, mountID, query)
		case strings.HasPrefix(sub, "/files/remove"):
			f.handleRemove(w, r, mountID, query)
		case strings.HasPrefix(sub, "/links"):
			f.handleLinks(w, r, mountID, strings.TrimPrefix(sub, "/links"), query)
		default:
			f.writeError(w, r, http.StatusNotFound, "NotFound", "Page not found")
		}
	case strings.HasPrefix(path, "/content/api/"):
		rest := strings.TrimPrefix(path, "/content/api/")
		rest = strings.TrimPrefix(rest, "v2.1/")
		rest = strings.TrimPrefix(rest, "v2/")
		filesAt := strings.Index(rest, "/files/")
		if !strings.HasPrefix(rest, "mounts/") || filesAt < 0 {
			f.writeError(w, r, http.StatusNotFound, "NotFound", "Page not found")
			return
		}
		mountID := rest[len("mounts/"):filesAt]
		op := rest[filesAt+len("/files/"):]
		if mountID != "primary-mount" && !f.knownMount(mountID) {
			f.writeError(w, r, http.StatusNotFound, "NotFound", "mount not found")
			return
		}
		if f.mountNotSynced(mountID) {
			f.writeError(w, r, http.StatusInternalServerError, "FilesNotSynced", "Files not synced")
			return
		}
		switch {
		case strings.HasPrefix(op, "put"):
			f.handlePut(w, r, mountID, query)
		case strings.HasPrefix(op, "get"):
			f.handleGet(w, r, mountID, query)
		default:
			f.writeError(w, r, http.StatusNotFound, "NotFound", "Page not found")
		}
	case strings.HasPrefix(path, "/content/links/"):
		f.handleAnonymousGet(w, r, query)
	default:
		f.writeError(w, r, http.StatusNotFound, "NotFound", "Page not found")
	}
}

func (f *fakeProvider) authorized(r *http.Request) bool {
	if f.rejectAuth {
		return false
	}
	email, password, ok := r.BasicAuth()
	return ok && email == f.authEmail && password == f.authPassword
}

func (f *fakeProvider) knownMount(id string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, mount := range f.mounts {
		if mount.ID == id {
			return true
		}
	}
	return false
}

func (f *fakeProvider) mountNotSynced(id string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.notSynced[id]
}

func (f *fakeProvider) writeError(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	f.mu.Lock()
	f.reqSeq++
	requestID := fmt.Sprintf("req-%d", f.reqSeq)
	f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if code == "" {
		fmt.Fprintf(w, `{"error":{"code":"Other","message":%q},"requestId":%q}`, message, requestID)
		return
	}
	fmt.Fprintf(w, `{"error":{"code":%q,"message":%q},"requestId":%q}`, code, message, requestID)
}

func (f *fakeProvider) writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func (f *fakeProvider) handleUser(w http.ResponseWriter) {
	f.writeJSON(w, http.StatusOK, apiUser{ID: "user-1", Email: f.authEmail})
}

func (f *fakeProvider) handleMounts(w http.ResponseWriter) {
	f.mu.Lock()
	mounts := append([]apiMount(nil), f.mounts...)
	f.mu.Unlock()
	f.writeJSON(w, http.StatusOK, mounts)
}

func (f *fakeProvider) handleMount(w http.ResponseWriter, mountID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, mount := range f.mounts {
		if mount.ID == mountID {
			f.writeJSONLocked(w, http.StatusOK, mount)
			return
		}
	}
	f.writeErrorLocked(w, http.StatusNotFound, "NotFound", "mount not found")
}

// writeJSONLocked/writeErrorLocked assume f.mu is held; httptest writers are
// safe for this single-request usage.
func (f *fakeProvider) writeJSONLocked(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func (f *fakeProvider) writeErrorLocked(w http.ResponseWriter, status int, code, message string) {
	f.reqSeq++
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	fmt.Fprintf(w, `{"error":{"code":%q,"message":%q},"requestId":"req-%d"}`, code, message, f.reqSeq)
}

func (f *fakeProvider) handleList(w http.ResponseWriter, r *http.Request, mountID string, query url.Values) {
	path := query.Get("path")
	if path == "" || path[0] != '/' {
		f.writeError(w, r, http.StatusBadRequest, "InvalidPath", "path must be absolute")
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	tree := f.treeLocked(mountID)
	if tree == nil {
		f.writeErrorLocked(w, http.StatusNotFound, "NotFound", "mount not found")
		return
	}
	node, _ := f.findInLocked(tree, path)
	if node == nil {
		f.writeErrorLocked(w, http.StatusNotFound, "NotFound", "not found")
		return
	}
	if !node.isDir {
		f.writeErrorLocked(w, http.StatusBadRequest, "InvalidPath", "not a directory")
		return
	}
	files := []apiFile{}
	for _, child := range f.childrenInLocked(tree, path) {
		files = append(files, tree[child].apiFile())
	}
	f.writeJSONLocked(w, http.StatusOK, apiFileList{Files: files})
}

func (f *fakeProvider) handleInfo(w http.ResponseWriter, r *http.Request, mountID string, query url.Values) {
	path := query.Get("path")
	f.mu.Lock()
	defer f.mu.Unlock()
	tree := f.treeLocked(mountID)
	if tree == nil {
		f.writeErrorLocked(w, http.StatusNotFound, "NotFound", "mount not found")
		return
	}
	node, _ := f.findInLocked(tree, path)
	if node == nil {
		f.writeErrorLocked(w, http.StatusNotFound, "NotFound", "not found")
		return
	}
	f.writeJSONLocked(w, http.StatusOK, node.apiFile())
}

func (f *fakeProvider) handleFolder(w http.ResponseWriter, r *http.Request, mountID string, query url.Values) {
	parent := query.Get("path")
	var body struct {
		Name string `json:"name"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	// The provider answers invalid folder names with a plain-text 500.
	if body.Name == "" || strings.ContainsAny(body.Name, "/") {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("Name is required"))
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	tree := f.treeLocked(mountID)
	if tree == nil {
		f.writeErrorLocked(w, http.StatusNotFound, "NotFound", "mount not found")
		return
	}
	parentNode, _ := f.findInLocked(tree, parent)
	if parentNode == nil || !parentNode.isDir {
		f.writeErrorLocked(w, http.StatusNotFound, "NotFound", "parent not found")
		return
	}
	target := joinPath(parent, body.Name)
	if existing, _ := f.findInLocked(tree, target); existing != nil {
		f.writeErrorLocked(w, http.StatusConflict, "AlreadyExists", "File already exists")
		return
	}
	tree[target] = &fakeNode{name: body.Name, isDir: true, modMs: time.Now().UnixMilli()}
	w.WriteHeader(http.StatusOK)
}

func (f *fakeProvider) handleRename(w http.ResponseWriter, r *http.Request, mountID string, query url.Values) {
	path := query.Get("path")
	var body struct {
		Name string `json:"name"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	f.mu.Lock()
	defer f.mu.Unlock()
	tree := f.treeLocked(mountID)
	if tree == nil {
		f.writeErrorLocked(w, http.StatusNotFound, "NotFound", "mount not found")
		return
	}
	node, canonical := f.findInLocked(tree, path)
	if node == nil {
		f.writeErrorLocked(w, http.StatusNotFound, "NotFound", "not found")
		return
	}
	parent := parentPath(canonical)
	target := joinPath(parent, body.Name)
	if sibling, _ := f.findInLocked(tree, target); sibling != nil && !strings.EqualFold(target, canonical) {
		f.writeErrorLocked(w, http.StatusConflict, "AlreadyExists", "File already exists")
		return
	}
	delete(tree, canonical)
	node.name = body.Name
	if mountID != "primary-mount" {
		// Probe-observed: renames on external mounts reset the mtime.
		node.modMs = time.Now().UnixMilli()
	}
	tree[target] = node
	w.WriteHeader(http.StatusOK)
}

func (f *fakeProvider) handleMove(w http.ResponseWriter, r *http.Request, mountID string, query url.Values) {
	f.handleMoveCopy(w, r, mountID, query, false)
}

func (f *fakeProvider) handleCopy(w http.ResponseWriter, r *http.Request, mountID string, query url.Values) {
	f.handleMoveCopy(w, r, mountID, query, true)
}

func (f *fakeProvider) handleMoveCopy(w http.ResponseWriter, r *http.Request, mountID string, query url.Values, isCopy bool) {
	path := query.Get("path")
	var body struct {
		ToMountID string `json:"toMountId"`
		ToPath    string `json:"toPath"`
		Modified  int64  `json:"modified"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if body.ToMountID == "" {
		body.ToMountID = mountID
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	srcTree := f.treeLocked(mountID)
	dstTree := f.treeLocked(body.ToMountID)
	if srcTree == nil || dstTree == nil {
		f.writeErrorLocked(w, http.StatusNotFound, "NotFound", "mount not found")
		return
	}
	node, canonical := f.findInLocked(srcTree, path)
	if node == nil {
		f.writeErrorLocked(w, http.StatusNotFound, "NotFound", "not found")
		return
	}
	dstParent, _ := f.findInLocked(dstTree, parentPath(body.ToPath))
	if dstParent == nil || !dstParent.isDir {
		f.writeErrorLocked(w, http.StatusNotFound, "NotFound", "destination not found")
		return
	}
	if target, _ := f.findInLocked(dstTree, body.ToPath); target != nil {
		f.writeErrorLocked(w, http.StatusConflict, "AlreadyExists", "File already exists")
		return
	}
	mod := body.Modified
	if mod == 0 {
		mod = node.modMs
	}
	if isCopy {
		cloned := *node
		cloned.name = baseName(body.ToPath)
		cloned.modMs = mod
		dstTree[body.ToPath] = &cloned
	} else {
		delete(srcTree, canonical)
		node.name = baseName(body.ToPath)
		node.modMs = mod
		dstTree[body.ToPath] = node
	}
	f.writeJSONLocked(w, http.StatusOK, map[string]any{"name": baseName(body.ToPath)})
}

func (f *fakeProvider) handleRemove(w http.ResponseWriter, r *http.Request, mountID string, query url.Values) {
	path := query.Get("path")
	f.mu.Lock()
	defer f.mu.Unlock()
	tree := f.treeLocked(mountID)
	if tree == nil {
		f.writeErrorLocked(w, http.StatusNotFound, "NotFound", "mount not found")
		return
	}
	node, canonical := f.findInLocked(tree, path)
	if node == nil {
		f.writeErrorLocked(w, http.StatusNotFound, "NotFound", "not found")
		return
	}
	if raw := query.Get("removeIfSize"); raw != "" && !node.isDir {
		if want, err := strconv.ParseInt(raw, 10, 64); err != nil || want != int64(len(node.data)) {
			f.writeErrorLocked(w, http.StatusConflict, "Conflict", "size mismatch")
			return
		}
	}
	if raw := query.Get("removeIfModified"); raw != "" {
		if want, err := strconv.ParseInt(raw, 10, 64); err != nil || want != node.modMs {
			f.writeErrorLocked(w, http.StatusConflict, "Conflict", "modified mismatch")
			return
		}
	}
	if raw := query.Get("removeIfHash"); raw != "" && !node.isDir {
		sum := md5.Sum(node.data)
		if raw != hex.EncodeToString(sum[:]) {
			f.writeErrorLocked(w, http.StatusConflict, "Conflict", "hash mismatch")
			return
		}
	}
	// Cascade: removing a directory removes every descendant (they land in
	// the provider trash, which the fake does not model).
	delete(tree, canonical)
	for candidate := range tree {
		if isWithin(candidate, canonical) {
			delete(tree, candidate)
		}
	}
	f.writeJSONLocked(w, http.StatusOK, map[string]any{"version": "v1"})
}

// --- content endpoints ------------------------------------------------------

func (f *fakeProvider) handlePut(w http.ResponseWriter, r *http.Request, mountID string, query url.Values) {
	f.mu.Lock()
	f.lastPutQuery = cloneQuery(query)
	f.mu.Unlock()
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		f.writeError(w, r, http.StatusBadRequest, "InvalidPath", "bad multipart")
		return
	}
	file, _, err := r.FormFile("file")
	if err != nil {
		f.writeError(w, r, http.StatusBadRequest, "InvalidPath", "missing file part")
		return
	}
	defer file.Close()
	data, err := io.ReadAll(file)
	if err != nil {
		f.writeError(w, r, http.StatusBadRequest, "InvalidPath", "unreadable file part")
		return
	}
	name := query.Get("filename")
	if !utf8.ValidString(name) {
		// The provider ignores an invalid-UTF-8 query filename and falls
		// back to the multipart part filename.
		name = "part-fallback.bin"
	}
	parent := query.Get("path")
	modMs := time.Now().UnixMilli()
	if raw := query.Get("modified"); raw != "" {
		if parsed, err := strconv.ParseInt(raw, 10, 64); err == nil {
			modMs = parsed
		}
	}
	overwrite := query.Get("overwrite") == "true"
	autorename := query.Get("autorename") == "true"
	f.mu.Lock()
	defer f.mu.Unlock()
	tree := f.treeLocked(mountID)
	if tree == nil {
		f.writeErrorLocked(w, http.StatusNotFound, "NotFound", "mount not found")
		return
	}
	parentNode, _ := f.findInLocked(tree, parent)
	if parentNode == nil || !parentNode.isDir {
		f.writeErrorLocked(w, http.StatusNotFound, "NotFound", "parent not found")
		return
	}
	target := joinPath(parent, name)
	if existing, canonical := f.findInLocked(tree, target); existing != nil {
		if existing.isDir {
			f.writeErrorLocked(w, http.StatusConflict, "AlreadyExists", "File already exists")
			return
		}
		if !overwrite && !autorename {
			// Provider default: silent auto-rename (probe-observed).
			target = f.autorenameLocked(tree, parent, name)
			tree[target] = &fakeNode{name: baseName(target), data: data, modMs: modMs}
			f.writeJSONLocked(w, http.StatusOK, tree[target].apiFile())
			return
		}
		if !overwrite && autorename {
			target = f.autorenameLocked(tree, parent, name)
			tree[target] = &fakeNode{name: baseName(target), data: data, modMs: modMs}
			f.writeJSONLocked(w, http.StatusOK, tree[target].apiFile())
			return
		}
		// Overwrite keeps the existing canonical name (probe: uploading
		// PROBE2.BIN over probe2.bin left the lowercase name in listings).
		delete(tree, canonical)
		tree[canonical] = &fakeNode{name: baseName(canonical), data: data, modMs: modMs}
		f.writeJSONLocked(w, http.StatusOK, tree[canonical].apiFile())
		return
	}
	tree[target] = &fakeNode{name: name, data: data, modMs: modMs}
	f.writeJSONLocked(w, http.StatusOK, tree[target].apiFile())
}

func (f *fakeProvider) autorenameLocked(tree map[string]*fakeNode, parent, name string) string {
	ext := ""
	base := name
	if i := strings.LastIndexByte(name, '.'); i > 0 {
		ext = name[i:]
		base = name[:i]
	}
	for i := 1; ; i++ {
		candidate := fmt.Sprintf("%s (%d)%s", base, i, ext)
		if _, canonical := f.findInLocked(tree, joinPath(parent, candidate)); canonical == "" {
			return joinPath(parent, candidate)
		}
	}
}

func (f *fakeProvider) handleGet(w http.ResponseWriter, r *http.Request, mountID string, query url.Values) {
	path := query.Get("path")
	f.mu.Lock()
	ignore := f.ignoreRange
	tree := f.treeLocked(mountID)
	node, _ := f.findInLocked(tree, path)
	var data []byte
	var isDir bool
	if node != nil {
		data = node.data
		isDir = node.isDir
	}
	f.mu.Unlock()
	if node == nil {
		f.writeError(w, r, http.StatusNotFound, "NotFound", "not found")
		return
	}
	if isDir {
		f.writeError(w, r, http.StatusBadRequest, "InvalidPath", "cannot download a directory")
		return
	}
	rangeHeader := r.Header.Get("Range")
	if ignore || rangeHeader == "" {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(data)
		return
	}
	start, end, ok := parseRange(rangeHeader, int64(len(data)))
	if !ok || start >= int64(len(data)) {
		f.writeError(w, r, http.StatusRequestedRangeNotSatisfiable, "Other", "Invalid range")
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(data)))
	w.WriteHeader(http.StatusPartialContent)
	_, _ = w.Write(data[start : end+1])
}

func parseRange(header string, size int64) (int64, int64, bool) {
	if !strings.HasPrefix(header, "bytes=") {
		return 0, 0, false
	}
	spec := strings.TrimPrefix(header, "bytes=")
	parts := strings.SplitN(spec, "-", 2)
	if len(parts) != 2 {
		return 0, 0, false
	}
	start, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || start < 0 {
		return 0, 0, false
	}
	end := size - 1
	if parts[1] != "" {
		parsed, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil || parsed < start {
			return 0, 0, false
		}
		end = parsed
	}
	if end >= size {
		end = size - 1
	}
	return start, end, true
}

// --- links ---------------------------------------------------------------

func (f *fakeProvider) handleAllLinks(w http.ResponseWriter) {
	f.mu.Lock()
	defer f.mu.Unlock()
	// Live probing showed link listings return {"links":[...]} (the
	// account-level endpoint also carries a "mounts" map).
	out := []apiLink{}
	for _, link := range f.links {
		out = append(out, link.apiLink)
	}
	f.writeJSONLocked(w, http.StatusOK, map[string]any{"links": out})
}

func (f *fakeProvider) handleLinks(w http.ResponseWriter, r *http.Request, mountID, sub string, query url.Values) {
	// sub is "" | "/{id}" | "/{id}/password/set" | "/{id}/validity" | "/{id}/downloadable"
	if sub == "" {
		switch r.Method {
		case http.MethodGet:
			f.handleAllLinks(w)
		case http.MethodPost:
			f.createLink(w, r, mountID)
		default:
			f.writeError(w, r, http.StatusMethodNotAllowed, "", "method not allowed")
		}
		return
	}
	parts := strings.Split(strings.TrimPrefix(sub, "/"), "/")
	linkID := parts[0]
	operation := ""
	if len(parts) > 1 {
		operation = strings.Join(parts[1:], "/")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	link, ok := f.links[linkID]
	if !ok {
		f.writeErrorLocked(w, http.StatusNotFound, "NotFound", "link not found")
		return
	}
	switch {
	case operation == "" && r.Method == http.MethodGet:
		f.writeJSONLocked(w, http.StatusOK, link.apiLink)
	case operation == "" && r.Method == http.MethodDelete:
		delete(f.links, linkID)
		w.WriteHeader(http.StatusNoContent)
	case operation == "password/set" && r.Method == http.MethodPut:
		var body struct {
			Password string `json:"password"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		link.HasPassword = true
		link.PasswordRequired = true
		link.Password = body.Password
		link.password = body.Password
		// Probe-observed: the response echoes the plaintext password.
		f.writeJSONLocked(w, http.StatusOK, link.apiLink)
	case operation == "validity" && r.Method == http.MethodPut:
		var body struct {
			ValidFrom int64 `json:"validFrom"`
			ValidTo   int64 `json:"validTo"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		link.ValidFrom = body.ValidFrom
		link.ValidTo = body.ValidTo
		f.writeJSONLocked(w, http.StatusOK, link.apiLink)
	case operation == "downloadable" && r.Method == http.MethodPut:
		var body struct {
			Downloadable bool `json:"downloadable"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		link.Downloadable = body.Downloadable
		f.writeJSONLocked(w, http.StatusOK, link.apiLink)
	default:
		f.writeErrorLocked(w, http.StatusNotFound, "NotFound", "Page not found")
	}
}

func (f *fakeProvider) createLink(w http.ResponseWriter, r *http.Request, mountID string) {
	var body struct {
		Path string `json:"path"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	f.mu.Lock()
	defer f.mu.Unlock()
	tree := f.treeLocked(mountID)
	if tree == nil {
		f.writeErrorLocked(w, http.StatusNotFound, "NotFound", "mount not found")
		return
	}
	node, canonical := f.findInLocked(tree, body.Path)
	if node == nil {
		f.writeErrorLocked(w, http.StatusBadRequest, "InvalidPath", "path not found")
		return
	}
	f.linkSeq++
	id := fmt.Sprintf("link-%d", f.linkSeq)
	link := &fakeLink{apiLink: apiLink{
		ID:           id,
		Name:         node.name,
		MountID:      mountID,
		Path:         canonical,
		URL:          f.server.URL + "/links/" + id,
		ShortURL:     "https://k00.fr/" + id,
		Hash:         id,
		Downloadable: true,
		Created:      time.Now().UnixMilli(),
	}}
	f.links[id] = link
	f.writeJSONLocked(w, http.StatusCreated, link.apiLink)
}

func (f *fakeProvider) handleAnonymousGet(w http.ResponseWriter, r *http.Request, query url.Values) {
	parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/content/links/"), "/", 2)
	if len(parts) != 2 || parts[1] != "files/get" {
		f.writeError(w, r, http.StatusNotFound, "NotFound", "Page not found")
		return
	}
	f.mu.Lock()
	link, ok := f.links[parts[0]]
	var target *fakeNode
	if ok {
		full := link.Path
		if rel := query.Get("path"); rel != "" && rel != "/" {
			// The real provider expects a canonical /a/b path; a "//a"
			// double slash from sloppy client joining misses the file.
			if !strings.HasPrefix(rel, "/") || strings.Contains(rel, "//") {
				f.mu.Unlock()
				f.writeError(w, r, http.StatusBadRequest, "InvalidPath", "path must be a canonical absolute path")
				return
			}
			full = strings.TrimSuffix(link.Path, "/") + rel
		}
		target, _ = f.findInLocked(f.treeLocked(link.MountID), full)
	}
	password := query.Get("password")
	f.mu.Unlock()
	if !ok {
		f.writeError(w, r, http.StatusNotFound, "NotFound", "link not found")
		return
	}
	if !link.Downloadable {
		f.writeError(w, r, http.StatusForbidden, "Other", "downloads disabled")
		return
	}
	if link.HasPassword && password != link.password {
		f.writeError(w, r, http.StatusUnauthorized, "Other", "password required")
		return
	}
	if target == nil {
		f.writeError(w, r, http.StatusNotFound, "NotFound", "not found")
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	if target.isDir {
		// Directory paths return a zip archive upstream.
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("PK-zip-archive"))
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(target.data)
}

// --- small locked helpers --------------------------------------------------

// findInLocked resolves a case-insensitive path inside one mount tree.
// f.mu must be held.
func (f *fakeProvider) findInLocked(tree map[string]*fakeNode, path string) (*fakeNode, string) {
	if tree == nil {
		return nil, ""
	}
	path = "/" + strings.Trim(path, "/")
	if path == "/" {
		return tree["/"], "/"
	}
	parts := strings.Split(strings.Trim(path, "/"), "/")
	current := "/"
	for _, part := range parts {
		var match string
		for childPath, node := range tree {
			if childPath == "/" {
				continue
			}
			if parentPath(childPath) == current && strings.EqualFold(node.name, part) {
				match = childPath
				break
			}
		}
		if match == "" {
			return nil, ""
		}
		current = match
	}
	return tree[current], current
}

func (f *fakeProvider) childrenInLocked(tree map[string]*fakeNode, dir string) []string {
	var out []string
	for path := range tree {
		if path != "/" && parentPath(path) == dir {
			out = append(out, path)
		}
	}
	sort.Strings(out)
	return out
}

func cloneQuery(query url.Values) url.Values {
	out := url.Values{}
	for key, values := range query {
		out[key] = append([]string(nil), values...)
	}
	return out
}
