package mount

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestEnsureMountPointCreatesMissingDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "Qrypt")

	if err := ensureMountPoint(path); err != nil {
		t.Fatalf("ensureMountPoint: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat mount point: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("mount point is not a directory: %v", info.Mode())
	}
}

func TestEnsureMountPointAcceptsExistingDirectory(t *testing.T) {
	// A clean unmount leaves the directory behind, and FUSE mounts over a
	// non-empty directory are rejected by the kernel, not by this function.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "keep.txt"), []byte("keep"), 0o644); err != nil {
		t.Fatalf("seed directory: %v", err)
	}

	if err := ensureMountPoint(dir); err != nil {
		t.Fatalf("ensureMountPoint on existing directory: %v", err)
	}
}

func TestEnsureMountPointRejectsNonDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "Qrypt")
	if err := os.WriteFile(path, []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	err := ensureMountPoint(path)
	if err == nil {
		t.Fatal("ensureMountPoint accepted a regular file as a mount point")
	}
	if !strings.Contains(err.Error(), "is not a directory") {
		t.Fatalf("error does not explain the problem: %v", err)
	}
}

func TestEnsureMountPointAcceptsPlainDirectory(t *testing.T) {
	// The macOS dead-mount detection runs on every successfully created mount
	// point; an ordinary directory must not be mistaken for one.
	dir := t.TempDir()

	if root, dead := deadFuseMountRoot(dir); dead {
		t.Fatalf("deadFuseMountRoot(%s) reported the mount root %q as dead", dir, root)
	}
	if err := ensureMountPoint(dir); err != nil {
		t.Fatalf("ensureMountPoint on a plain directory: %v", err)
	}
}

func TestIsFuseFilesystem(t *testing.T) {
	cases := []struct {
		name   string
		fsType string
		want   bool
	}{
		{name: "macfuse", fsType: "macfuse", want: true},
		{name: "osxfuse", fsType: "osxfuse", want: true},
		{name: "linux subtype", fsType: "fuse.qrypt", want: true},
		{name: "fuseblk", fsType: "fuseblk", want: true},
		{name: "case insensitive", fsType: "macFUSE", want: true},
		{name: "apfs", fsType: "apfs", want: false},
		{name: "nfs", fsType: "nfs", want: false},
		{name: "empty", fsType: "", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isFuseFilesystem(tc.fsType); got != tc.want {
				t.Fatalf("isFuseFilesystem(%q) = %v, want %v", tc.fsType, got, tc.want)
			}
		})
	}
}

func TestDetachRootForPlainDirectoryIsThePathItself(t *testing.T) {
	// Nothing is mounted on an ordinary directory, so there is no mount root to
	// prefer over the path the user asked for.
	dir := t.TempDir()

	if got := detachRootFor(dir); got != dir {
		t.Fatalf("detachRootFor(%s) = %q, want the path itself", dir, got)
	}
}

func TestBoundedProbeReturnsTheProbeAnswer(t *testing.T) {
	want := errors.New("device not configured")

	err := boundedProbe(time.Second, func() error { return want })
	if !errors.Is(err, want) {
		t.Fatalf("boundedProbe = %v, want %v", err, want)
	}
}

func TestBoundedProbeGivesUpOnASilentProbe(t *testing.T) {
	// A hung FUSE daemon must not look dead: force-detaching a working mount
	// would be worse than the failure the user already has.
	blocked := make(chan struct{})
	defer close(blocked)

	start := time.Now()
	err := boundedProbe(50*time.Millisecond, func() error { <-blocked; return errors.New("never") })
	if err != nil {
		t.Fatalf("boundedProbe on a silent probe = %v, want nil (inconclusive)", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("boundedProbe waited %s, want it to give up after 50ms", elapsed)
	}
}
