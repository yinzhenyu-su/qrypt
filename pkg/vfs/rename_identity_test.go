package vfs_test

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drivers/localfs"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

// newRenameIdentityVFS builds a localfs-backed filesystem whose entry ids are
// remote paths - the case where a rename changes the backend identity - and
// returns the remote root for direct setup and assertions.
func newRenameIdentityVFS(t *testing.T) (*vfs.VFS, string) {
	t.Helper()
	remote := t.TempDir()
	fs, err := vfs.New(localfs.New(remote), vfs.Options{
		StorageDir:    t.TempDir(),
		CacheMaxBytes: 10 << 20,
		UploadDelay:   time.Hour,
		DeleteDelay:   10 * time.Millisecond,
		RootID:        remote,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fs.Close(context.Background()) })
	fs.Start(context.Background())
	return fs, remote
}

func seedRemoteFile(t *testing.T, root, rel, data string) {
	t.Helper()
	path := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

// waitRemoteGone waits for a delayed delete to reach the backend.
func waitRemoteGone(t *testing.T, path string) {
	t.Helper()
	waitForCondition(t, func() bool {
		_, err := os.Stat(path)
		return os.IsNotExist(err)
	})
}

func readPath(t *testing.T, fs vfs.FileSystem, path string) string {
	t.Helper()
	rc, err := fs.Read(context.Background(), path, 0, 1<<20)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func listNames(t *testing.T, fs vfs.FileSystem, path string) []string {
	t.Helper()
	entries, err := fs.List(context.Background(), path)
	if err != nil {
		t.Fatalf("list %s: %v", path, err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name)
	}
	return names
}

// TestRenameServesBackendIdentity: after a rename on a backend whose entry id
// encodes the path, the renamed path must resolve to the id of its new
// location, so reads and deletes reach the moved object instead of the old
// path it came from.
func TestRenameServesBackendIdentity(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fs, remote := newRenameIdentityVFS(t)
	seedRemoteFile(t, remote, "a.txt", "alpha")

	if err := fs.Rename(ctx, "/a.txt", "/b.txt"); err != nil {
		t.Fatal(err)
	}

	entry, err := fs.Stat(ctx, "/b.txt")
	if err != nil {
		t.Fatalf("stat renamed path: %v", err)
	}
	if want := filepath.Join(remote, "b.txt"); entry.ID != want {
		t.Fatalf("renamed entry id = %q, want the backend's id for the new location %q", entry.ID, want)
	}
	if got := readPath(t, fs, "/b.txt"); got != "alpha" {
		t.Fatalf("read renamed path = %q, want %q", got, "alpha")
	}

	if err := fs.Remove(ctx, "/b.txt"); err != nil {
		t.Fatalf("remove renamed path: %v", err)
	}
	waitRemoteGone(t, filepath.Join(remote, "b.txt"))
}

// TestMoveAcrossDirectoriesUsesNewIdentity: a cross-directory move commits the
// destination identity, so a later read or remove does not fall back to the
// source location.
func TestMoveAcrossDirectoriesUsesNewIdentity(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fs, remote := newRenameIdentityVFS(t)
	seedRemoteFile(t, remote, "a.txt", "alpha")
	if _, err := fs.Mkdir(ctx, "/dst"); err != nil {
		t.Fatal(err)
	}
	// Warm the source listing so the moved entry is cached under its old
	// identity before the move.
	if got := listNames(t, fs, "/"); len(got) == 0 {
		t.Fatal("source listing is empty")
	}

	if err := fs.Rename(ctx, "/a.txt", "/dst/a.txt"); err != nil {
		t.Fatal(err)
	}

	entry, err := fs.Stat(ctx, "/dst/a.txt")
	if err != nil {
		t.Fatalf("stat moved path: %v", err)
	}
	if want := filepath.Join(remote, "dst", "a.txt"); entry.ID != want {
		t.Fatalf("moved entry id = %q, want %q", entry.ID, want)
	}
	if got := readPath(t, fs, "/dst/a.txt"); got != "alpha" {
		t.Fatalf("read moved path = %q, want %q", got, "alpha")
	}
	if err := fs.Remove(ctx, "/dst/a.txt"); err != nil {
		t.Fatalf("remove moved path: %v", err)
	}
	waitRemoteGone(t, filepath.Join(remote, "dst", "a.txt"))
}

// TestDirectoryRenameFollowsDescendantIdentity: children cached before their
// directory was renamed must not keep addressing the old location.
func TestDirectoryRenameFollowsDescendantIdentity(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fs, remote := newRenameIdentityVFS(t)
	seedRemoteFile(t, remote, "dir/top.txt", "top")
	seedRemoteFile(t, remote, "dir/sub/c.txt", "gamma")

	// Cache the subtree under its pre-rename identities.
	if got := listNames(t, fs, "/dir"); len(got) != 2 {
		t.Fatalf("pre-rename listing = %v, want the directory's two children", got)
	}
	if got := listNames(t, fs, "/dir/sub"); len(got) != 1 {
		t.Fatalf("pre-rename sublisting = %v, want one child", got)
	}

	if err := fs.Rename(ctx, "/dir", "/moved"); err != nil {
		t.Fatal(err)
	}

	if got := readPath(t, fs, "/moved/top.txt"); got != "top" {
		t.Fatalf("read descendant after rename = %q, want %q", got, "top")
	}
	if got := readPath(t, fs, "/moved/sub/c.txt"); got != "gamma" {
		t.Fatalf("read nested descendant after rename = %q, want %q", got, "gamma")
	}
	if got := listNames(t, fs, "/moved"); len(got) != 2 {
		t.Fatalf("post-rename listing = %v, want the renamed directory's children", got)
	}
	if got := listNames(t, fs, "/moved/sub"); len(got) != 1 {
		t.Fatalf("post-rename sublisting = %v, want one child", got)
	}
	if _, err := fs.Stat(ctx, "/dir"); err == nil {
		t.Fatal("pre-rename directory path still resolves")
	}
	if _, err := os.Stat(filepath.Join(remote, "dir")); !os.IsNotExist(err) {
		t.Fatalf("remote directory still present at the pre-rename path (err=%v)", err)
	}
}
