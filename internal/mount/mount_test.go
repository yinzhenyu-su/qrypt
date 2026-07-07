package mount

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/winfsp/cgofuse/fuse"
	"github.com/yinzhenyu/qrypt/internal/driver/localfs"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

type stubFS struct {
	entries  map[string]drive.Entry
	lists    map[string][]drive.Entry
	readOnly map[string]bool
}

type stubSpaceFS struct {
	stubFS
	space drive.Space
}

type createRouteFS struct {
	stubFS
	created  []string
	mkdirs   []string
	writes   []string
	flushes  []string
	removed  []string
	rmdirs   []string
	renames  []string
	truncate []string
}

type copyPrepareFS struct {
	stubFS
	prepared []string
}

type failingStatFS struct {
	stubFS
	err error
}

type failingListFS struct {
	stubFS
	err error
}

type blockingReadFS struct {
	stubFS
	entered chan struct{}
}

func (stubFS) Start(context.Context) {}

func (s stubSpaceFS) Space(context.Context) (drive.Space, error) {
	return s.space, nil
}

func (s stubFS) IsReadOnlyPath(path string) bool {
	return s.readOnly[path]
}

func (s stubFS) Stat(_ context.Context, path string) (drive.Entry, error) {
	entry, ok := s.entries[path]
	if !ok {
		return drive.Entry{}, errNotFound
	}
	return entry, nil
}

func (s stubFS) List(_ context.Context, path string) ([]drive.Entry, error) {
	entries, ok := s.lists[path]
	if !ok {
		return nil, errNotFound
	}
	return entries, nil
}

func (s failingStatFS) Stat(context.Context, string) (drive.Entry, error) {
	return drive.Entry{}, s.err
}

func (s failingListFS) List(context.Context, string) ([]drive.Entry, error) {
	return nil, s.err
}

func (s blockingReadFS) Read(ctx context.Context, path string, offset, size int64) (io.ReadCloser, error) {
	close(s.entered)
	<-ctx.Done()
	return nil, ctx.Err()
}

func (stubFS) Read(context.Context, string, int64, int64) (io.ReadCloser, error) {
	return nil, errNotFound
}
func (stubFS) Create(context.Context, string) error                        { return nil }
func (stubFS) WriteAt(context.Context, string, []byte, int64) (int, error) { return 0, nil }
func (stubFS) Flush(context.Context, string) error                         { return nil }
func (stubFS) Mkdir(context.Context, string) (drive.Entry, error)          { return drive.Entry{}, nil }
func (stubFS) Remove(context.Context, string) error                        { return nil }
func (stubFS) RemoveDir(context.Context, string) error                     { return nil }
func (stubFS) Rename(context.Context, string, string) error                { return nil }
func (stubFS) Truncate(context.Context, string, int64) error               { return nil }
func (stubFS) Pending() []vfs.PendingFile                                  { return nil }

func (s *createRouteFS) Create(_ context.Context, path string) error {
	s.created = append(s.created, path)
	return nil
}

func (s *createRouteFS) Mkdir(_ context.Context, path string) (drive.Entry, error) {
	s.mkdirs = append(s.mkdirs, path)
	return drive.Entry{ID: path, Name: filepath.Base(path), IsDir: true}, nil
}

func (s *createRouteFS) WriteAt(_ context.Context, path string, data []byte, off int64) (int, error) {
	s.writes = append(s.writes, path)
	return len(data), nil
}

func (s *createRouteFS) Flush(_ context.Context, path string) error {
	s.flushes = append(s.flushes, path)
	return nil
}

func (s *createRouteFS) Remove(_ context.Context, path string) error {
	s.removed = append(s.removed, path)
	return nil
}

func (s *createRouteFS) RemoveDir(_ context.Context, path string) error {
	s.rmdirs = append(s.rmdirs, path)
	return nil
}

func (s *createRouteFS) Rename(_ context.Context, oldPath, newPath string) error {
	s.renames = append(s.renames, oldPath+"->"+newPath)
	return nil
}

func (s *createRouteFS) Truncate(_ context.Context, path string, size int64) error {
	s.truncate = append(s.truncate, path)
	return nil
}

func (s *copyPrepareFS) PrepareDirectoryCopy(_ context.Context, path string) error {
	s.prepared = append(s.prepared, path)
	return nil
}

var errNotFound = vfs.ErrNotFound

func TestMountOptionsUseStableMetadataCaching(t *testing.T) {
	opts := mountOptions(Options{})
	for _, want := range []string{"attr_timeout=1", "entry_timeout=1", "negative_timeout=0", "use_ino"} {
		if !hasMountOption(opts, want) {
			t.Fatalf("mount options %v missing %q", opts, want)
		}
	}
	if runtime.GOOS != "darwin" {
		return
	}
	for _, want := range []string{"defer_permissions", "fsname=qrypt", "subtype=qrypt", "iosize=1048576"} {
		if !hasMountOption(opts, want) {
			t.Fatalf("darwin mount options %v missing %q", opts, want)
		}
	}
}

func TestMountOptionsUseConfiguredMetadataTimeouts(t *testing.T) {
	opts := mountOptions(Options{
		AttrTimeout:     1500 * time.Millisecond,
		AttrTimeoutSet:  true,
		EntryTimeout:    2 * time.Second,
		EntryTimeoutSet: true,
		NegativeTimeout: 250 * time.Millisecond,
	})
	for _, want := range []string{"attr_timeout=1.500", "entry_timeout=2", "negative_timeout=0.250"} {
		if !hasMountOption(opts, want) {
			t.Fatalf("mount options %v missing %q", opts, want)
		}
	}
}

func TestMountOptionsAllowDisablingMetadataTimeouts(t *testing.T) {
	opts := mountOptions(Options{AttrTimeoutSet: true, EntryTimeoutSet: true})
	for _, want := range []string{"attr_timeout=0", "entry_timeout=0"} {
		if !hasMountOption(opts, want) {
			t.Fatalf("mount options %v missing %q", opts, want)
		}
	}
}

func TestMountOptionsDoNotUseMacFUSENoAppleDouble(t *testing.T) {
	opts := mountOptions(Options{NoAppleDouble: true})
	if hasMountOption(opts, "noappledouble") {
		t.Fatalf("mount options %v should not pass macFUSE noappledouble", opts)
	}
}

func TestMountOptionsUseConfiguredKernelOptions(t *testing.T) {
	opts := mountOptions(Options{
		ReadOnly:           true,
		AllowOther:         true,
		DefaultPermissions: true,
	})
	for _, want := range []string{"ro", "allow_other", "default_permissions"} {
		if !hasMountOption(opts, want) {
			t.Fatalf("mount options %v missing %q", opts, want)
		}
	}
	if hasMountOption(opts, "rw") {
		t.Fatalf("mount options %v should not include rw", opts)
	}
}

func TestAdapterStatfsUsesConfiguredSpace(t *testing.T) {
	ad := newAdapter(stubSpaceFS{
		space: drive.Space{Total: 2 << 40, Free: 1 << 40},
	}, StatfsOptions{
		TotalSpace: 1 << 40,
		FreeSpace:  512 << 30,
	})

	var stat fuse.Statfs_t
	if errc := ad.Statfs("/", &stat); errc != 0 {
		t.Fatalf("Statfs err = %d, want 0", errc)
	}
	if stat.Bsize != 4096 || stat.Frsize != 4096 {
		t.Fatalf("Statfs block size = %d/%d, want 4096/4096", stat.Bsize, stat.Frsize)
	}
	if stat.Blocks != (1<<40)/4096 {
		t.Fatalf("Statfs blocks = %d, want %d", stat.Blocks, (1<<40)/4096)
	}
	if stat.Bavail != (512<<30)/4096 {
		t.Fatalf("Statfs available blocks = %d, want %d", stat.Bavail, (512<<30)/4096)
	}
}

func TestAdapterStatfsUsesAutomaticSpace(t *testing.T) {
	ad := newAdapter(stubSpaceFS{
		space: drive.Space{Total: 3 << 40, Free: 2 << 40},
	}, StatfsOptions{})

	var stat fuse.Statfs_t
	if errc := ad.Statfs("/", &stat); errc != 0 {
		t.Fatalf("Statfs err = %d, want 0", errc)
	}
	if stat.Blocks != (3<<40)/4096 {
		t.Fatalf("Statfs blocks = %d, want %d", stat.Blocks, (3<<40)/4096)
	}
	if stat.Bavail != (2<<40)/4096 {
		t.Fatalf("Statfs available blocks = %d, want %d", stat.Bavail, (2<<40)/4096)
	}
}

func TestAdapterShutdownCancelsActiveRead(t *testing.T) {
	fs := blockingReadFS{
		stubFS: stubFS{entries: map[string]drive.Entry{
			"/file.txt": {ID: "file-id", Name: "file.txt", Size: 8},
		}},
		entered: make(chan struct{}),
	}
	ad := newAdapter(fs, StatfsOptions{})
	result := make(chan int, 1)
	go func() {
		result <- ad.Read("/file.txt", make([]byte, 8), 0, 1)
	}()

	select {
	case <-fs.entered:
	case <-time.After(time.Second):
		t.Fatal("read did not enter filesystem")
	}
	if active := ad.activeOpsSnapshot(); len(active) != 1 || active[0].Op != "Read" {
		t.Fatalf("active ops = %#v, want one Read", active)
	}

	ad.shutdown()

	select {
	case got := <-result:
		if got != -fuse.EIO {
			t.Fatalf("Read after shutdown returned %d, want %d", got, -fuse.EIO)
		}
	case <-time.After(time.Second):
		t.Fatal("read did not unblock after shutdown")
	}
	if active := ad.activeOpsSnapshot(); len(active) != 0 {
		t.Fatalf("active ops after read returned = %#v, want empty", active)
	}
}

func TestAdapterRejectsNewWriteAfterShutdown(t *testing.T) {
	ad := newAdapter(&createRouteFS{}, StatfsOptions{})
	ad.shutdown()

	if got := ad.Write("/file.txt", []byte("data"), 0, 1); got != -fuse.EIO {
		t.Fatalf("Write after shutdown returned %d, want %d", got, -fuse.EIO)
	}
	if active := ad.activeOpsSnapshot(); len(active) != 0 {
		t.Fatalf("active ops = %#v, want empty", active)
	}
}

func TestAdapterXattrsNoop(t *testing.T) {
	ad := newAdapter(stubFS{entries: map[string]drive.Entry{
		"/": {ID: "root", Name: "", IsDir: true, ModTime: time.Unix(1, 0)},
	}}, StatfsOptions{})

	if errc, _ := ad.Getxattr("/", "com.apple.FinderInfo"); errc != -fuse.ENOATTR {
		t.Fatalf("Getxattr FinderInfo err = %d, want ENOATTR", errc)
	}
	if errc, _ := ad.Getxattr("/", "com.apple.ResourceFork"); errc != -fuse.ENOATTR {
		t.Fatalf("Getxattr ResourceFork err = %d, want ENOATTR", errc)
	}
	if errc, _ := ad.Getxattr("/", "user.foo"); errc != -fuse.ENOATTR {
		t.Fatalf("Getxattr unknown err = %d, want ENOATTR", errc)
	}
	if errc := ad.Setxattr("/", "user.foo", []byte("bar"), 0); errc != 0 {
		t.Fatalf("Setxattr err = %d, want 0", errc)
	}
	if errc := ad.Removexattr("/", "user.foo"); errc != 0 {
		t.Fatalf("Removexattr err = %d, want 0", errc)
	}
	if errc := ad.Listxattr("/", func(name string) bool { return true }); errc != 0 {
		t.Fatalf("Listxattr err = %d, want 0", errc)
	}
}

func TestAdapterXattrsAllNoop(t *testing.T) {
	ad := newAdapter(stubFS{entries: map[string]drive.Entry{
		"/": {ID: "root", Name: "", IsDir: true},
	}}, StatfsOptions{})

	// All xattr operations are no-ops that return success.
	if errc := ad.Setxattr("/", "user.foo", []byte("bar"), fuse.XATTR_CREATE); errc != 0 {
		t.Fatalf("Setxattr err = %d, want 0", errc)
	}
	if errc := ad.Removexattr("/", "user.foo"); errc != 0 {
		t.Fatalf("Removexattr err = %d, want 0", errc)
	}
	if errc := ad.Setxattr("/", "user.foo", nil, fuse.XATTR_REPLACE); errc != 0 {
		t.Fatalf("Setxattr XATTR_REPLACE err = %d, want 0", errc)
	}
	// Getxattr always returns ENOATTR.
	if errc, got := ad.Getxattr("/", "user.foo"); errc != -fuse.ENOATTR || len(got) != 0 {
		t.Fatalf("Getxattr err=%d len=%d, want ENOATTR/0", errc, len(got))
	}
	// Listxattr always returns empty list.
	names := map[string]bool{}
	if errc := ad.Listxattr("/", func(name string) bool {
		names[name] = true
		return true
	}); errc != 0 {
		t.Fatalf("Listxattr err = %d, want 0", errc)
	}
	if len(names) != 0 {
		t.Fatalf("Listxattr names = %v, want empty", names)
	}
}

func TestAdapterNoAppleXattrIgnoresAppleXattrs(t *testing.T) {
	ad := newAdapterWithOptions(stubFS{}, adapterOptions{IgnoreAppleXattr: true})

	if errc := ad.Setxattr("/", "com.apple.FinderInfo", []byte("ignored"), 0); errc != 0 {
		t.Fatalf("Setxattr FinderInfo err = %d, want 0", errc)
	}
	if errc, got := ad.Getxattr("/", "com.apple.ResourceFork"); errc != -fuse.ENOATTR || len(got) != 0 {
		t.Fatalf("Getxattr ResourceFork err=%d len=%d, want ENOATTR/0", errc, len(got))
	}
	if errc := ad.Removexattr("/", "com.apple.quarantine"); errc != 0 {
		t.Fatalf("Removexattr quarantine err = %d, want 0", errc)
	}
	if errc := ad.Setxattr("/", "user.foo", []byte("bar"), 0); errc != 0 {
		t.Fatalf("Setxattr user.foo err = %d, want 0", errc)
	}
}

func TestAdapterNoAppleXattrStillPreparesFinderDirectoryCopy(t *testing.T) {
	fs := &copyPrepareFS{}
	ad := newAdapterWithOptions(fs, adapterOptions{IgnoreAppleXattr: true})

	if errc := ad.Setxattr("/copied", "com.apple.finder.copy.source", []byte("source"), 0); errc != 0 {
		t.Fatalf("Setxattr copy source err = %d, want 0", errc)
	}
	if got := strings.Join(fs.prepared, ","); got != "/copied" {
		t.Fatalf("prepared = %q, want /copied", got)
	}
}

func TestAdapterCreateRoutesFinderDirectoryCreatesToMkdir(t *testing.T) {
	fs := &createRouteFS{stubFS: stubFS{entries: map[string]drive.Entry{
		"/": {ID: "root", Name: "", IsDir: true},
	}}}
	ad := newAdapter(fs, StatfsOptions{})

	if errc, fh := ad.Create("/_nuxt", 0, fuse.S_IFREG|0o644); errc != 0 || fh != 0 {
		t.Fatalf("Create extensionless err=%d fh=%d, want 0/0", errc, fh)
	}
	if got := strings.Join(fs.mkdirs, ","); got != "/_nuxt" {
		t.Fatalf("mkdirs = %q, want /_nuxt", got)
	}
	if len(fs.created) != 0 {
		t.Fatalf("created = %v, want none", fs.created)
	}

	if errc, fh := ad.Create("/asset.js", 0, fuse.S_IFREG|0o644); errc != 0 || fh == 0 {
		t.Fatalf("Create file err=%d fh=%d, want err 0 and nonzero fh", errc, fh)
	}
	if got := strings.Join(fs.created, ","); got != "/asset.js" {
		t.Fatalf("created = %q, want /asset.js", got)
	}
}

func TestAdapterMknodCreatesRegularFile(t *testing.T) {
	fs := &createRouteFS{stubFS: stubFS{entries: map[string]drive.Entry{
		"/": {ID: "root", Name: "", IsDir: true},
	}}}
	ad := newAdapter(fs, StatfsOptions{})

	if errc := ad.Mknod("/asset.js", fuse.S_IFREG|0o644, 0); errc != 0 {
		t.Fatalf("Mknod err = %d, want 0", errc)
	}
	if got := strings.Join(fs.created, ","); got != "/asset.js" {
		t.Fatalf("created = %q, want /asset.js", got)
	}
}

func TestAdapterNoAppleDoubleIgnoresAppleMetadata(t *testing.T) {
	fs := &createRouteFS{}
	ad := newAdapterWithOptions(fs, adapterOptions{IgnoreAppleMetadata: true})

	if errc, fh := ad.Create("/folder/.DS_Store", 0, fuse.S_IFREG|0o644); errc != 0 || fh == 0 {
		t.Fatalf("Create .DS_Store err=%d fh=%d, want success with fh", errc, fh)
	}
	if got := ad.Write("/folder/.DS_Store", []byte("finder"), 0, 1); got != len("finder") {
		t.Fatalf("Write .DS_Store = %d, want %d", got, len("finder"))
	}
	if errc := ad.Flush("/folder/.DS_Store", 1); errc != 0 {
		t.Fatalf("Flush .DS_Store err=%d, want 0", errc)
	}
	if errc := ad.Mknod("/folder/._asset.js", fuse.S_IFREG|0o644, 0); errc != 0 {
		t.Fatalf("Mknod AppleDouble err=%d, want 0", errc)
	}
	if errc := ad.Mkdir("/.Spotlight-V100", 0o755); errc != 0 {
		t.Fatalf("Mkdir Spotlight err=%d, want 0", errc)
	}
	if got := ad.Write("/.Spotlight-V100/store", []byte("ignored"), 0, 0); got != len("ignored") {
		t.Fatalf("Write Spotlight child = %d, want %d", got, len("ignored"))
	}
	if errc := ad.Rename("/.DS_Store", "/.DS_Store.tmp"); errc != 0 {
		t.Fatalf("Rename .DS_Store err=%d, want 0", errc)
	}
	if errc := ad.Unlink("/.DS_Store"); errc != 0 {
		t.Fatalf("Unlink .DS_Store err=%d, want 0", errc)
	}
	if errc, fh := ad.Create("/.DS_Store", 0, fuse.S_IFREG|0o644); errc != 0 || fh == 0 {
		t.Fatalf("Create second .DS_Store err=%d fh=%d, want success with fh", errc, fh)
	}
	if got := ad.Write("/.DS_Store", []byte("finder"), 0, 1); got != len("finder") {
		t.Fatalf("Write second .DS_Store = %d, want %d", got, len("finder"))
	}
	var stat fuse.Stat_t
	if errc := ad.Getattr("/.DS_Store", &stat, 0); errc != 0 {
		t.Fatalf("Getattr .DS_Store err=%d, want 0", errc)
	}
	if stat.Size != int64(len("finder")) || stat.Mode&fuse.S_IFREG == 0 {
		t.Fatalf("Getattr .DS_Store stat mode=%o size=%d, want regular file size %d", stat.Mode, stat.Size, len("finder"))
	}
	buf := make([]byte, 16)
	if got := ad.Read("/.DS_Store", buf, 2, 1); got != len("finder")-2 {
		t.Fatalf("Read .DS_Store = %d, want %d", got, len("finder")-2)
	}
	if errc := ad.Truncate("/.DS_Store", 2, 1); errc != 0 {
		t.Fatalf("Truncate .DS_Store err=%d, want 0", errc)
	}
	if errc := ad.Rename("/.DS_Store", "/.DS_Store.tmp"); errc != 0 {
		t.Fatalf("Rename second .DS_Store err=%d, want 0", errc)
	}
	if errc := ad.Getattr("/.DS_Store.tmp", &stat, 0); errc != 0 {
		t.Fatalf("Getattr renamed .DS_Store err=%d, want 0", errc)
	}
	if stat.Size != 2 {
		t.Fatalf("renamed .DS_Store size=%d, want 2", stat.Size)
	}
	if errc := ad.Readdir("/.Spotlight-V100", func(string, *fuse.Stat_t, int64) bool { return true }, 0, 0); errc != 0 {
		t.Fatalf("Readdir Spotlight err=%d, want 0", errc)
	}

	if len(fs.created) != 0 || len(fs.mkdirs) != 0 || len(fs.writes) != 0 || len(fs.flushes) != 0 ||
		len(fs.removed) != 0 || len(fs.rmdirs) != 0 || len(fs.renames) != 0 || len(fs.truncate) != 0 {
		t.Fatalf("backend calls created=%v mkdirs=%v writes=%v flushes=%v removed=%v rmdirs=%v renames=%v truncate=%v, want none",
			fs.created, fs.mkdirs, fs.writes, fs.flushes, fs.removed, fs.rmdirs, fs.renames, fs.truncate)
	}
}

func TestAdapterNoAppleDoubleBypassesReadOnlyRootMetadata(t *testing.T) {
	ad := newAdapterWithOptions(stubFS{
		readOnly: map[string]bool{"/.DS_Store": true},
	}, adapterOptions{IgnoreAppleMetadata: true})

	if errc := ad.Truncate("/.DS_Store", 0, 1); errc != 0 {
		t.Fatalf("Truncate read-only .DS_Store err=%d, want 0", errc)
	}
	if got := ad.Write("/.DS_Store", []byte("Bud1"), 0, 1); got != 4 {
		t.Fatalf("Write read-only .DS_Store = %d, want 4", got)
	}
	if errc := ad.Unlink("/.DS_Store"); errc != 0 {
		t.Fatalf("Unlink read-only .DS_Store err=%d, want 0", errc)
	}
}

func TestAdapterNoAppleDoubleFalseUploadsAppleMetadata(t *testing.T) {
	fs := &createRouteFS{}
	ad := newAdapterWithOptions(fs, adapterOptions{IgnoreAppleMetadata: false})

	errc, fh := ad.Create("/.DS_Store", 0, fuse.S_IFREG|0o644)
	if errc != 0 || fh == 0 {
		t.Fatalf("Create .DS_Store err=%d fh=%d, want success with fh", errc, fh)
	}
	if got := ad.Write("/.DS_Store", []byte("finder"), 0, fh); got != len("finder") {
		t.Fatalf("Write .DS_Store = %d, want %d", got, len("finder"))
	}
	if errc := ad.Flush("/.DS_Store", fh); errc != 0 {
		t.Fatalf("Flush .DS_Store err=%d, want 0", errc)
	}
	if errc := ad.Mknod("/._asset.js", fuse.S_IFREG|0o644, 0); errc != 0 {
		t.Fatalf("Mknod AppleDouble err=%d, want 0", errc)
	}
	if errc := ad.Mkdir("/.Spotlight-V100", 0o755); errc != 0 {
		t.Fatalf("Mkdir Spotlight err=%d, want 0", errc)
	}

	if got := strings.Join(fs.created, ","); got != "/.DS_Store,/._asset.js" {
		t.Fatalf("created = %q, want Apple metadata files", got)
	}
	if got := strings.Join(fs.writes, ","); got != "/.DS_Store" {
		t.Fatalf("writes = %q, want .DS_Store", got)
	}
	if got := strings.Join(fs.flushes, ","); got != "/.DS_Store" {
		t.Fatalf("flushes = %q, want .DS_Store", got)
	}
	if got := strings.Join(fs.mkdirs, ","); got != "/.Spotlight-V100" {
		t.Fatalf("mkdirs = %q, want Spotlight", got)
	}
}

func TestAdapterResourceForkIsEmptyNoop(t *testing.T) {
	ad := newAdapter(stubFS{entries: map[string]drive.Entry{
		"/": {ID: "root", Name: "", IsDir: true},
	}}, StatfsOptions{})
	const name = "com.apple.ResourceFork"

	errc, value := ad.Getxattr("/", name)
	if errc != -fuse.ENOATTR {
		t.Fatalf("Getxattr ResourceFork err = %d, want ENOATTR", errc)
	}
	if len(value) != 0 {
		t.Fatalf("Getxattr ResourceFork len = %d, want 0", len(value))
	}
	if errc := ad.Setxattr("/", name, []byte("ignored"), 0); errc != 0 {
		t.Fatalf("Setxattr ResourceFork err = %d, want 0", errc)
	}
	if errc := ad.Removexattr("/", name); errc != 0 {
		t.Fatalf("Removexattr ResourceFork err = %d, want 0", errc)
	}
}

func TestAdapterXattrsMissingPath(t *testing.T) {
	ad := newAdapter(stubFS{entries: map[string]drive.Entry{}}, StatfsOptions{})
	// xattr operations are no-ops that don't check path existence.
	if errc, _ := ad.Getxattr("/missing", "x"); errc != -fuse.ENOATTR {
		t.Fatalf("Getxattr missing err = %d, want ENOATTR", errc)
	}
	if errc := ad.Setxattr("/missing", "x", nil, 0); errc != 0 {
		t.Fatalf("Setxattr missing err = %d, want 0", errc)
	}
	if errc := ad.Listxattr("/missing", func(string) bool { return true }); errc != 0 {
		t.Fatalf("Listxattr missing err = %d, want 0", errc)
	}
}

func TestAdapterGlobalReadOnlyModeAndWriteErrors(t *testing.T) {
	ad := newAdapterWithOptions(stubFS{
		entries: map[string]drive.Entry{
			"/": {ID: "root", Name: "", IsDir: true},
		},
	}, adapterOptions{ReadOnly: true})

	if errc := ad.Access("/", fuse.W_OK); errc != -fuse.EROFS {
		t.Fatalf("Access W_OK err = %d, want EROFS", errc)
	}
	if errc, fh := ad.Create("/new.txt", 0, fuse.S_IFREG|0o644); errc != -fuse.EROFS || fh != 0 {
		t.Fatalf("Create read-only err=%d fh=%d, want EROFS/0", errc, fh)
	}
	if got := ad.Write("/file.txt", []byte("x"), 0, 0); got != -fuse.EROFS {
		t.Fatalf("Write read-only = %d, want EROFS", got)
	}
}

func TestAdapterReadOnlyPathModeAndWriteErrors(t *testing.T) {
	ad := newAdapter(stubFS{
		entries: map[string]drive.Entry{
			"/":  {ID: "root", Name: "", IsDir: true},
			"/a": {ID: "/a", Name: "a", IsDir: true},
		},
		lists: map[string][]drive.Entry{
			"/": {{ID: "/a", Name: "a", IsDir: true}},
		},
		readOnly: map[string]bool{"/": true, "/a": true, "/new": true},
	}, StatfsOptions{})

	var stat fuse.Stat_t
	if errc := ad.Getattr("/a", &stat, ^uint64(0)); errc != 0 {
		t.Fatalf("Getattr err = %d, want 0", errc)
	}
	if stat.Mode&0o222 != 0 {
		t.Fatalf("readonly dir mode = %o, want no write bits", stat.Mode)
	}
	var listed fuse.Stat_t
	if errc := ad.Readdir("/", func(name string, stat *fuse.Stat_t, ofst int64) bool {
		if name == "a" {
			listed = *stat
		}
		return true
	}, 0, ^uint64(0)); errc != 0 {
		t.Fatalf("Readdir err = %d, want 0", errc)
	}
	if listed.Mode&0o222 != 0 {
		t.Fatalf("readonly readdir mode = %o, want no write bits", listed.Mode)
	}
	if errc := ad.Mkdir("/new", 0o755); errc != -fuse.EROFS {
		t.Fatalf("Mkdir readonly err = %d, want EROFS", errc)
	}
	if errc := ad.Rename("/a", "/renamed"); errc != -fuse.EROFS {
		t.Fatalf("Rename readonly err = %d, want EROFS", errc)
	}
	if errc := ad.Chmod("/a", 0o777); errc != -fuse.EROFS {
		t.Fatalf("Chmod readonly err = %d, want EROFS", errc)
	}
}

func TestAdapterGetattrMapsOnlyNotFoundToENOENT(t *testing.T) {
	ad := newAdapter(failingStatFS{err: errors.New("backend unavailable")}, StatfsOptions{})

	var stat fuse.Stat_t
	if errc := ad.Getattr("/file.txt", &stat, 0); errc != -fuse.EIO {
		t.Fatalf("Getattr backend error = %d, want EIO", errc)
	}
}

func TestAdapterMetadataCallbacksMapOnlyNotFoundToENOENT(t *testing.T) {
	statErr := errors.New("stat backend unavailable")
	listErr := errors.New("list backend unavailable")
	statAdapter := newAdapter(failingStatFS{err: statErr}, StatfsOptions{})
	listAdapter := newAdapter(failingListFS{err: listErr}, StatfsOptions{})

	if errc := statAdapter.Access("/file.txt", 0); errc != -fuse.EIO {
		t.Fatalf("Access backend error = %d, want EIO", errc)
	}
	if errc := listAdapter.Readdir("/dir", func(string, *fuse.Stat_t, int64) bool { return true }, 0, 0); errc != -fuse.EIO {
		t.Fatalf("Readdir backend error = %d, want EIO", errc)
	}
	if errc := statAdapter.Fsyncdir("/dir", false, 0); errc != -fuse.EIO {
		t.Fatalf("Fsyncdir backend error = %d, want EIO", errc)
	}
	if errc := statAdapter.Chflags("/file.txt", 0); errc != -fuse.EIO {
		t.Fatalf("Chflags backend error = %d, want EIO", errc)
	}
	if errc := statAdapter.Setcrtime("/file.txt", fuse.NewTimespec(time.Unix(1, 0))); errc != -fuse.EIO {
		t.Fatalf("Setcrtime backend error = %d, want EIO", errc)
	}
	if errc := statAdapter.Setchgtime("/file.txt", fuse.NewTimespec(time.Unix(1, 0))); errc != -fuse.EIO {
		t.Fatalf("Setchgtime backend error = %d, want EIO", errc)
	}
}

func TestAdapterGetattrUsesOpenHandleSnapshotWhenPathDisappears(t *testing.T) {
	entries := map[string]drive.Entry{
		"/file.txt": {ID: "file-id", Name: "file.txt", Size: 12},
	}
	ad := newAdapter(stubFS{entries: entries}, StatfsOptions{})

	errc, fh := ad.Open("/file.txt", 0)
	if errc != 0 || fh == 0 {
		t.Fatalf("Open err=%d fh=%d, want success", errc, fh)
	}
	delete(entries, "/file.txt")

	var stat fuse.Stat_t
	if errc := ad.Getattr("/file.txt", &stat, fh); errc != 0 {
		t.Fatalf("Getattr with open fh err=%d, want 0", errc)
	}
	if stat.Size != 12 || stat.Mode&fuse.S_IFREG == 0 {
		t.Fatalf("Getattr with open fh mode=%o size=%d, want regular file size 12", stat.Mode, stat.Size)
	}
}

func TestStableInodeFallsBackToPathWhenIDEmpty(t *testing.T) {
	entry := drive.Entry{Name: "same.txt"}

	inoA := stableInode(entry, "/a/same.txt")
	inoB := stableInode(entry, "/b/same.txt")
	if inoA == inoB {
		t.Fatalf("stableInode with empty ID returned same inode %d for different paths", inoA)
	}
}

func TestAdapterWritableMountRootModeAllowsFinderDrop(t *testing.T) {
	ctx := context.Background()
	fsA, err := vfs.New(localfs.New(t.TempDir()), vfs.Options{CacheDir: filepath.Join(t.TempDir(), "a")})
	if err != nil {
		t.Fatal(err)
	}
	ns, err := vfs.NewNamespace([]vfs.Mount{{Name: "a", FS: fsA}})
	if err != nil {
		t.Fatal(err)
	}
	ns.Start(ctx)
	ad := newAdapter(ns, StatfsOptions{})

	var stat fuse.Stat_t
	if errc := ad.Getattr("/a", &stat, 0); errc != 0 {
		t.Fatalf("Getattr mount root err=%d, want 0", errc)
	}
	if stat.Mode&0o222 == 0 {
		t.Fatalf("mount root mode=%o, want write bits visible for Finder", stat.Mode)
	}
	if errc := ad.Access("/a", fuse.W_OK); errc != 0 {
		t.Fatalf("Access W_OK mount root err=%d, want 0", errc)
	}
	if errc := ad.Mkdir("/a/copied", 0o755); errc != 0 {
		t.Fatalf("Mkdir child under mount root err=%d, want 0", errc)
	}
	if errc := ad.Mkdir("/a", 0o755); errc != -fuse.EROFS {
		t.Fatalf("Mkdir mount root itself err=%d, want EROFS", errc)
	}
}

func hasMountOption(opts []string, want string) bool {
	for i := 0; i < len(opts)-1; i++ {
		if opts[i] == "-o" && opts[i+1] == want {
			return true
		}
	}
	return false
}
