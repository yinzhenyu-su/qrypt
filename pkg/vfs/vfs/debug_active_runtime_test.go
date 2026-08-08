package vfs

import (
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/drivers/localfs"
)

func TestVFSDebugActiveRuntimeOwnsActiveState(t *testing.T) {
	fs, err := New(localfs.New(t.TempDir()), Options{Name: "cloud", StorageDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	runtime := newVFSDebugActiveRuntime(fs)

	opID := runtime.Begin(DebugActiveOp{Kind: "vfs_read", Path: "/file.txt", Extra: map[string]any{"phase": "start"}})
	if opID == 0 {
		t.Fatal("expected nonzero op id")
	}
	runtime.Update(opID, func(op *DebugActiveOp) {
		op.Phase = "read_range"
		op.Extra["phase"] = "updated"
	})

	ops := runtime.Snapshot()
	if len(ops) != 1 {
		t.Fatalf("ops = %+v", ops)
	}
	if ops[0].OpID == "" || ops[0].Mount != "cloud" || ops[0].State != "active" || ops[0].Phase != "read_range" {
		t.Fatalf("op defaults/update = %+v", ops[0])
	}
	ops[0].Extra["phase"] = "mutated"
	if runtime.Snapshot()[0].Extra["phase"] == "mutated" {
		t.Fatal("snapshot returned mutable extra map")
	}

	runtime.Finish(opID)
	if got := runtime.Snapshot(); len(got) != 0 {
		t.Fatalf("ops after finish = %+v", got)
	}
}
