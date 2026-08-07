package vfs

import (
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drivers/localfs"
)

func TestDebugUploadFaultRuntimeOwnsCancelFaultState(t *testing.T) {
	fs, err := New(localfs.New(t.TempDir()), Options{StorageDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	runtime := newVFSDebugUploadFaultRuntime(fs)
	now := time.Now()

	runtime.PutCancelFault(&debugUploadCancelFault{
		ID:        "expired",
		Path:      "/old.txt",
		Once:      true,
		CreatedAt: now.Add(-2 * time.Minute),
		ExpiresAt: now.Add(-time.Minute),
	})
	runtime.PutCancelFault(&debugUploadCancelFault{
		ID:        "active",
		Path:      "/file.txt",
		Once:      true,
		CreatedAt: now,
		ExpiresAt: now.Add(time.Minute),
	})

	faults := runtime.CancelFaults(now)
	if len(faults) != 1 || faults[0].ID != "active" {
		t.Fatalf("faults = %+v, want only active", faults)
	}
	if match := runtime.MatchCancelFault(now, "/missing.txt", ""); match != nil {
		t.Fatalf("unexpected match for missing Path: %+v", match)
	}
	match := runtime.MatchCancelFault(now, "/file.txt", "")
	if match == nil || match.ID != "active" || match.MatchedPath != "/file.txt" {
		t.Fatalf("match = %+v, want active matched path", match)
	}

	runtime.MarkCancelFaultFired("active", now)
	if faults := runtime.CancelFaults(now); len(faults) != 0 {
		t.Fatalf("once fired fault should be removed, got %+v", faults)
	}
}
