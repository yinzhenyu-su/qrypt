package observe

import (
	"testing"

	"github.com/yinzhenyu/qrypt/internal/vfs/vfstypes"
)

func TestActiveStoreOwnsActiveState(t *testing.T) {
	store := NewActiveStore("cloud")

	opID := store.Begin(vfstypes.DebugActiveOp{Kind: "vfs_read", Path: "/file.txt", Extra: map[string]any{"phase": "start"}})
	if opID == 0 {
		t.Fatal("expected nonzero op id")
	}
	store.Update(opID, func(op *vfstypes.DebugActiveOp) {
		op.Phase = "read_range"
		op.Extra["phase"] = "updated"
	})

	ops := store.Snapshot()
	if len(ops) != 1 {
		t.Fatalf("ops = %+v", ops)
	}
	if ops[0].OpID == "" || ops[0].Mount != "cloud" || ops[0].State != "active" || ops[0].Phase != "read_range" {
		t.Fatalf("op defaults/update = %+v", ops[0])
	}
	ops[0].Extra["phase"] = "mutated"
	if store.Snapshot()[0].Extra["phase"] == "mutated" {
		t.Fatal("snapshot returned mutable extra map")
	}

	store.Finish(opID)
	if got := store.Snapshot(); len(got) != 0 {
		t.Fatalf("ops after finish = %+v", got)
	}
}
