package vfs_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/drivers/localfs"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
	"github.com/yinzhenyu/qrypt/pkg/vfs/faultinject"
)

// newDebugReadFaultFixture returns a VFS over a remote directory holding one
// file, plus the namespace that mounts it as "local".
func newDebugReadFaultFixture(t *testing.T) (*vfs.VFS, *vfs.Namespace, string) {
	t.Helper()
	ctx := context.Background()
	remote := t.TempDir()
	if err := os.WriteFile(filepath.Join(remote, "data.bin"), []byte("payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(remote, "dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	drv := localfs.New(remote)
	if err := drv.Init(ctx); err != nil {
		t.Fatal(err)
	}
	fs, err := vfs.New(drv, vfs.Options{Name: "local", StorageDir: t.TempDir(), CacheMaxBytes: 10 << 20})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stopVFS(t, fs) })
	ns, err := vfs.NewNamespace([]vfs.Mount{{Name: "local", FS: fs}})
	if err != nil {
		t.Fatal(err)
	}
	return fs, ns, remote
}

func TestVFSDebugReadFaultLifecycle(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fs, _, remote := newDebugReadFaultFixture(t)

	fault, err := fs.DebugInjectReadFault(ctx, faultinject.DebugReadFaultRequest{Path: "/data.bin", Kind: faultinject.ReadFaultStall})
	if err != nil {
		t.Fatal(err)
	}
	wantID := filepath.Join(remote, "data.bin")
	if fault.Path != "/data.bin" || fault.EntryID != wantID {
		t.Fatalf("fault = %+v, want path /data.bin with entry id %q", fault, wantID)
	}
	if fault.ID == "" {
		t.Fatal("armed fault has no id")
	}
	if faults := fs.DebugReadFaults(ctx); len(faults) != 1 || faults[0].ID != fault.ID {
		t.Fatalf("faults = %+v, want the armed fault", faults)
	}

	if err := fs.DebugClearReadFault(ctx, fault.ID); err != nil {
		t.Fatal(err)
	}
	if faults := fs.DebugReadFaults(ctx); len(faults) != 0 {
		t.Fatalf("faults after clear = %+v, want none", faults)
	}

	// An empty id clears every armed fault.
	if _, err := fs.DebugInjectReadFault(ctx, faultinject.DebugReadFaultRequest{Path: "/data.bin", Kind: faultinject.ReadFaultStall}); err != nil {
		t.Fatal(err)
	}
	if err := fs.DebugClearReadFault(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if faults := fs.DebugReadFaults(ctx); len(faults) != 0 {
		t.Fatalf("faults after clear-all = %+v, want none", faults)
	}
}

func TestVFSDebugReadFaultRejectsUnusablePath(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fs, _, _ := newDebugReadFaultFixture(t)

	if _, err := fs.DebugInjectReadFault(ctx, faultinject.DebugReadFaultRequest{Path: "/dir", Kind: faultinject.ReadFaultStall}); err == nil {
		t.Fatal("expected a directory to be rejected")
	}
	if _, err := fs.DebugInjectReadFault(ctx, faultinject.DebugReadFaultRequest{Path: "/missing.bin", Kind: faultinject.ReadFaultStall}); err == nil {
		t.Fatal("expected a missing path to be rejected")
	}

	// An entry id arms the fault without a path lookup.
	byID, err := fs.DebugInjectReadFault(ctx, faultinject.DebugReadFaultRequest{EntryID: "remote-id", Kind: faultinject.ReadFaultStatus, Status: 503})
	if err != nil {
		t.Fatal(err)
	}
	if byID.Path != "" || byID.EntryID != "remote-id" {
		t.Fatalf("fault = %+v, want entry-id fault", byID)
	}
}

func TestVFSDebugReadFaultHonoursCancelledContext(t *testing.T) {
	t.Parallel()
	fs, _, _ := newDebugReadFaultFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := fs.DebugInjectReadFault(ctx, faultinject.DebugReadFaultRequest{EntryID: "remote-id", Kind: faultinject.ReadFaultStall}); err == nil {
		t.Fatal("expected the cancelled context error")
	}
	if err := fs.DebugClearReadFault(ctx, "any"); err == nil {
		t.Fatal("expected the cancelled context error")
	}
	if faults := fs.DebugReadFaults(ctx); faults != nil {
		t.Fatalf("faults = %+v, want nil for a cancelled context", faults)
	}
}

func TestNamespaceDebugReadFaultPathsAreMountQualified(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	_, ns, _ := newDebugReadFaultFixture(t)

	fault, err := ns.DebugInjectReadFault(ctx, faultinject.DebugReadFaultRequest{Path: "/local/data.bin", Kind: faultinject.ReadFaultStall})
	if err != nil {
		t.Fatal(err)
	}
	if fault.Path != "/local/data.bin" {
		t.Fatalf("fault path = %q, want /local/data.bin", fault.Path)
	}
	faults := ns.DebugReadFaults(ctx)
	if len(faults) != 1 || faults[0].Path != "/local/data.bin" {
		t.Fatalf("faults = %+v, want one mount-qualified fault", faults)
	}

	if _, err := ns.DebugInjectReadFault(ctx, faultinject.DebugReadFaultRequest{Path: "/local", Kind: faultinject.ReadFaultStall}); err == nil {
		t.Fatal("expected the namespace root to be rejected")
	}
	if _, err := ns.DebugInjectReadFault(ctx, faultinject.DebugReadFaultRequest{Path: "  ", Kind: faultinject.ReadFaultStall}); err == nil {
		t.Fatal("expected a blank path to be rejected")
	}
	if _, err := ns.DebugInjectReadFault(ctx, faultinject.DebugReadFaultRequest{Path: "/unknown/data.bin", Kind: faultinject.ReadFaultStall}); err == nil {
		t.Fatal("expected an unknown mount to be rejected")
	}

	if err := ns.DebugClearReadFault(ctx, fault.ID); err != nil {
		t.Fatal(err)
	}
	if faults := ns.DebugReadFaults(ctx); len(faults) != 0 {
		t.Fatalf("faults after clear = %+v, want none", faults)
	}
}

func TestNamespaceDebugInvalidateReadCacheRoutesToMount(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fs, ns, _ := newDebugReadFaultFixture(t)

	name, err := ns.DebugInvalidateReadCache(ctx, "/local/data.bin")
	if err != nil {
		t.Fatal(err)
	}
	if name != "local" {
		t.Fatalf("mount name = %q, want local", name)
	}
	if name, err := fs.DebugInvalidateReadCache(ctx, "/data.bin"); err != nil || name != "local" {
		t.Fatalf("VFS invalidate = %q, %v; want local, nil", name, err)
	}
	if _, err := fs.DebugInvalidateReadCache(ctx, "/dir"); err == nil {
		t.Fatal("expected a directory to be rejected")
	}
	if _, err := fs.DebugInvalidateReadCache(ctx, "/missing.bin"); err == nil {
		t.Fatal("expected a missing path to be rejected")
	}
	if _, err := ns.DebugInvalidateReadCache(ctx, "/local"); err == nil {
		t.Fatal("expected the namespace root to be rejected")
	}

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := fs.DebugInvalidateReadCache(cancelled, "/data.bin"); err == nil {
		t.Fatal("expected the cancelled context error")
	}
}
