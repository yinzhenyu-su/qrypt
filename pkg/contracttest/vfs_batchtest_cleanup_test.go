package contracttest

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
	"github.com/yinzhenyu/qrypt/pkg/vfs/diagnostics"
)

// raceModelFS models the production window the nightly batchmove cleanup kept
// hitting (runs 43523916292 and 43644833782): a directory removal issued right
// after Remove runs while the delayed file deletes are still in flight, so the
// directory still has children on the backend and RemoveDir reports
// vfs.ErrDirNotEmpty. The wrapper delegates to a real VFS and only adds the
// in-flight bookkeeping, so the harness is exercised end to end.
type raceModelFS struct {
	vfs.FileSystem
	inner *vfs.VFS
	delay time.Duration

	mu       sync.Mutex
	inflight map[string]time.Time
}

func newRaceModelFS(inner *vfs.VFS, delay time.Duration) *raceModelFS {
	return &raceModelFS{FileSystem: inner, inner: inner, delay: delay, inflight: map[string]time.Time{}}
}

func (f *raceModelFS) DebugSnapshot() diagnostics.DebugSnapshot {
	return f.inner.DebugSnapshot()
}

func (f *raceModelFS) Remove(ctx context.Context, path string) error {
	if err := f.inner.Remove(ctx, path); err != nil {
		return err
	}
	f.mu.Lock()
	f.inflight[path] = time.Now().Add(f.delay)
	f.mu.Unlock()
	return nil
}

func (f *raceModelFS) RemoveDir(ctx context.Context, path string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := time.Now()
	for file, deadline := range f.inflight {
		if now.After(deadline) {
			delete(f.inflight, file)
			continue
		}
		if strings.HasPrefix(file, path+"/") {
			return fmt.Errorf("%w: %s (child delete still in flight: %s)", vfs.ErrDirNotEmpty, path, file)
		}
	}
	return f.inner.RemoveDir(ctx, path)
}

// TestBatchCleanupWaitsForAsyncDeletes pins the nightly batchmove cleanup
// failure. The harness used to remove the fixture files and then immediately
// remove their parent directories; the file deletes only *schedule* the remote
// removal, so the rmdir raced the delete timers and the cleanup step failed
// with vfs.ErrDirNotEmpty. cleanup must now wait for the pending deletes to
// land before removing the directories.
func TestBatchCleanupWaitsForAsyncDeletes(t *testing.T) {
	const deleteDelay = 10 * time.Millisecond
	inner, err := vfs.New(drive.NewFakeDriver(), vfs.Options{
		StorageDir:  t.TempDir(),
		UploadDelay: time.Millisecond,
		DeleteDelay: deleteDelay,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	inner.Start(ctx)
	defer func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer stopCancel()
		_ = inner.Close(stopCtx)
	}()
	fs := newRaceModelFS(inner, deleteDelay)
	const mount = "default"

	writeFixture := func(baseDir string, files int) []string {
		t.Helper()
		for _, dir := range []string{baseDir, baseDir + "/source", baseDir + "/dest"} {
			if _, err := fs.Mkdir(ctx, dir); err != nil {
				t.Fatalf("mkdir %s: %v", dir, err)
			}
		}
		paths := make([]string, 0, files)
		for i := 0; i < files; i++ {
			file := fmt.Sprintf("%s/dest/file-%04d.bin", baseDir, i)
			if err := fs.Create(ctx, file); err != nil {
				t.Fatalf("create %s: %v", file, err)
			}
			if _, err := fs.WriteAt(ctx, file, []byte("payload"), 0); err != nil {
				t.Fatalf("write %s: %v", file, err)
			}
			if err := fs.Flush(ctx, file); err != nil {
				t.Fatalf("flush %s: %v", file, err)
			}
			paths = append(paths, file)
		}
		if err := waitVFSSmokeIdle(ctx, fs, mount, 2*time.Minute, nil); err != nil {
			t.Fatalf("wait for fixture uploads: %v", err)
		}
		return paths
	}

	// The old ordering is reproduced first so the regression test fails for the
	// right reason if the in-flight model ever stops catching the race.
	t.Run("immediate_rmdir_races_delete_timers", func(t *testing.T) {
		baseDir := "/__qrypt_batchcleanup_race_test"
		files := writeFixture(baseDir, 2)
		for _, file := range files {
			if err := fs.Remove(ctx, file); err != nil {
				t.Fatalf("remove %s: %v", file, err)
			}
		}
		if err := fs.RemoveDir(ctx, baseDir+"/dest"); !errors.Is(err, vfs.ErrDirNotEmpty) {
			t.Fatalf("RemoveDir right after Remove = %v, want vfs.ErrDirNotEmpty", err)
		}
		// Drain the fixture so the shared VFS is clean for the next subtest.
		if err := waitVFSSmokeIdle(ctx, fs, mount, 2*time.Minute, nil); err != nil {
			t.Fatalf("drain deletes: %v", err)
		}
		for _, dir := range vfsDeleteOrder([]string{baseDir + "/dest", baseDir + "/source", baseDir}) {
			if err := removeVFSDirectory(ctx, fs, mount, dir, 2*time.Minute, nil); err != nil {
				t.Fatalf("drain %s: %v", dir, err)
			}
		}
	})

	t.Run("cleanup_waits_for_pending_deletes", func(t *testing.T) {
		baseDir := "/__qrypt_batchcleanup_test"
		destFiles := writeFixture(baseDir, 3)
		bt := newBatchTest(ctx, fs, "batchmove", mount, len(destFiles), int64(len("payload")))
		bt.baseDir = baseDir
		bt.files = destFiles

		bt.cleanup(destFiles, baseDir+"/dest", baseDir+"/source", baseDir)
		if !bt.cleaned {
			t.Fatalf("cleanup failed: %+v", bt.result.Steps)
		}
		if got := fsMountState(fs, mount); got.DeleteTimers != 0 || got.ActiveDeletes != 0 {
			t.Fatalf("cleanup left pending deletes: timers=%d active=%d", got.DeleteTimers, got.ActiveDeletes)
		}
		for _, dir := range []string{baseDir, baseDir + "/source", baseDir + "/dest"} {
			if _, err := inner.Stat(ctx, dir); !vfs.IsNotFound(err) {
				t.Fatalf("%s still present after cleanup: err=%v", dir, err)
			}
		}
	})
}

// TestVFSDeleteOrder pins the child-before-parent ordering the cleanup relies
// on: a parent can only be removed once its children are gone.
func TestVFSDeleteOrder(t *testing.T) {
	got := vfsDeleteOrder([]string{"/base", "/base/dest", "/base/dest/sub"})
	want := []string{"/base/dest/sub", "/base", "/base/dest"}
	// Deepest first; same-depth entries keep their relative order.
	if len(got) != 3 || got[0] != want[0] || got[len(got)-1] != "/base" {
		t.Fatalf("vfsDeleteOrder = %v, want parent last and deepest first", got)
	}
}
