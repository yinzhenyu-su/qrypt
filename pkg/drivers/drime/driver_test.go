package drime

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"strconv"
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

func TestInitSpaceAndRootResolution(t *testing.T) {
	f := newFakeProvider(t)
	docs := f.addEntry(0, "docs", true, 0, "", "")
	sub := f.addEntry(docs.ID, "sub", true, 0, "", "")
	f.addEntry(sub.ID, "file.bin", false, 3, "", "")

	d := f.newDriver(t, Options{RootPath: "/docs/sub"})
	if err := d.Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if d.rootID != idString(sub.ID) {
		t.Fatalf("rootID = %q, want %q", d.rootID, idString(sub.ID))
	}
	entries, err := d.List(context.Background(), "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 1 || entries[0].Name != "file.bin" {
		t.Fatalf("entries = %+v", entries)
	}
	space, err := d.Space(context.Background())
	if err != nil {
		t.Fatalf("Space: %v", err)
	}
	if space.Total != 1000 || space.Free != 900 {
		t.Fatalf("space = %+v", space)
	}
	// A second Init must be idempotent.
	if err := d.Init(context.Background()); err != nil {
		t.Fatalf("second Init: %v", err)
	}
}

func TestInitMissingRootDoesNotFallback(t *testing.T) {
	f := newFakeProvider(t)
	d := f.newDriver(t, Options{RootPath: "/missing"})
	err := d.Init(context.Background())
	if err == nil || !errors.Is(err, drive.ErrNotFound) {
		t.Fatalf("Init error = %v, want not found", err)
	}
}

func TestInitRejectsWorkspaceMismatch(t *testing.T) {
	f := newFakeProvider(t)
	f.workspaceID = "0"
	docs := f.addEntry(0, "docs", true, 0, "", "")
	d := f.newDriver(t, Options{WorkspaceID: "77", RootID: idString(docs.ID)})
	err := d.Init(context.Background())
	if err == nil || !errors.Is(err, drive.ErrAuth) {
		t.Fatalf("Init error = %v, want auth", err)
	}
}

func TestInitAcceptsTeamWorkspace(t *testing.T) {
	f := newFakeProvider(t)
	f.workspaceID = "77"
	docs := f.addEntry(0, "docs", true, 0, "", "")
	d := f.newDriver(t, Options{WorkspaceID: "77", RootID: idString(docs.ID)})
	if err := d.Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}
}

func TestListPaginationAndMapping(t *testing.T) {
	f := newFakeProvider(t)
	for i := 0; i < 105; i++ {
		f.addEntry(0, "file-"+twoDigits(i)+".bin", false, int64(i), "", "")
	}
	folder := f.addEntry(0, "folder", true, 1234, "", "")
	f.addEntry(folder.ID, "child.bin", false, 10, "", "")
	modified := int64(1577934245000)
	folderEntry := f.entries[folder.ID]
	folderEntry.ClientLastModified = &modified

	d := f.newDriver(t, Options{})
	if err := d.Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	entries, err := d.List(context.Background(), "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 106 {
		t.Fatalf("len(entries) = %d, want 106", len(entries))
	}
	for _, entry := range entries {
		if entry.Name == "folder" {
			if entry.Size != 0 {
				t.Fatalf("folder size = %d, want 0", entry.Size)
			}
			if !entry.ModTime.Equal(time.UnixMilli(modified).UTC()) {
				t.Fatalf("folder modtime = %v", entry.ModTime)
			}
		}
	}
	reqs := f.requestsFor("GET", "/api/v1/drive/file-entries")
	if len(reqs) == 0 {
		t.Fatal("no listing requests")
	}
	last := reqs[len(reqs)-1]
	if last.query.Get("orderBy") != "name" || last.query.Get("orderDir") != "asc" || last.query.Get("perPage") != "100" {
		t.Fatalf("query = %v", last.query)
	}

	// An empty/short page terminates without asking for another page.
	subEntries, err := d.List(context.Background(), idString(folder.ID))
	if err != nil {
		t.Fatalf("List folder: %v", err)
	}
	if len(subEntries) != 1 {
		t.Fatalf("folder children = %d", len(subEntries))
	}

	// A deleted folder must be not found, never a root fallback.
	delete(f.entries, folder.ID)
	if _, err := d.List(context.Background(), idString(folder.ID)); !errors.Is(err, drive.ErrNotFound) {
		t.Fatalf("deleted folder error = %v, want not found", err)
	}
}

func TestListRepeatedPageFails(t *testing.T) {
	f := newFakeProvider(t)
	for i := 0; i < 105; i++ {
		f.addEntry(0, "file-"+twoDigits(i)+".bin", false, 1, "", "")
	}
	f.repeatPage = true
	d := f.newDriver(t, Options{})
	if err := d.Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if _, err := d.List(context.Background(), ""); err == nil {
		t.Fatal("repeated page did not fail")
	}
}

func TestListWrongWorkspaceReturnsEmpty(t *testing.T) {
	f := newFakeProvider(t)
	f.workspaceID = "0"
	folder := f.addEntry(0, "docs", true, 0, "", "")
	f.addEntry(folder.ID, "file.bin", false, 1, "", "")
	d := f.newDriver(t, Options{WorkspaceID: "77"})
	entries, err := d.List(context.Background(), idString(folder.ID))
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("wrong workspace returned %d entries", len(entries))
	}
}

func TestReadRangeRedirectAndIgnoredRange(t *testing.T) {
	f := newFakeProvider(t)
	entry := f.addEntry(0, "file.bin", false, 100, "", "")
	f.content[idString(entry.ID)] = []byte("0123456789abcdefghijklmnopqrstuvwxyz0123456789abcdefghijklmnopqrstuvwxyz0123456789abcdefghijklmnopqrstuvwxyz")
	d := f.newDriver(t, Options{})
	if err := d.Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	mapped := entry.entry("")

	read := func(offset, size int64) string {
		t.Helper()
		rc, err := d.Read(context.Background(), mapped, offset, size)
		if err != nil {
			t.Fatalf("Read(%d,%d): %v", offset, size, err)
		}
		defer rc.Close()
		data, err := io.ReadAll(rc)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		return string(data)
	}
	if got := read(10, 10); got != "abcdefghij" {
		t.Fatalf("range = %q", got)
	}
	// A read at/past the known size returns EOF without a network request.
	before := len(f.requests)
	if got := read(100, 10); got != "" {
		t.Fatalf("past EOF = %q", got)
	}
	if len(f.requests) != before {
		t.Fatalf("past EOF issued %d requests", len(f.requests)-before)
	}
	f.redirect = true
	if got := read(10, 10); got != "abcdefghij" {
		t.Fatalf("redirect range = %q", got)
	}
	f.ignoreRange = true
	if got := read(10, 10); got != "abcdefghij" {
		t.Fatalf("ignored range = %q", got)
	}
}

func TestMkdirRenameMoveRemoveCopy(t *testing.T) {
	f := newFakeProvider(t)
	d := f.newDriver(t, Options{})
	if err := d.Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	ctx := context.Background()
	dir, err := d.Mkdir(ctx, "", "docs")
	if err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	if again, err := d.Mkdir(ctx, "", "docs"); err != nil || again.ID != dir.ID {
		t.Fatalf("existing Mkdir = %+v, %v", again, err)
	}
	if _, err := d.Mkdir(ctx, "", "docs/file"); !errors.Is(err, drive.ErrInvalidInput) {
		t.Fatalf("invalid mkdir error = %v", err)
	}

	dirID, _ := strconv.ParseInt(dir.ID, 10, 64)
	file := f.addEntry(dirID, "a.bin", false, 4, "", "")
	mapped := file.entry(dir.ID)
	renamed, err := d.Rename(ctx, mapped, "b.bin")
	if err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if renamed.Name != "b.bin" {
		t.Fatalf("renamed = %q", renamed.Name)
	}
	reqs := f.requestsFor("PUT", "/api/v1/file-entries/"+idString(file.ID))
	if len(reqs) == 0 {
		t.Fatal("rename did not use literal PUT")
	}

	other, err := d.Mkdir(ctx, "", "other")
	if err != nil {
		t.Fatalf("Mkdir other: %v", err)
	}
	moved, err := d.Move(ctx, mapped, other.ID)
	if err != nil {
		t.Fatalf("Move: %v", err)
	}
	if moved.ParentID != other.ID {
		t.Fatalf("moved parent = %q", moved.ParentID)
	}

	// Directory move and the bounded settle wait.
	child := f.addEntry(dirID, "child.bin", false, 2, "", "")
	_ = child
	dirMapped := f.entries[dirID].entry("")
	if _, err := d.Move(ctx, dirMapped, other.ID); err != nil {
		t.Fatalf("Move dir: %v", err)
	}
	f.childDelay[dirID] = 1
	d.waitForChildren(ctx, idString(dirID), 1)

	// Copy collision: provider names it "a (1).bin"; the driver renames it back.
	otherID, _ := strconv.ParseInt(other.ID, 10, 64)
	copySrc := f.addEntry(otherID, "copy.bin", false, 7, "", "aabb")
	copied, err := d.Copy(ctx, f.entries[copySrc.ID].entry(other.ID), other.ID, "copy.bin")
	if err != nil {
		t.Fatalf("Copy: %v", err)
	}
	if copied.Name != "copy.bin" {
		t.Fatalf("copied name = %q", copied.Name)
	}

	if err := d.Remove(ctx, f.entries[file.ID].entry(other.ID)); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if err := d.Remove(ctx, mapped); !errors.Is(err, drive.ErrNotFound) {
		t.Fatalf("Remove missing error = %v", err)
	}
}

func TestResolvePathAndNameValidation(t *testing.T) {
	f := newFakeProvider(t)
	docs := f.addEntry(0, "docs", true, 0, "", "")
	sub := f.addEntry(docs.ID, "sub", true, 0, "", "")
	f.addEntry(sub.ID, "file.bin", false, 1, "", "")
	d := f.newDriver(t, Options{})
	if err := d.Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	id, err := d.ResolvePath(context.Background(), "/docs/sub/file.bin")
	_ = id
	if err == nil {
		t.Fatal("ResolvePath to a file should fail")
	}
	if got, err := d.ResolvePath(context.Background(), "/docs/sub"); err != nil || got != idString(sub.ID) {
		t.Fatalf("ResolvePath = %q, %v", got, err)
	}
	if _, err := d.ResolvePath(context.Background(), "../etc"); !errors.Is(err, drive.ErrInvalidInput) {
		t.Fatalf("escape error = %v", err)
	}
	if err := validateName(strings.Repeat("a", 256)); !errors.Is(err, drive.ErrInvalidInput) {
		t.Fatalf("256-byte name error = %v", err)
	}
	if err := validateName(strings.Repeat("a", 255)); err != nil {
		t.Fatalf("255-byte name error = %v", err)
	}
	for _, name := range []string{" leading.bin", "trailing.bin ", `back\slash.bin`} {
		if err := validateName(name); err != nil {
			t.Fatalf("name %q error = %v", name, err)
		}
		remote, err := encodeRemoteName(name)
		if err != nil || decodeRemoteName(remote) != name {
			t.Fatalf("name %q round-trip = %q, %v", name, remote, err)
		}
		if !strings.HasPrefix(remote, remoteNamePrefix) {
			t.Fatalf("name %q was not escaped: %q", name, remote)
		}
	}
}

func TestUploadSmallAndProgress(t *testing.T) {
	f := newFakeProvider(t)
	d := f.newDriver(t, Options{})
	if err := d.Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	data := bytes.Repeat([]byte("x"), 1024)
	progress := &progressRecorder{}
	entry, err := d.PutSource(context.Background(), drive.UploadRequest{
		ParentID: "",
		Name:     "small.bin",
		Source:   drive.NewBytesReadOnlyFileSource(data),
		ModTime:  time.UnixMilli(1609459200000),
		Progress: progress,
	})
	if err != nil {
		t.Fatalf("PutSource: %v", err)
	}
	if entry.Size != int64(len(data)) || entry.Name != "small.bin" {
		t.Fatalf("entry = %+v", entry)
	}
	if progress.uploaded != int64(len(data)) {
		t.Fatalf("uploaded = %d", progress.uploaded)
	}
	if !progress.has(drive.UploadPhaseUploading) || !progress.has(drive.UploadPhaseCompleted) {
		t.Fatalf("phases = %v", progress.phases)
	}
	if entryFileHash(entry) == "" {
		t.Fatal("upload did not persist a hash")
	}
	algo, hash, err := d.RemoteHash(context.Background(), entry)
	if err != nil || algo != drive.HashSHA256 || len(hash) != 64 {
		t.Fatalf("RemoteHash = %v %q %v", algo, hash, err)
	}
	md5Entry := entry
	md5Entry.Extra = map[string]any{"file_hash": strings.Repeat("a", 32)}
	if _, _, err := d.RemoteHash(context.Background(), md5Entry); !errors.Is(err, drive.ErrUnsupported) {
		t.Fatalf("MD5 RemoteHash error = %v", err)
	}
}

func TestUploadLargeResumeAndStaleHandle(t *testing.T) {
	f := newFakeProvider(t)
	d := f.newDriver(t, Options{ChunkSize: minChunkSize, UploadConcurrency: 1})
	if err := d.Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	data := bytes.Repeat([]byte("z"), int(6*1024*1024))
	source := drive.NewBytesReadOnlyFileSource(data)
	progress := &progressRecorder{}
	req := drive.UploadRequest{Name: "large.bin", Source: source, Progress: progress}

	f.failPart = 2
	if _, err := d.PutSource(context.Background(), req); err == nil {
		t.Fatal("expected the interrupted upload to fail")
	}
	f.failPart = 0
	entry, err := d.PutSource(context.Background(), req)
	if err != nil {
		t.Fatalf("resumed PutSource: %v", err)
	}
	if entry.Size != int64(len(data)) {
		t.Fatalf("size = %d", entry.Size)
	}
	if progress.uploaded != int64(len(data)) {
		t.Fatalf("uploaded = %d, want %d", progress.uploaded, len(data))
	}
	// Part 1 was already accepted before the interruption, so the resume must
	// not upload it again.
	var part1, part2 int
	for _, request := range f.requests {
		if request.method != "PUT" {
			continue
		}
		if strings.HasSuffix(request.path, "/1") {
			part1++
		}
		if strings.HasSuffix(request.path, "/2") {
			part2++
		}
	}
	if part1 != 1 || part2 != 2 {
		t.Fatalf("part PUTs = %d/%d, want 1/2", part1, part2)
	}

	// A dead handle restarts once from a fresh provider upload.
	for id := range f.uploads {
		delete(f.uploads, id)
	}
	if _, err := d.PutSource(context.Background(), req); err != nil {
		t.Fatalf("stale handle restart: %v", err)
	}
}

func TestUploadRejectsLongNameWithoutRequest(t *testing.T) {
	f := newFakeProvider(t)
	d := f.newDriver(t, Options{})
	before := len(f.requests)
	_, err := d.PutSource(context.Background(), drive.UploadRequest{
		Name:   strings.Repeat("a", 256),
		Source: drive.NewBytesReadOnlyFileSource([]byte("x")),
	})
	if !errors.Is(err, drive.ErrInvalidInput) {
		t.Fatalf("error = %v", err)
	}
	if len(f.requests) != before {
		t.Fatal("long name issued a request")
	}
}

func twoDigits(n int) string { return fmt.Sprintf("%03d", n) }

func idString(id int64) string { return strconv.FormatInt(id, 10) }

func TestSpaceUnsupported(t *testing.T) {
	f := newFakeProvider(t)
	f.forceStatus["/api/v1/user/space-usage"] = 404
	d := f.newDriver(t, Options{})
	if _, err := d.Space(context.Background()); !errors.Is(err, drive.ErrSpaceUnsupported) {
		t.Fatalf("Space error = %v, want unsupported", err)
	}
}

func TestListPageCapFails(t *testing.T) {
	f := newFakeProvider(t)
	f.neverEnding = true
	d := f.newDriver(t, Options{})
	if err := d.Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if _, err := d.List(context.Background(), ""); err == nil {
		t.Fatal("never-ending listing did not fail")
	}
}

func TestMkdirFileConflictAndZeroLengthUpload(t *testing.T) {
	f := newFakeProvider(t)
	f.addEntry(0, "docs", false, 1, "", "")
	d := f.newDriver(t, Options{})
	if err := d.Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if _, err := d.Mkdir(context.Background(), "", "docs"); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("file conflict error = %v", err)
	}
	progress := &progressRecorder{}
	entry, err := d.PutSource(context.Background(), drive.UploadRequest{
		Name:     "empty.bin",
		Source:   drive.NewBytesReadOnlyFileSource(nil),
		Progress: progress,
	})
	if err != nil {
		t.Fatalf("zero-length PutSource: %v", err)
	}
	if entry.Size != 0 {
		t.Fatalf("zero-length size = %d", entry.Size)
	}
	if got := d.RequiredUploadHashes(); len(got) != 1 || got[0] != drive.HashSHA256 {
		t.Fatalf("RequiredUploadHashes = %v", got)
	}
	for _, request := range f.requests {
		if strings.Contains(request.path, "/hash/check") {
			t.Fatal("driver performed an instant-upload hash check")
		}
	}
}

func TestReadHasNoMetadataCall(t *testing.T) {
	f := newFakeProvider(t)
	entry := f.addEntry(0, "file.bin", false, 4, "", "")
	f.content[idString(entry.ID)] = []byte("data")
	d := f.newDriver(t, Options{})
	if err := d.Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	before := len(f.requests)
	rc, err := d.Read(context.Background(), entry.entry(""), 0, 4)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if _, err := io.ReadAll(rc); err != nil {
		t.Fatalf("read body: %v", err)
	}
	rc.Close()
	for _, request := range f.requests[before:] {
		if strings.HasSuffix(request.path, "/model") {
			t.Fatalf("Read fetched metadata: %s", request.path)
		}
	}
}

func TestClientErrorMappingAndLimiter(t *testing.T) {
	f := newFakeProvider(t)
	d := f.newDriver(t, Options{})
	if err := d.Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	cases := []struct {
		status int
		want   error
	}{
		{401, drive.ErrAuth},
		{404, drive.ErrNotFound},
		{429, drive.ErrRateLimit},
		{422, drive.ErrInvalidInput},
	}
	for _, tc := range cases {
		f.forceStatus["/api/v1/drive/file-entries"] = tc.status
		if _, err := d.List(context.Background(), ""); !errors.Is(err, tc.want) {
			t.Fatalf("status %d error = %v, want %v", tc.status, err, tc.want)
		}
	}
	delete(f.forceStatus, "/api/v1/drive/file-entries")

	// Retryable 5xx is retried; collapse the backoff for the test.
	oldWait := retryWait
	retryWait = func(context.Context, int) error { return nil }
	defer func() { retryWait = oldWait }()
	f.forceStatus["/api/v1/drive/file-entries"] = 500
	before := len(f.requestsFor("GET", "/api/v1/drive/file-entries"))
	if _, err := d.List(context.Background(), ""); err == nil {
		t.Fatal("5xx succeeded")
	}
	after := len(f.requestsFor("GET", "/api/v1/drive/file-entries"))
	if after-before < 3 {
		t.Fatalf("retry count = %d, want 3", after-before)
	}
	delete(f.forceStatus, "/api/v1/drive/file-entries")

	// A 422 upload is non-retryable.
	f.forceStatus["/api/v1/uploads"] = 422
	_, err := d.PutSource(context.Background(), drive.UploadRequest{Name: "x.bin", Source: drive.NewBytesReadOnlyFileSource([]byte("x"))})
	if err == nil || !drive.IsNonRetryable(err) {
		t.Fatalf("422 upload error = %v, want non-retryable", err)
	}
	delete(f.forceStatus, "/api/v1/uploads")

	limiter := drive.NewBandwidthLimiter(drive.BandwidthLimits{DownloadBytesPerSecond: 1 << 20, UploadBytesPerSecond: 1 << 20})
	if directions := d.InstallBandwidthLimiter(limiter); directions != drive.BandwidthLimitDownload|drive.BandwidthLimitUpload {
		t.Fatalf("directions = %v", directions)
	}
	if _, err := d.PutSource(context.Background(), drive.UploadRequest{Name: "limited.bin", Source: drive.NewBytesReadOnlyFileSource([]byte("limited"))}); err != nil {
		t.Fatalf("upload with limiter: %v", err)
	}
}

func TestInstantUpload(t *testing.T) {
	f := newFakeProvider(t)
	d := f.newDriver(t, Options{InstantUpload: true})
	if err := d.Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	data := []byte("instant-content")
	if _, err := d.PutSource(context.Background(), drive.UploadRequest{Name: "first.bin", Source: drive.NewBytesReadOnlyFileSource(data)}); err != nil {
		t.Fatalf("first upload: %v", err)
	}
	progress := &progressRecorder{}
	entry, err := d.PutSource(context.Background(), drive.UploadRequest{Name: "second.bin", Source: drive.NewBytesReadOnlyFileSource(data), Progress: progress})
	if err != nil {
		t.Fatalf("instant upload: %v", err)
	}
	if entry.Name != "second.bin" {
		t.Fatalf("instant name = %q", entry.Name)
	}
	if !progress.has(drive.UploadPhaseInstant) {
		t.Fatalf("phases = %v", progress.phases)
	}
	snapshot, _ := d.DebugSnapshot(context.Background())
	if got := snapshot.Extra[drive.DebugExtraInstantUploadCount]; got != int64(1) {
		t.Fatalf("instant count = %v", got)
	}
	var uploads int
	for _, request := range f.requests {
		if request.path == "/api/v1/uploads" {
			uploads++
		}
	}
	if uploads != 1 {
		t.Fatalf("uploads = %d, want 1 (second should be instant)", uploads)
	}
}

func TestPresignedSmallUpload(t *testing.T) {
	f := newFakeProvider(t)
	d := f.newDriver(t, Options{PresignSmall: true})
	if err := d.Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	entry, err := d.PutSource(context.Background(), drive.UploadRequest{Name: "presigned.bin", Source: drive.NewBytesReadOnlyFileSource([]byte("presigned"))})
	if err != nil {
		t.Fatalf("PutSource: %v", err)
	}
	if entry.Name != "presigned.bin" || entry.Size != 9 {
		t.Fatalf("entry = %+v", entry)
	}
	if len(f.requestsFor("POST", "/api/v1/s3/simple/presign")) == 0 {
		t.Fatal("presign endpoint not called")
	}
	if len(f.requestsFor("POST", "/api/v1/uploads")) != 0 {
		t.Fatal("presigned mode used /uploads")
	}
}

func TestUploadLargeConcurrent(t *testing.T) {
	f := newFakeProvider(t)
	d := f.newDriver(t, Options{ChunkSize: minChunkSize, UploadConcurrency: 4})
	if err := d.Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	data := bytes.Repeat([]byte("c"), int(11*1024*1024))
	entry, err := d.PutSource(context.Background(), drive.UploadRequest{Name: "concurrent.bin", Source: drive.NewBytesReadOnlyFileSource(data)})
	if err != nil {
		t.Fatalf("PutSource: %v", err)
	}
	if entry.Size != int64(len(data)) {
		t.Fatalf("size = %d", entry.Size)
	}
	var puts int
	for _, request := range f.requests {
		if request.method == "PUT" && strings.HasPrefix(request.path, "/part/") {
			puts++
		}
	}
	if puts != 3 {
		t.Fatalf("part PUTs = %d, want 3", puts)
	}
}

func TestParseRetryAfter(t *testing.T) {
	if got := parseRetryAfter("2"); got != 2*time.Second {
		t.Fatalf("seconds = %v", got)
	}
	if got := parseRetryAfter("0"); got != 0 {
		t.Fatalf("zero = %v", got)
	}
	if got := parseRetryAfter("garbage"); got != 0 {
		t.Fatalf("garbage = %v", got)
	}
	future := time.Now().Add(2 * time.Second).UTC().Format(http.TimeFormat)
	if got := parseRetryAfter(future); got <= 0 || got > 3*time.Second {
		t.Fatalf("http date = %v", got)
	}
}

func TestEncodedNamesRoundTripThroughProvider(t *testing.T) {
	f := newFakeProvider(t)
	d := f.newDriver(t, Options{})
	if err := d.Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	plain := " leading\\name.bin "
	entry, err := d.PutSource(context.Background(), drive.UploadRequest{Name: plain, Source: drive.NewBytesReadOnlyFileSource([]byte("x"))})
	if err != nil {
		t.Fatalf("PutSource: %v", err)
	}
	if entry.Name != plain {
		t.Fatalf("plain name = %q", entry.Name)
	}
	if remote, ok := drive.EntryRemoteName(entry); !ok || !strings.HasPrefix(remote, remoteNamePrefix) {
		t.Fatalf("remote name = %q, %v", remote, ok)
	}
	listed, err := d.List(context.Background(), "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	found := false
	for _, candidate := range listed {
		if candidate.Name == plain {
			found = true
			if remote, ok := drive.EntryRemoteName(candidate); !ok || !strings.HasPrefix(remote, remoteNamePrefix) {
				t.Fatalf("listed remote name = %q, %v", remote, ok)
			}
		}
	}
	if !found {
		t.Fatalf("plain name not listed: %+v", listed)
	}
}

func TestReadErrorPaths(t *testing.T) {
	f := newFakeProvider(t)
	entry := f.addEntry(0, "file.bin", false, 10, "", "")
	f.content[idString(entry.ID)] = []byte("0123456789")
	d := f.newDriver(t, Options{})
	if err := d.Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	f.force416 = true
	if _, err := d.Read(context.Background(), entry.entry(""), 0, 4); !errors.Is(err, drive.ErrInvalidInput) {
		t.Fatalf("416 error = %v", err)
	}
	f.force416 = false
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := d.Read(ctx, entry.entry(""), 0, 4); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled read error = %v", err)
	}
}

func TestMalformedJSONAndRetryAfter(t *testing.T) {
	f := newFakeProvider(t)
	f.malformed["/api/v1/drive/file-entries"] = true
	d := f.newDriver(t, Options{})
	if err := d.Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if _, err := d.List(context.Background(), ""); err == nil {
		t.Fatal("malformed JSON accepted")
	}
	delete(f.malformed, "/api/v1/drive/file-entries")

	oldWait := retryWait
	retryWait = func(context.Context, int) error { return nil }
	defer func() { retryWait = oldWait }()
	f.retryOnce = true
	if _, err := d.List(context.Background(), ""); err != nil {
		t.Fatalf("429 retry: %v", err)
	}
	if len(f.requestsFor("GET", "/api/v1/drive/file-entries")) < 2 {
		t.Fatal("429 was not retried")
	}
}

func TestInstantUploadFallsBackWhenVerificationFails(t *testing.T) {
	f := newFakeProvider(t)
	d := f.newDriver(t, Options{InstantUpload: true})
	if err := d.Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	data := []byte("verify-false")
	if _, err := d.PutSource(context.Background(), drive.UploadRequest{Name: "one.bin", Source: drive.NewBytesReadOnlyFileSource(data)}); err != nil {
		t.Fatalf("first upload: %v", err)
	}
	f.verifyFalse = true
	if _, err := d.PutSource(context.Background(), drive.UploadRequest{Name: "two.bin", Source: drive.NewBytesReadOnlyFileSource(data)}); err != nil {
		t.Fatalf("fallback upload: %v", err)
	}
	var uploads int
	for _, request := range f.requests {
		if request.path == "/api/v1/uploads" {
			uploads++
		}
	}
	if uploads != 2 {
		t.Fatalf("uploads = %d, want 2 after failed verification", uploads)
	}
}

func TestRateLimiterConstruction(t *testing.T) {
	if newRateLimiter(0) != nil {
		t.Fatal("zero rate should disable the limiter")
	}
	if newRateLimiter(2) == nil {
		t.Fatal("positive rate should create a limiter")
	}
}
