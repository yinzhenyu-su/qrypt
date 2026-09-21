//go:build unix

package localfile

import (
	"context"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestWaitRejectsNonRegularFile(t *testing.T) {
	fifo := filepath.Join(t.TempDir(), "fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Wait(context.Background(), fifo, Options{Quiet: time.Millisecond, Poll: time.Millisecond})
	if err == nil || !strings.Contains(err.Error(), "is not a regular file") {
		t.Fatalf("Wait fifo err = %v, want not-a-regular-file error", err)
	}
}
