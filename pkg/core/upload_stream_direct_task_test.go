package core

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/drivers/localfs"
	"github.com/yinzhenyu/qrypt/pkg/task"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

func TestCreateTaskUploadStreamDirectUsesSourceUploader(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	drv := &directUploadTestDriver{}
	fs, err := vfs.New(drv, vfs.Options{StorageDir: filepath.Join(t.TempDir(), "cache"), UploadDelay: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	defer stopTestVFS(t, fs)
	fs.Start(ctx)
	c := newTestCore(t, fs)
	c.mountContentDedup = map[string]bool{"direct.txt": true}
	sourcePath := filepath.Join(t.TempDir(), "source.txt")
	payload := []byte("direct payload")
	if err := os.WriteFile(sourcePath, payload, 0o644); err != nil {
		t.Fatal(err)
	}

	item, err := c.CreateTask(ctx, task.Request{
		Type: task.TypeUploadStreamDirect,
		Items: []task.Item{{
			ItemID:     "item",
			SourcePath: sourcePath,
			DestPath:   "/direct.txt",
			Size:       int64(len(payload)),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	item = waitCoreTask(t, c, item.ID)
	if item.State != task.StateSucceeded || item.Type != task.TypeUploadStreamDirect {
		t.Fatalf("task = %+v, want direct upload success", item)
	}
	if item.Detail["channel"] != "direct" || item.Progress.StagingBytesTotal != 0 || item.Progress.CloudBytesDone != int64(len(payload)) {
		t.Fatalf("task progress/detail = %+v %+v, want direct channel without staging bytes", item.Progress, item.Detail)
	}
	if pendings := fs.PendingUploads(); len(pendings) != 0 {
		t.Fatalf("pending uploads = %+v, want none for direct upload", pendings)
	}
	wantSHA1 := sha1.Sum(payload)
	if got := drv.uploadedData(); string(got) != string(payload) {
		t.Fatalf("uploaded data = %q, want %q", got, payload)
	}
	if got := drv.sourceSHA1(); got != hex.EncodeToString(wantSHA1[:]) {
		t.Fatalf("source sha1 = %q, want %q", got, hex.EncodeToString(wantSHA1[:]))
	}
}

func TestCreateTaskUploadStreamDirectUsesLocalFSDirectPath(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	remote := t.TempDir()
	fs, err := vfs.New(localfs.New(remote), vfs.Options{StorageDir: filepath.Join(t.TempDir(), "cache"), UploadDelay: 10 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer stopTestVFS(t, fs)
	fs.Start(ctx)
	c := newTestCore(t, fs)
	sourcePath := filepath.Join(t.TempDir(), "source.txt")
	payload := []byte("fallback payload")
	if err := os.WriteFile(sourcePath, payload, 0o644); err != nil {
		t.Fatal(err)
	}

	item, err := c.CreateTask(ctx, task.Request{
		Type: task.TypeUploadStreamDirect,
		Items: []task.Item{{
			ItemID:     "item",
			SourcePath: sourcePath,
			DestPath:   "/fallback.txt",
			Size:       int64(len(payload)),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	item = waitCoreTask(t, c, item.ID)
	if item.State != task.StateSucceeded {
		t.Fatalf("task = %+v, want localfs direct upload success", item)
	}
	if got := item.Result.Items[0].Phase; got != "direct" {
		t.Fatalf("item phase = %q, want direct", got)
	}
	remotePath := filepath.Join(remote, "fallback.txt")
	if data, err := os.ReadFile(remotePath); err != nil || string(data) != string(payload) {
		t.Fatalf("remote data = %q err=%v, want %q", data, err, payload)
	}
}

func TestCreateTaskUploadStreamDirectNonResumableCleansPartialAndRetries(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	remote := t.TempDir()
	fs, err := vfs.New(localfs.New(remote), vfs.Options{StorageDir: filepath.Join(t.TempDir(), "cache"), UploadDelay: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	defer stopTestVFS(t, fs)
	fs.Start(ctx)
	c := newTestCore(t, fs)
	c.mountContentDedup = map[string]bool{"retry.txt": true}
	payload := []byte("non resumable retry payload")
	provider := &flakyDirectUploadSourceProvider{data: payload, failSecondOpen: true}
	c.SetUploadSourceProvider(provider)

	item, err := c.CreateTask(ctx, task.Request{
		Type: task.TypeUploadStreamDirect,
		Items: []task.Item{{
			ItemID:     "item",
			SourcePath: "token",
			DestPath:   "/retry.txt",
			Size:       int64(len(payload)),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	item = waitCoreTask(t, c, item.ID)
	if item.State != task.StateFailed {
		t.Fatalf("task = %+v, want failed first attempt", item)
	}
	entries, err := os.ReadDir(remote)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("remote entries after failed direct upload = %+v, want none", entries)
	}

	provider.setFailSecondOpen(false)
	if err := c.RetryTask(ctx, item.ID); err != nil {
		t.Fatal(err)
	}
	item = waitCoreTask(t, c, item.ID)
	if item.State != task.StateSucceeded {
		t.Fatalf("retried task = %+v, want succeeded", item)
	}
	data, err := os.ReadFile(filepath.Join(remote, "retry.txt"))
	if err != nil || string(data) != string(payload) {
		t.Fatalf("remote data = %q err=%v, want %q", data, err, payload)
	}
}

func TestCreateTaskUploadStreamDirectAutoRetryKeepsTaskID(t *testing.T) {
	oldBase, oldMax := DirectUploadRetryBaseDelay, DirectUploadRetryMaxDelay
	DirectUploadRetryBaseDelay = time.Millisecond
	DirectUploadRetryMaxDelay = 5 * time.Millisecond
	t.Cleanup(func() {
		DirectUploadRetryBaseDelay = oldBase
		DirectUploadRetryMaxDelay = oldMax
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	drv := &directUploadTestDriver{}
	fs, err := vfs.New(drv, vfs.Options{StorageDir: filepath.Join(t.TempDir(), "cache"), UploadDelay: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	defer stopTestVFS(t, fs)
	fs.Start(ctx)
	c := newTestCore(t, fs)
	c.mountContentDedup = map[string]bool{"auto-retry.txt": true}
	payload := []byte("persistent automatic retry")
	provider := &flakyDirectUploadSourceProvider{data: payload, failSecondOpen: true}
	c.SetUploadSourceProvider(provider)

	created, err := c.CreateTask(ctx, task.Request{
		Type:   task.TypeUploadStreamDirect,
		Detail: map[string]any{"auto_retry": true, "auto_upload_item_id": "logical-1"},
		Items: []task.Item{{
			ItemID:     "logical-1",
			SourcePath: "token",
			DestPath:   "/auto-retry.txt",
			Size:       int64(len(payload)),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	finished := waitCoreTask(t, c, created.ID)
	if finished.ID != created.ID || finished.State != task.StateSucceeded {
		t.Fatalf("task = %+v, want same task id succeeded", finished)
	}
	if finished.RetryCount != 1 {
		t.Fatalf("retry count = %d, want 1", finished.RetryCount)
	}
	if finished.Detail["auto_upload_item_id"] != "logical-1" {
		t.Fatalf("task detail = %+v, want logical id", finished.Detail)
	}
	if finished.Progress.SourceBytesDone != int64(len(payload)) || finished.Progress.SourceBytesTotal != int64(len(payload)) {
		t.Fatalf("source progress = %+v, want complete", finished.Progress)
	}
}

func TestCreateTaskUploadStreamDirectContentURIRequiresOpener(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	drv := &directUploadTestDriver{}
	fs, err := vfs.New(drv, vfs.Options{StorageDir: filepath.Join(t.TempDir(), "cache"), UploadDelay: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	defer stopTestVFS(t, fs)
	fs.Start(ctx)
	c := newTestCore(t, fs)

	created, err := c.CreateTask(ctx, task.Request{
		Type:  task.TypeUploadStreamDirect,
		Items: []task.Item{{ItemID: "item", SourcePath: "content://downloads/1", DestPath: "/missing.txt", Size: 1}},
	})
	if err != nil {
		t.Fatal(err)
	}
	finished := waitCoreTask(t, c, created.ID)
	if finished.State != task.StateFailed || finished.Error == nil || !strings.Contains(finished.Error.Message, "source opener unavailable") {
		t.Fatalf("task = %+v, want explicit opener error", finished)
	}
}

func TestCreateTaskUploadStreamDirectRejectsBadOffsetSource(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	drv := &directUploadTestDriver{}
	fs, err := vfs.New(drv, vfs.Options{StorageDir: filepath.Join(t.TempDir(), "cache"), UploadDelay: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	defer stopTestVFS(t, fs)
	fs.Start(ctx)
	c := newTestCore(t, fs)
	c.mountContentDedup = map[string]bool{"bad-offset.bin": true}
	payload := make([]byte, 2*uploadCopyChunkSize+123)
	for i := range payload {
		payload[i] = byte((i*31 + i/251) % 256)
	}
	c.SetUploadSourceProvider(badOffsetUploadSourceProvider{data: payload})

	item, err := c.CreateTask(ctx, task.Request{
		Type: task.TypeUploadStreamDirect,
		Items: []task.Item{{
			ItemID:     "item",
			SourcePath: "token",
			DestPath:   "/bad-offset.bin",
			Size:       int64(len(payload)),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	item = waitCoreTask(t, c, item.ID)
	if item.State != task.StateFailed {
		t.Fatalf("task = %+v, want failed", item)
	}
	if len(item.Result.Items) != 1 || item.Result.Items[0].Error == nil || !strings.Contains(item.Result.Items[0].Error.Message, "source opener must honor requested offsets") {
		t.Fatalf("task result = %+v, want offset validation failure", item.Result.Items)
	}
	if drv.putSourceCount() != 0 {
		t.Fatalf("PutSource calls = %d, want 0", drv.putSourceCount())
	}
}

type directUploadTestDriver struct {
	drive.UnsupportedOperations
	mu        sync.Mutex
	data      []byte
	sha1Hex   string
	putSource int
}

type badOffsetUploadSourceProvider struct {
	data []byte
}

type flakyDirectUploadSourceProvider struct {
	mu             sync.Mutex
	data           []byte
	opens          int
	failSecondOpen bool
}

func (p *flakyDirectUploadSourceProvider) setFailSecondOpen(fail bool) {
	p.mu.Lock()
	p.failSecondOpen = fail
	p.mu.Unlock()
}

func (p *flakyDirectUploadSourceProvider) OpenUploadSource(ctx context.Context, _ string, offset int64) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p.mu.Lock()
	p.opens++
	open := p.opens
	fail := p.failSecondOpen && open == 2
	data := append([]byte(nil), p.data...)
	p.mu.Unlock()
	if offset > 0 {
		data = data[offset:]
	}
	if fail {
		return failingReadCloser{Reader: bytes.NewReader(data[:len(data)/2])}, nil
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (p badOffsetUploadSourceProvider) OpenUploadSource(ctx context.Context, _ string, _ int64) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return io.NopCloser(bytes.NewReader(p.data)), nil
}

type failingReadCloser struct {
	*bytes.Reader
}

func (r failingReadCloser) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	if err == io.EOF {
		return n, errors.New("source interrupted")
	}
	return n, err
}

func (r failingReadCloser) Close() error {
	return nil
}

func (d *directUploadTestDriver) Capabilities() []drive.Capability {
	return []drive.Capability{drive.CapabilityWriter, drive.CapabilitySourceUploader, drive.CapabilityResumableUploader}
}

func (d *directUploadTestDriver) Init(context.Context) error { return nil }
func (d *directUploadTestDriver) Drop(context.Context) error { return nil }
func (d *directUploadTestDriver) DebugSnapshot(context.Context) (drive.DebugSnapshot, error) {
	return drive.DebugSnapshot{Driver: "direct-upload-test", Health: drive.HealthLevelOK}, nil
}
func (d *directUploadTestDriver) Metrics(context.Context, time.Time) ([]drive.MetricEvent, error) {
	return nil, nil
}
func (d *directUploadTestDriver) Space(context.Context) (drive.Space, error) {
	return drive.Space{}, drive.ErrSpaceUnsupported
}
func (d *directUploadTestDriver) List(context.Context, string) ([]drive.Entry, error) {
	return nil, nil
}
func (d *directUploadTestDriver) Read(context.Context, drive.Entry, int64, int64) (io.ReadCloser, error) {
	return nil, drive.ErrUnsupported
}
func (d *directUploadTestDriver) PutSource(ctx context.Context, req drive.UploadRequest) (drive.Entry, error) {
	sha1Sum, _ := drive.SourceHash(req.Source, drive.HashSHA1)
	f, err := req.Source.Open(ctx)
	if err != nil {
		return drive.Entry{}, err
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		return drive.Entry{}, err
	}
	drive.ReportUploadPhase(req.Progress, drive.UploadPhaseUploading)
	drive.ReportUploadProgress(req.Progress, int64(len(data)))
	d.mu.Lock()
	defer d.mu.Unlock()
	d.putSource++
	d.data = append([]byte(nil), data...)
	d.sha1Hex = hex.EncodeToString(sha1Sum)
	return drive.Entry{ID: "uploaded-direct", ParentID: req.ParentID, Name: req.Name, Size: int64(len(data))}, nil
}

func (d *directUploadTestDriver) uploadedData() []byte {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]byte(nil), d.data...)
}

func (d *directUploadTestDriver) sourceSHA1() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.sha1Hex
}

func (d *directUploadTestDriver) putSourceCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.putSource
}

// countingUploadSourceProvider tracks the total bytes it hands out, so a test
// can distinguish "one full read" (dedup off) from "hash pass + upload pass".
type countingUploadSourceProvider struct {
	mu        sync.Mutex
	data      []byte
	bytesRead int64
	opens     int
}

func (p *countingUploadSourceProvider) OpenUploadSource(_ context.Context, _ string, offset int64) (io.ReadCloser, error) {
	if offset < 0 || offset > int64(len(p.data)) {
		return nil, fmt.Errorf("counting provider: offset %d out of range", offset)
	}
	p.mu.Lock()
	p.opens++
	p.mu.Unlock()
	return &countingSourceReader{p: p, data: p.data[offset:]}, nil
}

func (p *countingUploadSourceProvider) total() (int64, int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.bytesRead, p.opens
}

type countingSourceReader struct {
	p    *countingUploadSourceProvider
	data []byte
	off  int
}

func (r *countingSourceReader) Read(buf []byte) (int, error) {
	if r.off >= len(r.data) {
		return 0, io.EOF
	}
	n := copy(buf, r.data[r.off:])
	r.off += n
	r.p.mu.Lock()
	r.p.bytesRead += int64(n)
	r.p.mu.Unlock()
	return n, nil
}

func (r *countingSourceReader) Close() error { return nil }

func TestCreateTaskUploadStreamDirectSkippedSegmentsHashWhenDedupOff(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	drv := &directUploadTestDriver{}
	fs, err := vfs.New(drv, vfs.Options{StorageDir: filepath.Join(t.TempDir(), "cache"), UploadDelay: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	defer stopTestVFS(t, fs)
	fs.Start(ctx)
	c := newTestCore(t, fs) // no config: mountContentDedup empty => no dedup path
	payload := make([]byte, 2*uploadCopyChunkSize+123)
	for i := range payload {
		payload[i] = byte((i*17 + i/19) % 256)
	}
	provider := &countingUploadSourceProvider{data: payload}
	c.SetUploadSourceProvider(provider)

	item, err := c.CreateTask(ctx, task.Request{
		Type: task.TypeUploadStreamDirect,
		Items: []task.Item{{
			ItemID:     "item",
			SourcePath: "token",
			DestPath:   "/no-dedup.bin",
			Size:       int64(len(payload)),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	item = waitCoreTask(t, c, item.ID)
	if item.State != task.StateSucceeded {
		t.Fatalf("task = %+v, want succeeded", item)
	}
	if got := drv.uploadedData(); !bytes.Equal(got, payload) {
		t.Fatalf("uploaded data mismatch: %d vs %d bytes", len(got), len(payload))
	}
	read, opens := provider.total()
	if read != int64(len(payload)) {
		t.Fatalf("source bytes read = %d, want %d (one upload pass, no pre-hash read)", read, len(payload))
	}
	if opens > 2 {
		t.Fatalf("source opens = %d, want at most 2", opens)
	}
}

func TestCreateTaskUploadStreamDirectPrehashesWhenDedupOn(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	drv := &directUploadTestDriver{}
	fs, err := vfs.New(drv, vfs.Options{StorageDir: filepath.Join(t.TempDir(), "cache"), UploadDelay: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	defer stopTestVFS(t, fs)
	fs.Start(ctx)
	c := newTestCore(t, fs)
	c.mountContentDedup = map[string]bool{"dedup.bin": true}
	payload := make([]byte, 3*uploadCopyChunkSize+9)
	for i := range payload {
		payload[i] = byte((i*13 + i/7) % 256)
	}
	provider := &countingUploadSourceProvider{data: payload}
	c.SetUploadSourceProvider(provider)

	item, err := c.CreateTask(ctx, task.Request{
		Type: task.TypeUploadStreamDirect,
		Items: []task.Item{{
			ItemID:     "item",
			SourcePath: "token",
			DestPath:   "/dedup.bin",
			Size:       int64(len(payload)),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	item = waitCoreTask(t, c, item.ID)
	if item.State != task.StateSucceeded {
		t.Fatalf("task = %+v, want succeeded", item)
	}
	if got := drv.uploadedData(); !bytes.Equal(got, payload) {
		t.Fatalf("uploaded data mismatch: %d vs %d bytes", len(got), len(payload))
	}
	read, _ := provider.total()
	if read < 2*int64(len(payload)) {
		t.Fatalf("source bytes read = %d, want at least 2x payload (hash pass + upload pass) = %d", read, 2*int64(len(payload)))
	}
	if read >= 3*int64(len(payload)) {
		t.Fatalf("source bytes read = %d, want less than 3x payload", read)
	}
}

func TestDirectUploadSourceReleaseOpenFiles(t *testing.T) {
	closed := 0
	provider := closeTrackingProvider{onClose: func() { closed++ }}
	source := &directUploadSource{provider: provider, token: "t", size: 4}

	// Normal use: the consumer closes its file; release must find nothing.
	f, err := source.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Read(make([]byte, 4)); err != nil && !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if closed != 1 {
		t.Fatalf("reader closed %d times, want 1", closed)
	}
	source.releaseOpenFiles()
	if closed != 1 {
		t.Fatalf("release closed an already-closed file: closed %d, want 1", closed)
	}

	// Leak: the consumer forgets Close; release must reclaim the reader.
	f2, err := source.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f2.Read(make([]byte, 4)); err != nil && !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
	source.releaseOpenFiles()
	if closed != 2 {
		t.Fatalf("release did not close the leaked reader: closed %d, want 2", closed)
	}
	if err := f2.Close(); err != nil {
		t.Fatal(err)
	}
}

type closeTrackingProvider struct {
	onClose func()
}

func (p closeTrackingProvider) OpenUploadSource(context.Context, string, int64) (io.ReadCloser, error) {
	return &closeTrackingReader{onClose: p.onClose}, nil
}

type closeTrackingReader struct {
	onClose func()
	closed  bool
}

func (r *closeTrackingReader) Read([]byte) (int, error) { return 0, io.EOF }
func (r *closeTrackingReader) Close() error {
	if !r.closed {
		r.closed = true
		r.onClose()
	}
	return nil
}

func TestIsRecoverableUploadStreamTask(t *testing.T) {
	c := &Core{}
	base := task.Task{Type: task.TypeUploadStreamBatch, ID: "t1"}
	cases := []struct {
		name  string
		state task.State
		err   *task.Error
		want  bool
	}{
		{"interrupted failed", task.StateFailed, &task.Error{Code: "interrupted"}, true},
		{"waiting_input", task.StateWaitingInput, nil, true},
		{"running", task.StateRunning, nil, true},
		{"queued", task.StateQueued, nil, true},
		{"scheduled", task.StateScheduled, nil, true},
		{"succeeded", task.StateSucceeded, nil, false},
		{"canceled", task.StateCanceled, nil, false},
		// Without an upload inspector the failed/partial_failed branch stays
		// conservative (no staging file can be confirmed).
		{"plain failed", task.StateFailed, &task.Error{Code: "oops"}, false},
		{"partial_failed", task.StatePartialFailed, nil, false},
	}
	for _, tc := range cases {
		item := base
		item.State = tc.state
		item.Error = tc.err
		if got := c.isRecoverableUploadStreamTask(item); got != tc.want {
			t.Fatalf("isRecoverableUploadStreamTask(%s) = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestRequiresPrecomputedSourceHashes(t *testing.T) {
	c := &Core{
		mountContentDedup:  map[string]bool{"quark-test": true, "jianguoyun": false},
		defaultUploadMount: "quark-test",
	}
	cases := []struct {
		path string
		want bool
	}{
		{"/quark-test/Upload/a.jpg", true}, // explicit dedup mount
		{"/Upload/a.jpg", true},            // no mount prefix -> default upload mount
		{"/jianguoyun/x", false},           // known mount without dedup
		{"/", false},                       // rooted path
		{"", false},
	}
	for _, tc := range cases {
		if got := c.requiresPrecomputedSourceHashes(tc.path); got != tc.want {
			t.Fatalf("requiresPrecomputedSourceHashes(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

func TestUploadStreamBatchProgressSpeedAndETA(t *testing.T) {
	b := &uploadStreamBatch{}
	// First sample only seeds bookkeeping.
	if s, e := b.progressSpeedAndETA(0, 100_000); s != 0 || e != 0 {
		t.Fatalf("first sample = %d/%d, want 0/0", s, e)
	}
	// 50KB over ~1s.
	b.lastSpeedAt = b.lastSpeedAt.Add(-time.Second)
	s, e := b.progressSpeedAndETA(50_000, 100_000)
	if s < 40_000 || s > 60_000 {
		t.Fatalf("speed = %d, want ~50000", s)
	}
	if e < 800 || e > 1_200 {
		t.Fatalf("eta = %d, want ~1000", e)
	}
	// A stalled interval (no bytes moved) must report 0, not stale speed.
	b.lastSpeedAt = b.lastSpeedAt.Add(-time.Second)
	if s, e := b.progressSpeedAndETA(50_000, 100_000); s != 0 || e != 0 {
		t.Fatalf("stalled = %d/%d, want 0/0", s, e)
	}
	// Done equals total (complete) reports neither speed nor ETA.
	if s, e := b.progressSpeedAndETA(100_000, 100_000); s != 0 || e != 0 {
		t.Fatalf("complete = %d/%d, want 0/0", s, e)
	}
}

func TestCreateTaskUploadStreamDirectRetryNowDuringBackoff(t *testing.T) {
	oldBase := DirectUploadRetryBaseDelay
	DirectUploadRetryBaseDelay = 30 * time.Second
	t.Cleanup(func() { DirectUploadRetryBaseDelay = oldBase })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	drv := &directUploadTestDriver{}
	fs, err := vfs.New(drv, vfs.Options{StorageDir: filepath.Join(t.TempDir(), "cache"), UploadDelay: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	defer stopTestVFS(t, fs)
	fs.Start(ctx)
	c := newTestCore(t, fs)
	c.mountContentDedup = map[string]bool{"retry-now.txt": true}
	payload := []byte("immediate retry payload")
	provider := &flakyDirectUploadSourceProvider{data: payload, failSecondOpen: true}
	c.SetUploadSourceProvider(provider)

	created, err := c.CreateTask(ctx, task.Request{
		Type:   task.TypeUploadStreamDirect,
		Detail: map[string]any{"auto_retry": true},
		Items: []task.Item{{
			ItemID:     "item",
			SourcePath: "token",
			DestPath:   "/retry-now.txt",
			Size:       int64(len(payload)),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Wait for the first attempt to fail and the backoff to start.
	var waiting task.Task
	for {
		current, err := c.GetTask(context.Background(), created.ID)
		if err != nil {
			t.Fatal(err)
		}
		if current.State == task.StateRetryWait {
			waiting = current
			break
		}
		if current.State == task.StateSucceeded || current.State == task.StateFailed {
			t.Fatalf("task reached %s before retry_wait: %+v", current.State, current)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !waiting.Capabilities.Retryable {
		t.Fatalf("retry_wait capabilities = %+v, want Retryable", waiting.Capabilities)
	}
	if waiting.NextAttempt.IsZero() {
		t.Fatalf("retry_wait task has no next_attempt_at: %+v", waiting)
	}

	provider.setFailSecondOpen(false)
	if err := c.RetryTask(context.Background(), waiting.ID); err != nil {
		t.Fatal(err)
	}
	// waitCoreTask's 3s deadline is far shorter than the 30s backoff, so a
	// success here proves the retry-now cut the wait short.
	finished := waitCoreTask(t, c, created.ID)
	if finished.State != task.StateSucceeded {
		t.Fatalf("retried task = %+v, want succeeded", finished)
	}
	if finished.RetryCount == 0 {
		t.Fatalf("retry count = 0, want the scheduled backoff counted")
	}
}

func TestCreateTaskUploadStreamDirectRetryWaitHeartbeats(t *testing.T) {
	oldBase := DirectUploadRetryBaseDelay
	oldBeat := DirectUploadRetryHeartbeatInterval
	DirectUploadRetryBaseDelay = 3 * time.Second
	DirectUploadRetryHeartbeatInterval = 40 * time.Millisecond
	t.Cleanup(func() {
		DirectUploadRetryBaseDelay = oldBase
		DirectUploadRetryHeartbeatInterval = oldBeat
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	drv := &directUploadTestDriver{}
	fs, err := vfs.New(drv, vfs.Options{StorageDir: filepath.Join(t.TempDir(), "cache"), UploadDelay: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	defer stopTestVFS(t, fs)
	fs.Start(ctx)
	c := newTestCore(t, fs)
	c.mountContentDedup = map[string]bool{"heartbeat.txt": true}
	payload := []byte("heartbeat payload")
	provider := &flakyDirectUploadSourceProvider{data: payload, failSecondOpen: true}
	c.SetUploadSourceProvider(provider)

	created, err := c.CreateTask(ctx, task.Request{
		Type:   task.TypeUploadStreamDirect,
		Detail: map[string]any{"auto_retry": true},
		Items: []task.Item{{
			ItemID:     "item",
			SourcePath: "token",
			DestPath:   "/heartbeat.txt",
			Size:       int64(len(payload)),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}

	for {
		current, err := c.GetTask(context.Background(), created.ID)
		if err != nil {
			t.Fatal(err)
		}
		if current.State == task.StateRetryWait {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	// While the 3s backoff runs, heartbeat updates must bump updated_at
	// repeatedly (40ms tick, observed over 400ms => comfortably many beats).
	updates := map[time.Time]bool{}
	observeUntil := time.Now().Add(400 * time.Millisecond)
	for time.Now().Before(observeUntil) {
		current, err := c.GetTask(context.Background(), created.ID)
		if err != nil {
			t.Fatal(err)
		}
		updates[current.UpdatedAt] = true
		time.Sleep(10 * time.Millisecond)
	}
	if len(updates) < 3 {
		t.Fatalf("distinct updated_at during backoff = %d, want >= 3 heartbeats", len(updates))
	}
}
