package koofr

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

type progressRecorder struct {
	phases   []drive.UploadPhase
	uploaded int64
}

func (r *progressRecorder) Phase(phase drive.UploadPhase) { r.phases = append(r.phases, phase) }
func (r *progressRecorder) Uploaded(n int64)              { r.uploaded += n }

func (r *progressRecorder) has(phase drive.UploadPhase) bool {
	for _, p := range r.phases {
		if p == phase {
			return true
		}
	}
	return false
}

// --- Init -----------------------------------------------------------------

func TestInitRejectsBadCredentialsWithError(t *testing.T) {
	f := newFakeProvider(t)
	f.rejectAuth = true
	opts := Options{Email: f.authEmail, Password: f.authPassword, APIBaseURL: f.server.URL}
	d := New(opts)
	d.cl.wait = func(ctx context.Context, _ int) error { return ctx.Err() }
	err := d.Init(context.Background())
	if err == nil || !errors.Is(err, drive.ErrAuth) {
		t.Fatalf("Init error = %v, want auth", err)
	}
}

func TestInitSelectsPrimaryMountByDefault(t *testing.T) {
	f := newFakeProvider(t)
	d := New(Options{Email: f.authEmail, Password: f.authPassword, APIBaseURL: f.server.URL})
	d.cl.wait = func(ctx context.Context, _ int) error { return ctx.Err() }
	if err := d.Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if d.mountID != "primary-mount" {
		t.Fatalf("mountID = %q, want primary-mount", d.mountID)
	}
}

func TestInitExplicitMountID(t *testing.T) {
	f := newFakeProvider(t)
	f.mu.Lock()
	f.mounts = append(f.mounts, apiMount{ID: "second-mount", Name: "Second", Online: true, CanWrite: true, CanUpload: true, SpaceTotal: 100, SpaceUsed: 10})
	f.mu.Unlock()
	d := f.newDriver(t, Options{MountID: "second-mount"})
	if d.mountID != "second-mount" {
		t.Fatalf("mountID = %q, want second-mount", d.mountID)
	}
}

func TestInitUnknownMountIDRejected(t *testing.T) {
	f := newFakeProvider(t)
	opts := Options{Email: f.authEmail, Password: f.authPassword, APIBaseURL: f.server.URL, MountID: "ghost"}
	d := New(opts)
	d.cl.wait = func(ctx context.Context, _ int) error { return ctx.Err() }
	err := d.Init(context.Background())
	if err == nil || !errors.Is(err, drive.ErrInvalidInput) {
		t.Fatalf("Init error = %v, want invalid input", err)
	}
}

func TestInitOfflineMountRejected(t *testing.T) {
	f := newFakeProvider(t)
	f.mu.Lock()
	f.mounts[0].Online = false
	f.mu.Unlock()
	opts := Options{Email: f.authEmail, Password: f.authPassword, APIBaseURL: f.server.URL}
	d := New(opts)
	d.cl.wait = func(ctx context.Context, _ int) error { return ctx.Err() }
	err := d.Init(context.Background())
	if err == nil || !strings.Contains(err.Error(), "offline") {
		t.Fatalf("Init error = %v, want offline rejection", err)
	}
}

func TestInitReadOnlyMountRejected(t *testing.T) {
	f := newFakeProvider(t)
	f.mu.Lock()
	f.mounts[0].CanUpload = false
	f.mu.Unlock()
	opts := Options{Email: f.authEmail, Password: f.authPassword, APIBaseURL: f.server.URL}
	d := New(opts)
	d.cl.wait = func(ctx context.Context, _ int) error { return ctx.Err() }
	err := d.Init(context.Background())
	if err == nil || !errors.Is(err, drive.ErrPermission) {
		t.Fatalf("Init error = %v, want permission", err)
	}
}

func TestInitMissingRootPathDoesNotFallback(t *testing.T) {
	f := newFakeProvider(t)
	opts := Options{Email: f.authEmail, Password: f.authPassword, APIBaseURL: f.server.URL, RootPath: "/missing"}
	d := New(opts)
	d.cl.wait = func(ctx context.Context, _ int) error { return ctx.Err() }
	err := d.Init(context.Background())
	if err == nil || !errors.Is(err, drive.ErrNotFound) {
		t.Fatalf("Init error = %v, want not found", err)
	}
	// A retry must fail the same way, never silently mounting the root.
	if err := d.Init(context.Background()); err == nil || !errors.Is(err, drive.ErrNotFound) {
		t.Fatalf("second Init error = %v, want not found", err)
	}
}

func TestInitRootPathNestedAndIdempotent(t *testing.T) {
	f := newFakeProvider(t)
	f.mkdirAll("/docs/sub")
	d := f.newDriver(t, Options{RootPath: "/docs/sub"})
	if d.rootPath != "/docs/sub" {
		t.Fatalf("rootPath = %q", d.rootPath)
	}
	if err := d.Init(context.Background()); err != nil {
		t.Fatalf("second Init: %v", err)
	}
}

// --- Space ------------------------------------------------------------------

func TestSpaceConvertsMiBToBytes(t *testing.T) {
	f := newFakeProvider(t)
	d := f.newDriver(t, Options{})
	space, err := d.Space(context.Background())
	if err != nil {
		t.Fatalf("Space: %v", err)
	}
	wantTotal := int64(1058816) << 20
	wantFree := int64(1058816-6302) << 20
	if space.Total != wantTotal || space.Free != wantFree {
		t.Fatalf("space = %+v, want total=%d free=%d", space, wantTotal, wantFree)
	}
}

// --- Listing and mapping ------------------------------------------------------

func TestListMixedDirectoryMapping(t *testing.T) {
	f := newFakeProvider(t)
	modMs := int64(1700000000123)
	f.putFile("/dir/data.bin", []byte("hello"), modMs)
	f.mkdirAll("/dir/nested")

	d := f.newDriver(t, Options{RootPath: "/dir"})
	entries, err := d.List(context.Background(), "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("entries = %+v", entries)
	}
	var file, dir drive.Entry
	for _, entry := range entries {
		if entry.IsDir {
			dir = entry
		} else {
			file = entry
		}
	}
	if file.ID != "/dir/data.bin" || file.ParentID != "/dir" || file.Size != 5 {
		t.Fatalf("file = %+v", file)
	}
	if !file.ModTime.Equal(time.UnixMilli(modMs).UTC()) {
		t.Fatalf("file mtime = %v", file.ModTime)
	}
	if hash := entryFileHash(file); len(hash) != 32 {
		t.Fatalf("file hash = %q", hash)
	}
	if dir.ID != "/dir/nested" || !dir.IsDir || dir.Size != 0 {
		t.Fatalf("dir = %+v", dir)
	}
}

func TestListMissingPathNotFound(t *testing.T) {
	f := newFakeProvider(t)
	d := f.newDriver(t, Options{})
	_, err := d.List(context.Background(), "/missing")
	if err == nil || !errors.Is(err, drive.ErrNotFound) {
		t.Fatalf("List error = %v, want not found", err)
	}
}

func TestListRejectsRelativePathBeforeRequest(t *testing.T) {
	f := newFakeProvider(t)
	d := f.newDriver(t, Options{})
	_, err := d.List(context.Background(), "no-leading-slash")
	if err == nil || !errors.Is(err, drive.ErrInvalidInput) {
		t.Fatalf("List error = %v, want invalid input", err)
	}
	if got := f.requestsMatching("GET", "files/list"); got != 0 {
		t.Fatalf("list requests = %d, want 0", got)
	}
}

func TestResolvePathSegments(t *testing.T) {
	f := newFakeProvider(t)
	f.mkdirAll("/a/b")
	d := f.newDriver(t, Options{})

	id, err := d.ResolvePath(context.Background(), "/")
	if err != nil || id != "/" {
		t.Fatalf("ResolvePath / = %q, %v", id, err)
	}
	id, err = d.ResolvePath(context.Background(), "/a/b")
	if err != nil || id != "/a/b" {
		t.Fatalf("ResolvePath /a/b = %q, %v", id, err)
	}
	if _, err := d.ResolvePath(context.Background(), "/a/missing"); err == nil || !errors.Is(err, drive.ErrNotFound) {
		t.Fatalf("ResolvePath missing error = %v", err)
	}
	if _, err := d.ResolvePath(context.Background(), "/../escape"); err == nil || !errors.Is(err, drive.ErrInvalidInput) {
		t.Fatalf("ResolvePath escape error = %v", err)
	}
}

func TestResolveRemoteNameIdentityAndValidation(t *testing.T) {
	f := newFakeProvider(t)
	d := f.newDriver(t, Options{})
	info, err := d.ResolveRemoteName(context.Background(), "a.bin")
	if err != nil || info.PlainName != "a.bin" || info.RemoteName != "a.bin" {
		t.Fatalf("ResolveRemoteName = %+v, %v", info, err)
	}
	if _, err := d.ResolveRemoteName(context.Background(), `back\slash`); err == nil || !errors.Is(err, drive.ErrInvalidInput) {
		t.Fatalf("backslash name error = %v", err)
	}
}

// --- Read -------------------------------------------------------------------

func seededFile(t *testing.T, f *fakeProvider, path string, size int) drive.Entry {
	t.Helper()
	data := make([]byte, size)
	for i := range data {
		data[i] = byte('0' + i%10)
	}
	f.putFile(path, data, 1700000000123)
	d := f.newDriver(t, Options{})
	entries, err := d.List(context.Background(), parentPath(path))
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, entry := range entries {
		if entry.Name == baseName(path) {
			return entry
		}
	}
	t.Fatalf("seeded entry %s not found", path)
	return drive.Entry{}
}

func TestReadRangeExact(t *testing.T) {
	f := newFakeProvider(t)
	entry := seededFile(t, f, "/data.bin", 100)
	d := f.newDriver(t, Options{})
	reader, err := d.Read(context.Background(), entry, 50, 10)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	reader.Close()
	if len(data) != 10 || data[0] != byte('0'+50%10) || data[9] != byte('0'+59%10) {
		t.Fatalf("data = %q", data)
	}
	if got := f.requestsMatching("GET", "files/info"); got != 0 {
		t.Fatalf("metadata requests during read = %d, want 0", got)
	}
}

func TestReadAtOrPastEOFReturnsEmpty(t *testing.T) {
	f := newFakeProvider(t)
	entry := seededFile(t, f, "/data.bin", 100)
	d := f.newDriver(t, Options{})
	reader, err := d.Read(context.Background(), entry, 100, 10)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	data, _ := io.ReadAll(reader)
	reader.Close()
	if len(data) != 0 {
		t.Fatalf("data = %q, want empty", data)
	}
	if got := f.requestsMatching("GET", "files/get"); got != 0 {
		t.Fatalf("content requests = %d, want 0", got)
	}
}

func TestRead416PastRealSizeIsError(t *testing.T) {
	f := newFakeProvider(t)
	entry := seededFile(t, f, "/data.bin", 100)
	entry.Size = 200 // stale metadata: the driver thinks the file is longer
	d := f.newDriver(t, Options{})
	_, err := d.Read(context.Background(), entry, 150, 10)
	if err == nil || !errors.Is(err, drive.ErrInvalidInput) {
		t.Fatalf("Read error = %v, want invalid input", err)
	}
}

func TestReadIgnoresRangeFallback(t *testing.T) {
	f := newFakeProvider(t)
	entry := seededFile(t, f, "/data.bin", 100)
	f.mu.Lock()
	f.ignoreRange = true
	f.mu.Unlock()
	d := f.newDriver(t, Options{})
	reader, err := d.Read(context.Background(), entry, 90, 5)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	reader.Close()
	if len(data) != 5 || data[0] != byte('0'+90%10) {
		t.Fatalf("data = %q", data)
	}
}

func TestReadMissingFile(t *testing.T) {
	f := newFakeProvider(t)
	d := f.newDriver(t, Options{})
	entry := drive.Entry{ID: "/gone.bin", Name: "gone.bin", Size: 10}
	_, err := d.Read(context.Background(), entry, 0, 10)
	if err == nil || !errors.Is(err, drive.ErrNotFound) {
		t.Fatalf("Read error = %v, want not found", err)
	}
}

// --- Write -------------------------------------------------------------------

func TestMkdirCreatesDirectory(t *testing.T) {
	f := newFakeProvider(t)
	d := f.newDriver(t, Options{})
	created, err := d.Mkdir(context.Background(), "/", "docs")
	if err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	if created.ID != "/docs" || !created.IsDir || created.Name != "docs" {
		t.Fatalf("created = %+v", created)
	}
}

func TestMkdirIdempotent(t *testing.T) {
	f := newFakeProvider(t)
	f.mkdirAll("/docs")
	d := f.newDriver(t, Options{})
	existing, err := d.Mkdir(context.Background(), "/", "DOCS") // case-insensitive collision
	if err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	if existing.ID != "/docs" {
		t.Fatalf("existing = %+v", existing)
	}
}

func TestMkdirFileConflict(t *testing.T) {
	f := newFakeProvider(t)
	f.putFile("/occupied.bin", []byte("x"), 1)
	d := f.newDriver(t, Options{})
	_, err := d.Mkdir(context.Background(), "/", "OCCUPIED.BIN")
	if err == nil || !errors.Is(err, drive.ErrConflict) {
		t.Fatalf("Mkdir error = %v, want conflict", err)
	}
}

func TestMkdirInvalidNameRejectedBeforeRequest(t *testing.T) {
	f := newFakeProvider(t)
	d := f.newDriver(t, Options{})
	_, err := d.Mkdir(context.Background(), "/", "")
	if err == nil || !errors.Is(err, drive.ErrInvalidInput) {
		t.Fatalf("Mkdir error = %v, want invalid input", err)
	}
	if got := f.requestsMatching("POST", "files/folder"); got != 0 {
		t.Fatalf("folder requests = %d, want 0", got)
	}
}

func TestMkdirNonJSONErrorBodyHandled(t *testing.T) {
	f := newFakeProvider(t)
	f.addFault(func(r *http.Request) bool {
		return r.Method == http.MethodPost && strings.Contains(r.URL.Path, "files/folder")
	}, 3, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("Name is required"))
	})
	d := f.newDriver(t, Options{})
	_, err := d.Mkdir(context.Background(), "/", "docs")
	if err == nil {
		t.Fatalf("Mkdir error = nil, want failure")
	}
	if strings.Contains(err.Error(), "panic") {
		t.Fatalf("error mentions panic: %v", err)
	}
	if !strings.Contains(err.Error(), "Name is required") {
		t.Fatalf("error = %v, want provider text", err)
	}
}

func TestRenameUpdatesIdentity(t *testing.T) {
	f := newFakeProvider(t)
	f.putFile("/old.bin", []byte("data"), 1700000000123)
	d := f.newDriver(t, Options{})
	entries, err := d.List(context.Background(), "/")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	var entry drive.Entry
	for _, e := range entries {
		if e.Name == "old.bin" {
			entry = e
		}
	}
	updated, err := d.Rename(context.Background(), entry, "new.bin")
	if err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if updated.ID != "/new.bin" || updated.Name != "new.bin" {
		t.Fatalf("updated = %+v", updated)
	}
}

func TestRenameCaseVariantConflict(t *testing.T) {
	f := newFakeProvider(t)
	f.putFile("/a.bin", []byte("a"), 1)
	f.putFile("/b.bin", []byte("b"), 1)
	d := f.newDriver(t, Options{})
	entries, _ := d.List(context.Background(), "/")
	var b drive.Entry
	for _, e := range entries {
		if e.Name == "b.bin" {
			b = e
		}
	}
	_, err := d.Rename(context.Background(), b, "A.BIN")
	if err == nil || !errors.Is(err, drive.ErrConflict) {
		t.Fatalf("Rename error = %v, want conflict", err)
	}
}

func TestMoveWithinMount(t *testing.T) {
	f := newFakeProvider(t)
	f.putFile("/src/file.bin", []byte("data"), 1700000000123)
	f.mkdirAll("/dst")
	d := f.newDriver(t, Options{})
	entries, _ := d.List(context.Background(), "/src")
	moved, err := d.Move(context.Background(), entries[0], "/dst")
	if err != nil {
		t.Fatalf("Move: %v", err)
	}
	if moved.ID != "/dst/file.bin" || moved.ParentID != "/dst" {
		t.Fatalf("moved = %+v", moved)
	}
}

func TestMoveIntoItselfRejected(t *testing.T) {
	f := newFakeProvider(t)
	f.mkdirAll("/tree/inner")
	d := f.newDriver(t, Options{})
	_, err := d.Move(context.Background(), drive.Entry{ID: "/tree", Name: "tree", IsDir: true}, "/tree/inner")
	if err == nil || !errors.Is(err, drive.ErrInvalidInput) {
		t.Fatalf("Move error = %v, want invalid input", err)
	}
}

func TestCopyPreservesMtime(t *testing.T) {
	f := newFakeProvider(t)
	f.putFile("/orig.bin", []byte("payload"), 1700000000123)
	d := f.newDriver(t, Options{})
	entries, _ := d.List(context.Background(), "/")
	copied, err := d.Copy(context.Background(), entries[0], "/", "dup.bin")
	if err != nil {
		t.Fatalf("Copy: %v", err)
	}
	if copied.ID != "/dup.bin" || copied.Size != 7 {
		t.Fatalf("copied = %+v", copied)
	}
	if !copied.ModTime.Equal(time.UnixMilli(1700000000123).UTC()) {
		t.Fatalf("copied mtime = %v, want source mtime", copied.ModTime)
	}
}

func TestCopyConflict(t *testing.T) {
	f := newFakeProvider(t)
	f.putFile("/orig.bin", []byte("payload"), 1)
	f.putFile("/dup.bin", []byte("existing"), 1)
	d := f.newDriver(t, Options{})
	entries, _ := d.List(context.Background(), "/")
	_, err := d.Copy(context.Background(), entries[0], "/", "DUP.BIN")
	if err == nil || !errors.Is(err, drive.ErrConflict) {
		t.Fatalf("Copy error = %v, want conflict", err)
	}
}

func TestRemoveConditionalGuards(t *testing.T) {
	f := newFakeProvider(t)
	f.putFile("/guarded.bin", []byte("payload"), 1700000000123)
	d := f.newDriver(t, Options{})
	entries, _ := d.List(context.Background(), "/")
	entry := entries[0]

	stale := entry
	stale.Size += 100
	if err := d.Remove(context.Background(), stale); err == nil || !errors.Is(err, drive.ErrConflict) {
		t.Fatalf("Remove stale error = %v, want conflict", err)
	}
	if err := d.Remove(context.Background(), entry); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if err := d.Remove(context.Background(), entry); err == nil || !errors.Is(err, drive.ErrNotFound) {
		t.Fatalf("Remove missing error = %v, want not found", err)
	}
}

func TestRemoveNonEmptyDirectoryCascades(t *testing.T) {
	f := newFakeProvider(t)
	f.putFile("/tree/inner/leaf.bin", []byte("leaf"), 1)
	d := f.newDriver(t, Options{})
	if err := d.Remove(context.Background(), drive.Entry{ID: "/tree", ParentID: "/", Name: "tree", IsDir: true}); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := d.List(context.Background(), "/tree"); err == nil || !errors.Is(err, drive.ErrNotFound) {
		t.Fatalf("List after cascade = %v, want not found", err)
	}
}

// --- Upload -------------------------------------------------------------------

func TestUploadPinsOverwriteAndMtime(t *testing.T) {
	f := newFakeProvider(t)
	d := f.newDriver(t, Options{})
	modTime := time.UnixMilli(1700000000123).UTC()
	entry, err := d.PutSource(context.Background(), drive.UploadRequest{
		ParentID: "/",
		Name:     "upload.bin",
		Source:   drive.NewBytesReadOnlyFileSource([]byte("payload")),
		ModTime:  modTime,
	})
	if err != nil {
		t.Fatalf("PutSource: %v", err)
	}
	params := f.lastPutParams()
	for key, want := range map[string]string{
		"overwrite":  "true",
		"autorename": "false",
		"info":       "true",
		"filename":   "upload.bin",
		"modified":   "1700000000123",
	} {
		if got := params.Get(key); got != want {
			t.Fatalf("put %s = %q, want %q", key, got, want)
		}
	}
	if entry.ID != "/upload.bin" || entry.Size != 7 || entry.Name != "upload.bin" {
		t.Fatalf("entry = %+v", entry)
	}
	if !entry.ModTime.Equal(modTime) {
		t.Fatalf("entry mtime = %v, want %v", entry.ModTime, modTime)
	}
	if hash := entryFileHash(entry); len(hash) != 32 {
		t.Fatalf("entry hash = %q", hash)
	}
}

func TestUploadOverwriteKeepsCanonicalNameAndMtime(t *testing.T) {
	f := newFakeProvider(t)
	f.putFile("/probe2.bin", []byte("old"), 1)
	d := f.newDriver(t, Options{})
	modTime := time.UnixMilli(1700000000999).UTC()
	entry, err := d.PutSource(context.Background(), drive.UploadRequest{
		ParentID: "/",
		Name:     "PROBE2.BIN",
		Source:   drive.NewBytesReadOnlyFileSource([]byte("new content")),
		ModTime:  modTime,
	})
	if err != nil {
		t.Fatalf("PutSource: %v", err)
	}
	if entry.Name != "probe2.bin" {
		t.Fatalf("entry name = %q, want canonical probe2.bin", entry.Name)
	}
	if entry.Size != 11 || !entry.ModTime.Equal(modTime) {
		t.Fatalf("entry = %+v", entry)
	}
	// No auto-renamed fork may appear.
	entries, _ := d.List(context.Background(), "/")
	if len(entries) != 1 {
		t.Fatalf("entries after overwrite = %+v", entries)
	}
}

func TestUploadInvalidNamesRejectedWithoutRequest(t *testing.T) {
	f := newFakeProvider(t)
	d := f.newDriver(t, Options{})
	cases := []string{
		`back\slash.bin`,
		"bad\xff utf8",
		strings.Repeat("x", 300),
		"sla/sh",
	}
	for _, name := range cases {
		_, err := d.PutSource(context.Background(), drive.UploadRequest{
			ParentID: "/",
			Name:     name,
			Source:   drive.NewBytesReadOnlyFileSource([]byte("x")),
		})
		if err == nil || !errors.Is(err, drive.ErrInvalidInput) {
			t.Fatalf("PutSource(%q) error = %v, want invalid input", name, err)
		}
	}
	if got := f.requestsMatching("POST", "files/put"); got != 0 {
		t.Fatalf("put requests = %d, want 0", got)
	}
}

func TestUploadProgressReportsBytes(t *testing.T) {
	f := newFakeProvider(t)
	d := f.newDriver(t, Options{})
	recorder := &progressRecorder{}
	_, err := d.PutSource(context.Background(), drive.UploadRequest{
		ParentID: "/",
		Name:     "progress.bin",
		Source:   drive.NewBytesReadOnlyFileSource(make([]byte, 4096)),
		Progress: recorder,
		ModTime:  time.UnixMilli(1),
	})
	if err != nil {
		t.Fatalf("PutSource: %v", err)
	}
	if recorder.uploaded != 4096 {
		t.Fatalf("uploaded = %d, want 4096", recorder.uploaded)
	}
	if !recorder.has(drive.UploadPhaseCompleted) {
		t.Fatalf("phases = %v", recorder.phases)
	}
	if recorder.has(drive.UploadPhaseInstant) {
		t.Fatalf("instant phase reported: %v", recorder.phases)
	}
}

func TestRequiredUploadHashesEmpty(t *testing.T) {
	f := newFakeProvider(t)
	d := f.newDriver(t, Options{})
	if hashes := d.RequiredUploadHashes(); len(hashes) != 0 {
		t.Fatalf("RequiredUploadHashes = %v", hashes)
	}
}

func TestRemoteHashMD5(t *testing.T) {
	f := newFakeProvider(t)
	f.putFile("/hashed.bin", []byte("payload"), 1)
	d := f.newDriver(t, Options{})
	entries, _ := d.List(context.Background(), "/")
	algorithm, hash, err := d.RemoteHash(context.Background(), entries[0])
	if err != nil || algorithm != drive.HashMD5 || len(hash) != 32 {
		t.Fatalf("RemoteHash = %s, %s, %v", algorithm, hash, err)
	}
	info, err := d.RemoteHashInfo(context.Background(), entries[0])
	if err != nil || !info.Verified || info.Source != drive.RemoteHashSourceProvider {
		t.Fatalf("RemoteHashInfo = %+v, %v", info, err)
	}
	// A directory carries no hash.
	_, _, err = d.RemoteHash(context.Background(), drive.Entry{IsDir: true})
	if err == nil || !errors.Is(err, drive.ErrUnsupported) {
		t.Fatalf("dir RemoteHash error = %v", err)
	}
	// A malformed hash stays unsupported.
	bad := drive.Entry{Extra: map[string]any{"hash": "not-a-hash"}}
	if _, _, err := d.RemoteHash(context.Background(), bad); err == nil || !errors.Is(err, drive.ErrUnsupported) {
		t.Fatalf("malformed RemoteHash error = %v", err)
	}
}

// --- Error handling and retry -------------------------------------------------

func TestRateLimitRetriesThenSucceeds(t *testing.T) {
	f := newFakeProvider(t)
	f.putFile("/rate.bin", []byte("x"), 1)
	f.faultStatus("GET", "files/list", 1, http.StatusTooManyRequests, `{"error":{"code":"Other","message":"slow down"}}`)
	d := f.newDriver(t, Options{})
	if _, err := d.List(context.Background(), "/"); err != nil {
		t.Fatalf("List: %v", err)
	}
	if got := f.requestsMatching("GET", "files/list"); got != 2 {
		t.Fatalf("list requests = %d, want 2", got)
	}
}

func TestServerErrorRetriesThenSucceeds(t *testing.T) {
	f := newFakeProvider(t)
	f.putFile("/flaky.bin", []byte("x"), 1)
	f.faultStatus("GET", "files/list", 2, http.StatusInternalServerError, `{"error":{"code":"Other","message":"boom"}}`)
	d := f.newDriver(t, Options{})
	if _, err := d.List(context.Background(), "/"); err != nil {
		t.Fatalf("List: %v", err)
	}
	if got := f.requestsMatching("GET", "files/list"); got != 3 {
		t.Fatalf("list requests = %d, want 3", got)
	}
}

func TestFilesNotSyncedIsNotRetried(t *testing.T) {
	f := newFakeProvider(t)
	f.faultStatus("GET", "files/list", 1, http.StatusInternalServerError, `{"error":{"code":"FilesNotSynced","message":"Files not synced"}}`)
	d := f.newDriver(t, Options{})
	_, err := d.List(context.Background(), "/")
	if err == nil || !errors.Is(err, errMountNotSynced) {
		t.Fatalf("List error = %v, want mount-not-synced", err)
	}
	if got := f.requestsMatching("GET", "files/list"); got != 1 {
		t.Fatalf("list requests = %d, want 1 (no retry)", got)
	}
}

func TestErrorCarriesRequestId(t *testing.T) {
	f := newFakeProvider(t)
	d := f.newDriver(t, Options{})
	_, err := d.List(context.Background(), "/missing")
	if err == nil || !strings.Contains(err.Error(), "requestId") {
		t.Fatalf("error = %v, want requestId", err)
	}
}

func TestMalformedJSONErrorBodyHandled(t *testing.T) {
	f := newFakeProvider(t)
	f.addFault(func(r *http.Request) bool {
		return r.Method == http.MethodGet && strings.Contains(r.URL.Path, "files/list")
	}, 3, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"code":`))
	})
	d := f.newDriver(t, Options{})
	_, err := d.List(context.Background(), "/")
	if err == nil {
		t.Fatalf("List error = nil, want failure")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Fatalf("error = %v, want status 500", err)
	}
}

func TestRequestDeadlineSurfaces(t *testing.T) {
	f := newFakeProvider(t)
	f.mu.Lock()
	f.delay = 300 * time.Millisecond
	f.mu.Unlock()
	d := f.newDriver(t, Options{})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := d.List(ctx, "/")
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("List error = %v, want deadline exceeded", err)
	}
}

// --- Observability -----------------------------------------------------------

func TestDebugSnapshotRedacted(t *testing.T) {
	f := newFakeProvider(t)
	d := f.newDriver(t, Options{})
	_, _ = d.List(context.Background(), "/missing") // record a failure
	snapshot, err := d.DebugSnapshot(context.Background())
	if err != nil {
		t.Fatalf("DebugSnapshot: %v", err)
	}
	if snapshot.Driver != "koofr" || snapshot.Health != drive.HealthLevelDegraded {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	encoded, _ := json.Marshal(snapshot)
	if strings.Contains(string(encoded), f.authPassword) || strings.Contains(string(encoded), f.authEmail) {
		t.Fatalf("snapshot leaks credentials: %s", encoded)
	}
	if snapshot.Stats["mount_id"] != "primary-mount" {
		t.Fatalf("stats = %+v", snapshot.Stats)
	}
}

func TestMetricsNormalized(t *testing.T) {
	f := newFakeProvider(t)
	f.putFile("/metric.bin", []byte("x"), 1)
	d := f.newDriver(t, Options{})
	if _, err := d.List(context.Background(), "/"); err != nil {
		t.Fatalf("List: %v", err)
	}
	events, err := d.Metrics(context.Background(), time.Now().Add(-time.Minute))
	if err != nil {
		t.Fatalf("Metrics: %v", err)
	}
	if len(events) == 0 {
		t.Fatalf("no metric events")
	}
	for _, event := range events {
		if event.Driver != "koofr" {
			t.Fatalf("event driver = %q", event.Driver)
		}
		if event.URL != "" {
			t.Fatalf("event url not masked: %q", event.URL)
		}
	}
}

func TestForeignEntriesUnsupported(t *testing.T) {
	f := newFakeProvider(t)
	d := f.newDriver(t, Options{})
	if _, err := d.ForeignEntries(context.Background(), "/"); err == nil || !errors.Is(err, drive.ErrUnsupported) {
		t.Fatalf("ForeignEntries error = %v, want unsupported", err)
	}
}

// --- Full scan ---------------------------------------------------------------

func TestListRecursiveStreamsNestedEntries(t *testing.T) {
	f := newFakeProvider(t)
	f.putFile("/scan/sub/deep.bin", []byte("deep"), 1)
	f.putFile("/scan/top.bin", []byte("top"), 1)
	d := f.newDriver(t, Options{})
	var seen []string
	err := d.listRecursive(context.Background(), "/scan", func(rel string, entry drive.Entry) error {
		seen = append(seen, rel+"|"+entry.ID)
		return nil
	})
	if err != nil {
		t.Fatalf("listRecursive: %v", err)
	}
	joined := strings.Join(seen, ",")
	if !strings.Contains(joined, "top.bin|/scan/top.bin") || !strings.Contains(joined, "sub/deep.bin|/scan/sub/deep.bin") {
		t.Fatalf("seen = %v", seen)
	}
}

func TestListRecursiveMalformedLineErrors(t *testing.T) {
	f := newFakeProvider(t)
	f.mkdirAll("/scan")
	f.addFault(func(r *http.Request) bool {
		return strings.Contains(r.URL.Path, "listrecursive")
	}, 1, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("this is not ndjson at all {{{\n"))
	})
	d := f.newDriver(t, Options{})
	err := d.listRecursive(context.Background(), "/scan", func(string, drive.Entry) error { return nil })
	if err == nil {
		t.Fatalf("listRecursive error = nil, want decode failure")
	}
}
