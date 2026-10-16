package p123

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/drive/session"
)

type recordedRequest struct {
	method string
	path   string
	query  string
	body   string
}

type fakeAPI struct {
	t        *testing.T
	server   *httptest.Server
	mu       sync.Mutex
	handlers map[string]http.HandlerFunc
	requests []recordedRequest
}

func newFakeAPI(t *testing.T) *fakeAPI {
	t.Helper()
	f := &fakeAPI{t: t, handlers: map[string]http.HandlerFunc{}}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.requests = append(f.requests, recordedRequest{
			method: r.Method,
			path:   r.URL.Path,
			query:  r.URL.RawQuery,
			body:   string(data),
		})
		handler := f.handlers[r.URL.Path]
		f.mu.Unlock()
		if handler == nil {
			http.Error(w, "fake: no handler for "+r.URL.Path, http.StatusNotFound)
			return
		}
		if len(data) > 0 {
			r.Body = io.NopCloser(bytes.NewReader(data))
		}
		handler(w, r)
	}))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeAPI) on(path string, handler http.HandlerFunc) {
	f.handlers[path] = handler
}

func (f *fakeAPI) json(path, body string) {
	f.on(path, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	})
}

func (f *fakeAPI) driver(opts Options) *Driver {
	d := New(opts)
	d.cl.baseURL = f.server.URL + "/b/api"
	d.cl.loginURL = f.server.URL + "/api/user/sign_in"
	// Directory listings are throttled in production; tests do not need the
	// pacing and it would only add wall-clock time.
	d.cl.listLimiter = nil
	return d
}

func (f *fakeAPI) url(path string) string {
	return f.server.URL + path
}

func (f *fakeAPI) lastBodyFor(path string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.requests) - 1; i >= 0; i-- {
		if f.requests[i].path == path {
			return f.requests[i].body
		}
	}
	return ""
}

func (f *fakeAPI) sawCall(path string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, req := range f.requests {
		if req.path == path {
			return true
		}
	}
	return false
}

func (f *fakeAPI) sawQueryParam(path, param string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, req := range f.requests {
		if req.path != path {
			continue
		}
		if strings.Contains(req.query, param+"=") {
			return true
		}
	}
	return false
}

// installLogin wires a successful login response and the root user info call.
func installLogin(f *fakeAPI, token string) {
	f.json("/api/user/sign_in", fmt.Sprintf(`{"code":200,"data":{"token":%q,"expire":"2099-01-14T12:17:53+08:00"}}`, token))
	f.json("/b/api/user/info", `{"code":0,"data":{"UID":7,"SpaceUsed":100,"SpacePermanent":1000,"SpaceTemp":24}}`)
}

func TestFactoryRegistration(t *testing.T) {
	if _, err := drive.New("123", drive.Params{}); err == nil {
		t.Fatal("expected missing-credential error")
	}
	drv, err := drive.New("123", drive.Params{"username": "user", "password": "secret"})
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	if _, ok := drv.(*Driver); !ok {
		t.Fatalf("factory returned %T, want *Driver", drv)
	}

	schema := drive.ParamSchema("123")
	byName := map[string]drive.ParamDef{}
	for _, def := range schema {
		byName[def.Name] = def
	}
	for _, name := range []string{"username", "password"} {
		def, ok := byName[name]
		if !ok || !def.Required {
			t.Fatalf("param %q = %+v, want required", name, def)
		}
	}
	if !byName["password"].Secret {
		t.Fatal("password must be marked secret")
	}
	if byName["username"].Secret {
		t.Fatal("username must not be marked secret")
	}
}

func TestSignKey(t *testing.T) {
	now := time.Date(2026, 10, 16, 4, 30, 0, 0, time.UTC)
	got := signKey(now)
	const want = "2456478571"
	if got != want {
		t.Fatalf("signKey = %s, want %s", got, want)
	}
	// The workspace's independent reference: CRC32 over the digit-substituted
	// minute stamp.
	stamp := []byte("202610161230")
	mapped := make([]byte, len(stamp))
	for i, b := range stamp {
		mapped[i] = signTable[b-'0']
	}
	if uint64(crc32.ChecksumIEEE(mapped)) != 2456478571 {
		t.Fatal("reference computation drifted")
	}
}

func TestParseAPITime(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"2023-11-12T14:03:57+08:00", true},
		{"2026-10-16T12:18:36.354084634+08:00", true},
		{"2026-10-16 12:18:36", true},
		{"", false},
		{"garbage", false},
	}
	for _, tc := range cases {
		got := parseAPITime(tc.in)
		if got.IsZero() == tc.want {
			t.Fatalf("parseAPITime(%q) = %v, want non-zero=%v", tc.in, got, tc.want)
		}
	}
}

func TestCapabilities(t *testing.T) {
	d := New(Options{Username: "u", Password: "p"})
	got := drive.Capabilities(d)
	want := []drive.Capability{
		drive.CapabilityPathResolver,
		drive.CapabilityResumableUploader,
		drive.CapabilityServerSideCopy,
		drive.CapabilitySourceUploader,
		drive.CapabilitySpace,
		drive.CapabilityWriter,
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("capabilities = %v, want %v", got, want)
	}
	if violations := drive.CheckUnsupportedCapabilities(context.Background(), d); len(violations) != 0 {
		t.Fatalf("contract violations: %+v", violations)
	}
}

func TestInitResolvesRootPath(t *testing.T) {
	f := newFakeAPI(t)
	installLogin(f, "tok")
	f.json("/b/api/file/list/new", `{"code":0,"data":{"Next":"-1","Total":1,"InfoList":[
		{"FileId":42,"FileName":"qrypt","Type":1,"Size":0,"ParentFileId":0,"CreateAt":"2026-01-01T00:00:00+08:00","UpdateAt":"2026-01-01T00:00:00+08:00"}
	]}}`)

	d := f.driver(Options{Username: "u", Password: "p", RootPath: "/qrypt"})
	if err := d.Init(context.Background()); err != nil {
		t.Fatalf("init: %v", err)
	}
	if d.rootID != "42" {
		t.Fatalf("root id = %q, want 42", d.rootID)
	}
}

func TestListMapsEntriesAndPaginates(t *testing.T) {
	f := newFakeAPI(t)
	installLogin(f, "tok")
	pages := 0
	f.on("/b/api/file/list/new", func(w http.ResponseWriter, r *http.Request) {
		pages++
		w.Header().Set("Content-Type", "application/json")
		if pages == 1 {
			_, _ = io.WriteString(w, `{"code":0,"data":{"Next":"123","Total":2,"InfoList":[
				{"FileId":11,"FileName":"dir","Type":1,"Size":0,"ParentFileId":0,"CreateAt":"2026-01-01T00:00:00+08:00","UpdateAt":"2026-01-02T00:00:00+08:00","Etag":"","S3KeyFlag":"9-0"}
			]}}`)
			return
		}
		_, _ = io.WriteString(w, `{"code":0,"data":{"Next":"-1","Total":2,"InfoList":[
			{"FileId":12,"FileName":"a.txt","Type":0,"Size":5,"ParentFileId":0,"Etag":"ABCDEF","S3KeyFlag":"9-0","CreateAt":"2026-01-01T00:00:00+08:00","UpdateAt":"2026-01-03T00:00:00+08:00"}
		]}}`)
	})

	d := f.driver(Options{Username: "u", Password: "p"})
	entries, err := d.List(context.Background(), "0")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2", len(entries))
	}
	dir, file := entries[0], entries[1]
	if dir.ID != "11" || dir.Name != "dir" || !dir.IsDir || dir.ParentID != "0" {
		t.Fatalf("dir entry = %+v", dir)
	}
	if file.ID != "12" || file.Name != "a.txt" || file.IsDir || file.Size != 5 || file.ParentID != "0" {
		t.Fatalf("file entry = %+v", file)
	}
	if file.ModTime.IsZero() || !file.ModTime.Equal(parseAPITime("2026-01-03T00:00:00+08:00")) {
		t.Fatalf("file modtime = %v", file.ModTime)
	}
	meta, ok := metaFromEntry(file)
	if !ok {
		t.Fatal("file entry missing provider metadata")
	}
	if meta.FileID != 12 || meta.Etag != "ABCDEF" || meta.S3KeyFlag != "9-0" || meta.ParentID != 0 {
		t.Fatalf("file meta = %+v", meta)
	}
	if pages != 2 {
		t.Fatalf("expected 2 list pages, got %d", pages)
	}
	if !f.sawQueryParam("/b/api/file/list/new", "Page") {
		t.Fatal("list request missing Page param")
	}
}

func TestMkdirReturnsExistingDirectory(t *testing.T) {
	f := newFakeAPI(t)
	installLogin(f, "tok")
	f.json("/b/api/file/list/new", `{"code":0,"data":{"Next":"-1","Total":1,"InfoList":[
		{"FileId":77,"FileName":"docs","Type":1,"Size":0,"ParentFileId":5,"UpdateAt":"2026-02-02T00:00:00+08:00"}
	]}}`)
	f.json("/b/api/file/upload_request", `{"code":5060,"data":{},"message":"同名文件已存在"}`)

	d := f.driver(Options{Username: "u", Password: "p"})
	entry, err := d.Mkdir(context.Background(), "5", "docs")
	if err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if entry.ID != "77" || entry.Name != "docs" || !entry.IsDir {
		t.Fatalf("entry = %+v", entry)
	}
	if f.sawCall("/b/api/file/upload_request") {
		t.Fatal("an existing directory must be reused without a create call")
	}
}

func TestMkdirCreatesAndUsesInfo(t *testing.T) {
	f := newFakeAPI(t)
	installLogin(f, "tok")
	f.json("/b/api/file/list/new", `{"code":0,"data":{"Next":"-1","Total":0,"InfoList":[]}}`)
	f.json("/b/api/file/upload_request", `{"code":0,"data":{"FileId":0,"Info":{
		"FileId":88,"FileName":"new","Type":1,"Size":0,"ParentFileId":5,"UpdateAt":"2026-02-02T00:00:00+08:00"
	}}}`)

	d := f.driver(Options{Username: "u", Password: "p"})
	entry, err := d.Mkdir(context.Background(), "5", "new")
	if err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if entry.ID != "88" || !entry.IsDir || entry.ParentID != "5" {
		t.Fatalf("entry = %+v", entry)
	}
	if !strings.Contains(f.lastBodyFor("/b/api/file/upload_request"), `"type":1`) {
		t.Fatalf("mkdir body = %s", f.lastBodyFor("/b/api/file/upload_request"))
	}
}

// TestMkdirRejectsProviderRename covers a create response whose name differs
// from the requested one: committing it would leave the path unaddressable.
func TestMkdirRejectsProviderRename(t *testing.T) {
	f := newFakeAPI(t)
	installLogin(f, "tok")
	f.json("/b/api/file/list/new", `{"code":0,"data":{"Next":"-1","Total":0,"InfoList":[]}}`)
	f.json("/b/api/file/upload_request", `{"code":0,"data":{"FileId":0,"Info":{
		"FileId":89,"FileName":"new(1)","Type":1,"Size":0,"ParentFileId":5,"UpdateAt":"2026-02-02T00:00:00+08:00"
	}}}`)

	d := f.driver(Options{Username: "u", Password: "p"})
	if _, err := d.Mkdir(context.Background(), "5", "new"); err == nil {
		t.Fatal("expected a conflict error for a renamed directory")
	} else if drive.ErrorCategory(err) != drive.ErrorCategoryConflict {
		t.Fatalf("error category = %q (err = %v), want conflict", drive.ErrorCategory(err), err)
	}
}

func TestRenameMoveRemoveBodies(t *testing.T) {
	f := newFakeAPI(t)
	installLogin(f, "tok")
	f.json("/b/api/file/rename", `{"code":0,"data":{"Info":{"FileId":12,"FileName":"b.txt","Type":0,"Size":5,"ParentFileId":5,"UpdateAt":"2026-03-03T00:00:00+08:00","Etag":"E","S3KeyFlag":"9-0"}}}`)
	f.json("/b/api/file/mod_pid", `{"code":0,"data":{"Info":[{"FileId":12,"FileName":"b.txt","Type":0,"Size":5,"ParentFileId":9,"UpdateAt":"2026-03-03T00:00:00+08:00","Etag":"E","S3KeyFlag":"9-0"}]}}`)
	f.json("/b/api/file/trash", `{"code":0,"data":{"InfoList":[{"FileId":12}]}}`)

	d := f.driver(Options{Username: "u", Password: "p"})
	entry := drive.Entry{
		ID: "12", ParentID: "5", Name: "a.txt", Size: 5,
		Extra: fileMeta{FileID: 12, Name: "a.txt", Type: 0, Size: 5, Etag: "E", S3KeyFlag: "9-0", ParentID: 5},
	}

	renamed, err := d.Rename(context.Background(), entry, "b.txt")
	if err != nil {
		t.Fatalf("rename: %v", err)
	}
	if renamed.Name != "b.txt" || renamed.ID != "12" {
		t.Fatalf("renamed = %+v", renamed)
	}

	moved, err := d.Move(context.Background(), renamed, "9")
	if err != nil {
		t.Fatalf("move: %v", err)
	}
	if moved.ParentID != "9" {
		t.Fatalf("moved parent = %q", moved.ParentID)
	}

	if err := d.Remove(context.Background(), moved); err != nil {
		t.Fatalf("remove: %v", err)
	}
	trashBody := f.lastBodyFor("/b/api/file/trash")
	if !strings.Contains(trashBody, `"operation":true`) {
		t.Fatalf("trash body = %s", trashBody)
	}
	// ParentFileId must be a JSON number: 123pan silently ignores a string.
	if !strings.Contains(trashBody, `"ParentFileId":9`) {
		t.Fatalf("trash ParentFileId not numeric: %s", trashBody)
	}
}

func TestSpace(t *testing.T) {
	f := newFakeAPI(t)
	installLogin(f, "tok")
	d := f.driver(Options{Username: "u", Password: "p"})
	space, err := d.Space(context.Background())
	if err != nil {
		t.Fatalf("space: %v", err)
	}
	if space.Total != 1024 || space.Free != 924 {
		t.Fatalf("space = %+v", space)
	}
}

func TestDebugSnapshotHidesSecret(t *testing.T) {
	f := newFakeAPI(t)
	installLogin(f, "super-secret-token")
	d := f.driver(Options{Username: "u", Password: "super-secret-password"})
	if err := d.Init(context.Background()); err != nil {
		t.Fatalf("init: %v", err)
	}
	snap, err := d.DebugSnapshot(context.Background())
	if err != nil {
		t.Fatalf("debug: %v", err)
	}
	blob, _ := json.Marshal(snap)
	if strings.Contains(string(blob), "super-secret") {
		t.Fatalf("snapshot leaked a credential: %s", blob)
	}
	if snap.Driver != "123" {
		t.Fatalf("driver = %q", snap.Driver)
	}
}

func TestReloginOnExpiredToken(t *testing.T) {
	f := newFakeAPI(t)
	installLogin(f, "fresh")
	attempts := 0
	f.on("/b/api/user/info", func(w http.ResponseWriter, _ *http.Request) {
		attempts++
		w.Header().Set("Content-Type", "application/json")
		if attempts == 1 {
			_, _ = io.WriteString(w, `{"code":401,"message":"cookie token is empty"}`)
			return
		}
		_, _ = io.WriteString(w, `{"code":0,"data":{"SpaceUsed":0,"SpacePermanent":10}}`)
	})

	d := f.driver(Options{Username: "u", Password: "p"})
	d.cl.setToken("stale")
	if err := d.Init(context.Background()); err != nil {
		t.Fatalf("init: %v", err)
	}
	if !f.sawCall("/api/user/sign_in") {
		t.Fatal("expected a re-login call")
	}
	if d.cl.tokenValue() != "fresh" {
		t.Fatalf("token = %q, want fresh", d.cl.tokenValue())
	}
}

// installDownload wires the download_info -> CDN redirect -> content chain.
func installDownload(f *fakeAPI, payload []byte) {
	f.on("/b/api/file/download_info", func(w http.ResponseWriter, _ *http.Request) {
		final := f.url("/cdn/final")
		iroh := f.url("/cdn/iroh?params=" + base64.StdEncoding.EncodeToString([]byte(final)))
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"code":0,"data":{"DownloadUrl":%q}}`, iroh)
	})
	f.on("/cdn/iroh", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(210)
		_, _ = fmt.Fprintf(w, `{"code":0,"data":{"redirect_url":%q}}`, f.url("/cdn/final"))
	})
	f.on("/cdn/final", func(w http.ResponseWriter, r *http.Request) {
		serveRange(w, r, payload)
	})
}

func serveRange(w http.ResponseWriter, r *http.Request, payload []byte) {
	rangeHeader := r.Header.Get("Range")
	if rangeHeader == "" {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(payload)
		return
	}
	var start, end int64
	if _, err := fmt.Sscanf(rangeHeader, "bytes=%d-%d", &start, &end); err != nil {
		var openEnd int64
		if _, err := fmt.Sscanf(rangeHeader, "bytes=%d-", &openEnd); err != nil {
			http.Error(w, "bad range", http.StatusRequestedRangeNotSatisfiable)
			return
		}
		start, end = openEnd, int64(len(payload))-1
	}
	if start >= int64(len(payload)) {
		w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
		return
	}
	if end >= int64(len(payload)) {
		end = int64(len(payload)) - 1
	}
	w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(payload)))
	w.WriteHeader(http.StatusPartialContent)
	_, _ = w.Write(payload[start : end+1])
}

func TestReadOffsetsAndEOF(t *testing.T) {
	f := newFakeAPI(t)
	installLogin(f, "tok")
	payload := []byte("0123456789abcdefghij")
	installDownload(f, payload)
	f.json("/b/api/restful/goapi/v1/file/details", `{"code":0,"data":{"totalSize":20}}`)

	d := f.driver(Options{Username: "u", Password: "p"})
	entry := drive.Entry{
		ID: "12", ParentID: "5", Name: "a.txt", Size: int64(len(payload)),
		Extra: fileMeta{FileID: 12, Name: "a.txt", Type: 0, Size: int64(len(payload)), Etag: "E", S3KeyFlag: "9-0", ParentID: 5},
	}
	ctx := context.Background()

	got := readEntry(t, d, entry, 0, 5)
	if !bytes.Equal(got, payload[:5]) {
		t.Fatalf("read(0,5) = %q", got)
	}
	got = readEntry(t, d, entry, 10, 3)
	if !bytes.Equal(got, payload[10:13]) {
		t.Fatalf("read(10,3) = %q", got)
	}
	got = readEntry(t, d, entry, 15, 0)
	if !bytes.Equal(got, payload[15:]) {
		t.Fatalf("read(15,0) = %q", got)
	}
	got = readEntry(t, d, entry, int64(len(payload)), 4)
	if len(got) != 0 {
		t.Fatalf("read at EOF returned %d bytes", len(got))
	}
	// A window that runs past EOF should be clamped, not rejected.
	got = readEntry(t, d, entry, 18, 100)
	if !bytes.Equal(got, payload[18:]) {
		t.Fatalf("clamped read = %q", got)
	}
	if _, err := d.Read(ctx, entry, -1, 1); err == nil {
		t.Fatal("negative offset must fail")
	}
}

func readEntry(t *testing.T, d *Driver, entry drive.Entry, offset, size int64) []byte {
	t.Helper()
	rc, err := d.Read(context.Background(), entry, offset, size)
	if err != nil {
		t.Fatalf("read(%d,%d): %v", offset, size, err)
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return data
}

func TestReadNotFoundWhenTrashed(t *testing.T) {
	f := newFakeAPI(t)
	installLogin(f, "tok")
	installDownload(f, []byte("content"))
	f.json("/b/api/restful/goapi/v1/file/details", `{"code":2,"data":null,"message":"不存在的文件"}`)

	d := f.driver(Options{Username: "u", Password: "p"})
	entry := drive.Entry{
		ID: "12", ParentID: "5", Name: "a.txt", Size: 7,
		Extra: fileMeta{FileID: 12, Type: 0, Size: 7, Etag: "E", S3KeyFlag: "9-0", ParentID: 5},
	}
	_, err := d.Read(context.Background(), entry, 0, 1)
	if !errors.Is(err, drive.ErrNotFound) {
		t.Fatalf("read error = %v, want ErrNotFound", err)
	}
	if f.sawCall("/b/api/file/download_info") {
		t.Fatal("download_info must not be called for a missing file")
	}
}

func TestReadURLRefreshOnForbidden(t *testing.T) {
	f := newFakeAPI(t)
	installLogin(f, "tok")
	f.json("/b/api/restful/goapi/v1/file/details", `{"code":0,"data":{}}`)
	f.on("/b/api/file/download_info", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"code":0,"data":{"DownloadUrl":%q}}`, f.url("/cdn/iroh"))
	})
	f.on("/cdn/iroh", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(210)
		_, _ = fmt.Fprintf(w, `{"code":0,"data":{"redirect_url":%q}}`, f.url("/cdn/final"))
	})
	forbidden := true
	f.on("/cdn/final", func(w http.ResponseWriter, r *http.Request) {
		if forbidden {
			forbidden = false
			w.WriteHeader(http.StatusForbidden)
			return
		}
		serveRange(w, r, []byte("fresh"))
	})

	d := f.driver(Options{Username: "u", Password: "p"})
	entry := drive.Entry{
		ID: "12", ParentID: "5", Name: "a.txt", Size: 5,
		Extra: fileMeta{FileID: 12, Type: 0, Size: 5, Etag: "E", S3KeyFlag: "9-0", ParentID: 5},
	}
	got := readEntry(t, d, entry, 0, 5)
	if !bytes.Equal(got, []byte("fresh")) {
		t.Fatalf("got %q", got)
	}
}

func TestParseRedirectURL(t *testing.T) {
	if got := parseRedirectURL([]byte(`{"data":{"redirect_url":"https://x/y"}}`)); got != "https://x/y" {
		t.Fatalf("got %q", got)
	}
	if got := parseRedirectURL([]byte(`not json`)); got != "" {
		t.Fatalf("got %q", got)
	}
}

func TestUploadInstantReuse(t *testing.T) {
	f := newFakeAPI(t)
	installLogin(f, "tok")
	payload := []byte("dedup me")
	f.on("/b/api/file/upload_request", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"code":0,"data":{"Reuse":true,"Key":"k/1","FileId":555,"Bucket":"b","StorageNode":"m1","UploadId":"u"}}`)
	})
	f.json("/b/api/file/upload_complete/v2", `{"code":0,"data":{"file_info":{
		"FileId":31,"FileName":"a.txt","Type":0,"Size":8,"ParentFileId":5,"Etag":"deadbeef","S3KeyFlag":"9-0","UpdateAt":"2026-04-04T00:00:00+08:00"
	}}}`)

	d := f.driver(Options{Username: "u", Password: "p"})
	entry, err := d.PutSource(context.Background(), drive.UploadRequest{
		ParentID: "5", Name: "a.txt", Source: drive.NewBytesReadOnlyFileSource(payload),
	})
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	if entry.ID != "31" || entry.Name != "a.txt" || entry.Size != int64(len(payload)) {
		t.Fatalf("entry = %+v", entry)
	}
	if !strings.Contains(f.lastBodyFor("/b/api/file/upload_request"), md5Hex(payload)) {
		t.Fatalf("upload_request body missing md5: %s", f.lastBodyFor("/b/api/file/upload_request"))
	}
	if !strings.Contains(f.lastBodyFor("/b/api/file/upload_complete/v2"), `"isMultipart":false`) {
		t.Fatalf("complete body = %s", f.lastBodyFor("/b/api/file/upload_complete/v2"))
	}
	snapshot, err := d.DebugSnapshot(context.Background())
	if err != nil || snapshot.Extra[drive.DebugExtraInstantUploadCount] != int64(1) {
		t.Fatalf("instant upload count = %v, err = %v", snapshot.Extra, err)
	}
	f.json("/b/api/file/upload_complete/v2", `{"code":400,"message":"commit rejected"}`)
	if _, err := d.PutSource(context.Background(), drive.UploadRequest{
		ParentID: "5", Name: "a.txt", Source: drive.NewBytesReadOnlyFileSource(payload),
	}); err == nil {
		t.Fatal("expected instant upload commit to fail")
	}
	snapshot, err = d.DebugSnapshot(context.Background())
	if err != nil || snapshot.Extra[drive.DebugExtraInstantUploadCount] != int64(1) {
		t.Fatalf("failed commit changed instant upload count: %v, err = %v", snapshot.Extra, err)
	}
}

func TestUploadPresignedSinglePart(t *testing.T) {
	f := newFakeAPI(t)
	installLogin(f, "tok")
	payload := []byte("single part payload")
	var uploaded bytes.Buffer
	f.on("/b/api/file/upload_request", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"code":0,"data":{"Reuse":false,"Key":"k/1","FileId":555,"Bucket":"b","StorageNode":"m1","UploadId":"u"}}`)
	})
	f.on("/b/api/file/s3_upload_object/auth", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if fmt.Sprint(body["partNumberEnd"]) != "2" {
			t.Errorf("partNumberEnd = %v, want 2 (exclusive)", body["partNumberEnd"])
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"code":0,"data":{"presignedUrls":{"1":%q}}}`, f.url("/s3/part/1"))
	})
	f.on("/s3/part/1", func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		uploaded.Write(data)
		w.WriteHeader(http.StatusOK)
	})
	f.json("/b/api/file/upload_complete/v2", `{"code":0,"data":{"file_info":{
		"FileId":41,"FileName":"small.txt","Type":0,"Size":19,"ParentFileId":5,"Etag":"e","S3KeyFlag":"9-0","UpdateAt":"2026-04-04T00:00:00+08:00"
	}}}`)

	d := f.driver(Options{Username: "u", Password: "p"})
	entry, err := d.PutSource(context.Background(), drive.UploadRequest{
		ParentID: "5", Name: "small.txt", Source: drive.NewBytesReadOnlyFileSource(payload),
	})
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	if entry.ID != "41" {
		t.Fatalf("entry = %+v", entry)
	}
	if !bytes.Equal(uploaded.Bytes(), payload) {
		t.Fatalf("uploaded bytes = %q", uploaded.Bytes())
	}
	if f.sawCall("/b/api/file/s3_repare_upload_parts_batch") {
		t.Fatal("single-part upload must not use the batch endpoint")
	}
}

func TestUploadPresignedMultipart(t *testing.T) {
	restore := uploadChunkSize
	uploadChunkSize = 8
	defer func() { uploadChunkSize = restore }()

	f := newFakeAPI(t)
	installLogin(f, "tok")
	payload := []byte("0123456789abcdefghij") // 20 bytes -> 3 parts of 8/8/4
	parts := map[string][]byte{}
	var partsMu sync.Mutex
	f.on("/b/api/file/upload_request", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"code":0,"data":{"Reuse":false,"Key":"k/2","FileId":556,"Bucket":"b","StorageNode":"m1","UploadId":"u"}}`)
	})
	f.on("/b/api/file/s3_repare_upload_parts_batch", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			PartNumberStart int `json:"partNumberStart"`
			PartNumberEnd   int `json:"partNumberEnd"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		urls := map[string]string{}
		for part := body.PartNumberStart; part < body.PartNumberEnd; part++ {
			urls[strconv.Itoa(part)] = f.url(fmt.Sprintf("/s3/part/%d", part))
		}
		w.Header().Set("Content-Type", "application/json")
		out, _ := json.Marshal(map[string]any{"code": 0, "data": map[string]any{"presignedUrls": urls}})
		_, _ = w.Write(out)
	})
	f.on("/s3/part/1", putPartHandler(&partsMu, parts, "1"))
	f.on("/s3/part/2", putPartHandler(&partsMu, parts, "2"))
	f.on("/s3/part/3", putPartHandler(&partsMu, parts, "3"))
	f.json("/b/api/file/upload_complete/v2", `{"code":0,"data":{"file_info":{
		"FileId":42,"FileName":"big.bin","Type":0,"Size":20,"ParentFileId":5,"Etag":"e","S3KeyFlag":"9-0","UpdateAt":"2026-04-04T00:00:00+08:00"
	}}}`)

	d := f.driver(Options{Username: "u", Password: "p"})
	entry, err := d.PutSource(context.Background(), drive.UploadRequest{
		ParentID: "5", Name: "big.bin", Source: drive.NewBytesReadOnlyFileSource(payload),
	})
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	if entry.ID != "42" || entry.Size != 20 {
		t.Fatalf("entry = %+v", entry)
	}
	if !strings.Contains(f.lastBodyFor("/b/api/file/upload_complete/v2"), `"isMultipart":true`) {
		t.Fatalf("complete body = %s", f.lastBodyFor("/b/api/file/upload_complete/v2"))
	}
	if got := string(parts["1"]) + string(parts["2"]) + string(parts["3"]); got != string(payload) {
		t.Fatalf("uploaded parts = %q", got)
	}
}

func putPartHandler(mu *sync.Mutex, parts map[string][]byte, name string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		mu.Lock()
		parts[name] = data
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}
}

func TestUploadStaticCredentials(t *testing.T) {
	f := newFakeAPI(t)
	installLogin(f, "tok")
	payload := []byte("static creds body")

	var s3Body bytes.Buffer
	// The AWS SDK signs a path-style PUT to /{bucket}/{key}.
	f.on("/123-9/key/object", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Errorf("s3 method = %s, want PUT", r.Method)
		}
		data, _ := io.ReadAll(r.Body)
		s3Body.Write(data)
		w.Header().Set("ETag", `"abc"`)
		w.WriteHeader(http.StatusOK)
	})
	f.on("/b/api/file/upload_request", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"code":0,"data":{"Reuse":false,"Key":"key/object","FileId":557,"Bucket":"123-9","StorageNode":"m1","UploadId":"u","AccessKeyId":"ak","SecretAccessKey":"sk","SessionToken":"st","EndPoint":%q}}`, f.server.URL)
	})
	f.json("/b/api/file/upload_complete", `{"code":0,"data":{}}`)
	f.json("/b/api/file/list/new", `{"code":0,"data":{"Next":"-1","Total":1,"InfoList":[
		{"FileId":43,"FileName":"static.txt","Type":0,"Size":17,"ParentFileId":5,"Etag":"e","S3KeyFlag":"9-0","UpdateAt":"2026-04-04T00:00:00+08:00"}
	]}}`)

	d := f.driver(Options{Username: "u", Password: "p"})
	entry, err := d.PutSource(context.Background(), drive.UploadRequest{
		ParentID: "5", Name: "static.txt", Source: drive.NewBytesReadOnlyFileSource(payload),
	})
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	if entry.ID != "43" {
		t.Fatalf("entry = %+v", entry)
	}
	if !bytes.Equal(s3Body.Bytes(), payload) {
		t.Fatalf("s3 body = %q", s3Body.Bytes())
	}
	if !f.sawCall("/b/api/file/upload_complete") {
		t.Fatal("static path must confirm with upload_complete")
	}
	if f.sawCall("/b/api/file/upload_complete/v2") {
		t.Fatal("static path must not use upload_complete/v2")
	}
}

func TestChunkCountFor(t *testing.T) {
	restore := uploadChunkSize
	uploadChunkSize = 10
	defer func() { uploadChunkSize = restore }()
	cases := map[int64]int{0: 1, 1: 1, 10: 1, 11: 2, 20: 2, 21: 3}
	for size, want := range cases {
		if got := chunkCountFor(size); got != want {
			t.Fatalf("chunkCountFor(%d) = %d, want %d", size, got, want)
		}
	}
}

func TestSourceMD5HexPrefersProviderHash(t *testing.T) {
	payload := []byte("hash me")
	source := drive.NewBytesReadOnlyFileSource(payload)
	got, err := sourceMD5Hex(context.Background(), source)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if got != md5Hex(payload) {
		t.Fatalf("hash = %s, want %s", got, md5Hex(payload))
	}
}

func TestTokenPersistedAcrossRestart(t *testing.T) {
	f := newFakeAPI(t)
	installLogin(f, "persisted-token")
	store := drive.NewFileStateStore(t.TempDir())

	d := f.driver(Options{Username: "u", Password: "p"})
	d.InstallStateStore(store)
	if err := d.Init(context.Background()); err != nil {
		t.Fatalf("init: %v", err)
	}
	if !f.sawCall("/api/user/sign_in") {
		t.Fatal("first init should log in")
	}

	// A second driver over the same state store must reuse the saved token and
	// validate without calling sign_in again.
	f.mu.Lock()
	f.requests = nil
	f.mu.Unlock()
	d2 := f.driver(Options{Username: "u", Password: "p"})
	d2.InstallStateStore(store)
	if err := d2.Init(context.Background()); err != nil {
		t.Fatalf("second init: %v", err)
	}
	if f.sawCall("/api/user/sign_in") {
		t.Fatal("restart must reuse the persisted token")
	}
	if d2.cl.tokenValue() != "persisted-token" {
		t.Fatalf("token = %q", d2.cl.tokenValue())
	}
}

// TestCopyResolvesMetadataFromListing covers the drivecopy path: the caller
// rebuilds the entry from stat info and drops provider metadata, so Copy must
// re-resolve the source from its parent listing.
func TestCopyResolvesMetadataFromListing(t *testing.T) {
	f := newFakeAPI(t)
	installLogin(f, "tok")
	f.json("/b/api/file/list/new", `{"code":0,"data":{"Next":"-1","Total":1,"InfoList":[
		{"FileId":12,"FileName":"src.txt","Type":0,"Size":22,"ParentFileId":5,"Etag":"0b5a913105f577ce9ff88c4d7f3f0ee6","S3KeyFlag":"9-0","UpdateAt":"2026-05-05T00:00:00+08:00"}
	]}}`)
	f.json("/b/api/file/upload_request", `{"code":0,"data":{"Reuse":true,"Key":"k/9","FileId":700,"Bucket":"b","UploadId":"u"}}`)
	f.json("/b/api/file/upload_complete/v2", `{"code":0,"data":{"file_info":{
		"FileId":72,"FileName":"copy.txt","Type":0,"Size":22,"ParentFileId":9,"Etag":"0b5a913105f577ce9ff88c4d7f3f0ee6","S3KeyFlag":"9-0","UpdateAt":"2026-05-05T00:00:00+08:00"
	}}}`)

	d := f.driver(Options{Username: "u", Password: "p"})
	statEntry := drive.Entry{ID: "12", ParentID: "5", Name: "src.txt", Size: 22}
	entry, err := d.Copy(context.Background(), statEntry, "9", "copy.txt")
	if err != nil {
		t.Fatalf("copy: %v", err)
	}
	if entry.ID != "72" {
		t.Fatalf("entry = %+v", entry)
	}
	if !f.sawCall("/b/api/file/list/new") {
		t.Fatal("copy should have re-resolved the source metadata from a listing")
	}
}

func TestRemoteHashReportsContentMD5(t *testing.T) {
	d := New(Options{Username: "u", Password: "p"})
	file := drive.Entry{
		ID: "12", Name: "a.txt", Size: 5,
		Extra: fileMeta{FileID: 12, Type: fileTypeFile, Size: 5, Etag: "ABCDEF0123456789", S3KeyFlag: "9-0"},
	}
	algorithm, hash, err := d.RemoteHash(context.Background(), file)
	if err != nil {
		t.Fatalf("remote hash: %v", err)
	}
	if algorithm != drive.HashMD5 || hash != "abcdef0123456789" {
		t.Fatalf("remote hash = %s %s, want md5 abcdef0123456789", algorithm, hash)
	}
	if _, _, err := d.RemoteHash(context.Background(), drive.Entry{ID: "11", Name: "dir", IsDir: true}); !errors.Is(err, drive.ErrUnsupported) {
		t.Fatalf("directory hash error = %v, want ErrUnsupported", err)
	}
	if _, _, err := d.RemoteHash(context.Background(), drive.Entry{ID: "13", Name: "x"}); !errors.Is(err, drive.ErrUnsupported) {
		t.Fatalf("metadata-less hash error = %v, want ErrUnsupported", err)
	}
}

func TestCopyRelinksDeduplicatedBlob(t *testing.T) {
	f := newFakeAPI(t)
	installLogin(f, "tok")
	f.on("/b/api/file/upload_request", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["fileName"] != "copy.txt" || fmt.Sprint(body["parentFileId"]) != "9" {
			t.Errorf("copy upload_request body = %v", body)
		}
		if body["etag"] != "0b5a913105f577ce9ff88c4d7f3f0ee6" {
			t.Errorf("copy etag = %v", body["etag"])
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"code":0,"data":{"Reuse":true,"Key":"k/9","FileId":700,"Bucket":"b","UploadId":"u"}}`)
	})
	f.json("/b/api/file/upload_complete/v2", `{"code":0,"data":{"file_info":{
		"FileId":71,"FileName":"copy.txt","Type":0,"Size":22,"ParentFileId":9,"Etag":"0b5a913105f577ce9ff88c4d7f3f0ee6","S3KeyFlag":"9-0","UpdateAt":"2026-05-05T00:00:00+08:00"
	}}}`)

	d := f.driver(Options{Username: "u", Password: "p"})
	src := drive.Entry{
		ID: "12", ParentID: "5", Name: "src.txt", Size: 22,
		Extra: fileMeta{FileID: 12, Type: fileTypeFile, Size: 22, Etag: "0b5a913105f577ce9ff88c4d7f3f0ee6", S3KeyFlag: "9-0", ParentID: 5},
	}
	entry, err := d.Copy(context.Background(), src, "9", "copy.txt")
	if err != nil {
		t.Fatalf("copy: %v", err)
	}
	if entry.ID != "71" || entry.Name != "copy.txt" || entry.ParentID != "9" || entry.Size != 22 {
		t.Fatalf("entry = %+v", entry)
	}
	if f.sawCall("/s3/part/1") {
		t.Fatal("instant copy must not upload bytes")
	}
}

func TestCopyRejectsUnsupported(t *testing.T) {
	d := New(Options{Username: "u", Password: "p"})
	if _, err := d.Copy(context.Background(), drive.Entry{ID: "1", Name: "dir", IsDir: true}, "9", "d"); !errors.Is(err, drive.ErrUnsupported) {
		t.Fatalf("directory copy error = %v, want ErrUnsupported", err)
	}
	if _, err := d.Copy(context.Background(), drive.Entry{ID: "1", Name: "x"}, "9", "y"); !errors.Is(err, drive.ErrUnsupported) {
		t.Fatalf("metadata-less copy error = %v, want ErrUnsupported", err)
	}
}

func TestCopyWithoutReuseIsUnsupported(t *testing.T) {
	f := newFakeAPI(t)
	installLogin(f, "tok")
	f.json("/b/api/file/upload_request", `{"code":0,"data":{"Reuse":false,"Key":"k/1","FileId":700,"Bucket":"b","UploadId":"u","StorageNode":"m1"}}`)

	d := f.driver(Options{Username: "u", Password: "p"})
	src := drive.Entry{
		ID: "12", ParentID: "5", Name: "src.txt", Size: 22,
		Extra: fileMeta{FileID: 12, Type: fileTypeFile, Size: 22, Etag: "0b5a913105f577ce9ff88c4d7f3f0ee6", S3KeyFlag: "9-0", ParentID: 5},
	}
	if _, err := d.Copy(context.Background(), src, "9", "copy.txt"); !errors.Is(err, drive.ErrUnsupported) {
		t.Fatalf("copy error = %v, want ErrUnsupported", err)
	}
	if f.sawCall("/b/api/file/upload_complete") {
		t.Fatal("a refused instant copy must not complete an upload")
	}
}

// installResumableUpload wires a multipart upload whose parts can be counted,
// so tests can assert which parts a later attempt re-uploads.
func installResumableUpload(f *fakeAPI, payload []byte, parts *sync.Map) {
	f.on("/b/api/file/upload_request", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"code":0,"data":{"Reuse":false,"Key":"k/resume","FileId":900,"Bucket":"b","StorageNode":"m1","UploadId":"upload-1"}}`)
	})
	f.on("/b/api/file/s3_repare_upload_parts_batch", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			PartNumberStart int `json:"partNumberStart"`
			PartNumberEnd   int `json:"partNumberEnd"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		urls := map[string]string{}
		for part := body.PartNumberStart; part < body.PartNumberEnd; part++ {
			urls[strconv.Itoa(part)] = f.url(fmt.Sprintf("/s3/part/%d", part))
		}
		w.Header().Set("Content-Type", "application/json")
		out, _ := json.Marshal(map[string]any{"code": 0, "data": map[string]any{"presignedUrls": urls}})
		_, _ = w.Write(out)
	})
	f.on("/s3/part/1", func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		parts.Store(1, data)
		w.WriteHeader(http.StatusOK)
	})
	f.on("/s3/part/2", func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		parts.Store(2, data)
		w.WriteHeader(http.StatusOK)
	})
	f.on("/s3/part/3", func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		parts.Store(3, data)
		w.WriteHeader(http.StatusOK)
	})
	f.json("/b/api/file/upload_complete/v2", `{"code":0,"data":{"file_info":{
		"FileId":90,"FileName":"resume.bin","Type":0,"Size":20,"ParentFileId":5,"Etag":"e","S3KeyFlag":"9-0","UpdateAt":"2026-05-05T00:00:00+08:00"
	}}}`)
}

func TestResumableUploadSkipsConfirmedParts(t *testing.T) {
	restore := uploadChunkSize
	uploadChunkSize = 8
	defer func() { uploadChunkSize = restore }()

	f := newFakeAPI(t)
	installLogin(f, "tok")
	payload := []byte("0123456789abcdefghij") // 3 parts: 8/8/4
	var firstParts sync.Map
	installResumableUpload(f, payload, &firstParts)

	store := drive.NewFileStateStore(t.TempDir())
	d := f.driver(Options{Username: "u", Password: "p"})
	d.InstallStateStore(store)

	// First attempt: fail part 2 so the upload does not commit.
	f.mu.Lock()
	f.handlers["/s3/part/2"] = func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}
	f.mu.Unlock()
	if _, err := d.PutSource(context.Background(), drive.UploadRequest{
		ParentID: "5", Name: "resume.bin", Source: drive.NewBytesReadOnlyFileSource(payload),
	}); err == nil {
		t.Fatal("expected the first attempt to fail on part 2")
	}
	if _, ok := firstParts.Load(1); !ok {
		t.Fatal("part 1 should have uploaded before the failure")
	}

	// The binding must be resumable: part 1 confirmed, parts 2 and 3 pending.
	sessionKey := session.Identity{ParentID: "5", Name: "resume.bin", Size: int64(len(payload)), Fingerprint: md5Hex(payload)}.Key()
	binding, ok := d.sessions.Get(sessionKey)
	if !ok {
		t.Fatal("expected a persisted upload session")
	}
	var tok p123UploadToken
	if err := json.Unmarshal(binding.Token, &tok); err != nil {
		t.Fatalf("decode token: %v", err)
	}
	if tok.UploadID != "upload-1" {
		t.Fatalf("session upload id = %q", tok.UploadID)
	}
	if confirmed := session.ConfirmedParts(tok.Confirmed); !confirmed[1] || confirmed[2] {
		t.Fatalf("confirmed parts = %v, want part 1 only", confirmed)
	}

	// Second attempt: everything succeeds and must not re-PUT part 1.
	var secondParts sync.Map
	f.mu.Lock()
	for part := 1; part <= 3; part++ {
		path := fmt.Sprintf("/s3/part/%d", part)
		f.handlers[path] = func(w http.ResponseWriter, r *http.Request) {
			data, _ := io.ReadAll(r.Body)
			secondParts.Store(r.URL.Path, data)
			w.WriteHeader(http.StatusOK)
		}
	}
	f.mu.Unlock()

	entry, err := d.PutSource(context.Background(), drive.UploadRequest{
		ParentID: "5", Name: "resume.bin", Source: drive.NewBytesReadOnlyFileSource(payload),
	})
	if err != nil {
		t.Fatalf("resumed put: %v", err)
	}
	if entry.ID != "90" || entry.Size != int64(len(payload)) {
		t.Fatalf("entry = %+v", entry)
	}
	if _, attempted := secondParts.Load("/s3/part/1"); attempted {
		t.Fatal("part 1 was re-uploaded despite being confirmed")
	}
	for _, part := range []int{2, 3} {
		if _, ok := secondParts.Load(fmt.Sprintf("/s3/part/%d", part)); !ok {
			t.Fatalf("part %d was not uploaded on resume", part)
		}
	}
	if _, ok := d.sessions.Get(sessionKey); ok {
		t.Fatal("session binding must be deleted after a successful commit")
	}
}

func TestResumableUploadRestartsOnStaleSession(t *testing.T) {
	restore := uploadChunkSize
	uploadChunkSize = 8
	defer func() { uploadChunkSize = restore }()

	f := newFakeAPI(t)
	installLogin(f, "tok")
	payload := []byte("0123456789abcdefghij")
	uploads := 0
	f.on("/b/api/file/upload_request", func(w http.ResponseWriter, _ *http.Request) {
		uploads++
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"code":0,"data":{"Reuse":false,"Key":"k/stale","FileId":901,"Bucket":"b","StorageNode":"m1","UploadId":"upload-1"}}`)
	})
	presigns := 0
	f.on("/b/api/file/s3_repare_upload_parts_batch", func(w http.ResponseWriter, _ *http.Request) {
		presigns++
		w.Header().Set("Content-Type", "application/json")
		if presigns == 1 {
			// The stored handle is gone: the provider rejects the presign.
			_, _ = io.WriteString(w, `{"code":5061,"message":"上传任务不存在"}`)
			return
		}
		urls := map[string]string{}
		for part := 1; part <= 3; part++ {
			urls[strconv.Itoa(part)] = f.url(fmt.Sprintf("/s3/part/%d", part))
		}
		out, _ := json.Marshal(map[string]any{"code": 0, "data": map[string]any{"presignedUrls": urls}})
		_, _ = w.Write(out)
	})
	f.on("/s3/part/1", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	f.on("/s3/part/2", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	f.on("/s3/part/3", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	f.json("/b/api/file/upload_complete/v2", `{"code":0,"data":{"file_info":{
		"FileId":91,"FileName":"stale.bin","Type":0,"Size":20,"ParentFileId":5,"Etag":"e","S3KeyFlag":"9-0","UpdateAt":"2026-05-05T00:00:00+08:00"
	}}}`)

	store := drive.NewFileStateStore(t.TempDir())
	d := f.driver(Options{Username: "u", Password: "p"})
	d.InstallStateStore(store)
	// Seed a binding that looks resumable but points at a dead provider upload.
	sessionKey := session.Identity{ParentID: "5", Name: "stale.bin", Size: int64(len(payload)), Fingerprint: md5Hex(payload)}.Key()
	tok := p123UploadToken{Bucket: "b", Key: "k/stale", UploadID: "upload-1", StorageNode: "m1", FileID: 901, PartSize: uploadChunkSize}
	tok.Confirmed = session.ConfirmedBitmap(int64(len(payload)), uploadChunkSize, nil)
	if err := d.sessions.Create(sessionKey, mustJSON(tok)); err != nil {
		t.Fatal(err)
	}

	entry, err := d.PutSource(context.Background(), drive.UploadRequest{
		ParentID: "5", Name: "stale.bin", Source: drive.NewBytesReadOnlyFileSource(payload),
	})
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	if entry.ID != "91" {
		t.Fatalf("entry = %+v", entry)
	}
	if uploads != 1 {
		t.Fatalf("upload_request calls = %d, want 1 (the resumed handle is reused; only the restart creates a fresh upload)", uploads)
	}
	if presigns != 2 {
		t.Fatalf("presign calls = %d, want 2", presigns)
	}
}

func TestMD5Hex(t *testing.T) {
	if got := md5Hex([]byte("abc")); got != "900150983cd24fb0d6963f7d28e17f72" {
		t.Fatalf("md5Hex = %s", got)
	}
}

// installDownloadIgnoringRange wires a CDN node that answers every request
// with 200 and the whole object, ignoring the Range header.
func installDownloadIgnoringRange(f *fakeAPI, payload []byte) {
	f.on("/b/api/file/download_info", func(w http.ResponseWriter, _ *http.Request) {
		final := f.url("/cdn/full")
		iroh := f.url("/cdn/iroh?params=" + base64.StdEncoding.EncodeToString([]byte(final)))
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"code":0,"data":{"DownloadUrl":%q}}`, iroh)
	})
	f.on("/cdn/full", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(payload)
	})
}

func TestReadHandlesCDNIgnoringRange(t *testing.T) {
	f := newFakeAPI(t)
	installLogin(f, "tok")
	payload := []byte("0123456789abcdefghij")
	installDownloadIgnoringRange(f, payload)
	f.json("/b/api/restful/goapi/v1/file/details", `{"code":0,"data":{}}`)

	d := f.driver(Options{Username: "u", Password: "p"})
	entry := drive.Entry{
		ID: "12", ParentID: "5", Name: "a.txt", Size: int64(len(payload)),
		Extra: fileMeta{FileID: 12, Name: "a.txt", Type: fileTypeFile, Size: int64(len(payload)), Etag: "E", S3KeyFlag: "9-0", ParentID: 5},
	}

	cases := []struct {
		name   string
		offset int64
		size   int64
		want   []byte
	}{
		{"window", 4, 3, payload[4:7]},
		{"head", 0, 5, payload[:5]},
		{"tail", 4, 0, payload[4:]},
		{"clamped", 15, 100, payload[15:]},
		{"whole", 0, 0, payload},
		{"eof", int64(len(payload)), 3, nil},
	}
	for _, tc := range cases {
		got := readEntry(t, d, entry, tc.offset, tc.size)
		if !bytes.Equal(got, tc.want) {
			t.Fatalf("%s: read(%d,%d) = %q, want %q", tc.name, tc.offset, tc.size, got, tc.want)
		}
		if tc.size > 0 && int64(len(got)) > tc.size {
			t.Fatalf("%s: returned %d bytes for a %d-byte request", tc.name, len(got), tc.size)
		}
	}
}

func TestReadHandlesInfiniteRangeIgnoringBody(t *testing.T) {
	// A 200 response that ignores Range must not let the caller read past the
	// requested window even when more data is available.
	f := newFakeAPI(t)
	installLogin(f, "tok")
	payload := bytes.Repeat([]byte("x"), 4096)
	installDownloadIgnoringRange(f, payload)
	f.json("/b/api/restful/goapi/v1/file/details", `{"code":0,"data":{}}`)

	d := f.driver(Options{Username: "u", Password: "p"})
	entry := drive.Entry{
		ID: "12", ParentID: "5", Name: "big.txt", Size: int64(len(payload)),
		Extra: fileMeta{FileID: 12, Type: fileTypeFile, Size: int64(len(payload)), Etag: "E", S3KeyFlag: "9-0", ParentID: 5},
	}
	got := readEntry(t, d, entry, 1024, 16)
	if len(got) != 16 || !bytes.Equal(got, payload[1024:1040]) {
		t.Fatalf("read(1024,16) = %d bytes %q", len(got), got)
	}
}

func TestResolveRedirectURL(t *testing.T) {
	cases := []struct {
		base, location, want string
	}{
		{"https://a.example/b/c", "https://x.example/y", "https://x.example/y"},
		{"https://a.example/b/c?params=1", "/content", "https://a.example/content"},
		{"https://a.example/b/c", "//x.example/y", "https://x.example/y"},
		{"https://a.example/b/c", "sibling", "https://a.example/b/sibling"},
	}
	for _, tc := range cases {
		if got := resolveRedirectURL(tc.base, tc.location); got != tc.want {
			t.Fatalf("resolveRedirectURL(%q, %q) = %q, want %q", tc.base, tc.location, got, tc.want)
		}
	}
}

func TestReadFollowsRelativeRedirect(t *testing.T) {
	f := newFakeAPI(t)
	installLogin(f, "tok")
	payload := []byte("relative redirect content")
	f.json("/b/api/restful/goapi/v1/file/details", `{"code":0,"data":{}}`)
	f.on("/b/api/file/download_info", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"code":0,"data":{"DownloadUrl":%q}}`, f.url("/cdn/iroh"))
	})
	f.on("/cdn/iroh", func(w http.ResponseWriter, _ *http.Request) {
		// CDNs may answer with a path-only Location.
		w.Header().Set("Location", "/cdn/content")
		w.WriteHeader(http.StatusFound)
	})
	f.on("/cdn/content", func(w http.ResponseWriter, r *http.Request) {
		serveRange(w, r, payload)
	})

	d := f.driver(Options{Username: "u", Password: "p"})
	entry := drive.Entry{
		ID: "12", ParentID: "5", Name: "a.txt", Size: int64(len(payload)),
		Extra: fileMeta{FileID: 12, Type: fileTypeFile, Size: int64(len(payload)), Etag: "E", S3KeyFlag: "9-0", ParentID: 5},
	}
	if got := readEntry(t, d, entry, 0, 0); !bytes.Equal(got, payload) {
		t.Fatalf("read = %q, want %q", got, payload)
	}
	if got := readEntry(t, d, entry, 9, 9); !bytes.Equal(got, payload[9:18]) {
		t.Fatalf("ranged read = %q, want %q", got, payload[9:18])
	}
}

func TestReadFollowsRelativeJSONRedirect(t *testing.T) {
	f := newFakeAPI(t)
	installLogin(f, "tok")
	payload := []byte("relative json redirect")
	f.json("/b/api/restful/goapi/v1/file/details", `{"code":0,"data":{}}`)
	f.on("/b/api/file/download_info", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"code":0,"data":{"DownloadUrl":%q}}`, f.url("/cdn/iroh"))
	})
	f.on("/cdn/iroh", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"code":0,"data":{"redirect_url":"/cdn/content"}}`)
	})
	f.on("/cdn/content", func(w http.ResponseWriter, r *http.Request) {
		serveRange(w, r, payload)
	})

	d := f.driver(Options{Username: "u", Password: "p"})
	entry := drive.Entry{
		ID: "12", ParentID: "5", Name: "a.txt", Size: int64(len(payload)),
		Extra: fileMeta{FileID: 12, Type: fileTypeFile, Size: int64(len(payload)), Etag: "E", S3KeyFlag: "9-0", ParentID: 5},
	}
	if got := readEntry(t, d, entry, 0, 0); !bytes.Equal(got, payload) {
		t.Fatalf("read = %q, want %q", got, payload)
	}
}

func TestInitHonorsExplicitRootID(t *testing.T) {
	f := newFakeAPI(t)
	installLogin(f, "tok")
	f.json("/b/api/file/list/new", `{"code":0,"data":{"Next":"-1","Total":1,"InfoList":[
		{"FileId":99,"FileName":"qrypt","Type":1,"Size":0,"ParentFileId":0,"UpdateAt":"2026-01-01T00:00:00+08:00"}
	]}}`)

	// root_id is set alongside root_path: the explicit id must win and no path
	// walk may run. The buggy behaviour walked "/qrypt" from root_id (not from
	// the account root) and adopted the child id 99 instead of 42.
	d := f.driver(Options{Username: "u", Password: "p", RootID: "42", RootPath: "/qrypt"})
	if err := d.Init(context.Background()); err != nil {
		t.Fatalf("init: %v", err)
	}
	if d.rootID != "42" {
		t.Fatalf("rootID = %q, want 42", d.rootID)
	}
	if f.sawCall("/b/api/file/list/new") {
		t.Fatal("Init must not walk root_path when root_id is configured")
	}
	if resolved, err := d.ResolvePath(context.Background(), "/"); err != nil || resolved != "42" {
		t.Fatalf("ResolvePath(/) = %q, %v", resolved, err)
	}
	if got := d.resolve("0"); got != "42" {
		t.Fatalf("resolve(0) = %q, want 42", got)
	}
}

func TestInitDefaultsRootWhenOnlyPath(t *testing.T) {
	f := newFakeAPI(t)
	installLogin(f, "tok")
	f.json("/b/api/file/list/new", `{"code":0,"data":{"Next":"-1","Total":1,"InfoList":[
		{"FileId":77,"FileName":"qrypt","Type":1,"Size":0,"ParentFileId":0,"UpdateAt":"2026-01-01T00:00:00+08:00"}
	]}}`)

	d := f.driver(Options{Username: "u", Password: "p", RootPath: "/qrypt"})
	if err := d.Init(context.Background()); err != nil {
		t.Fatalf("init: %v", err)
	}
	if d.rootID != "77" {
		t.Fatalf("rootID = %q, want 77", d.rootID)
	}
}

func TestMkdirRejectsExistingFile(t *testing.T) {
	f := newFakeAPI(t)
	installLogin(f, "tok")
	// The same-name entry is a file, not a directory.
	f.json("/b/api/file/list/new", `{"code":0,"data":{"Next":"-1","Total":1,"InfoList":[
		{"FileId":55,"FileName":"docs","Type":0,"Size":12,"ParentFileId":5,"Etag":"e","S3KeyFlag":"9-0","UpdateAt":"2026-02-02T00:00:00+08:00"}
	]}}`)
	f.json("/b/api/file/upload_request", `{"code":0,"data":{"FileId":0,"Info":{
		"FileId":56,"FileName":"docs","Type":1,"Size":0,"ParentFileId":5,"UpdateAt":"2026-02-02T00:00:00+08:00"
	}}}`)

	d := f.driver(Options{Username: "u", Password: "p"})
	entry, err := d.Mkdir(context.Background(), "5", "docs")
	if err == nil {
		t.Fatalf("mkdir over an existing file returned entry %+v, want a conflict error", entry)
	}
	if drive.ErrorCategory(err) != drive.ErrorCategoryConflict {
		t.Fatalf("mkdir error category = %q, want conflict (err = %v)", drive.ErrorCategory(err), err)
	}
	if !errors.Is(err, fs.ErrExist) {
		t.Fatalf("error does not wrap fs.ErrExist: %v", err)
	}
	// 123pan would happily create a directory beside the file, so the driver
	// must reject before creating rather than repair afterwards.
	if f.sawCall("/b/api/file/upload_request") {
		t.Fatal("a file collision must not create a directory")
	}
}

func TestMkdirRaceDuplicateReturnsExistingDirectory(t *testing.T) {
	f := newFakeAPI(t)
	installLogin(f, "tok")
	// The pre-check sees nothing; the create then reports a duplicate (another
	// writer won the race) and the re-lookup finds the directory.
	calls := 0
	f.on("/b/api/file/list/new", func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		if calls == 1 {
			_, _ = io.WriteString(w, `{"code":0,"data":{"Next":"-1","Total":0,"InfoList":[]}}`)
			return
		}
		_, _ = io.WriteString(w, `{"code":0,"data":{"Next":"-1","Total":1,"InfoList":[
			{"FileId":77,"FileName":"docs","Type":1,"Size":0,"ParentFileId":5,"UpdateAt":"2026-02-02T00:00:00+08:00"}
		]}}`)
	})
	f.json("/b/api/file/upload_request", `{"code":5060,"data":{},"message":"同名文件已存在"}`)

	d := f.driver(Options{Username: "u", Password: "p"})
	entry, err := d.Mkdir(context.Background(), "5", "docs")
	if err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if entry.ID != "77" || !entry.IsDir {
		t.Fatalf("entry = %+v", entry)
	}
}

func TestMkdirFailsWhenDuplicateChildMissing(t *testing.T) {
	// The duplicate code arrives but the listing never shows the entry: the
	// driver must surface a conflict, not a fabricated entry.
	f := newFakeAPI(t)
	installLogin(f, "tok")
	f.json("/b/api/file/upload_request", `{"code":5060,"data":{},"message":"同名文件已存在"}`)
	f.json("/b/api/file/list/new", `{"code":0,"data":{"Next":"-1","Total":0,"InfoList":[]}}`)

	d := f.driver(Options{Username: "u", Password: "p"})
	if _, err := d.Mkdir(context.Background(), "5", "docs"); err == nil {
		t.Fatal("expected a conflict error")
	} else if drive.ErrorCategory(err) != drive.ErrorCategoryConflict {
		t.Fatalf("error category = %q, want conflict (err = %v)", drive.ErrorCategory(err), err)
	}
}

func TestInitRetryAfterRootPathFailure(t *testing.T) {
	f := newFakeAPI(t)
	installLogin(f, "tok")
	// First Init: /qrypt does not exist yet, so resolution fails.
	f.json("/b/api/file/list/new", `{"code":2,"data":null,"message":"不存在的文件"}`)

	d := f.driver(Options{Username: "u", Password: "p", RootPath: "/qrypt"})
	if err := d.Init(context.Background()); err == nil {
		t.Fatal("expected the first Init to fail on the missing root_path")
	}
	if d.rootID != "" {
		t.Fatalf("rootID = %q after a failed Init, want it left unset so a retry re-resolves", d.rootID)
	}

	// The directory now exists: a retried Init must resolve it, not fall back
	// to the account root.
	f.json("/b/api/file/list/new", `{"code":0,"data":{"Next":"-1","Total":1,"InfoList":[
		{"FileId":42,"FileName":"qrypt","Type":1,"Size":0,"ParentFileId":0,"UpdateAt":"2026-01-01T00:00:00+08:00"}
	]}}`)
	if err := d.Init(context.Background()); err != nil {
		t.Fatalf("retried init: %v", err)
	}
	if d.rootID != "42" {
		t.Fatalf("rootID = %q after retry, want 42", d.rootID)
	}
}

func TestDecodeParamsURL(t *testing.T) {
	inner := "https://cdn.example/path?x=1"
	wrapped := "https://web.example/download-v2/?params=" + base64.StdEncoding.EncodeToString([]byte(inner)) + "&is_s3=0"
	if got := decodeParamsURL(wrapped); got != inner {
		t.Fatalf("decodeParamsURL = %q, want %q", got, inner)
	}
	if got := decodeParamsURL("https://cdn.example/plain"); got != "https://cdn.example/plain" {
		t.Fatalf("decodeParamsURL(plain) = %q", got)
	}
}

func TestReadFollowsConsecutiveRedirects(t *testing.T) {
	f := newFakeAPI(t)
	installLogin(f, "tok")
	payload := []byte("consecutive redirect content")
	f.json("/b/api/restful/goapi/v1/file/details", `{"code":0,"data":{}}`)
	f.on("/b/api/file/download_info", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"code":0,"data":{"DownloadUrl":%q}}`, f.url("/cdn/first"))
	})
	f.on("/cdn/first", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", "/cdn/second") // relative first hop
		w.WriteHeader(http.StatusFound)
	})
	var secondRange string
	f.on("/cdn/second", func(w http.ResponseWriter, r *http.Request) {
		secondRange = r.Header.Get("Range")
		w.Header().Set("Location", "/cdn/content")
		w.WriteHeader(http.StatusFound)
	})
	f.on("/cdn/content", func(w http.ResponseWriter, r *http.Request) {
		serveRange(w, r, payload)
	})

	d := f.driver(Options{Username: "u", Password: "p"})
	entry := drive.Entry{
		ID: "12", ParentID: "5", Name: "a.txt", Size: int64(len(payload)),
		Extra: fileMeta{FileID: 12, Type: fileTypeFile, Size: int64(len(payload)), Etag: "E", S3KeyFlag: "9-0", ParentID: 5},
	}
	if got := readEntry(t, d, entry, 0, 0); !bytes.Equal(got, payload) {
		t.Fatalf("read = %q, want %q", got, payload)
	}
	if got := readEntry(t, d, entry, 11, 9); !bytes.Equal(got, payload[11:20]) {
		t.Fatalf("ranged read = %q, want %q", got, payload[11:20])
	}
	if secondRange != "bytes=0-0" {
		t.Fatalf("resolve hop Range = %q, want the probe range preserved", secondRange)
	}
}

func TestResolveCDNURLHopLimit(t *testing.T) {
	f := newFakeAPI(t)
	installLogin(f, "tok")
	f.json("/b/api/restful/goapi/v1/file/details", `{"code":0,"data":{}}`)
	f.on("/b/api/file/download_info", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"code":0,"data":{"DownloadUrl":%q}}`, f.url("/cdn/loop"))
	})
	f.on("/cdn/loop", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", "/cdn/loop")
		w.WriteHeader(http.StatusFound)
	})

	d := f.driver(Options{Username: "u", Password: "p"})
	entry := drive.Entry{
		ID: "12", ParentID: "5", Name: "a.txt", Size: 3,
		Extra: fileMeta{FileID: 12, Type: fileTypeFile, Size: 3, Etag: "E", S3KeyFlag: "9-0", ParentID: 5},
	}
	_, err := d.Read(context.Background(), entry, 0, 0)
	if err == nil || !strings.Contains(err.Error(), "too many redirects") {
		t.Fatalf("err = %v, want a hop-limit error", err)
	}
}

// installCDN416 wires a CDN whose probe request (Range: bytes=0-0) is answered
// with 416, as some nodes do for zero-length objects.
func installCDN416(f *fakeAPI) {
	f.on("/b/api/restful/goapi/v1/file/details", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"code":0,"data":{}}`)
	})
	f.on("/b/api/file/download_info", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"code":0,"data":{"DownloadUrl":%q}}`, f.url("/cdn/empty"))
	})
	f.on("/cdn/empty", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") == "bytes=0-0" {
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
}

func emptyFileMeta() fileMeta {
	return fileMeta{
		FileID: 12, Name: "empty.txt", Type: fileTypeFile, Size: 0,
		Etag: md5Hex(nil), S3KeyFlag: "9-0", ParentID: 5,
	}
}

func TestReadEmptyFileWithKnownSizeSkipsProbe(t *testing.T) {
	f := newFakeAPI(t)
	installLogin(f, "tok")
	installCDN416(f)

	d := f.driver(Options{Username: "u", Password: "p"})
	entry := drive.Entry{ID: "12", ParentID: "5", Name: "empty.txt", Extra: emptyFileMeta()}
	got := readEntry(t, d, entry, 0, 4)
	if len(got) != 0 {
		t.Fatalf("read of an empty file returned %d bytes", len(got))
	}
	if f.sawCall("/b/api/file/download_info") {
		t.Fatal("a known zero-length file must not hit the network at all")
	}
}

func TestReadEmptyFileWithListedSizeSkipsProbe(t *testing.T) {
	f := newFakeAPI(t)
	installLogin(f, "tok")
	installCDN416(f)
	f.json("/b/api/file/list/new", `{"code":0,"data":{"Next":"-1","Total":1,"InfoList":[
		{"FileId":12,"FileName":"empty.txt","Type":0,"Size":0,"ParentFileId":5,"Etag":"`+md5Hex(nil)+`","S3KeyFlag":"9-0","UpdateAt":"2026-02-02T00:00:00+08:00"}
	]}}`)

	d := f.driver(Options{Username: "u", Password: "p"})
	// A stat-style entry drops the provider metadata, so the size is learned
	// from the parent listing instead.
	entry := drive.Entry{ID: "12", ParentID: "5", Name: "empty.txt"}
	got := readEntry(t, d, entry, 0, 4)
	if len(got) != 0 {
		t.Fatalf("read returned %d bytes", len(got))
	}
	if f.sawCall("/b/api/file/download_info") {
		t.Fatal("a listed zero-length file must not hit the network")
	}
}

func TestReadEmptyFileProbeTolerates416(t *testing.T) {
	f := newFakeAPI(t)
	installLogin(f, "tok")
	installCDN416(f)

	// No metadata and no parent: the size cannot be confirmed, so the driver
	// must fall back to probing, and the probe's 416 must not be an error.
	d := f.driver(Options{Username: "u", Password: "p"})
	entry := drive.Entry{ID: "12", Name: "empty.txt"}
	got := readEntry(t, d, entry, 0, 4)
	if len(got) != 0 {
		t.Fatalf("read returned %d bytes", len(got))
	}
	if !f.sawCall("/b/api/file/download_info") {
		t.Fatal("an unconfirmed size must probe the CDN")
	}
}

func TestReadEmptyFileEOFAtOffsetZero(t *testing.T) {
	f := newFakeAPI(t)
	installLogin(f, "tok")
	installCDN416(f)
	d := f.driver(Options{Username: "u", Password: "p"})

	entry := drive.Entry{ID: "12", ParentID: "5", Name: "empty.txt", Extra: emptyFileMeta()}
	for _, size := range []int64{0, 1, 4096} {
		if got := readEntry(t, d, entry, 0, size); len(got) != 0 {
			t.Fatalf("read(0,%d) returned %d bytes", size, len(got))
		}
	}
}

func TestSessionKeyUsesResolvedParent(t *testing.T) {
	restore := uploadChunkSize
	uploadChunkSize = 8
	defer func() { uploadChunkSize = restore }()

	f := newFakeAPI(t)
	installLogin(f, "tok")
	payload := []byte("0123456789abcdefghij") // 3 parts: 8/8/4

	var requestsMu sync.Mutex
	var parents []string
	f.on("/b/api/file/upload_request", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		requestsMu.Lock()
		parents = append(parents, fmt.Sprint(body["parentFileId"]))
		requestsMu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"code":0,"data":{"Reuse":false,"Key":"k/root","FileId":900,"Bucket":"b","StorageNode":"m1","UploadId":"upload-1"}}`)
	})
	f.on("/b/api/file/s3_repare_upload_parts_batch", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			PartNumberStart int `json:"partNumberStart"`
			PartNumberEnd   int `json:"partNumberEnd"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		urls := map[string]string{}
		for part := body.PartNumberStart; part < body.PartNumberEnd; part++ {
			urls[strconv.Itoa(part)] = f.url(fmt.Sprintf("/s3/part/%d", part))
		}
		w.Header().Set("Content-Type", "application/json")
		out, _ := json.Marshal(map[string]any{"code": 0, "data": map[string]any{"presignedUrls": urls}})
		_, _ = w.Write(out)
	})
	failPart2 := true
	var partMu sync.Mutex
	for part := 1; part <= 3; part++ {
		part := part
		f.on(fmt.Sprintf("/s3/part/%d", part), func(w http.ResponseWriter, _ *http.Request) {
			partMu.Lock()
			fail := failPart2 && part == 2
			partMu.Unlock()
			if fail {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusOK)
		})
	}
	f.json("/b/api/file/upload_complete/v2", `{"code":0,"data":{"file_info":{
		"FileId":90,"FileName":"x.bin","Type":0,"Size":20,"Etag":"e","S3KeyFlag":"9-0","UpdateAt":"2026-05-05T00:00:00+08:00"
	}}}`)

	store := drive.NewFileStateStore(t.TempDir())
	identity := func(parent string) string {
		return session.Identity{ParentID: parent, Name: "x.bin", Size: int64(len(payload)), Fingerprint: md5Hex(payload)}.Key()
	}

	// Root 42: the upload fails partway, leaving a resumable binding.
	d1 := f.driver(Options{Username: "u", Password: "p", RootID: "42"})
	d1.InstallStateStore(store)
	if err := d1.Init(context.Background()); err != nil {
		t.Fatalf("init: %v", err)
	}
	if _, err := d1.PutSource(context.Background(), drive.UploadRequest{
		ParentID: "0", Name: "x.bin", Source: drive.NewBytesReadOnlyFileSource(payload),
	}); err == nil {
		t.Fatal("expected the first upload to fail on part 2")
	}
	if _, ok := d1.sessions.Get(identity("42")); !ok {
		t.Fatal("session must be keyed by the resolved parent id 42")
	}
	if _, ok := d1.sessions.Get(identity("0")); ok {
		t.Fatal("session must not be keyed by the unresolved root alias \"0\"")
	}

	// Root 99 over the same state store: the same "0" alias must not resume
	// root 42's upload, or the commit would land in the wrong directory.
	partMu.Lock()
	failPart2 = false
	partMu.Unlock()
	requestsMu.Lock()
	parents = nil
	requestsMu.Unlock()

	d2 := f.driver(Options{Username: "u", Password: "p", RootID: "99"})
	d2.InstallStateStore(store)
	if err := d2.Init(context.Background()); err != nil {
		t.Fatalf("init 2: %v", err)
	}
	if _, err := d2.PutSource(context.Background(), drive.UploadRequest{
		ParentID: "0", Name: "x.bin", Source: drive.NewBytesReadOnlyFileSource(payload),
	}); err != nil {
		t.Fatalf("second upload: %v", err)
	}
	requestsMu.Lock()
	got := append([]string(nil), parents...)
	requestsMu.Unlock()
	found := false
	for _, parent := range got {
		if parent == "99" {
			found = true
		}
		if parent == "0" {
			t.Fatalf("upload_request used the unresolved alias: %v", got)
		}
	}
	if !found {
		t.Fatalf("a fresh upload for root 99 must be created, upload_request parents = %v", got)
	}
}

// fakeS3Multipart serves the three S3 multipart calls path-style, so the
// static-credential upload path can be exercised against a real SDK client.
type fakeS3Multipart struct {
	mu        sync.Mutex
	uploads   int
	parts     map[int][]byte
	partCalls map[int]int
	failPart  map[int]int // part number -> how many initial attempts to fail
	aborted   int
}

func newFakeS3Multipart() *fakeS3Multipart {
	return &fakeS3Multipart{parts: map[int][]byte{}, partCalls: map[int]int{}, failPart: map[int]int{}}
}

func (s *fakeS3Multipart) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		switch {
		case query.Has("uploads"):
			s.mu.Lock()
			s.uploads++
			s.mu.Unlock()
			w.Header().Set("Content-Type", "application/xml")
			_, _ = io.WriteString(w, `<?xml version="1.0" encoding="UTF-8"?>`+
				`<InitiateMultipartUploadResult><Bucket>b</Bucket><Key>k</Key><UploadId>upload-1</UploadId></InitiateMultipartUploadResult>`)
		case query.Has("partNumber"):
			var part int
			_, _ = fmt.Sscanf(query.Get("partNumber"), "%d", &part)
			data, _ := io.ReadAll(r.Body)
			s.mu.Lock()
			s.partCalls[part]++
			remaining := s.failPart[part]
			if remaining > 0 {
				s.failPart[part] = remaining - 1
			}
			if remaining == 0 {
				s.parts[part] = data
			}
			s.mu.Unlock()
			if remaining > 0 {
				// Retryable server error: the SDK must rewind and try again.
				w.Header().Set("Content-Type", "application/xml")
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = io.WriteString(w, `<?xml version="1.0"?><Error><Code>InternalError</Code><Message>try again</Message></Error>`)
				return
			}
			w.Header().Set("ETag", fmt.Sprintf(`"etag-%d"`, part))
			w.WriteHeader(http.StatusOK)
		case query.Has("uploadId"):
			w.Header().Set("Content-Type", "application/xml")
			_, _ = io.WriteString(w, `<?xml version="1.0"?><CompleteMultipartUploadResult><Bucket>b</Bucket><Key>k</Key><ETag>"final"</ETag></CompleteMultipartUploadResult>`)
		default:
			if r.Method == http.MethodDelete {
				s.mu.Lock()
				s.aborted++
				s.mu.Unlock()
				w.WriteHeader(http.StatusNoContent)
				return
			}
			http.Error(w, "unexpected s3 call", http.StatusBadRequest)
		}
	}
}

func (s *fakeS3Multipart) assembled() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []byte{}
	for part := 1; part <= len(s.parts); part++ {
		out = append(out, s.parts[part]...)
	}
	return out
}

func TestStaticCredentialMultipartRetriesSeekableParts(t *testing.T) {
	restoreChunk, restorePut := uploadChunkSize, s3SinglePutMax
	uploadChunkSize = 8
	s3SinglePutMax = 4 // force the multipart path with a tiny payload
	defer func() { uploadChunkSize, s3SinglePutMax = restoreChunk, restorePut }()

	f := newFakeAPI(t)
	installLogin(f, "tok")
	payload := []byte("0123456789abcdefghij") // 3 parts: 8/8/4

	s3f := newFakeS3Multipart()
	s3f.failPart[2] = 1 // one retryable failure on part 2
	f.on("/123-9/key/object", s3f.handler())

	f.on("/b/api/file/upload_request", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"code":0,"data":{"Reuse":false,"Key":"key/object","FileId":557,"Bucket":"123-9","StorageNode":"m1","UploadId":"u","AccessKeyId":"ak","SecretAccessKey":"sk","SessionToken":"st","EndPoint":%q}}`, f.server.URL)
	})
	f.json("/b/api/file/upload_complete", `{"code":0,"data":{}}`)
	f.json("/b/api/file/list/new", `{"code":0,"data":{"Next":"-1","Total":1,"InfoList":[
		{"FileId":43,"FileName":"multipart.bin","Type":0,"Size":20,"ParentFileId":5,"Etag":"e","S3KeyFlag":"9-0","UpdateAt":"2026-04-04T00:00:00+08:00"}
	]}}`)

	d := f.driver(Options{Username: "u", Password: "p"})
	entry, err := d.PutSource(context.Background(), drive.UploadRequest{
		ParentID: "5", Name: "multipart.bin", Source: drive.NewBytesReadOnlyFileSource(payload),
	})
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	if entry.ID != "43" {
		t.Fatalf("entry = %+v", entry)
	}
	if !bytes.Equal(s3f.assembled(), payload) {
		t.Fatalf("assembled parts = %q, want %q", s3f.assembled(), payload)
	}
	if s3f.partCalls[2] < 2 {
		t.Fatalf("part 2 calls = %d, want the failed attempt retried", s3f.partCalls[2])
	}
	if s3f.aborted != 0 {
		t.Fatalf("upload aborted %d times, want 0", s3f.aborted)
	}
}

func TestSectionReadSeekerBoundedAndSeekable(t *testing.T) {
	src := bytes.NewReader([]byte("0123456789abcdefghij"))
	section, err := newSectionReadSeeker(src, 8, 8)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 3)

	n, err := section.Read(buf)
	if err != nil || string(buf[:n]) != "89a" {
		t.Fatalf("read = %q, %v", buf[:n], err)
	}
	if pos, err := section.Seek(0, io.SeekStart); err != nil || pos != 0 {
		t.Fatalf("seek start = %d, %v", pos, err)
	}
	n, err = section.Read(buf)
	if err != nil || string(buf[:n]) != "89a" {
		t.Fatalf("rewound read = %q, %v", buf[:n], err)
	}
	if pos, err := section.Seek(4, io.SeekStart); err != nil || pos != 4 {
		t.Fatalf("seek 4 = %d, %v", pos, err)
	}
	rest, err := io.ReadAll(section)
	if err != nil || string(rest) != "cdef" {
		t.Fatalf("rest = %q, %v", rest, err)
	}
	// The window never reads past its end.
	if _, err := section.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	all, err := io.ReadAll(section)
	if err != nil || string(all) != "89abcdef" || len(all) != 8 {
		t.Fatalf("all = %q (%d bytes), %v", all, len(all), err)
	}
}

func TestFinalizeUploadKeepsExtraConsistent(t *testing.T) {
	f := newFakeAPI(t)
	installLogin(f, "tok")
	payload := []byte("non empty upload payload")
	// The completion response omits Size and FileName but carries the hash and
	// storage flag that mark the metadata as provider-sourced.
	f.json("/b/api/file/upload_request", `{"code":0,"data":{"Reuse":true,"Key":"k/1","FileId":555,"Bucket":"b","StorageNode":"m1","UploadId":"u"}}`)
	f.json("/b/api/file/upload_complete/v2", `{"code":0,"data":{"file_info":{
		"FileId":31,"Type":0,"Etag":"abc123","S3KeyFlag":"9-0","ParentFileId":5,"UpdateAt":"2026-04-04T00:00:00+08:00"
	}}}`)

	d := f.driver(Options{Username: "u", Password: "p"})
	entry, err := d.PutSource(context.Background(), drive.UploadRequest{
		ParentID: "5", Name: "a.txt", Source: drive.NewBytesReadOnlyFileSource(payload),
	})
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	if entry.Size != int64(len(payload)) {
		t.Fatalf("entry.Size = %d, want %d", entry.Size, len(payload))
	}
	// A freshly uploaded non-empty file must be readable, not mistaken for an
	// empty one because the metadata size still said zero.
	f.json("/b/api/restful/goapi/v1/file/details", `{"code":0,"data":{}}`)
	installDownload(f, payload)
	if got := readEntry(t, d, entry, 0, 0); !bytes.Equal(got, payload) {
		t.Fatalf("read returned %d bytes (%q), want the %d-byte payload", len(got), got, len(payload))
	}

	meta, ok := metaFromEntry(entry)
	if !ok {
		t.Fatal("entry is missing provider metadata")
	}
	if meta.Size != int64(len(payload)) {
		t.Fatalf("meta.Size = %d, want %d (public field and Extra must agree)", meta.Size, len(payload))
	}
	if meta.Name != "a.txt" {
		t.Fatalf("meta.Name = %q, want a.txt", meta.Name)
	}
}

func TestFinalizeUploadFillsMissingEtag(t *testing.T) {
	f := newFakeAPI(t)
	installLogin(f, "tok")
	payload := []byte("etag fill payload")
	f.json("/b/api/file/upload_request", `{"code":0,"data":{"Reuse":false,"Key":"k/1","FileId":555,"Bucket":"b","StorageNode":"m1","UploadId":"u"}}`)
	f.on("/b/api/file/s3_upload_object/auth", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"code":0,"data":{"presignedUrls":{"1":%q}}}`, f.url("/s3/part/1"))
	})
	f.on("/s3/part/1", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	f.json("/b/api/file/upload_complete/v2", `{"code":0,"data":{"file_info":{
		"FileId":32,"FileName":"a.txt","Type":0,"Size":18,"ParentFileId":5,"UpdateAt":"2026-04-04T00:00:00+08:00"
	}}}`)

	d := f.driver(Options{Username: "u", Password: "p"})
	entry, err := d.PutSource(context.Background(), drive.UploadRequest{
		ParentID: "5", Name: "a.txt", Source: drive.NewBytesReadOnlyFileSource(payload),
	})
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	algorithm, hash, err := d.RemoteHash(context.Background(), entry)
	if err != nil {
		t.Fatalf("remote hash: %v", err)
	}
	if algorithm != drive.HashMD5 || hash != md5Hex(payload) {
		t.Fatalf("remote hash = %s %s, want md5 %s (filled from the computed upload hash)", algorithm, hash, md5Hex(payload))
	}
}

// pagedListHandler serves `total` entries `perPage` at a time, reporting the
// Next cursor as "-1" only on the final page (unless neverLast is set, which
// models a server that always claims more data).
func pagedListHandler(total, perPage int, neverLast bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		page, _ := strconv.Atoi(r.URL.Query().Get("Page"))
		start := (page - 1) * perPage
		end := min(start+perPage, total)

		var b strings.Builder
		b.WriteString(`{"code":0,"data":{"Total":`)
		b.WriteString(strconv.Itoa(total))
		b.WriteString(`,"Next":"`)
		if end >= total && !neverLast {
			b.WriteString("-1")
		} else {
			b.WriteString("cursor")
		}
		b.WriteString(`","InfoList":[`)
		for i := start; i < end; i++ {
			if i > start {
				b.WriteByte(',')
			}
			fmt.Fprintf(&b, `{"FileId":%d,"FileName":"f-%d","Type":0,"Size":1,"ParentFileId":7,"Etag":"e","S3KeyFlag":"9-0","UpdateAt":"2026-01-01T00:00:00+08:00"}`, i+1, i+1)
		}
		b.WriteString(`]}}`)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, b.String())
	}
}

// TestListPaginatesPastTheOldCap reproduces the reported shape: a listing that
// needs more than the previous 1000-page guard must paginate to completion
// rather than returning a silently truncated result. The page count (1001) is
// what mattered in the report (Total=100001 at 100/page); the per-page size is
// scaled down to one entry so the test stays fast without changing the branch
// under test.
func TestListPaginatesPastTheOldCap(t *testing.T) {
	const total = 1001
	f := newFakeAPI(t)
	installLogin(f, "tok")
	f.on("/b/api/file/list/new", pagedListHandler(total, 1, false))

	d := f.driver(Options{Username: "u", Password: "p"})
	entries, err := d.List(context.Background(), "7")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(entries) != total {
		t.Fatalf("got %d entries, want %d", len(entries), total)
	}
	if entries[0].Name != "f-1" || entries[len(entries)-1].Name != fmt.Sprintf("f-%d", total) {
		t.Fatalf("unexpected boundaries: %q .. %q", entries[0].Name, entries[len(entries)-1].Name)
	}
}

// TestListErrorsWhenPaginationLimitReached: a server that keeps reporting more
// data must surface an error once the safety cap is hit, never a short success.
func TestListErrorsWhenPaginationLimitReached(t *testing.T) {
	restore := listMaxPages
	listMaxPages = 3
	defer func() { listMaxPages = restore }()

	f := newFakeAPI(t)
	installLogin(f, "tok")
	f.on("/b/api/file/list/new", pagedListHandler(100000, listPageLimit, true))

	d := f.driver(Options{Username: "u", Password: "p"})
	entries, err := d.List(context.Background(), "7")
	if err == nil {
		t.Fatalf("list returned %d entries without an error, want a limit error", len(entries))
	}
	if !strings.Contains(err.Error(), "pagination safety limit") {
		t.Fatalf("err = %v, want a pagination safety limit error", err)
	}
	if errors.Is(err, drive.ErrNotFound) {
		t.Fatalf("limit error must not classify as not-found: %v", err)
	}
}

func TestListErrorsWhenPaginationLimitReachedAtCap(t *testing.T) {
	restore := listMaxPages
	listMaxPages = 2
	defer func() { listMaxPages = restore }()

	f := newFakeAPI(t)
	installLogin(f, "tok")
	f.on("/b/api/file/list/new", pagedListHandler(250, 100, false))

	d := f.driver(Options{Username: "u", Password: "p"})
	// 250 entries need 3 pages, one more than the cap.
	if _, err := d.List(context.Background(), "7"); err == nil || !strings.Contains(err.Error(), "pagination safety limit") {
		t.Fatalf("err = %v, want a pagination safety limit error", err)
	}
}

func TestListStopsOnEmptyPageEvenWithPendingTotal(t *testing.T) {
	// A server whose Total overstates the data must not loop forever once it
	// returns an empty page.
	f := newFakeAPI(t)
	installLogin(f, "tok")
	pages := 0
	f.on("/b/api/file/list/new", func(w http.ResponseWriter, r *http.Request) {
		pages++
		w.Header().Set("Content-Type", "application/json")
		if pages == 1 {
			_, _ = io.WriteString(w, `{"code":0,"data":{"Total":5000,"Next":"cursor","InfoList":[
				{"FileId":1,"FileName":"a","Type":0,"Size":1,"ParentFileId":7,"Etag":"e","S3KeyFlag":"9-0","UpdateAt":"2026-01-01T00:00:00+08:00"}
			]}}`)
			return
		}
		_, _ = io.WriteString(w, `{"code":0,"data":{"Total":5000,"Next":"cursor","InfoList":[]}}`)
	})

	d := f.driver(Options{Username: "u", Password: "p"})
	entries, err := d.List(context.Background(), "7")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(entries) != 1 || pages != 2 {
		t.Fatalf("entries = %d, pages = %d; want 1 entry over 2 pages", len(entries), pages)
	}
}

func TestAPIFailureIsClassified(t *testing.T) {
	f := newFakeAPI(t)
	installLogin(f, "tok")
	f.json("/b/api/file/list/new", `{"code":2,"message":"不存在的文件"}`)
	d := f.driver(Options{Username: "u", Password: "p"})
	_, err := d.List(context.Background(), "5")
	if !errors.Is(err, drive.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func md5Hex(data []byte) string {
	sum := md5.Sum(data)
	return hex.EncodeToString(sum[:])
}
