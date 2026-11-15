package vfs_test

import (
	"context"
	"testing"
	"time"
)

func TestVFSReadPlanOverrideAcceptsOnlyOwnMount(t *testing.T) {
	t.Parallel()
	fs, _, _ := newDebugReadFaultFixture(t)

	if err := fs.SetReadPlanOverride("", 4, 8, 50*time.Millisecond, time.Second); err != nil {
		t.Fatalf("SetReadPlanOverride for the only mount: %v", err)
	}
	if err := fs.SetReadPlanOverride("local", 2, 4, 0, 0); err != nil {
		t.Fatalf("SetReadPlanOverride for the matching mount: %v", err)
	}
	if err := fs.SetReadPlanOverride("other", 2, 4, 0, 0); err == nil {
		t.Fatal("expected an unknown mount to be rejected")
	}
	fs.ClearReadPlanOverride()
}

func TestNamespaceReadPlanOverrideRoutesToMount(t *testing.T) {
	t.Parallel()
	_, ns, _ := newDebugReadFaultFixture(t)

	if err := ns.SetReadPlanOverride("local", 4, 8, 0, 0); err != nil {
		t.Fatalf("SetReadPlanOverride: %v", err)
	}
	if err := ns.SetReadPlanOverride("missing", 4, 8, 0, 0); err == nil {
		t.Fatal("expected an unknown mount to be rejected")
	}
	ns.ClearReadPlanOverride()
}

func TestVFSAndNamespaceCancelReadPrefetch(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fs, ns, _ := newDebugReadFaultFixture(t)

	if err := fs.CancelReadPrefetch(ctx, "/data.bin"); err != nil {
		t.Fatalf("VFS CancelReadPrefetch: %v", err)
	}
	if err := fs.CancelReadPrefetch(ctx, "/missing.bin"); err == nil {
		t.Fatal("expected a missing path to be rejected")
	}
	if err := ns.CancelReadPrefetch(ctx, "/local/data.bin"); err != nil {
		t.Fatalf("Namespace CancelReadPrefetch: %v", err)
	}
	// The virtual root's Apple metadata is served without a mount.
	if err := ns.CancelReadPrefetch(ctx, "/.DS_Store"); err != nil {
		t.Fatalf("CancelReadPrefetch on virtual metadata: %v", err)
	}
	if err := ns.CancelReadPrefetch(ctx, "/local"); err != nil {
		t.Fatalf("CancelReadPrefetch on the mount root: %v", err)
	}
	if err := ns.CancelReadPrefetch(ctx, "/unknown/data.bin"); err == nil {
		t.Fatal("expected an unknown mount to be rejected")
	}
}
