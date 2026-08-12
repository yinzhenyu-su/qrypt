package core

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
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

type directUploadTestDriver struct {
	drive.UnsupportedOperations
	mu        sync.Mutex
	data      []byte
	sha1Hex   string
	putSource int
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
