package vfs

import (
	"context"
	"strings"
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/drivers/localfs"
	"github.com/yinzhenyu/qrypt/pkg/vfs/faultinject"
)

// TestNamespaceFaultRoutingAcrossMounts: namespace-level fault IDs are
// mount-scoped (mount:fault_id), so clearing a fault on one mount never
// touches another and the owning mount is always the one cleared.
func TestNamespaceFaultRoutingAcrossMounts(t *testing.T) {
	ctx := context.Background()
	alpha, err := New(localfs.New(t.TempDir()), Options{Name: "alpha", StorageDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer alpha.Close(context.Background())
	beta, err := New(localfs.New(t.TempDir()), Options{Name: "beta", StorageDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer beta.Close(context.Background())
	ns, err := NewNamespace([]Mount{{Name: "alpha", FS: alpha}, {Name: "beta", FS: beta}})
	if err != nil {
		t.Fatal(err)
	}

	// Inject one fault per mount.
	alphaResult, err := ns.DebugInjectUploadCancel(ctx, faultinject.DebugUploadCancelRequest{Path: "/alpha/file.txt", Phase: "uploading"})
	if err != nil {
		t.Fatal(err)
	}
	betaResult, err := ns.DebugInjectUploadCancel(ctx, faultinject.DebugUploadCancelRequest{Path: "/beta/other.txt", Phase: "uploading"})
	if err != nil {
		t.Fatal(err)
	}
	if alphaResult.ID == betaResult.ID {
		t.Fatalf("namespace fault ids must be unique, both = %q", alphaResult.ID)
	}
	if !strings.HasPrefix(alphaResult.ID, "alpha:") || !strings.HasPrefix(betaResult.ID, "beta:") {
		t.Fatalf("ids must be mount-scoped: alpha=%q beta=%q", alphaResult.ID, betaResult.ID)
	}

	// Clearing alpha's fault must NOT clear beta's.
	if err := ns.DebugClearUploadCancel(ctx, alphaResult.ID); err != nil {
		t.Fatal(err)
	}
	faults := ns.DebugUploadCancelFaults(ctx)
	if len(faults) != 1 {
		t.Fatalf("faults after clearing alpha = %+v, want only beta", faults)
	}
	if faults[0].ID != betaResult.ID {
		t.Fatalf("remaining fault = %q, want %q", faults[0].ID, betaResult.ID)
	}

	// Clearing beta's fault empties everything.
	if err := ns.DebugClearUploadCancel(ctx, betaResult.ID); err != nil {
		t.Fatal(err)
	}
	if got := ns.DebugUploadCancelFaults(ctx); len(got) != 0 {
		t.Fatalf("faults after clearing beta = %+v, want none", got)
	}

	// Unknown mount in id must fail loudly (no silent cross-mount cleanup).
	if err := ns.DebugClearUploadCancel(ctx, "nope:upload-cancel-1"); err == nil {
		t.Fatal("clearing unknown mount should fail")
	}
	// Malformed id must fail.
	if err := ns.DebugClearUploadCancel(ctx, "no-colon"); err == nil {
		t.Fatal("clearing malformed id should fail")
	}
}

// TestNamespaceFaultIDsUniquenessUnderDuplicateRegistrySequences: two
// mounts each numbering from 1 still produce distinct namespace IDs.
func TestNamespaceFaultIDsUniquenessUnderDuplicateRegistrySequences(t *testing.T) {
	ctx := context.Background()
	alpha, err := New(drive.NewFakeDriver(), Options{Name: "alpha", StorageDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer alpha.Close(context.Background())
	beta, err := New(drive.NewFakeDriver(), Options{Name: "beta", StorageDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer beta.Close(context.Background())
	ns, err := NewNamespace([]Mount{{Name: "alpha", FS: alpha}, {Name: "beta", FS: beta}})
	if err != nil {
		t.Fatal(err)
	}

	a, err := ns.DebugInjectUploadCancel(ctx, faultinject.DebugUploadCancelRequest{Path: "/alpha/f.txt", AfterBytes: 1})
	if err != nil {
		t.Fatal(err)
	}
	b, err := ns.DebugInjectUploadCancel(ctx, faultinject.DebugUploadCancelRequest{Path: "/beta/g.txt", AfterBytes: 1})
	if err != nil {
		t.Fatal(err)
	}
	if a.ID == b.ID {
		t.Fatalf("ids collide: %q", a.ID)
	}
}

// TestNamespaceFaultRoutingUsesMountKeyNotVFSName: the namespace mount
// key ("photos") must be used in the fault ID even when VFS.name differs
// ("backend-a"); clearing via the returned ID must route correctly.
func TestNamespaceFaultRoutingUsesMountKeyNotVFSName(t *testing.T) {
	ctx := context.Background()
	// VFS.name differs from the namespace mount key.
	backend, err := New(localfs.New(t.TempDir()), Options{Name: "backend-a", StorageDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close(context.Background())
	ns, err := NewNamespace([]Mount{{Name: "photos", FS: backend}})
	if err != nil {
		t.Fatal(err)
	}

	result, err := ns.DebugInjectUploadCancel(ctx, faultinject.DebugUploadCancelRequest{Path: "/photos/photo.jpg", Phase: "uploading"})
	if err != nil {
		t.Fatal(err)
	}
	wantPrefix := "photos:"
	if len(result.ID) <= len(wantPrefix) || result.ID[:len(wantPrefix)] != wantPrefix {
		t.Fatalf("fault id = %q, want mount-key prefix %q (not VFS name)", result.ID, wantPrefix)
	}

	if err := ns.DebugClearUploadCancel(ctx, result.ID); err != nil {
		t.Fatalf("clear via mount-key id failed: %v", err)
	}
	if got := ns.DebugUploadCancelFaults(ctx); len(got) != 0 {
		t.Fatalf("faults after clear = %+v, want none", got)
	}
}
