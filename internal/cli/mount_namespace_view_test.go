package cli

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/config"
	"github.com/yinzhenyu/qrypt/pkg/drive"
)

// TestMountNameViewKeepsDriveRoot locks the namespace semantics of the two
// build paths: a persistent mount (forceNamespace=false) presents the
// selected drive's contents at "/", while the one-shot fs namespace view
// (forceNamespace=true) keeps the /MOUNT/ prefix. Both must stay buildable
// and observable, and any change to either view is a deliberate product
// decision, not an accident of the builder plumbing.
func TestMountNameViewKeepsDriveRoot(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tmp := t.TempDir()
	remote := filepath.Join(tmp, "remote")
	if err := os.MkdirAll(remote, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(remote, "hello.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Storage: config.StorageConfig{
			ReadCacheDir: filepath.Join(tmp, "cache", "read"),
			UploadDir:    filepath.Join(tmp, "upload"),
			StateDir:     filepath.Join(tmp, "state"),
		},
		Mounts: []config.MountConfig{{
			Name:   "loc",
			Type:   "localfs",
			Params: config.ParamMap{"root_path": remote},
		}},
	}

	// The mount path (as used by "qrypt mount loc"): "/" is the drive root.
	mounted, cleanupM, err := buildFileSystemFromConfigMountMode(ctx, cfg, "loc", false)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanupM()
	mounted.Start(ctx)
	directEntries, err := mounted.List(ctx, "/")
	if err != nil {
		t.Fatalf("mount view list /: %v", err)
	}
	if !hasEntry(directEntries, "hello.txt") {
		t.Fatalf("mount view root should contain the drive contents, got %v", entryNames(directEntries))
	}
	if err := mounted.Close(context.Background()); err != nil {
		t.Fatal(err)
	}

	// The one-shot view (as used by "qrypt fs --mount loc list /"): "/" lists
	// the mount name, keeping /MOUNT/path addressing.
	ns, cleanupN, err := buildFileSystemFromConfigMountMode(ctx, cfg, "loc", true)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanupN()
	ns.Start(ctx)
	nsEntries, err := ns.List(ctx, "/")
	if err != nil {
		t.Fatalf("namespace view list /: %v", err)
	}
	if !hasEntry(nsEntries, "loc") {
		t.Fatalf("namespace view root should contain the /MOUNT/ dir, got %v", entryNames(nsEntries))
	}
	if err := ns.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func entryNames(entries []drive.Entry) []string {
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name)
	}
	return names
}

func hasEntry(entries []drive.Entry, name string) bool {
	for _, entry := range entries {
		if entry.Name == name {
			return true
		}
	}
	return false
}
