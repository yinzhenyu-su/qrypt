package vfs

import (
	"context"
	"strings"
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs/diagnostics"
	"github.com/yinzhenyu/qrypt/pkg/vfs/faultinject"
)

// Compile-time: Namespace keeps the full diagnostics capability surface
// that drivecopy and CLI rely on.
var (
	_ diagnostics.DebugResolver           = (*Namespace)(nil)
	_ diagnostics.RemoteIDResolver        = (*Namespace)(nil)
	_ diagnostics.DebugConsistencyChecker = (*Namespace)(nil)
)

// TestNamespaceDebugResolveAdapters: namespace resolve adapters must
// prefix mount-relative results with the mount name.
func TestNamespaceDebugResolveAdapters(t *testing.T) {
	ctx := context.Background()
	mount, err := New(drive.NewFakeDriver(), Options{Name: "backend-a", StorageDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer mount.Close(ctx)
	ns, err := NewNamespace([]Mount{{Name: "photos", FS: mount}})
	if err != nil {
		t.Fatal(err)
	}

	info, err := ns.DebugResolve(ctx, "/photos/photo.jpg", false)
	if err != nil {
		t.Fatal(err)
	}
	if info.Path != "/photos/photo.jpg" || info.Mount != "photos" || info.Parent != "/photos" {
		t.Fatalf("resolve adapter = %+v", info)
	}

	consistency, err := ns.DebugConsistency(ctx, "/photos/photo.jpg")
	if err != nil {
		t.Fatal(err)
	}
	if consistency.Path != "/photos/photo.jpg" {
		t.Fatalf("consistency adapter path = %q", consistency.Path)
	}

	// Remote-ID resolution: unknown IDs error; the root path resolves.
	if _, _, err := ns.DebugResolveByRemoteID(ctx, "nope"); err == nil {
		t.Fatal("unknown remote id should fail")
	}
}

// TestNamespaceDebugResolveByRemoteIDAmbiguity: when two mounts report
// the same remote ID, the namespace-level lookup must refuse to guess
// instead of returning whichever mount the map order hits first.
func TestNamespaceDebugResolveByRemoteIDAmbiguity(t *testing.T) {
	ctx := context.Background()
	mountA, err := New(drive.NewFakeDriver(), Options{Name: "backend-a", StorageDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer mountA.Close(ctx)
	mountB, err := New(drive.NewFakeDriver(), Options{Name: "backend-b", StorageDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer mountB.Close(ctx)
	ns, err := NewNamespace([]Mount{{Name: "photos", FS: mountA}, {Name: "docs", FS: mountB}})
	if err != nil {
		t.Fatal(err)
	}

	// Both fake drivers map the same remote ID (they share "0" semantics),
	// so any ID that resolves in one also resolves in the other.
	if _, _, err := ns.DebugResolveByRemoteID(ctx, "0"); err == nil {
		t.Fatal("ambiguous remote ID must be rejected")
	} else if !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("error should mention ambiguity: %v", err)
	}
}

// Capability assertions: VFS and Namespace must keep the full diagnostics
// surface (so future file splits can never silently drop a method).
var (
	// RemoteIDResolver is namespace-scoped (returns the owning mount), so
	// the single-mount VFS does not implement it.
	_ diagnostics.DebugResolver             = (*VFS)(nil)
	_ diagnostics.DebugConsistencyChecker   = (*VFS)(nil)
	_ diagnostics.DebugStagingInspector     = (*VFS)(nil)
	_ diagnostics.DebugMountSnapshotter     = (*VFS)(nil)
	_ diagnostics.DebugSnapshotProvider     = (*VFS)(nil)
	_ diagnostics.MountHealthChecker        = (*VFS)(nil)
	_ diagnostics.DriverProvider            = (*VFS)(nil)
	_ diagnostics.DebugActiveProvider       = (*VFS)(nil)
	_ faultinject.DebugUploadCancelInjector = (*VFS)(nil)

	_ diagnostics.DebugResolver             = (*Namespace)(nil)
	_ diagnostics.RemoteIDResolver          = (*Namespace)(nil)
	_ diagnostics.DebugConsistencyChecker   = (*Namespace)(nil)
	_ diagnostics.DebugStagingInspector     = (*Namespace)(nil)
	_ diagnostics.DebugMountSnapshotter     = (*Namespace)(nil)
	_ diagnostics.DebugSnapshotProvider     = (*Namespace)(nil)
	_ diagnostics.MountHealthChecker        = (*Namespace)(nil)
	_ diagnostics.DriverProvider            = (*Namespace)(nil)
	_ diagnostics.DebugActiveProvider       = (*Namespace)(nil)
	_ faultinject.DebugUploadCancelInjector = (*Namespace)(nil)
)
