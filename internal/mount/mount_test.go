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
	created []string
	mkdirs  []string
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

var errNotFound = errors.New("not found")

func TestMountOptionsUseStableMetadataCaching(t *testing.T) {
	opts := mountOptions(Options{})
	for _, want := range []string{"attr_timeout=0", "entry_timeout=0", "negative_timeout=0", "use_ino"} {
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

func hasMountOption(opts []string, want string) bool {
	for i := 0; i < len(opts)-1; i++ {
		if opts[i] == "-o" && opts[i+1] == want {
			return true
		}
	}
	return false
}
