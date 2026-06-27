package vfs_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/internal/driver/localfs"
	"github.com/yinzhenyu/qrypt/pkg/crypt"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

const testReadChunkSize = 512 * 1024
const testUploadDelay = 10 * time.Millisecond

type countingReadDriver struct {
	data    []byte
	mu      sync.Mutex
	read    map[int64]int
	block   map[int64]chan struct{}
	entered map[int64]chan struct{}
}

type countingListDriver struct {
	mu    sync.Mutex
	lists map[string]int
}

type countingUploadDriver struct {
	mu          sync.Mutex
	uploads     int
	last        []byte
	entries     map[string]drive.Entry
	removed     []string
	blockReturn chan struct{}
	entered     chan struct{}
}

type blockingUploadDriver struct {
	mu      sync.Mutex
	uploads int
	entries map[string]drive.Entry
	entered chan struct{}
	release chan struct{}
}

type countingRemoveDriver struct {
	mu      sync.Mutex
	entries map[string]drive.Entry
	removed []string
	mkdirs  []string
}

type staleMkdirListDriver struct {
	mu           sync.Mutex
	failFirstPut bool
	putAttempts  int
	lastParent   string
	lastName     string
	lastData     []byte
}

type staleMoveListDriver struct {
	renamed   []string
	moved     []string
	converged bool
}

type existingMkdirDriver struct {
	mkdirs int
	lists  int
}

type fileUploadDriver struct {
	mu           sync.Mutex
	entries      map[string]drive.Entry
	putCalls     int
	putFileCalls int
	lastPath     string
	lastData     []byte
}

func newCountingReadDriver(data []byte) *countingReadDriver {
	return &countingReadDriver{data: data, read: map[int64]int{}, block: map[int64]chan struct{}{}, entered: map[int64]chan struct{}{}}
}

func newCountingRemoveDriver() *countingRemoveDriver {
	return &countingRemoveDriver{entries: map[string]drive.Entry{
		"dir": {ID: "dir", ParentID: "0", Name: "dir", IsDir: true},
		"a":   {ID: "a", ParentID: "dir", Name: "a.txt"},
		"sub": {ID: "sub", ParentID: "dir", Name: "sub", IsDir: true},
		"b":   {ID: "b", ParentID: "sub", Name: "b.txt"},
	}}
}

func (d *countingReadDriver) Init(context.Context) error { return nil }
func (d *countingReadDriver) Drop(context.Context) error { return nil }

func (d *countingReadDriver) List(context.Context, string) ([]drive.Entry, error) {
	return []drive.Entry{{
		ID:       "file",
		ParentID: "0",
		Name:     "data.bin",
		Size:     int64(len(d.data)),
	}}, nil
}

func (d *countingReadDriver) Read(_ context.Context, _ drive.Entry, offset, size int64) (io.ReadCloser, error) {
	d.mu.Lock()
	d.read[offset]++
	entered := d.entered[offset]
	block := d.block[offset]
	d.mu.Unlock()
	if entered != nil {
		select {
		case <-entered:
		default:
			close(entered)
		}
	}
	if block != nil {
		<-block
	}
	if offset >= int64(len(d.data)) {
		return io.NopCloser(bytes.NewReader(nil)), nil
	}
	end := offset + size
	if size <= 0 || end > int64(len(d.data)) {
		end = int64(len(d.data))
	}
	return io.NopCloser(bytes.NewReader(d.data[offset:end])), nil
}

func (d *countingReadDriver) readCount(offset int64) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.read[offset]
}

func (d *countingReadDriver) blockRead(offset int64) (entered chan struct{}, release func()) {
	d.mu.Lock()
	defer d.mu.Unlock()
	block := make(chan struct{})
	entered = make(chan struct{})
	d.block[offset] = block
	d.entered[offset] = entered
	return entered, func() { close(block) }
}

func (d *countingListDriver) Init(context.Context) error { return nil }
func (d *countingListDriver) Drop(context.Context) error { return nil }
func (d *countingListDriver) Read(context.Context, drive.Entry, int64, int64) (io.ReadCloser, error) {
	return nil, io.EOF
}

func (d *countingListDriver) List(_ context.Context, parentID string) ([]drive.Entry, error) {
	d.mu.Lock()
	d.lists[parentID]++
	d.mu.Unlock()
	return []drive.Entry{{
		ID:       "child",
		ParentID: parentID,
		Name:     "child.txt",
		Size:     1,
	}}, nil
}

func (d *countingListDriver) listCount(parentID string) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.lists[parentID]
}

func (d *countingUploadDriver) Init(context.Context) error { return nil }
func (d *countingUploadDriver) Drop(context.Context) error { return nil }
func (d *countingUploadDriver) List(_ context.Context, parentID string) ([]drive.Entry, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	var entries []drive.Entry
	for _, entry := range d.entries {
		if entry.ParentID == parentID {
			entries = append(entries, entry)
		}
	}
	return entries, nil
}
func (d *countingUploadDriver) Read(context.Context, drive.Entry, int64, int64) (io.ReadCloser, error) {
	return nil, io.EOF
}
func (d *countingUploadDriver) Put(_ context.Context, parentID, name string, size int64, body io.Reader) (drive.Entry, error) {
	data, err := io.ReadAll(body)
	if err != nil {
		return drive.Entry{}, err
	}
	d.mu.Lock()
	d.uploads++
	d.last = append(d.last[:0], data...)
	uploadID := name + "-" + strconv.Itoa(d.uploads)
	if d.entries == nil {
		d.entries = map[string]drive.Entry{}
	}
	d.entries[uploadID] = drive.Entry{ID: uploadID, ParentID: parentID, Name: name, Size: size}
	block := d.blockReturn
	entered := d.entered
	d.blockReturn = nil
	d.entered = nil
	d.mu.Unlock()
	if entered != nil {
		close(entered)
	}
	if block != nil {
		<-block
	}
	return drive.Entry{ID: uploadID, ParentID: parentID, Name: name, Size: size}, nil
}
func (d *countingUploadDriver) uploadCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.uploads
}
func (d *countingUploadDriver) lastUpload() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return string(d.last)
}
func (d *countingUploadDriver) removedIDs() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.removed...)
}
func (d *countingUploadDriver) Mkdir(context.Context, string, string) (drive.Entry, error) {
	return drive.Entry{}, errors.New("mkdir should not be called")
}
func (d *countingUploadDriver) Move(context.Context, drive.Entry, string) error { return nil }
func (d *countingUploadDriver) Rename(context.Context, drive.Entry, string) error {
	return nil
}
func (d *countingUploadDriver) Remove(_ context.Context, entry drive.Entry) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.entries, entry.ID)
	d.removed = append(d.removed, entry.ID)
	return nil
}

func newBlockingUploadDriver() *blockingUploadDriver {
	return &blockingUploadDriver{
		entries: map[string]drive.Entry{},
		entered: make(chan struct{}, 16),
		release: make(chan struct{}),
	}
}

func (d *blockingUploadDriver) Init(context.Context) error { return nil }
func (d *blockingUploadDriver) Drop(context.Context) error { return nil }
func (d *blockingUploadDriver) List(_ context.Context, parentID string) ([]drive.Entry, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	var entries []drive.Entry
	for _, entry := range d.entries {
		if entry.ParentID == parentID {
			entries = append(entries, entry)
		}
	}
	return entries, nil
}
func (d *blockingUploadDriver) Read(context.Context, drive.Entry, int64, int64) (io.ReadCloser, error) {
	return nil, io.EOF
}
func (d *blockingUploadDriver) Mkdir(context.Context, string, string) (drive.Entry, error) {
	return drive.Entry{}, errors.New("mkdir should not be called")
}
func (d *blockingUploadDriver) Move(context.Context, drive.Entry, string) error { return nil }
func (d *blockingUploadDriver) Rename(context.Context, drive.Entry, string) error {
	return nil
}
func (d *blockingUploadDriver) Remove(_ context.Context, entry drive.Entry) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.entries, entry.ID)
	return nil
}
func (d *blockingUploadDriver) Put(_ context.Context, parentID, name string, size int64, body io.Reader) (drive.Entry, error) {
	if _, err := io.Copy(io.Discard, body); err != nil {
		return drive.Entry{}, err
	}
	d.mu.Lock()
	d.uploads++
	id := name + "-" + strconv.Itoa(d.uploads)
	entry := drive.Entry{ID: id, ParentID: parentID, Name: name, Size: size}
	d.entries[id] = entry
	d.mu.Unlock()
	d.entered <- struct{}{}
	<-d.release
	return entry, nil
}

func (d *countingRemoveDriver) Init(context.Context) error { return nil }
func (d *countingRemoveDriver) Drop(context.Context) error { return nil }
func (d *countingRemoveDriver) Read(context.Context, drive.Entry, int64, int64) (io.ReadCloser, error) {
	return nil, io.EOF
}
func (d *countingRemoveDriver) List(_ context.Context, parentID string) ([]drive.Entry, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	var entries []drive.Entry
	for _, entry := range d.entries {
		if entry.ParentID == parentID {
			entries = append(entries, entry)
		}
	}
	return entries, nil
}
func (d *countingRemoveDriver) Mkdir(context.Context, string, string) (drive.Entry, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.mkdirs = append(d.mkdirs, "")
	return drive.Entry{}, errors.New("mkdir should not be called")
}
func (d *countingRemoveDriver) mkdirCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.mkdirs)
}
func (d *countingRemoveDriver) Move(context.Context, drive.Entry, string) error { return nil }
func (d *countingRemoveDriver) Rename(context.Context, drive.Entry, string) error {
	return nil
}
func (d *countingRemoveDriver) Remove(_ context.Context, entry drive.Entry) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.removed = append(d.removed, entry.ID)
	for id, candidate := range d.entries {
		if id == entry.ID || isEntryUnder(candidate, entry.ID, d.entries) {
			delete(d.entries, id)
		}
	}
	return nil
}
func (d *countingRemoveDriver) removedIDs() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.removed...)
}

func isEntryUnder(entry drive.Entry, parentID string, entries map[string]drive.Entry) bool {
	for entry.ParentID != "" && entry.ParentID != "0" {
		if entry.ParentID == parentID {
			return true
		}
		parent, ok := entries[entry.ParentID]
		if !ok {
			return false
		}
		entry = parent
	}
	return false
}

func (d *staleMkdirListDriver) Init(context.Context) error { return nil }
func (d *staleMkdirListDriver) Drop(context.Context) error { return nil }
func (d *staleMkdirListDriver) Read(context.Context, drive.Entry, int64, int64) (io.ReadCloser, error) {
	return nil, io.EOF
}
func (d *staleMkdirListDriver) List(context.Context, string) ([]drive.Entry, error) {
	return nil, nil
}
func (d *staleMkdirListDriver) Mkdir(_ context.Context, parentID, name string) (drive.Entry, error) {
	return drive.Entry{ID: "dir-id", ParentID: parentID, Name: name, IsDir: true}, nil
}
func (d *staleMkdirListDriver) Move(context.Context, drive.Entry, string) error { return nil }
func (d *staleMkdirListDriver) Rename(context.Context, drive.Entry, string) error {
	return nil
}
func (d *staleMkdirListDriver) Remove(context.Context, drive.Entry) error { return nil }
func (d *staleMkdirListDriver) Put(_ context.Context, parentID, name string, size int64, body io.Reader) (drive.Entry, error) {
	data, err := io.ReadAll(body)
	if err != nil {
		return drive.Entry{}, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.putAttempts++
	if d.failFirstPut && d.putAttempts == 1 {
		return drive.Entry{}, errors.New("parent not ready")
	}
	d.lastParent = parentID
	d.lastName = name
	d.lastData = append(d.lastData[:0], data...)
	return drive.Entry{ID: name, ParentID: parentID, Name: name, Size: size}, nil
}
func (d *staleMkdirListDriver) lastPut() (attempts int, parent, name, data string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.putAttempts, d.lastParent, d.lastName, string(d.lastData)
}

func (d *staleMoveListDriver) Init(context.Context) error { return nil }
func (d *staleMoveListDriver) Drop(context.Context) error { return nil }
func (d *staleMoveListDriver) Read(context.Context, drive.Entry, int64, int64) (io.ReadCloser, error) {
	return nil, io.EOF
}
func (d *staleMoveListDriver) List(_ context.Context, parentID string) ([]drive.Entry, error) {
	if d.converged {
		switch parentID {
		case "0":
			return []drive.Entry{{ID: "dir-id", ParentID: "0", Name: "video", IsDir: true}}, nil
		case "dir-id":
			return []drive.Entry{{ID: "movie-id", ParentID: "dir-id", Name: "movie.mp4", Size: 10}}, nil
		default:
			return nil, nil
		}
	}
	switch parentID {
	case "0":
		return []drive.Entry{
			{ID: "dir-id", ParentID: "0", Name: "新建文件夹", IsDir: true},
			{ID: "movie-id", ParentID: "0", Name: "movie.mp4", Size: 10},
		}, nil
	case "dir-id":
		return nil, nil
	default:
		return nil, nil
	}
}
func (d *staleMoveListDriver) Mkdir(context.Context, string, string) (drive.Entry, error) {
	return drive.Entry{}, nil
}
func (d *staleMoveListDriver) Rename(_ context.Context, entry drive.Entry, newName string) error {
	d.renamed = append(d.renamed, entry.ID+":"+newName)
	return nil
}
func (d *staleMoveListDriver) Move(_ context.Context, entry drive.Entry, dstParentID string) error {
	d.moved = append(d.moved, entry.ID+":"+dstParentID)
	return nil
}
func (d *staleMoveListDriver) Remove(context.Context, drive.Entry) error { return nil }

func (d *existingMkdirDriver) Init(context.Context) error { return nil }
func (d *existingMkdirDriver) Drop(context.Context) error { return nil }
func (d *existingMkdirDriver) Read(context.Context, drive.Entry, int64, int64) (io.ReadCloser, error) {
	return nil, io.EOF
}
func (d *existingMkdirDriver) List(_ context.Context, parentID string) ([]drive.Entry, error) {
	if parentID != "0" {
		return nil, nil
	}
	d.lists++
	if d.lists == 1 {
		return nil, nil
	}
	return []drive.Entry{{ID: "existing-dir", ParentID: "0", Name: "dir", IsDir: true}}, nil
}
func (d *existingMkdirDriver) Mkdir(context.Context, string, string) (drive.Entry, error) {
	d.mkdirs++
	return drive.Entry{}, errors.New("already exists")
}
func (d *existingMkdirDriver) Move(context.Context, drive.Entry, string) error { return nil }
func (d *existingMkdirDriver) Rename(context.Context, drive.Entry, string) error {
	return nil
}
func (d *existingMkdirDriver) Remove(context.Context, drive.Entry) error { return nil }

func (d *fileUploadDriver) Init(context.Context) error { return nil }
func (d *fileUploadDriver) Drop(context.Context) error { return nil }
func (d *fileUploadDriver) List(_ context.Context, parentID string) ([]drive.Entry, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	var entries []drive.Entry
	for _, entry := range d.entries {
		if entry.ParentID == parentID {
			entries = append(entries, entry)
		}
	}
	return entries, nil
}
func (d *fileUploadDriver) Read(context.Context, drive.Entry, int64, int64) (io.ReadCloser, error) {
	return nil, io.EOF
}
func (d *fileUploadDriver) Put(_ context.Context, parentID, name string, size int64, body io.Reader) (drive.Entry, error) {
	data, err := io.ReadAll(body)
	if err != nil {
		return drive.Entry{}, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.putCalls++
	d.lastData = append(d.lastData[:0], data...)
	return d.saveEntryLocked(parentID, name, size), nil
}
func (d *fileUploadDriver) PutFile(_ context.Context, parentID, name string, size int64, localPath string) (drive.Entry, error) {
	data, err := os.ReadFile(localPath)
	if err != nil {
		return drive.Entry{}, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.putFileCalls++
	d.lastPath = localPath
	d.lastData = append(d.lastData[:0], data...)
	return d.saveEntryLocked(parentID, name, size), nil
}
func (d *fileUploadDriver) saveEntryLocked(parentID, name string, size int64) drive.Entry {
	if d.entries == nil {
		d.entries = map[string]drive.Entry{}
	}
	id := name + "-" + strconv.Itoa(d.putCalls+d.putFileCalls)
	entry := drive.Entry{ID: id, ParentID: parentID, Name: name, Size: size}
	d.entries[id] = entry
	return entry
}

func TestVFSStagesUploadsAndReadsBack(t *testing.T) {
	ctx := context.Background()
	remote := t.TempDir()
	cache := t.TempDir()
	raw := localfs.New(remote)
	if err := raw.Init(ctx); err != nil {
		t.Fatal(err)
	}

	fs, err := vfs.New(raw, vfs.Options{CacheDir: cache, CacheMaxBytes: 10 << 20, UploadDelay: testUploadDelay})
	if err != nil {
		t.Fatal(err)
	}
	fs.Start(ctx)

	if _, err := fs.WriteAt(ctx, "/hello.txt", []byte("hello qrypt"), 0); err != nil {
		t.Fatal(err)
	}
	if err := fs.Flush(ctx, "/hello.txt"); err != nil {
		t.Fatal(err)
	}
	waitNoPending(t, fs)

	rc, err := fs.Read(ctx, "/hello.txt", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello qrypt" {
		t.Fatalf("unexpected data: %q", data)
	}
	snapshot := fs.DebugSnapshot()
	if len(snapshot.Mounts) != 1 || len(snapshot.Mounts[0].UploadHistory) != 1 {
		t.Fatalf("expected one upload history item, got %+v", snapshot)
	}
	history := snapshot.Mounts[0].UploadHistory[0]
	if history.Path != "/hello.txt" || history.State != "completed" || history.BytesUploaded != int64(len("hello qrypt")) {
		t.Fatalf("unexpected upload history: %+v", history)
	}
	report, err := fs.DebugConsistency(ctx, "/hello.txt")
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != "ok" || !report.RemoteFound || !report.SizeMatches {
		t.Fatalf("unexpected consistency report: %+v", report)
	}
}

func TestVFSUsesFileUploaderForStagingPath(t *testing.T) {
	ctx := context.Background()
	drv := &fileUploadDriver{}
	fs, err := vfs.New(drv, vfs.Options{CacheDir: t.TempDir(), CacheMaxBytes: 10 << 20, UploadDelay: testUploadDelay})
	if err != nil {
		t.Fatal(err)
	}
	fs.Start(ctx)

	if _, err := fs.WriteAt(ctx, "/fast.txt", []byte("use staging path"), 0); err != nil {
		t.Fatal(err)
	}
	if err := fs.Flush(ctx, "/fast.txt"); err != nil {
		t.Fatal(err)
	}
	waitNoPending(t, fs)

	drv.mu.Lock()
	defer drv.mu.Unlock()
	if drv.putFileCalls != 1 || drv.putCalls != 0 {
		t.Fatalf("putFileCalls=%d putCalls=%d, want 1 and 0", drv.putFileCalls, drv.putCalls)
	}
	if drv.lastPath == "" {
		t.Fatal("expected PutFile to receive local staging path")
	}
	if string(drv.lastData) != "use staging path" {
		t.Fatalf("unexpected uploaded data: %q", drv.lastData)
	}
}

func TestVFSDebugConsistencyPreservesZeroBytePendingSize(t *testing.T) {
	ctx := context.Background()
	drv := &countingUploadDriver{entries: map[string]drive.Entry{
		"remote-zero": {ID: "remote-zero", ParentID: "0", Name: "zero.txt", Size: 5},
	}}
	fs, err := vfs.New(drv, vfs.Options{CacheDir: t.TempDir(), CacheMaxBytes: 10 << 20})
	if err != nil {
		t.Fatal(err)
	}
	if err := fs.Create(ctx, "/zero.txt"); err != nil {
		t.Fatal(err)
	}

	report, err := fs.DebugConsistency(ctx, "/zero.txt")
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != "mismatch" || report.ExpectedSize != 0 || report.RemoteSize != 5 || report.SizeMatches {
		t.Fatalf("expected zero-byte pending mismatch, got %+v", report)
	}
}

func TestVFSDebugSnapshotShowsActiveUploadProgress(t *testing.T) {
	ctx := context.Background()
	drv := newBlockingUploadDriver()
	fs, err := vfs.New(drv, vfs.Options{CacheDir: t.TempDir(), CacheMaxBytes: 10 << 20, UploadDelay: testUploadDelay})
	if err != nil {
		t.Fatal(err)
	}
	fs.Start(ctx)

	if _, err := fs.WriteAt(ctx, "/active.txt", []byte("active upload"), 0); err != nil {
		t.Fatal(err)
	}
	if err := fs.Flush(ctx, "/active.txt"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-drv.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("upload did not start")
	}

	snapshot := fs.DebugSnapshot()
	if len(snapshot.Mounts) != 1 || len(snapshot.Mounts[0].Uploads) != 1 {
		t.Fatalf("expected one active upload, got %+v", snapshot)
	}
	upload := snapshot.Mounts[0].Uploads[0]
	if upload.Path != "/active.txt" || upload.State != "uploading" || upload.BytesUploaded != int64(len("active upload")) {
		t.Fatalf("unexpected active upload: %+v", upload)
	}
	close(drv.release)
	waitNoPending(t, fs)
}

func TestVFSDebugReadCacheCountsHitsAndMisses(t *testing.T) {
	ctx := context.Background()
	data := []byte("cache me")
	drv := newCountingReadDriver(data)
	fs, err := vfs.New(drv, vfs.Options{CacheDir: t.TempDir(), CacheMaxBytes: 10 << 20})
	if err != nil {
		t.Fatal(err)
	}

	rc, err := fs.Read(ctx, "/data.bin", 0, int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	rc.Close()
	rc, err = fs.Read(ctx, "/data.bin", 0, int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	rc.Close()
	cache := fs.DebugSnapshot().Mounts[0].ReadCache
	if cache.Misses == 0 || cache.Hits == 0 || cache.Puts == 0 || cache.ChunkCount == 0 {
		t.Fatalf("expected cache hit/miss/put stats, got %+v", cache)
	}
	if len(cache.Files) == 0 {
		t.Fatalf("expected per-file cache details, got %+v", cache)
	}
}

func TestVFSCoalescesFlushUploads(t *testing.T) {
	ctx := context.Background()
	drv := &countingUploadDriver{}
	fs, err := vfs.New(drv, vfs.Options{CacheDir: t.TempDir(), CacheMaxBytes: 10 << 20, UploadDelay: 50 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	fs.Start(ctx)

	if _, err := fs.WriteAt(ctx, "/draft.txt", []byte("one"), 0); err != nil {
		t.Fatal(err)
	}
	if err := fs.Flush(ctx, "/draft.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.WriteAt(ctx, "/draft.txt", []byte("two"), 0); err != nil {
		t.Fatal(err)
	}
	if err := fs.Flush(ctx, "/draft.txt"); err != nil {
		t.Fatal(err)
	}

	waitNoPending(t, fs)
	if got := drv.uploadCount(); got != 1 {
		t.Fatalf("upload count = %d, want 1", got)
	}
	if got := drv.lastUpload(); got != "two" {
		t.Fatalf("last upload = %q, want two", got)
	}
}

func TestVFSCoalescesSpacedFlushUploads(t *testing.T) {
	ctx := context.Background()
	drv := &countingUploadDriver{}
	fs, err := vfs.New(drv, vfs.Options{CacheDir: t.TempDir(), CacheMaxBytes: 10 << 20, UploadDelay: 80 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	fs.Start(ctx)

	for _, data := range []string{"one", "two", "three"} {
		if _, err := fs.WriteAt(ctx, "/log.txt", []byte(data), 0); err != nil {
			t.Fatal(err)
		}
		if err := fs.Flush(ctx, "/log.txt"); err != nil {
			t.Fatal(err)
		}
		time.Sleep(30 * time.Millisecond)
	}

	waitNoPending(t, fs)
	if got := drv.uploadCount(); got != 1 {
		t.Fatalf("upload count = %d, want 1", got)
	}
	if got := drv.lastUpload(); got != "three" {
		t.Fatalf("last upload = %q, want three", got)
	}
}

func TestVFSUploadWorkersRunConcurrently(t *testing.T) {
	ctx := context.Background()
	drv := newBlockingUploadDriver()
	fs, err := vfs.New(drv, vfs.Options{
		CacheDir:      t.TempDir(),
		CacheMaxBytes: 10 << 20,
		UploadDelay:   testUploadDelay,
		UploadWorkers: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	fs.Start(ctx)

	for _, path := range []string{"/one.txt", "/two.txt", "/three.txt"} {
		if _, err := fs.WriteAt(ctx, path, []byte(path), 0); err != nil {
			t.Fatal(err)
		}
		if err := fs.Flush(ctx, path); err != nil {
			t.Fatal(err)
		}
	}

	for i := 0; i < 3; i++ {
		select {
		case <-drv.entered:
		case <-time.After(3 * time.Second):
			t.Fatalf("upload worker %d did not start", i+1)
		}
	}
	close(drv.release)
	waitNoPending(t, fs)
}

func TestVFSUploadDoesNotClearNewerPending(t *testing.T) {
	ctx := context.Background()
	entered := make(chan struct{})
	release := make(chan struct{})
	drv := &countingUploadDriver{entered: entered, blockReturn: release}
	fs, err := vfs.New(drv, vfs.Options{CacheDir: t.TempDir(), CacheMaxBytes: 10 << 20, UploadDelay: 10 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	fs.Start(ctx)

	if _, err := fs.WriteAt(ctx, "/draft.txt", []byte("one"), 0); err != nil {
		t.Fatal(err)
	}
	if err := fs.Flush(ctx, "/draft.txt"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("upload did not start")
	}
	if _, err := fs.WriteAt(ctx, "/draft.txt", []byte("two"), 0); err != nil {
		t.Fatal(err)
	}
	if err := fs.Flush(ctx, "/draft.txt"); err != nil {
		t.Fatal(err)
	}
	close(release)

	waitNoPending(t, fs)
	if got := drv.uploadCount(); got != 2 {
		t.Fatalf("upload count = %d, want 2", got)
	}
	if got := drv.lastUpload(); got != "two" {
		t.Fatalf("last upload = %q, want two", got)
	}
	if removed := drv.removedIDs(); len(removed) != 1 || removed[0] != "draft.txt-1" {
		t.Fatalf("removed stale uploads = %v, want [draft.txt-1]", removed)
	}
}

func TestVFSAppleMetadataWrittenAndUploaded(t *testing.T) {
	ctx := context.Background()
	drv := &countingUploadDriver{}
	fs, err := vfs.New(drv, vfs.Options{CacheDir: t.TempDir(), CacheMaxBytes: 10 << 20, UploadDelay: testUploadDelay})
	if err != nil {
		t.Fatal(err)
	}
	fs.Start(ctx)

	if n, err := fs.WriteAt(ctx, "/.DS_Store", []byte("finder"), 0); err != nil || n != len("finder") {
		t.Fatalf("WriteAt .DS_Store n=%d err=%v", n, err)
	}
	if err := fs.Flush(ctx, "/.DS_Store"); err != nil {
		t.Fatal(err)
	}

	// After flush, the file is pending and will be uploaded (like any normal file).
	pending := fs.Pending()
	if len(pending) != 1 || pending[0].Name != ".DS_Store" {
		t.Fatalf("pending = %v, want [.DS_Store]", pending)
	}

	// Stat finds the pending file.
	info, err := fs.Stat(ctx, "/.DS_Store")
	if err != nil {
		t.Fatalf("Stat .DS_Store err=%v", err)
	}
	if info.Name != ".DS_Store" || info.Size != 6 {
		t.Fatalf("Stat .DS_Store = %+v", info)
	}
}

func TestVFSRemoteAppleMetadataVisible(t *testing.T) {
	ctx := context.Background()
	drv := &countingUploadDriver{entries: map[string]drive.Entry{
		"meta":   {ID: "meta", ParentID: "0", Name: ".DS_Store", Size: 1},
		"double": {ID: "double", ParentID: "0", Name: "._asset.js", Size: 1},
		"file":   {ID: "file", ParentID: "0", Name: "asset.js", Size: 1},
	}}
	fs, err := vfs.New(drv, vfs.Options{CacheDir: t.TempDir(), CacheMaxBytes: 10 << 20})
	if err != nil {
		t.Fatal(err)
	}

	// Apple metadata files are now visible like any other file.
	entries, err := fs.List(ctx, "/")
	if err != nil {
		t.Fatal(err)
	}
	got := namesOf(entries)
	if !strings.Contains(got, ".DS_Store") || !strings.Contains(got, "._asset.js") || !strings.Contains(got, "asset.js") {
		t.Fatalf("entries = %q, want all three entries including .DS_Store and ._asset.js", got)
	}

	info, err := fs.Stat(ctx, "/.DS_Store")
	if err != nil {
		t.Fatalf("Stat .DS_Store err=%v", err)
	}
	if info.Name != ".DS_Store" || info.Size != 1 {
		t.Fatalf("Stat .DS_Store = %+v", info)
	}
}

func TestVFSCoalescesChildDeletesIntoDirectoryDelete(t *testing.T) {
	ctx := context.Background()
	drv := newCountingRemoveDriver()
	fs, err := vfs.New(drv, vfs.Options{CacheDir: t.TempDir(), CacheMaxBytes: 10 << 20, DeleteDelay: 50 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	fs.Start(ctx)

	if err := fs.Remove(ctx, "/dir/a.txt"); err != nil {
		t.Fatal(err)
	}
	if err := fs.Remove(ctx, "/dir/sub/b.txt"); err != nil {
		t.Fatal(err)
	}
	if err := fs.RemoveDir(ctx, "/dir"); err != nil {
		t.Fatal(err)
	}

	waitForCondition(t, func() bool { return len(drv.removedIDs()) == 1 })
	removed := drv.removedIDs()
	if len(removed) != 1 || removed[0] != "dir" {
		t.Fatalf("removed ids = %v, want [dir]", removed)
	}
	if _, err := fs.Stat(ctx, "/dir"); err == nil {
		t.Fatal("deleted directory should be hidden from stat")
	}
}

func TestVFSMkdirRestoresPendingDeletedDirectory(t *testing.T) {
	ctx := context.Background()
	drv := newCountingRemoveDriver()
	fs, err := vfs.New(drv, vfs.Options{CacheDir: t.TempDir(), CacheMaxBytes: 10 << 20, DeleteDelay: 30 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}

	if err := fs.RemoveDir(ctx, "/dir"); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.Mkdir(ctx, "/dir"); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.Mkdir(ctx, "/dir/sub"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(60 * time.Millisecond)

	if got := drv.mkdirCount(); got != 0 {
		t.Fatalf("remote mkdir count = %d, want 0", got)
	}
	if removed := drv.removedIDs(); len(removed) != 0 {
		t.Fatalf("remote deletes = %v, want none", removed)
	}
	if _, err := fs.Stat(ctx, "/dir/sub"); err != nil {
		t.Fatalf("restored child directory should remain visible: %v", err)
	}
}

func TestVFSRemoveDirDropsPendingChildren(t *testing.T) {
	ctx := context.Background()
	remote := t.TempDir()
	if err := os.Mkdir(filepath.Join(remote, "dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	fs, err := vfs.New(localfs.New(remote), vfs.Options{
		CacheDir:      t.TempDir(),
		CacheMaxBytes: 10 << 20,
		UploadDelay:   time.Hour,
		DeleteDelay:   time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := fs.Create(ctx, "/dir/pending.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.WriteAt(ctx, "/dir/pending.txt", []byte("data"), 0); err != nil {
		t.Fatal(err)
	}
	if err := fs.Flush(ctx, "/dir/pending.txt"); err != nil {
		t.Fatal(err)
	}
	if err := fs.RemoveDir(ctx, "/dir"); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.Mkdir(ctx, "/dir"); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.Stat(ctx, "/dir/pending.txt"); err == nil {
		t.Fatal("pending child should not survive directory removal")
	}
	entries, err := fs.List(ctx, "/dir")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name == "pending.txt" {
			t.Fatalf("pending child leaked into restored directory list: %v", entries)
		}
	}
	if pending := fs.Pending(); len(pending) != 0 {
		t.Fatalf("pending files = %v, want none", pending)
	}
}

func TestVFSRecoversPendingUploads(t *testing.T) {
	ctx := context.Background()
	remote := t.TempDir()
	cache := t.TempDir()

	first, err := vfs.New(localfs.New(remote), vfs.Options{CacheDir: cache, CacheMaxBytes: 10 << 20})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.WriteAt(ctx, "/resume.txt", []byte("resume me"), 0); err != nil {
		t.Fatal(err)
	}
	if len(first.Pending()) != 1 {
		t.Fatalf("expected one pending file, got %d", len(first.Pending()))
	}

	second, err := vfs.New(localfs.New(remote), vfs.Options{CacheDir: cache, CacheMaxBytes: 10 << 20, UploadDelay: testUploadDelay})
	if err != nil {
		t.Fatal(err)
	}
	second.Start(ctx)
	waitNoPending(t, second)

	data, err := os.ReadFile(remote + "/resume.txt")
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "resume me" {
		t.Fatalf("unexpected recovered data: %q", data)
	}
}

func TestEncryptedDriverRoundTrip(t *testing.T) {
	ctx := context.Background()
	remote := t.TempDir()
	cache := t.TempDir()
	raw := localfs.New(remote)
	cp, err := crypt.NewRcloneCipher("password", "salt")
	if err != nil {
		t.Fatal(err)
	}
	drv := crypt.NewDriver(raw, cp)
	fs, err := vfs.New(drv, vfs.Options{CacheDir: cache, CacheMaxBytes: 10 << 20, UploadDelay: testUploadDelay})
	if err != nil {
		t.Fatal(err)
	}
	fs.Start(ctx)

	if _, err := fs.WriteAt(ctx, "/secret.txt", bytes.Repeat([]byte("a"), 80*1024), 0); err != nil {
		t.Fatal(err)
	}
	if err := fs.Flush(ctx, "/secret.txt"); err != nil {
		t.Fatal(err)
	}
	waitNoPending(t, fs)

	rawEntries, err := raw.List(ctx, "0")
	if err != nil {
		t.Fatal(err)
	}
	if len(rawEntries) != 1 {
		t.Fatalf("expected one raw encrypted entry, got %d", len(rawEntries))
	}
	if rawEntries[0].Name == "secret.txt" {
		t.Fatal("expected encrypted filename on raw backend")
	}
	info, err := fs.Stat(ctx, "/secret.txt")
	if err != nil {
		t.Fatal(err)
	}
	if info.Size != 80*1024 {
		t.Fatalf("expected plaintext size, got %d", info.Size)
	}

	rc, err := fs.Read(ctx, "/secret.txt", 64*1024, 16)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "aaaaaaaaaaaaaaaa" {
		t.Fatalf("unexpected encrypted read data: %q", data)
	}
}

func TestVFSReadSpansChunks(t *testing.T) {
	ctx := context.Background()
	data := bytes.Repeat([]byte("a"), testReadChunkSize+10)
	drv := newCountingReadDriver(data)
	fs, err := vfs.New(drv, vfs.Options{CacheDir: t.TempDir(), CacheMaxBytes: 10 << 20})
	if err != nil {
		t.Fatal(err)
	}

	rc, err := fs.Read(ctx, "/data.bin", 0, int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("read length = %d, want %d", len(got), len(data))
	}
}

func TestVFSReadPrefetchesAdjacentChunk(t *testing.T) {
	ctx := context.Background()
	data := bytes.Repeat([]byte("b"), 3*testReadChunkSize)
	drv := newCountingReadDriver(data)
	fs, err := vfs.New(drv, vfs.Options{CacheDir: t.TempDir(), CacheMaxBytes: 10 << 20})
	if err != nil {
		t.Fatal(err)
	}

	rc, err := fs.Read(ctx, "/data.bin", 0, 16)
	if err != nil {
		t.Fatal(err)
	}
	_ = rc.Close()

	waitForCondition(t, func() bool {
		return drv.readCount(testReadChunkSize) == 1
	})
	before := drv.readCount(testReadChunkSize)

	rc, err = fs.Read(ctx, "/data.bin", testReadChunkSize, 16)
	if err != nil {
		t.Fatal(err)
	}
	_ = rc.Close()
	if got := drv.readCount(testReadChunkSize); got != before {
		t.Fatalf("prefetched chunk read count = %d, want %d", got, before)
	}
}

func TestVFSReadWaitsForInFlightPrefetch(t *testing.T) {
	ctx := context.Background()
	data := bytes.Repeat([]byte("c"), 3*testReadChunkSize)
	drv := newCountingReadDriver(data)
	entered, release := drv.blockRead(testReadChunkSize)
	fs, err := vfs.New(drv, vfs.Options{CacheDir: t.TempDir(), CacheMaxBytes: 10 << 20})
	if err != nil {
		t.Fatal(err)
	}

	rc, err := fs.Read(ctx, "/data.bin", 0, 16)
	if err != nil {
		t.Fatal(err)
	}
	_ = rc.Close()

	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("prefetch did not start")
	}

	readDone := make(chan error, 1)
	go func() {
		rc, err := fs.Read(ctx, "/data.bin", testReadChunkSize, 16)
		if err != nil {
			readDone <- err
			return
		}
		_ = rc.Close()
		readDone <- nil
	}()

	time.Sleep(50 * time.Millisecond)
	if got := drv.readCount(testReadChunkSize); got != 1 {
		t.Fatalf("in-flight chunk read count = %d, want 1", got)
	}
	release()
	if err := <-readDone; err != nil {
		t.Fatal(err)
	}
	if got := drv.readCount(testReadChunkSize); got != 1 {
		t.Fatalf("completed chunk read count = %d, want 1", got)
	}
}

func TestVFSListCachesChildrenForStat(t *testing.T) {
	ctx := context.Background()
	drv := &countingListDriver{lists: map[string]int{}}
	fs, err := vfs.New(drv, vfs.Options{CacheDir: t.TempDir(), CacheMaxBytes: 10 << 20})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := fs.List(ctx, "/"); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.Stat(ctx, "/child.txt"); err != nil {
		t.Fatal(err)
	}
	if got := drv.listCount("0"); got != 1 {
		t.Fatalf("root list count = %d, want 1", got)
	}
}

func TestVFSMkdirStaysVisibleWhenBackendListIsStale(t *testing.T) {
	ctx := context.Background()
	fs, err := vfs.New(&staleMkdirListDriver{}, vfs.Options{CacheDir: t.TempDir(), CacheMaxBytes: 10 << 20})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := fs.Mkdir(ctx, "/new-folder"); err != nil {
		t.Fatal(err)
	}
	entries, err := fs.List(ctx, "/")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name != "new-folder" || !entries[0].IsDir {
		t.Fatalf("created directory should remain visible, got %+v", entries)
	}
	if _, err := fs.Stat(ctx, "/new-folder"); err != nil {
		t.Fatalf("created directory should remain stat-able: %v", err)
	}
}

func TestVFSMkdirReusesExistingDirectoryOnConflict(t *testing.T) {
	ctx := context.Background()
	drv := &existingMkdirDriver{}
	fs, err := vfs.New(drv, vfs.Options{CacheDir: t.TempDir(), CacheMaxBytes: 10 << 20})
	if err != nil {
		t.Fatal(err)
	}

	entry, err := fs.Mkdir(ctx, "/dir")
	if err != nil {
		t.Fatal(err)
	}
	if entry.ID != "existing-dir" || !entry.IsDir {
		t.Fatalf("unexpected reused directory: %+v", entry)
	}
	if drv.mkdirs != 1 {
		t.Fatalf("mkdir count = %d, want 1", drv.mkdirs)
	}
	if _, err := fs.Mkdir(ctx, "/dir"); err != nil {
		t.Fatal(err)
	}
	if drv.mkdirs != 1 {
		t.Fatalf("cached mkdir count = %d, want 1", drv.mkdirs)
	}
}

func TestVFSUploadsFileInsideLocallyKnownStaleDirectory(t *testing.T) {
	ctx := context.Background()
	drv := &staleMkdirListDriver{failFirstPut: true}
	fs, err := vfs.New(drv, vfs.Options{CacheDir: t.TempDir(), CacheMaxBytes: 10 << 20, UploadDelay: testUploadDelay})
	if err != nil {
		t.Fatal(err)
	}
	fs.Start(ctx)

	if _, err := fs.Mkdir(ctx, "/new-folder"); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.WriteAt(ctx, "/new-folder/file.txt", []byte("content"), 0); err != nil {
		t.Fatal(err)
	}
	if err := fs.Flush(ctx, "/new-folder/file.txt"); err != nil {
		t.Fatal(err)
	}

	waitNoPending(t, fs)
	attempts, parent, name, data := drv.lastPut()
	if attempts != 2 {
		t.Fatalf("put attempts = %d, want 2", attempts)
	}
	if parent != "dir-id" || name != "file.txt" || data != "content" {
		t.Fatalf("unexpected put parent=%q name=%q data=%q", parent, name, data)
	}
}

func TestVFSPrepareDirectoryCopyClearsPendingChildren(t *testing.T) {
	ctx := context.Background()
	drv := &staleMkdirListDriver{}
	fs, err := vfs.New(drv, vfs.Options{CacheDir: t.TempDir(), CacheMaxBytes: 10 << 20, UploadDelay: time.Hour})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := fs.Mkdir(ctx, "/_nuxt"); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.WriteAt(ctx, "/_nuxt/LimitGroup.B_5XwyXE.css", []byte("body"), 0); err != nil {
		t.Fatal(err)
	}
	if err := fs.Flush(ctx, "/_nuxt/LimitGroup.B_5XwyXE.css"); err != nil {
		t.Fatal(err)
	}
	entries, err := fs.List(ctx, "/_nuxt")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("pending child should be visible before copy prepare, got %+v", entries)
	}

	if err := fs.PrepareDirectoryCopy(ctx, "/_nuxt"); err != nil {
		t.Fatal(err)
	}
	entries, err = fs.List(ctx, "/_nuxt")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("pending children should be cleared, got %+v", entries)
	}
	if pending := fs.Pending(); len(pending) != 0 {
		t.Fatalf("pending should be empty, got %+v", pending)
	}
}

func TestVFSPrepareDirectoryCopyHidesExistingRemoteChildrenUntilRecreated(t *testing.T) {
	ctx := context.Background()
	remote := t.TempDir()
	if err := os.Mkdir(filepath.Join(remote, "_nuxt"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(remote, "_nuxt", "LimitGroup.B_5XwyXE.css"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	raw := localfs.New(remote)
	if err := raw.Init(ctx); err != nil {
		t.Fatal(err)
	}
	fs, err := vfs.New(raw, vfs.Options{CacheDir: t.TempDir(), CacheMaxBytes: 10 << 20, UploadDelay: time.Hour})
	if err != nil {
		t.Fatal(err)
	}

	entries, err := fs.List(ctx, "/_nuxt")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected existing remote child, got %+v", entries)
	}
	if err := fs.PrepareDirectoryCopy(ctx, "/_nuxt"); err != nil {
		t.Fatal(err)
	}
	entries, err = fs.List(ctx, "/_nuxt")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("existing children should be hidden during copy, got %+v", entries)
	}
	if _, err := fs.Stat(ctx, "/_nuxt/LimitGroup.B_5XwyXE.css"); err == nil {
		t.Fatal("hidden child should not stat before recreate")
	}

	if _, err := fs.WriteAt(ctx, "/_nuxt/LimitGroup.B_5XwyXE.css", []byte("new"), 0); err != nil {
		t.Fatal(err)
	}
	entries, err = fs.List(ctx, "/_nuxt")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name != "LimitGroup.B_5XwyXE.css" || entries[0].Size != 3 {
		t.Fatalf("recreated pending child should be visible, got %+v", entries)
	}
}

func TestVFSRenameMoveOverlayHidesStaleBackendEntries(t *testing.T) {
	ctx := context.Background()
	drv := &staleMoveListDriver{}
	fs, err := vfs.New(drv, vfs.Options{CacheDir: t.TempDir(), CacheMaxBytes: 10 << 20})
	if err != nil {
		t.Fatal(err)
	}

	if err := fs.Rename(ctx, "/新建文件夹", "/video"); err != nil {
		t.Fatal(err)
	}
	if err := fs.Rename(ctx, "/movie.mp4", "/video/movie.mp4"); err != nil {
		t.Fatal(err)
	}

	rootEntries, err := fs.List(ctx, "/")
	if err != nil {
		t.Fatal(err)
	}
	if namesOf(rootEntries) != "video" {
		t.Fatalf("root entries = %q, want video", namesOf(rootEntries))
	}
	videoEntries, err := fs.List(ctx, "/video")
	if err != nil {
		t.Fatal(err)
	}
	if namesOf(videoEntries) != "movie.mp4" {
		t.Fatalf("video entries = %q, want movie.mp4", namesOf(videoEntries))
	}
	if _, err := fs.Stat(ctx, "/movie.mp4"); err == nil {
		t.Fatal("old moved file path should be hidden")
	}
}

func TestVFSRenameMoveOverlayConfirmsRemoteConvergence(t *testing.T) {
	ctx := context.Background()
	drv := &staleMoveListDriver{}
	fs, err := vfs.New(drv, vfs.Options{CacheDir: t.TempDir(), CacheMaxBytes: 10 << 20})
	if err != nil {
		t.Fatal(err)
	}

	if err := fs.Rename(ctx, "/新建文件夹", "/video"); err != nil {
		t.Fatal(err)
	}
	if err := fs.Rename(ctx, "/movie.mp4", "/video/movie.mp4"); err != nil {
		t.Fatal(err)
	}
	drv.converged = true

	rootEntries, err := fs.List(ctx, "/")
	if err != nil {
		t.Fatal(err)
	}
	if namesOf(rootEntries) != "video" {
		t.Fatalf("root entries = %q, want video", namesOf(rootEntries))
	}
	videoEntries, err := fs.List(ctx, "/video")
	if err != nil {
		t.Fatal(err)
	}
	if namesOf(videoEntries) != "movie.mp4" {
		t.Fatalf("video entries = %q, want movie.mp4", namesOf(videoEntries))
	}
}

func TestVFSRenameUploadedFile(t *testing.T) {
	ctx := context.Background()
	remote := t.TempDir()
	fs, err := vfs.New(localfs.New(remote), vfs.Options{CacheDir: t.TempDir(), CacheMaxBytes: 10 << 20, UploadDelay: testUploadDelay})
	if err != nil {
		t.Fatal(err)
	}
	fs.Start(ctx)
	if _, err := fs.WriteAt(ctx, "/old.txt", []byte("rename me"), 0); err != nil {
		t.Fatal(err)
	}
	if err := fs.Flush(ctx, "/old.txt"); err != nil {
		t.Fatal(err)
	}
	waitNoPending(t, fs)

	if err := fs.Rename(ctx, "/old.txt", "/new.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(remote + "/old.txt"); !os.IsNotExist(err) {
		t.Fatalf("old file should not exist, err=%v", err)
	}
	data, err := os.ReadFile(remote + "/new.txt")
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "rename me" {
		t.Fatalf("unexpected renamed data: %q", data)
	}
}

func TestVFSRenamePendingFile(t *testing.T) {
	ctx := context.Background()
	remote := t.TempDir()
	fs, err := vfs.New(localfs.New(remote), vfs.Options{CacheDir: t.TempDir(), CacheMaxBytes: 10 << 20, UploadDelay: testUploadDelay})
	if err != nil {
		t.Fatal(err)
	}
	fs.Start(ctx)
	if _, err := fs.WriteAt(ctx, "/draft.txt", []byte("pending rename"), 0); err != nil {
		t.Fatal(err)
	}
	if err := fs.Rename(ctx, "/draft.txt", "/final.txt"); err != nil {
		t.Fatal(err)
	}
	if err := fs.Flush(ctx, "/final.txt"); err != nil {
		t.Fatal(err)
	}
	waitNoPending(t, fs)
	data, err := os.ReadFile(remote + "/final.txt")
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "pending rename" {
		t.Fatalf("unexpected pending renamed data: %q", data)
	}
}

func TestVFSWriteAtStagesExistingFile(t *testing.T) {
	ctx := context.Background()
	remote := t.TempDir()
	if err := os.WriteFile(filepath.Join(remote, "data.txt"), []byte("abcdef"), 0o644); err != nil {
		t.Fatal(err)
	}
	fs, err := vfs.New(localfs.New(remote), vfs.Options{RootID: remote, CacheDir: t.TempDir(), CacheMaxBytes: 10 << 20, UploadDelay: testUploadDelay})
	if err != nil {
		t.Fatal(err)
	}
	fs.Start(ctx)

	if _, err := fs.WriteAt(ctx, "/data.txt", []byte("XY"), 2); err != nil {
		t.Fatal(err)
	}
	if err := fs.Flush(ctx, "/data.txt"); err != nil {
		t.Fatal(err)
	}
	waitNoPending(t, fs)

	data, err := os.ReadFile(filepath.Join(remote, "data.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "abXYef" {
		t.Fatalf("unexpected patched backend data: %q", data)
	}
}

func TestVFSTruncateUploadedFile(t *testing.T) {
	ctx := context.Background()
	remote := t.TempDir()
	fs, err := vfs.New(localfs.New(remote), vfs.Options{CacheDir: t.TempDir(), CacheMaxBytes: 10 << 20, UploadDelay: testUploadDelay})
	if err != nil {
		t.Fatal(err)
	}
	fs.Start(ctx)
	if _, err := fs.WriteAt(ctx, "/data.txt", []byte("abcdef"), 0); err != nil {
		t.Fatal(err)
	}
	if err := fs.Flush(ctx, "/data.txt"); err != nil {
		t.Fatal(err)
	}
	waitNoPending(t, fs)

	if err := fs.Truncate(ctx, "/data.txt", 3); err != nil {
		t.Fatal(err)
	}
	rc, err := fs.Read(ctx, "/data.txt", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(rc)
	rc.Close()
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "abc" {
		t.Fatalf("unexpected staged truncate data: %q", data)
	}
	if err := fs.Flush(ctx, "/data.txt"); err != nil {
		t.Fatal(err)
	}
	waitNoPending(t, fs)
	data, err = os.ReadFile(remote + "/data.txt")
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "abc" {
		t.Fatalf("unexpected truncated backend data: %q", data)
	}
}

func waitNoPending(t *testing.T, fs vfs.FileSystem) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if len(fs.Pending()) == 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("pending uploads did not drain: %+v", fs.Pending())
}

func waitForCondition(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("condition was not met before deadline")
}

func namesOf(entries []drive.Entry) string {
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name)
	}
	return strings.Join(names, ",")
}

var _ drive.Driver = (*localfs.Driver)(nil)
