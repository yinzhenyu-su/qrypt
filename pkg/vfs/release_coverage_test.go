package vfs

// Coverage tests for the namespace/runtime surfaces added by the v0.5.0
// view-domain refactor. These keep pkg/vfs above its release coverage floor
// (scripts/coverage.sh, 75%) by exercising the thin adapter layers that the
// feature tests do not reach.

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/drivers/localfs"
	"github.com/yinzhenyu/qrypt/pkg/task"
	"github.com/yinzhenyu/qrypt/pkg/vfs/diagnostics"
)

func newCoverageTestVFS(t *testing.T) *VFS {
	t.Helper()
	fs, err := New(localfs.New(t.TempDir()), Options{Name: "m", StorageDir: t.TempDir(), TestEnabled: true, UploadDelay: 10 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	// Upload/delete workers only run after Start; cancelling the context in
	// teardown triggers the AfterFunc that closes the VFS and drains them.
	ctx, cancel := context.WithCancel(context.Background())
	fs.Start(ctx)
	t.Cleanup(func() {
		cancel()
		_ = fs.CloseReadCache()
	})
	return fs
}

func newCoverageTestNamespace(t *testing.T) *Namespace {
	t.Helper()
	ns, err := NewNamespace([]Mount{{Name: "m", FS: newCoverageTestVFS(t)}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ns.CloseReadCache() })
	return ns
}

// coverageDrainer is implemented by both *VFS and *Namespace so the wait can
// poll queued and in-flight uploads alike.
type coverageDrainer interface {
	PendingUploads() []PendingUpload
	DebugSnapshot() diagnostics.DebugSnapshot
}

// waitCoverageDrained polls until all queued and in-flight uploads finish.
// Writes are staged asynchronously and only become visible to reads and
// removes once every upload drains.
func waitCoverageDrained(t *testing.T, d coverageDrainer) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		active := 0
		for _, mount := range d.DebugSnapshot().Mounts {
			active += len(mount.ActiveUploads())
		}
		if len(d.PendingUploads()) == 0 && active == 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("uploads did not drain")
}

func TestCoverageNamespaceWriteSurface(t *testing.T) {
	ctx := context.Background()
	ns := newCoverageTestNamespace(t)

	// Namespace root is read-only for every mutation entry point.
	for path, fn := range map[string]func() error{
		"/create":  func() error { return ns.Create(ctx, "/") },
		"/writeAt": func() error { _, err := ns.WriteAt(ctx, "/", []byte("x"), 0); return err },
		"/flush":   func() error { return ns.Flush(ctx, "/") },
		"/mkdir":   func() error { _, err := ns.Mkdir(ctx, "/"); return err },
		"/remove":  func() error { return ns.Remove(ctx, "/") },
		"/rmdir":   func() error { return ns.RemoveDir(ctx, "/") },
		"/truncate": func() error {
			return ns.Truncate(ctx, "/", 0)
		},
		"/setmtime": func() error {
			return ns.SetModTime(ctx, "/", time.Now())
		},
		"/prep-copy": func() error { return ns.PrepareDirectoryCopy(ctx, "/") },
	} {
		if err := fn(); err != ErrReadOnly {
			t.Errorf("%s: err = %v, want ErrReadOnly", path, err)
		}
	}
	// Rename rejects roots in either position.
	if err := ns.Rename(ctx, "/", "/m/x"); err != ErrReadOnly {
		t.Errorf("rename from root: %v", err)
	}
	if err := ns.Rename(ctx, "/m/x", "/"); err != ErrReadOnly {
		t.Errorf("rename to root: %v", err)
	}

	// Unresolved paths fail before touching a mount.
	if err := ns.Create(ctx, "/nope/f.txt"); !IsNotFound(err) {
		t.Errorf("create unresolved: %v", err)
	}
	if err := ns.Remove(ctx, "/nope/f.txt"); !IsNotFound(err) {
		t.Errorf("remove unresolved: %v", err)
	}

	// Success paths through a real mount. Truncate and SetModTime operate on
	// the still-open pending write generation (before the first flush), so a
	// single upload carries all mutations and no replacement-upload dance
	// renames the file to a temporary name in between.
	if _, err := ns.Mkdir(ctx, "/m/d"); err != nil {
		t.Fatal(err)
	}
	full := "/m/d/f.txt"
	if _, err := ns.WriteAt(ctx, full, []byte("hello world"), 0); err != nil {
		t.Fatal(err)
	}
	if err := ns.Truncate(ctx, full, 5); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	if err := ns.SetModTime(ctx, full, time.Now()); err != nil {
		t.Fatalf("set mtime: %v", err)
	}
	if err := ns.Flush(ctx, full); err != nil {
		t.Fatalf("flush: %v", err)
	}
	waitCoverageDrained(t, ns)
	if _, err := ns.Stat(ctx, full); err != nil {
		t.Fatalf("stat after upload: %v", err)
	}
	if err := ns.Remove(ctx, full); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if err := ns.RemoveDir(ctx, "/m/d"); err != nil {
		t.Fatalf("remove dir: %v", err)
	}
	// Directory-copy prep hides children in the view for the copy duration,
	// so run it last, on the now-deleted directory (resolve-error branch).
	if err := ns.PrepareDirectoryCopy(ctx, "/m/d"); err == nil {
		t.Log("PrepareDirectoryCopy unexpectedly succeeded on a removed directory")
	}

	// Crossing mounts is rejected even when no files exist.
	b := newCoverageTestVFS(t)
	if err := ns.AddMount(Mount{Name: "b", FS: b}); err != nil {
		t.Fatal(err)
	}
	if err := ns.Rename(ctx, "/m/a.txt", "/b/a.txt"); !errors.Is(err, ErrCrossMount) {
		t.Errorf("cross-mount rename: %v", err)
	}
}

func TestCoverageNamespaceReadSurface(t *testing.T) {
	ctx := context.Background()
	ns := newCoverageTestNamespace(t)

	// Root entries and root read guards.
	if _, err := ns.Stat(ctx, "/"); err != nil {
		t.Fatalf("stat root: %v", err)
	}
	if _, err := ns.List(ctx, "/"); err != nil {
		t.Fatalf("list root: %v", err)
	}
	if _, err := ns.ListPage(ctx, "/", "", 10); err != nil {
		t.Fatalf("list page root: %v", err)
	}
	if _, err := ns.RemoteList(ctx, "/"); err != nil {
		t.Fatalf("remote list root: %v", err)
	}
	if _, err := ns.Read(ctx, "/", 0, 0); err == nil {
		t.Error("expected read of namespace root to fail")
	}
	if _, err := ns.ReadStream(ctx, "/"); err == nil {
		t.Error("expected stream of namespace root to fail")
	}
	if _, err := ns.ReadRaw(ctx, "/", 0, 0); err == nil {
		t.Error("expected raw read of namespace root to fail")
	}
	ns.RefreshPath("/")
	ns.ReleaseReadSession(0)

	// Cache lifecycle across mounts, including unknown-mount errors.
	if err := ns.FlushReadCache(); err != nil {
		t.Fatalf("flush read cache: %v", err)
	}
	if err := ns.ClearReadCache(); err != nil {
		t.Fatalf("clear read cache: %v", err)
	}
	if err := ns.CloseReadCache(); err != nil {
		t.Fatalf("close read cache: %v", err)
	}
	if err := ns.ClearReadCacheForMount("nope"); err == nil {
		t.Error("expected unknown mount error")
	}
	if err := ns.ClearReadCacheForMount("m"); err != nil {
		t.Fatalf("clear cache for mount m: %v", err)
	}
	ns.StartDirectoryPrefetch(ctx)

	// Mount dir and file reads.
	if _, err := ns.Mkdir(ctx, "/m/d"); err != nil {
		t.Fatal(err)
	}
	if _, err := ns.WriteAt(ctx, "/m/d/f.txt", []byte("data bytes"), 0); err != nil {
		t.Fatal(err)
	}
	if err := ns.Flush(ctx, "/m/d/f.txt"); err != nil {
		t.Fatal(err)
	}
	waitCoverageDrained(t, ns)
	if _, err := ns.List(ctx, "/m/d"); err != nil {
		t.Fatalf("list: %v", err)
	}
	if page, err := ns.ListPage(ctx, "/m/d", "", 1); err != nil || len(page.Entries) != 1 {
		t.Fatalf("list page: %+v err=%v", page, err)
	}
	if _, err := ns.ListPage(ctx, "/m/d", "zzz", 1); err != nil {
		t.Fatalf("list page with cursor: %v", err)
	}
	if _, err := ns.RemoteList(ctx, "/m/d"); err != nil {
		t.Fatalf("remote list: %v", err)
	}
	rc, err := ns.Read(ctx, "/m/d/f.txt", 0, 0)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	got, err := io.ReadAll(rc)
	rc.Close()
	if err != nil || string(got) != "data bytes" {
		t.Fatalf("read content %q err=%v", got, err)
	}
	if rc, err := ns.ReadStream(ctx, "/m/d/f.txt"); err == nil {
		rc.Close()
	} else {
		t.Logf("ReadStream: %v", err)
	}
	if _, err := ns.ReadRaw(ctx, "/m/d/f.txt", 0, 4); err != nil {
		t.Fatalf("raw read: %v", err)
	}
	ns.RefreshPath("/m/d/f.txt")
	ns.RefreshPath("/nope/x.txt")
	if _, err := ns.ReadRaw(ctx, "/m/d", 0, 0); err == nil {
		t.Error("expected raw read of a directory to fail")
	}
}

func TestCoverageNamespaceTaskSurface(t *testing.T) {
	ctx := context.Background()
	ns := newCoverageTestNamespace(t)

	if tasks := ns.Tasks(task.Filter{}); len(tasks) != 0 {
		t.Fatalf("expected no tasks, got %+v", tasks)
	}
	if tasks := ns.Tasks(task.Filter{Mount: "m", Path: "/m/x", Limit: 1}); len(tasks) != 0 {
		t.Fatalf("unexpected filtered tasks: %+v", tasks)
	}
	if n, err := ns.DismissFinishedTasks(ctx, task.Filter{}); err != nil || n != 0 {
		t.Fatalf("dismiss finished: n=%d err=%v", n, err)
	}
	if _, err := ns.ListTasks(ctx, task.Filter{}); err != nil {
		t.Fatalf("list tasks: %v", err)
	}

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := ns.ListTasks(cancelled, task.Filter{}); err == nil {
		t.Error("expected cancelled context error")
	}
	if _, err := ns.GetTask(cancelled, "m:x"); err == nil {
		t.Error("expected cancelled context error in GetTask")
	}

	// Unknown task ids fall through every dispatch path.
	if _, err := ns.GetTask(ctx, "m:nope"); err != task.ErrNotFound {
		t.Errorf("get task = %v", err)
	}
	if _, err := ns.GetTask(ctx, "unqualified"); err != task.ErrNotFound {
		t.Errorf("get unqualified = %v", err)
	}
	if err := ns.DismissTask(ctx, "m:nope"); err != task.ErrNotFound {
		t.Errorf("dismiss = %v", err)
	}
	if err := ns.DismissTask(ctx, "zzz:nope"); err != task.ErrNotFound {
		t.Errorf("dismiss unknown mount = %v", err)
	}
	if err := ns.RetryTask(ctx, "m:nope"); err != task.ErrNotFound {
		t.Errorf("retry = %v", err)
	}
	if err := ns.CancelTask(ctx, "m:nope"); err != task.ErrNotFound {
		t.Errorf("cancel = %v", err)
	}
	if err := ns.RetryTask(ctx, "unqualified"); err != task.ErrNotFound {
		t.Errorf("retry unqualified = %v", err)
	}
}

func TestCoverageMountsAndSpaces(t *testing.T) {
	ctx := context.Background()
	fs := newCoverageTestVFS(t)
	ns := newCoverageTestNamespace(t)

	mounts := fs.Mounts()
	if len(mounts) != 1 || mounts[0].Name != "m" || mounts[0].Path != "/" {
		t.Fatalf("vfs mounts = %+v", mounts)
	}
	nmounts := ns.Mounts()
	if len(nmounts) != 1 || nmounts[0].Name != "m" || nmounts[0].Path != "/m" {
		t.Fatalf("namespace mounts = %+v", nmounts)
	}
	if pending := ns.PendingUploads(); len(pending) != 0 {
		t.Fatalf("unexpected pending uploads: %+v", pending)
	}
	spaces := ns.MountSpaces(ctx)
	if len(spaces) != 1 || spaces[0].Name != "m" {
		t.Fatalf("mount spaces = %+v", spaces)
	}
	if _, err := ns.Space(ctx); err != nil {
		t.Fatalf("namespace space: %v", err)
	}
	if _, err := fs.Space(ctx); err != nil {
		t.Fatalf("vfs space: %v", err)
	}

	// Nil-receiver guards.
	var nilVFS *VFS
	if got := nilVFS.Mounts(); got != nil {
		t.Fatalf("nil vfs mounts = %+v", got)
	}
	var nilNS *Namespace
	if got := nilNS.Mounts(); got != nil {
		t.Fatalf("nil namespace mounts = %+v", got)
	}
}

func TestCoverageRemoteHashSurface(t *testing.T) {
	ctx := context.Background()
	fs := newCoverageTestVFS(t)
	ns := newCoverageTestNamespace(t)

	if _, err := fs.WriteAt(ctx, "/f.txt", []byte("hash me"), 0); err != nil {
		t.Fatal(err)
	}
	if err := fs.Flush(ctx, "/f.txt"); err != nil {
		t.Fatal(err)
	}
	waitCoverageDrained(t, fs)

	// localfs reports neither remote hashes nor encrypted hashes; both must
	// surface ErrUnsupported rather than panic.
	if _, _, err := fs.RemoteHash(ctx, "/f.txt"); err != drive.ErrUnsupported {
		t.Errorf("remote hash = %v", err)
	}
	if _, err := fs.EncryptedHash(ctx, "/f.txt", bytes.NewReader([]byte("x")), 1, drive.HashSHA1); err != drive.ErrUnsupported {
		t.Errorf("encrypted hash = %v", err)
	}
	if _, _, err := fs.RemoteHash(ctx, "/nope"); !IsNotFound(err) {
		t.Errorf("remote hash unresolved = %v", err)
	}
	if _, err := fs.EncryptedHash(ctx, "/nope", bytes.NewReader(nil), 0, drive.HashSHA1); !IsNotFound(err) {
		t.Errorf("encrypted hash unresolved = %v", err)
	}

	// The namespace-level assertions resolve within the namespace's own
	// mount tree, so a file must exist there too.
	if _, err := ns.WriteAt(ctx, "/m/f.txt", []byte("hash me"), 0); err != nil {
		t.Fatal(err)
	}
	if err := ns.Flush(ctx, "/m/f.txt"); err != nil {
		t.Fatal(err)
	}
	waitCoverageDrained(t, ns)
	if _, _, err := ns.RemoteHash(ctx, "/"); err != drive.ErrUnsupported {
		t.Errorf("namespace remote hash root = %v", err)
	}
	if _, _, err := ns.RemoteHash(ctx, "/m/f.txt"); err != drive.ErrUnsupported {
		t.Errorf("namespace remote hash = %v", err)
	}
	if _, err := ns.EncryptedHash(ctx, "/m/f.txt", bytes.NewReader(nil), 0, drive.HashSHA1); err != drive.ErrUnsupported {
		t.Errorf("namespace encrypted hash = %v", err)
	}
	if _, _, err := ns.RemoteHash(ctx, "/nope/x"); !IsNotFound(err) {
		t.Errorf("namespace remote hash unresolved = %v", err)
	}
}

func TestCoverageDriverRuntimeRemaining(t *testing.T) {
	ctx := context.Background()
	fs := newCoverageTestVFS(t)
	runtime := newVFSDriverRuntime(fs.driver, fs.testEnabled)

	if caps := runtime.Capabilities(); len(caps) == 0 {
		t.Fatal("expected driver capabilities")
	}
	if runtime.Encrypted() {
		t.Fatal("localfs is not encrypted")
	}
	if _, err := runtime.DebugSnapshot(ctx); err != nil {
		t.Fatalf("debug snapshot: %v", err)
	}
	_, _ = runtime.Metrics(ctx, time.Now().Add(-time.Hour))
	if _, err := runtime.Space(ctx); err != nil {
		t.Fatalf("space: %v", err)
	}
	info, err := runtime.ResolveRemoteName(ctx, "plain-name")
	if err != nil || info.PlainName != "plain-name" {
		t.Fatalf("resolve remote name: %+v err=%v", info, err)
	}
	if _, err := runtime.ForeignEntries(ctx, "/"); err == nil {
		t.Error("expected foreign entries to be unsupported on localfs")
	}
	if err := runtime.RequireCapability(drive.CapabilityForeignEntries, "foreign entries"); err == nil {
		t.Error("expected capability gate error")
	}
	if _, err := runtime.List(ctx, "/"); err != nil {
		t.Fatalf("list: %v", err)
	}
	_ = runtime.RequiredUploadHashes()
	_ = runtime.MutationBackend()

	// Remove through the raw driver.
	if _, err := fs.WriteAt(ctx, "/gone.txt", []byte("x"), 0); err != nil {
		t.Fatal(err)
	}
	if err := fs.Flush(ctx, "/gone.txt"); err != nil {
		t.Fatal(err)
	}
	waitCoverageDrained(t, fs)
	entry, err := fs.Stat(ctx, "/gone.txt")
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.Remove(ctx, entry); err != nil {
		t.Fatalf("remove: %v", err)
	}

	// A nil driver makes RequiredUploadHashes return nil without panicking.
	if got := newVFSDriverRuntime(nil, false).RequiredUploadHashes(); got != nil {
		t.Fatalf("nil driver hashes = %+v", got)
	}
}

func TestCoverageListingAndReadHost(t *testing.T) {
	ctx := context.Background()
	fs := newCoverageTestVFS(t)

	if _, err := fs.Mkdir(ctx, "/d"); err != nil {
		t.Fatal(err)
	}
	for i, name := range []string{"a.txt", "b.txt", "c.txt"} {
		if _, err := fs.WriteAt(ctx, "/d/"+name, []byte{byte('a' + i)}, 0); err != nil {
			t.Fatal(err)
		}
		if err := fs.Flush(ctx, "/d/"+name); err != nil {
			t.Fatal(err)
		}
	}
	waitCoverageDrained(t, fs)
	if _, err := fs.ListPage(ctx, "/d", "", 2); err != nil {
		t.Fatalf("list page: %v", err)
	}
	if _, err := fs.ListPage(ctx, "/d", "a.txt", 10); err != nil {
		t.Fatalf("list page cursor: %v", err)
	}
	if _, err := fs.ListPage(ctx, "/d", "", 0); err != nil {
		t.Fatalf("list page unlimited: %v", err)
	}
	if _, err := fs.RemoteList(ctx, "/d"); err != nil {
		t.Fatalf("remote list: %v", err)
	}
	if rc, err := fs.ReadRaw(ctx, "/d/a.txt", 0, 1); err != nil {
		t.Fatalf("raw read: %v", err)
	} else {
		rc.Close()
	}
	if rc, err := fs.ReadStream(ctx, "/d/a.txt"); err != nil {
		t.Fatalf("read stream: %v", err)
	} else {
		rc.Close()
	}
	fs.ReleaseReadSession(123)
}

func TestCoverageSourceUploadSurface(t *testing.T) {
	ctx := context.Background()
	fs := newCoverageTestVFS(t)
	ns := newCoverageTestNamespace(t)

	if ns.SupportsSourceUpload("/") {
		t.Error("expected source upload support to be rejected at the namespace root")
	}
	if ns.SupportsResumableSourceUpload("/") {
		t.Error("expected resumable support to be rejected at the namespace root")
	}
	if ns.SupportsSourceUpload("/nope/x") {
		t.Error("expected source upload support to be rejected on unresolved paths")
	}
	if _, err := ns.UploadSource(ctx, "/", SourceUploadRequest{Source: drive.NewBytesReadOnlyFileSource([]byte("x"))}); err != ErrReadOnly {
		t.Errorf("namespace root upload = %v, want ErrReadOnly", err)
	}
	if _, err := fs.UploadSource(ctx, "/", SourceUploadRequest{Source: drive.NewBytesReadOnlyFileSource([]byte("x"))}); err != ErrReadOnly {
		t.Errorf("vfs root upload = %v, want ErrReadOnly", err)
	}
	if _, err := fs.UploadSource(ctx, "/f.txt", SourceUploadRequest{}); err == nil {
		t.Error("expected nil source to be rejected")
	}
	if _, err := ns.UploadSource(ctx, "/unresolved/f.txt", SourceUploadRequest{Source: drive.NewBytesReadOnlyFileSource(nil)}); !IsNotFound(err) {
		t.Errorf("unresolved namespace upload = %v", err)
	}
	if !ns.SupportsSourceUpload("/m/f.txt") {
		t.Error("expected source upload support on a real mount path")
	}
}

func TestCoverageDebugSurface(t *testing.T) {
	ctx := context.Background()
	fs := newCoverageTestVFS(t)
	ns := newCoverageTestNamespace(t)

	if drivers := fs.Drivers(); len(drivers) == 0 {
		t.Fatal("expected named drivers")
	}
	if ops, err := fs.DebugActiveOps(ctx, nil); err != nil || ops == nil {
		t.Fatalf("debug active ops: %+v err=%v", ops, err)
	}
	if _, err := fs.MountHealth(ctx, "m"); err != nil {
		t.Fatalf("mount health: %v", err)
	}
	if err := fs.DebugReset(ctx); err != nil {
		t.Fatalf("debug reset: %v", err)
	}
	if _, err := fs.DebugStaging(ctx, "/d"); err != nil && !IsNotFound(err) {
		t.Fatalf("debug staging: %v", err)
	}
	if nsDrivers := ns.Drivers(); len(nsDrivers) == 0 {
		t.Fatal("expected namespace drivers")
	}
	if ops, err := ns.DebugActiveOps(ctx, nil); err != nil || ops == nil {
		t.Fatalf("namespace debug active ops: %+v err=%v", ops, err)
	}
	if _, err := ns.MountHealth(ctx, "m"); err != nil {
		t.Fatalf("namespace mount health: %v", err)
	}
	if err := ns.DebugReset(ctx); err != nil {
		t.Fatalf("namespace debug reset: %v", err)
	}
	if _, err := ns.DebugStaging(ctx, "/m/d"); err != nil && !IsNotFound(err) {
		t.Fatalf("namespace debug staging: %v", err)
	}
}
