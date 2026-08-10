package vfs

import (
	"context"
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

// Compile-time: Namespace keeps the full diagnostics capability surface
// that drivecopy and CLI rely on.
var (
	_ DebugResolver           = (*Namespace)(nil)
	_ RemoteIDResolver        = (*Namespace)(nil)
	_ DebugConsistencyChecker = (*Namespace)(nil)
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
