package upload

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/logging"
)

// writeOrphanedStagingTemp creates a staging temp file that the store
// constructor cleans up, which makes it emit a line during construction.
func writeOrphanedStagingTemp(t *testing.T, dir string) {
	t.Helper()
	stagingDir := filepath.Join(dir, "staging")
	if err := os.MkdirAll(stagingDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stagingDir, "leftover.staging.upload-1"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// A store built for a mount writes that mount into the durable log line, so a
// multi-mount log file is attributable without guessing from paths.
func TestPendingStoreLogsCarryMount(t *testing.T) {
	previous := logging.L
	logPath := filepath.Join(t.TempDir(), "qrypt.log")
	testLogger, err := logging.New("debug", logPath, logPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	logging.L = testLogger
	t.Cleanup(func() {
		_ = testLogger.Close()
		logging.L = previous
	})

	dir := t.TempDir()
	writeOrphanedStagingTemp(t, dir)
	if _, err := NewPendingStore(dir, "mount-x"); err != nil {
		t.Fatal(err)
	}
	_ = testLogger.Close()

	written, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(written), `mount="mount-x" [CACHE] cleaned 1 orphaned staging upload files`) {
		t.Fatalf("durable log line is not mount-attributed:\n%s", written)
	}
}

// Without a mount the line keeps its original shape: no field, no format
// change for call sites that were never mount-scoped.
func TestPendingStoreWithoutMountKeepsLineShape(t *testing.T) {
	previous := logging.L
	logPath := filepath.Join(t.TempDir(), "qrypt.log")
	testLogger, err := logging.New("debug", logPath, logPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	logging.L = testLogger
	t.Cleanup(func() {
		_ = testLogger.Close()
		logging.L = previous
	})

	dir := t.TempDir()
	writeOrphanedStagingTemp(t, dir)
	if _, err := NewPendingStore(dir); err != nil {
		t.Fatal(err)
	}
	_ = testLogger.Close()

	written, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(written), "mount=") {
		t.Fatalf("unscoped store emitted a mount field:\n%s", written)
	}
	if !strings.Contains(string(written), "[CACHE] cleaned 1 orphaned staging upload files") {
		t.Fatalf("unscoped line lost its message:\n%s", written)
	}
}
