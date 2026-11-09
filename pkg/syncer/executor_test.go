package sync

import (
	"context"
	"encoding/json"
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
)

// failingExecutorFS is a minimal destination filesystem for local→VFS sync.
// It fails one path at Create, records concurrent WriteAt calls, and leaves
// every other path working, which is exactly the failure-isolation contract
// the file worker pool must hold.
type failingExecutorFS struct {
	mu            sync.Mutex
	failPath      string
	delay         time.Duration
	created       map[string]bool
	written       map[string]int
	active        int
	maxConcurrent int
}

func newFailingExecutorFS(failPath string, delay time.Duration) *failingExecutorFS {
	return &failingExecutorFS{
		failPath: failPath,
		delay:    delay,
		created:  map[string]bool{},
		written:  map[string]int{},
	}
}

func (f *failingExecutorFS) Stat(context.Context, string) (drive.Entry, error) {
	return drive.Entry{ID: "dest", Name: "dest", Size: 1}, nil
}

func (f *failingExecutorFS) List(context.Context, string) ([]drive.Entry, error) {
	return nil, nil
}

func (f *failingExecutorFS) Read(context.Context, string, int64, int64) (io.ReadCloser, error) {
	return nil, errors.New("unexpected read")
}

func (f *failingExecutorFS) Create(_ context.Context, path string) error {
	if path == f.failPath {
		return errors.New("injected create failure")
	}
	f.mu.Lock()
	f.created[path] = true
	f.mu.Unlock()
	return nil
}

func (f *failingExecutorFS) WriteAt(_ context.Context, path string, data []byte, _ int64) (int, error) {
	f.mu.Lock()
	f.active++
	if f.active > f.maxConcurrent {
		f.maxConcurrent = f.active
	}
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		f.active--
		f.mu.Unlock()
	}()
	if f.delay > 0 {
		time.Sleep(f.delay)
	}
	f.mu.Lock()
	f.written[path] += len(data)
	f.mu.Unlock()
	return len(data), nil
}

func (*failingExecutorFS) Flush(context.Context, string) error { return nil }

func (*failingExecutorFS) Mkdir(context.Context, string) (drive.Entry, error) {
	return drive.Entry{ID: "dir", Name: "dir", IsDir: true}, nil
}

func (*failingExecutorFS) Remove(context.Context, string) error          { return nil }
func (*failingExecutorFS) RemoveDir(context.Context, string) error       { return nil }
func (*failingExecutorFS) Rename(context.Context, string, string) error  { return nil }
func (*failingExecutorFS) Truncate(context.Context, string, int64) error { return nil }

var _ executorFS = (*failingExecutorFS)(nil)

func TestExecutePlanFileWorkersIsolateFailures(t *testing.T) {
	useTestQryptHome(t)
	srcDir := t.TempDir()
	const fileCount = 8
	plan := make([]PlanEntry, 0, fileCount)
	for i := 0; i < fileCount; i++ {
		name := fmt.Sprintf("file-%d.txt", i)
		if err := os.WriteFile(filepath.Join(srcDir, name), []byte("x"), 0o644); err != nil {
			t.Fatalf("write source: %v", err)
		}
		plan = append(plan, PlanEntry{Path: name, Action: ActionAdd})
	}

	const failedName = "file-3.txt"
	failPath := "/dest/" + failedName
	fs := newFailingExecutorFS(failPath, 10*time.Millisecond)
	source := Target{Kind: TargetLocal, LocalPath: srcDir}
	destination := Target{Kind: TargetVFS, MountName: "test", VFSPath: "/dest"}
	session, err := NewSession(source, destination, SessionFlags{}, plan)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer session.Close()

	failed := ExecutePlan(context.Background(), fs, plan, source, destination, session, func(context.Context, any) error {
		return nil
	})
	if failed != 1 {
		t.Fatalf("failed ops = %d, want 1", failed)
	}

	fs.mu.Lock()
	maxConcurrent := fs.maxConcurrent
	created := make(map[string]bool, len(fs.created))
	for path := range fs.created {
		created[path] = true
	}
	written := make(map[string]int, len(fs.written))
	for path, n := range fs.written {
		written[path] = n
	}
	fs.mu.Unlock()

	if maxConcurrent > syncFileWorkers {
		t.Fatalf("max concurrent writes = %d, want at most %d", maxConcurrent, syncFileWorkers)
	}
	if maxConcurrent < 2 {
		t.Fatalf("max concurrent writes = %d, want at least 2", maxConcurrent)
	}
	if created[failPath] {
		t.Fatalf("failed path %q was created", failPath)
	}
	for i := 0; i < fileCount; i++ {
		path := fmt.Sprintf("/dest/file-%d.txt", i)
		if path == failPath {
			continue
		}
		if !created[path] {
			t.Fatalf("path %q was not created", path)
		}
		if written[path] != 1 {
			t.Fatalf("path %q writes = %d, want 1", path, written[path])
		}
	}

	pending := session.PendingOps()
	if len(pending) != 1 || pending[0].Path != failedName {
		t.Fatalf("pending ops = %+v, want only %s", pending, failedName)
	}

	journal := filepath.Join(PersistRoot(), SessionKey(source, destination), "state.jsonl")
	data, err := os.ReadFile(journal)
	if err != nil {
		t.Fatalf("read journal: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != fileCount {
		t.Fatalf("journal lines = %d, want %d", len(lines), fileCount)
	}
	okCount, failCount := 0, 0
	for _, line := range lines {
		var entry StateEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("parse journal line %q: %v", line, err)
		}
		if entry.OK {
			okCount++
			continue
		}
		failCount++
		if entry.Path != failedName {
			t.Fatalf("failed journal entry path = %q, want %q", entry.Path, failedName)
		}
	}
	if okCount != fileCount-1 || failCount != 1 {
		t.Fatalf("journal ok=%d fail=%d, want %d/1", okCount, failCount, fileCount-1)
	}
}
