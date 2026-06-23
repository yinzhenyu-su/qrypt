package vfs_test

import (
	"bytes"
	"context"
	"io"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/internal/driver/localfs"
	"github.com/yinzhenyu/qrypt/pkg/crypt"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

const testReadChunkSize = 512 * 1024

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

func newCountingReadDriver(data []byte) *countingReadDriver {
	return &countingReadDriver{data: data, read: map[int64]int{}, block: map[int64]chan struct{}{}, entered: map[int64]chan struct{}{}}
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

func TestVFSStagesUploadsAndReadsBack(t *testing.T) {
	ctx := context.Background()
	remote := t.TempDir()
	cache := t.TempDir()
	raw := localfs.New(remote)
	if err := raw.Init(ctx); err != nil {
		t.Fatal(err)
	}

	fs, err := vfs.New(raw, vfs.Options{CacheDir: cache, CacheMaxBytes: 10 << 20})
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

	second, err := vfs.New(localfs.New(remote), vfs.Options{CacheDir: cache, CacheMaxBytes: 10 << 20})
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
	fs, err := vfs.New(drv, vfs.Options{CacheDir: cache, CacheMaxBytes: 10 << 20})
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

func TestVFSRenameUploadedFile(t *testing.T) {
	ctx := context.Background()
	remote := t.TempDir()
	fs, err := vfs.New(localfs.New(remote), vfs.Options{CacheDir: t.TempDir(), CacheMaxBytes: 10 << 20})
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
	fs, err := vfs.New(localfs.New(remote), vfs.Options{CacheDir: t.TempDir(), CacheMaxBytes: 10 << 20})
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

func TestVFSTruncateUploadedFile(t *testing.T) {
	ctx := context.Background()
	remote := t.TempDir()
	fs, err := vfs.New(localfs.New(remote), vfs.Options{CacheDir: t.TempDir(), CacheMaxBytes: 10 << 20})
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

var _ drive.Driver = (*localfs.Driver)(nil)
