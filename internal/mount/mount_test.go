package mount

import (
	"context"
	"errors"
	"io"
	"os"
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

func (stubFS) Start(context.Context) {}

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
func (stubFS) Rename(context.Context, string, string) error                { return nil }
func (stubFS) Truncate(context.Context, string, int64) error               { return nil }
func (stubFS) Pending() []vfs.PendingFile                                  { return nil }

var errNotFound = errors.New("not found")

func TestMountOptionsUseStableMetadataCaching(t *testing.T) {
	opts := mountOptions(Options{})
	for _, want := range []string{"attr_timeout=10", "entry_timeout=10", "negative_timeout=0", "use_ino"} {
		if !hasMountOption(opts, want) {
			t.Fatalf("mount options %v missing %q", opts, want)
		}
	}
	if runtime.GOOS != "darwin" {
		return
	}
	for _, want := range []string{"defer_permissions", "fsname=qrypt", "subtype=qrypt", "local", "iosize=1048576"} {
		if !hasMountOption(opts, want) {
			t.Fatalf("darwin mount options %v missing %q", opts, want)
		}
	}
}

func TestTraceLoggerUsesConfiguredFile(t *testing.T) {
	t.Setenv("QRYPT_FUSE_TRACE", "")
	t.Setenv("QRYPT_FUSE_TRACE_FILE", "")
	path := filepath.Join(t.TempDir(), "fuse.log")
	logger := newTraceLogger(TraceOptions{Enabled: true, File: path})
	logger.log("TestOp", "/path", "err=%d", 0)
	logger.close()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `TestOp path="/path" err=0`) {
		t.Fatalf("trace log missing operation: %q", data)
	}
}

func TestTraceLoggerConfiguredFileRequiresEnable(t *testing.T) {
	t.Setenv("QRYPT_FUSE_TRACE", "")
	t.Setenv("QRYPT_FUSE_TRACE_FILE", "")
	path := filepath.Join(t.TempDir(), "fuse.log")
	logger := newTraceLogger(TraceOptions{File: path})
	logger.log("TestOp", "/path", "err=%d", 0)
	logger.close()

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("trace file should not exist when tracing is disabled: %v", err)
	}
}

func TestAdapterXattrsProvideStableFinderInfo(t *testing.T) {
	ad := newAdapter(stubFS{entries: map[string]drive.Entry{
		"/": {ID: "root", Name: "", IsDir: true, ModTime: time.Unix(1, 0)},
	}}, TraceOptions{})

	errc, value := ad.Getxattr("/", "com.apple.FinderInfo")
	if errc != 0 {
		t.Fatalf("Getxattr FinderInfo err = %d, want 0", errc)
	}
	if len(value) != 32 {
		t.Fatalf("FinderInfo len = %d, want 32", len(value))
	}

	var names []string
	errc = ad.Listxattr("/", func(name string) bool {
		names = append(names, name)
		return true
	})
	if errc != 0 {
		t.Fatalf("Listxattr err = %d, want 0", errc)
	}
	if len(names) != 1 || names[0] != "com.apple.FinderInfo" {
		t.Fatalf("Listxattr names = %v, want FinderInfo", names)
	}
}

func TestAdapterXattrsSetListRemove(t *testing.T) {
	ad := newAdapter(stubFS{entries: map[string]drive.Entry{
		"/": {ID: "root", Name: "", IsDir: true},
	}}, TraceOptions{})
	const name = "com.apple.metadata:_kMDItemUserTags"
	value := []byte("tags")

	if errc := ad.Setxattr("/", name, value, fuse.XATTR_CREATE); errc != 0 {
		t.Fatalf("Setxattr err = %d, want 0", errc)
	}
	value[0] = 'T'
	errc, got := ad.Getxattr("/", name)
	if errc != 0 {
		t.Fatalf("Getxattr err = %d, want 0", errc)
	}
	if string(got) != "tags" {
		t.Fatalf("Getxattr value = %q, want tags", got)
	}

	names := map[string]bool{}
	errc = ad.Listxattr("/", func(name string) bool {
		names[name] = true
		return true
	})
	if errc != 0 {
		t.Fatalf("Listxattr err = %d, want 0", errc)
	}
	if !names["com.apple.FinderInfo"] || !names[name] {
		t.Fatalf("Listxattr names = %v, want FinderInfo and stored xattr", names)
	}

	if errc := ad.Removexattr("/", name); errc != 0 {
		t.Fatalf("Removexattr err = %d, want 0", errc)
	}
	if errc, _ := ad.Getxattr("/", name); errc != -fuse.ENOATTR {
		t.Fatalf("Getxattr after remove err = %d, want ENOATTR", errc)
	}
}

func TestAdapterXattrsMissingPath(t *testing.T) {
	ad := newAdapter(stubFS{entries: map[string]drive.Entry{}}, TraceOptions{})
	if errc, _ := ad.Getxattr("/missing", "com.apple.FinderInfo"); errc != -fuse.ENOENT {
		t.Fatalf("Getxattr missing err = %d, want ENOENT", errc)
	}
	if errc := ad.Setxattr("/missing", "com.apple.FinderInfo", nil, 0); errc != -fuse.ENOENT {
		t.Fatalf("Setxattr missing err = %d, want ENOENT", errc)
	}
	if errc := ad.Listxattr("/missing", func(string) bool { return true }); errc != -fuse.ENOENT {
		t.Fatalf("Listxattr missing err = %d, want ENOENT", errc)
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
	}, TraceOptions{})

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
	if errc := ad.Setxattr("/a", "com.apple.FinderInfo", nil, 0); errc != -fuse.EROFS {
		t.Fatalf("Setxattr readonly err = %d, want EROFS", errc)
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
