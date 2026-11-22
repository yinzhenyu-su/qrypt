package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/vfs/diagnostics"

	clifs "github.com/yinzhenyu/qrypt/internal/cli/fs"
	cliruntime "github.com/yinzhenyu/qrypt/internal/cli/runtime"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

func TestPrintPendingVerboseIncludesDebugState(t *testing.T) {
	tmp := t.TempDir()
	localPath := filepath.Join(tmp, "file.staging")
	if err := os.WriteFile(localPath, []byte("abc"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	clifs.PrintPendingVerbose(&out, []vfs.PendingUpload{{
		Path:       "/file.txt",
		Size:       4,
		LocalPath:  localPath,
		RetryCount: 1,
		LastError:  "boom",
	}})
	text := out.String()
	for _, want := range []string{"/file.txt", "size-mismatch(3)", "boom", "RETRY"} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected verbose pending output to contain %q, got:\n%s", want, text)
		}
	}
}

// waitPendingEmpty waits for the upload engine to drain. Both build paths
// return a *vfs.VFS or *vfs.Namespace, which expose the event-driven drain
// primitive, so the engine broadcasts every transition the answer depends on
// instead of the caller polling for it.
func waitPendingEmpty(t *testing.T, fs builtFS) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if waiter, ok := fs.(vfs.UploadIdleWaiter); ok {
		if err := waiter.WaitUploadsIdle(ctx); err != nil {
			t.Fatalf("pending uploads did not drain: %v (pending=%+v)", err, cliruntime.PendingFiles(fs))
		}
		if active := activeUploadCount(fs); active != 0 {
			t.Fatalf("active uploads still reported after the engine drained: %d", active)
		}
		return
	}
	// Compatibility fallback for a builtFS that does not expose the drain
	// primitive (no build path does today).
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if len(cliruntime.PendingFiles(fs)) == 0 && activeUploadCount(fs) == 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("pending uploads did not drain: %+v", cliruntime.PendingFiles(fs))
}

func activeUploadCount(fs vfs.FileSystem) int {
	snapshotter, ok := fs.(interface {
		DebugSnapshot() diagnostics.DebugSnapshot
	})
	if !ok {
		return 0
	}
	count := 0
	for _, mount := range snapshotter.DebugSnapshot().Mounts {
		count += len(mount.ActiveUploads())
	}
	return count
}

func waitPathMissing(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		_, lastErr = os.Stat(path)
		if os.IsNotExist(lastErr) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("path still exists: %s err=%v", path, lastErr)
}

func TestJournalMaintenanceCommandsWereRemoved(t *testing.T) {
	for _, sub := range []string{"replay", "prune"} {
		root := NewRootCommand()
		root.SetOut(&bytes.Buffer{})
		root.SetErr(&bytes.Buffer{})
		root.SetArgs([]string{"fs", "journal", sub, "--cache-dir", t.TempDir()})
		err := root.Execute()
		if err == nil {
			t.Fatalf("journal %s must be removed", sub)
		}
		if !strings.Contains(err.Error(), "qrypt core ops reconcile") {
			t.Fatalf("journal %s error = %v, want a pointer to core ops reconcile", sub, err)
		}
	}

	help := runCommand(t, "fs", "journal", "--help")
	if strings.Contains(help, "replay") || strings.Contains(help, "prune") {
		t.Fatalf("journal help still advertises maintenance commands:\n%s", help)
	}
}
