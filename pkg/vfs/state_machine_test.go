package vfs_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/task"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

// readRemoteFile returns the contents of a file in the fake driver's root.
func readRemoteFile(t *testing.T, d *drive.FakeDriver, name string) (string, error) {
	t.Helper()
	entries, err := d.List(context.Background(), "0")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name == name {
			rc, err := d.Read(context.Background(), e, 0, -1)
			if err != nil {
				t.Fatal(err)
			}
			defer rc.Close()
			buf := new(strings.Builder)
			_, err = ioCopy(buf, rc)
			if err != nil {
				t.Fatal(err)
			}
			return buf.String(), nil
		}
	}
	return "", errors.New("not found")
}

// putSourceCount counts PutSource invocations in the fake's call log.
func putSourceCount(d *drive.FakeDriver) int {
	n := 0
	for _, call := range d.FakeCalls() {
		if strings.HasPrefix(call, "PutSource:") {
			n++
		}
	}
	return n
}

// waitPutSourceCount polls until the driver has recorded at least n
// PutSource calls (the call is recorded before the gate/delay, so this
// also observes calls that are still blocked).
func waitPutSourceCount(t *testing.T, d *drive.FakeDriver, n int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if putSourceCount(d) >= n {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("PutSource count = %d, want >= %d (calls: %v)", putSourceCount(d), n, d.FakeCalls())
}

func writeAndFlush(t *testing.T, fs *vfs.VFS, path, content string) {
	t.Helper()
	ctx := context.Background()
	if err := fs.Create(ctx, path); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.WriteAt(ctx, path, []byte(content), 0); err != nil {
		t.Fatal(err)
	}
	if err := fs.Flush(ctx, path); err != nil {
		t.Fatal(err)
	}
}

// TestUploadRetrySupersededByNewGeneration: the first PutSource fails
// (transient, so the worker requeues with backoff); before the retry
// fires, a new generation is written to the same path. The retry must
// notice the pending was superseded, skip the stale v1 payload and upload
// only the new generation - never clobbering v2 with v1.
func TestUploadRetrySupersededByNewGeneration(t *testing.T) {
	d := drive.NewFakeDriver()
	fs, err := vfs.New(d, vfs.Options{
		StorageDir:    t.TempDir(),
		CacheMaxBytes: 10 << 20,
		UploadDelay:   10 * time.Millisecond,
		UploadWorkers: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fs.Start(ctx)
	defer stopVFS(t, fs)

	d.FailAfter("PutSource", 1, errors.New("transient boom"))
	writeAndFlush(t, fs, "/gen.txt", "v1")
	waitPutSourceCount(t, d, 1) // v1 attempt failed and was requeued

	writeAndFlush(t, fs, "/gen.txt", "v2") // new generation before retry
	waitNoPending(t, fs)

	content, err := readRemoteFile(t, d, "gen.txt")
	if err != nil {
		t.Fatalf("remote file missing after retry: %v", err)
	}
	if content != "v2" {
		t.Fatalf("remote content = %q, want v2 (stale retry must not clobber)", content)
	}
	if n := putSourceCount(d); n != 2 {
		t.Fatalf("PutSource calls = %d, want 2 (v1 failed once, v2 succeeded once)", n)
	}
}

// TestUploadCancelDoesNotCommit: a PutSource parked on the fake's PutDelay
// must not commit when the lifecycle context is cancelled - cancellation
// wins over commit. PutDelay is the context-aware blocking point: the
// upload attempt is recorded before the delay, so waitPutSourceCount
// observes it while it is still blocked.
func TestUploadCancelDoesNotCommit(t *testing.T) {
	d := drive.NewFakeDriver()
	fs, err := vfs.New(d, vfs.Options{
		StorageDir:    t.TempDir(),
		CacheMaxBytes: 10 << 20,
		UploadDelay:   10 * time.Millisecond,
		UploadWorkers: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	fs.Start(ctx)
	defer stopVFS(t, fs)

	d.PutDelay = 10 * time.Second // context-aware block inside PutSource
	writeAndFlush(t, fs, "/c.txt", "data")
	waitPutSourceCount(t, d, 1)
	if _, err := readRemoteFile(t, d, "c.txt"); err == nil {
		t.Fatal("upload committed while still blocked on the gate")
	}

	cancel() // lifecycle ctx; the gated PutSource must abort, not commit
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		_, err := readRemoteFile(t, d, "c.txt")
		if err != nil {
			time.Sleep(50 * time.Millisecond)
			continue
		}
		t.Fatal("cancelled upload committed to the remote")
	}
	if n := putSourceCount(d); n != 1 {
		t.Fatalf("PutSource calls = %d, want 1 (no retry after cancel)", n)
	}
}

// TestRecoveredPendingFailsAgainThenSucceeds: a pending upload persisted in
// the journal, recovered by a fresh instance, fails again on its first
// attempt, and then succeeds on retry. The fault counter lives on the
// shared driver, so it survives across the two VFS instances.
func TestRecoveredPendingFailsAgainThenSucceeds(t *testing.T) {
	d := drive.NewFakeDriver()
	cache := t.TempDir()

	// Generation 1: persist a frozen pending without starting workers.
	first, err := vfs.New(d, vfs.Options{StorageDir: cache, CacheMaxBytes: 10 << 20})
	if err != nil {
		t.Fatal(err)
	}
	writeAndFlush(t, first, "/r.txt", "recover me")
	stopVFS(t, first)

	// Recovery: the first PutSource attempt fails (transient), then the
	// 500ms backoff retry succeeds.
	d.FailAfter("PutSource", 1, errors.New("transient boom"))
	second, err := vfs.New(d, vfs.Options{
		StorageDir:    cache,
		CacheMaxBytes: 10 << 20,
		UploadDelay:   10 * time.Millisecond,
		UploadWorkers: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	sctx, scancel := context.WithCancel(context.Background())
	defer scancel()
	second.Start(sctx)
	defer stopVFS(t, second)

	waitNoPending(t, second) // retry lands within the 3s poll

	content, err := readRemoteFile(t, d, "r.txt")
	if err != nil {
		t.Fatalf("recovered upload missing after retry: %v", err)
	}
	if content != "recover me" {
		t.Fatalf("recovered content = %q", content)
	}
	if n := putSourceCount(d); n != 2 {
		t.Fatalf("PutSource calls = %d, want 2 (fail then retry)", n)
	}
}

// TestDeleteTimerRestoredFileSurvives: removing a file arms a delayed
// delete; recreating the same path before the timer fires cancels the
// delete and the recreated file must survive past the original deadline.
func TestDeleteTimerRestoredFileSurvives(t *testing.T) {
	d := drive.NewFakeDriver()
	fs, err := vfs.New(d, vfs.Options{
		StorageDir:    t.TempDir(),
		CacheMaxBytes: 10 << 20,
		UploadDelay:   10 * time.Millisecond,
		DeleteDelay:   500 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fs.Start(ctx)
	defer stopVFS(t, fs)

	writeAndFlush(t, fs, "/f.txt", "v1")
	waitNoPending(t, fs) // v1 is on the remote before removal

	if err := fs.Remove(ctx, "/f.txt"); err != nil {
		t.Fatal(err)
	}
	writeAndFlush(t, fs, "/f.txt", "v2") // restore before the 500ms timer
	waitNoPending(t, fs)

	time.Sleep(700 * time.Millisecond) // let the original delete fire
	content, err := readRemoteFile(t, d, "f.txt")
	if err != nil {
		t.Fatalf("restored file was deleted by the stale timer: %v", err)
	}
	if content != "v2" {
		t.Fatalf("restored content = %q, want v2", content)
	}
}

// ioCopy is a tiny io.Copy for the read-back helper.
func ioCopy(dst *strings.Builder, src interface {
	Read([]byte) (int, error)
}) (int64, error) {
	buf := make([]byte, 32*1024)
	var n int64
	for {
		m, err := src.Read(buf)
		dst.Write(buf[:m])
		n += int64(m)
		if err != nil {
			if err.Error() == "EOF" {
				return n, nil
			}
			return n, err
		}
	}
}

// TestThreeGenerationsFinalVisibility: three rapid writes to the same path
// must converge on the newest generation - no earlier payload may be the
// last one committed, regardless of how the worker interleaves with the
// writes.
func TestThreeGenerationsFinalVisibility(t *testing.T) {
	d := drive.NewFakeDriver()
	fs, err := vfs.New(d, vfs.Options{
		StorageDir:    t.TempDir(),
		CacheMaxBytes: 10 << 20,
		UploadDelay:   10 * time.Millisecond,
		UploadWorkers: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fs.Start(ctx)
	defer stopVFS(t, fs)

	writeAndFlush(t, fs, "/g.txt", "v1")
	writeAndFlush(t, fs, "/g.txt", "v2")
	writeAndFlush(t, fs, "/g.txt", "v3")
	waitNoPending(t, fs)

	content, err := readRemoteFile(t, d, "g.txt")
	if err != nil {
		t.Fatalf("remote file missing: %v", err)
	}
	if content != "v3" {
		t.Fatalf("final remote content = %q, want v3", content)
	}
	if n := putSourceCount(d); n < 1 {
		t.Fatal("no upload attempt happened")
	}
}

// TestDeleteRetryAfterRestoreSkipsNewFile: a delete that failed on the
// remote is retried (task Retry) after the same path was recreated; the
// retry must notice the delete was restored and skip, leaving the new file
// intact.
func TestDeleteRetryAfterRestoreSkipsNewFile(t *testing.T) {
	d := drive.NewFakeDriver()
	fs, err := vfs.New(d, vfs.Options{
		StorageDir:    t.TempDir(),
		CacheMaxBytes: 10 << 20,
		UploadDelay:   10 * time.Millisecond,
		DeleteDelay:   10 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fs.Start(ctx)
	defer stopVFS(t, fs)

	writeAndFlush(t, fs, "/f.txt", "v1")
	waitNoPending(t, fs)

	// The remote remove fails once; the delete task lands in failed state.
	d.FailAfter("Remove", 1, errors.New("transient remove failure"))
	if err := fs.Remove(ctx, "/f.txt"); err != nil {
		t.Fatal(err)
	}
	waitDeleteTaskState(t, fs, "/f.txt", task.StateFailed)

	var id string
	for _, tk := range fs.Tasks(task.Filter{}) {
		if tk.Type == task.TypeDeleteRemote && tk.Path == "/f.txt" {
			id = tk.ID
		}
	}
	if id == "" {
		t.Fatalf("no delete task for /f.txt: %v", fs.Tasks(task.Filter{}))
	}

	// Recreate the path: restore must revoke the failed delete (the deleted
	// marker is removed), so retrying the stale task is a no-op that cannot
	// touch the new file.
	writeAndFlush(t, fs, "/f.txt", "v2")
	waitNoPending(t, fs) // v2 must be on the remote before the retry check
	for _, tk := range fs.Tasks(task.Filter{}) {
		if tk.Type == task.TypeDeleteRemote && tk.Path == "/f.txt" {
			t.Fatalf("restored path still has a delete task: %+v", tk)
		}
	}
	if err := fs.RetryTask(ctx, id); err == nil {
		t.Fatal("retrying a restored delete must fail (task gone)")
	}

	content, err := readRemoteFile(t, d, "f.txt")
	if err != nil {
		t.Fatalf("recreated file was deleted by the stale task: %v", err)
	}
	if content != "v2" {
		t.Fatalf("recreated content = %q, want v2", content)
	}
}

// waitDeleteTaskState polls the task list until a delete task for path
// reaches the wanted state (StateFailed or StateScheduled).
func waitDeleteTaskState(t *testing.T, fs *vfs.VFS, path string, want task.State) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		for _, tk := range fs.Tasks(task.Filter{}) {
			if tk.Type == task.TypeDeleteRemote && tk.Path == path && tk.State == want {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("delete task for %q never reached %s (tasks: %+v)", path, want, fs.Tasks(task.Filter{}))
}
