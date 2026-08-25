package mount

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/winfsp/cgofuse/fuse"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/drivers/localfs"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
	vfsread "github.com/yinzhenyu/qrypt/pkg/vfs/read"
)

type stubFS struct {
	entries  map[string]drive.Entry
	lists    map[string][]drive.Entry
	readOnly map[string]bool
}

type stubSpaceFS struct {
	stubFS
	space      drive.Space
	mu         sync.Mutex
	spaceCalls int
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

type readHintFS struct {
	stubFS
	mu       sync.Mutex
	hints    []vfsread.AccessHint
	released []uint64
}

type invalidationStubFS struct {
	stubFS
	listener     func(string)
	unsubscribed bool
}

func (s *invalidationStubFS) SubscribeInvalidations(listener func(string)) func() {
	s.listener = listener
	return func() {
		s.unsubscribed = true
		s.listener = nil
	}
}

func (s *invalidationStubFS) emitInvalidation(path string) {
	if s.listener != nil {
		s.listener(path)
	}
}

func (stubFS) Start(context.Context) {}

func (s *stubSpaceFS) Space(context.Context) (drive.Space, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.spaceCalls++
	return s.space, nil
}

func (s *stubSpaceFS) SpaceCalls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.spaceCalls
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

type crossMountRenameFS struct {
	stubFS
}

func (crossMountRenameFS) Rename(context.Context, string, string) error {
	return vfs.ErrCrossMount
}

func (s blockingReadFS) Read(ctx context.Context, path string, offset, size int64) (io.ReadCloser, error) {
	close(s.entered)
	<-ctx.Done()
	return nil, ctx.Err()
}

func (s *readHintFS) Read(ctx context.Context, _ string, _, _ int64) (io.ReadCloser, error) {
	hint, ok := vfsread.AccessHintFromContext(ctx)
	if !ok {
		return nil, errors.New("missing read access hint")
	}
	s.mu.Lock()
	s.hints = append(s.hints, hint)
	s.mu.Unlock()
	return io.NopCloser(strings.NewReader("x")), nil
}

func (s *readHintFS) ReleaseReadSession(sessionID uint64) {
	s.mu.Lock()
	s.released = append(s.released, sessionID)
	s.mu.Unlock()
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
func (stubFS) RefreshPath(string)                                          {}
func (stubFS) PendingUploads() []vfs.PendingUpload                         { return nil }

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
	opts := mountOptions(Options{PlatformOptions: map[string][]string{
		"darwin": {"defer_permissions", "auto_xattr", "iosize=8388608"},
	}})
	for _, want := range []string{"attr_timeout=1", "entry_timeout=1", "negative_timeout=0", "use_ino"} {
		if !hasMountOption(opts, want) {
			t.Fatalf("mount options %v missing %q", opts, want)
		}
	}
	if runtime.GOOS != "darwin" {
		return
	}
	for _, want := range []string{"defer_permissions", "auto_xattr", "fsname=qrypt", "subtype=qrypt", "iosize=8388608"} {
		if !hasMountOption(opts, want) {
			t.Fatalf("darwin mount options %v missing %q", opts, want)
		}
	}
}

func TestMountInvalidationPaths(t *testing.T) {
	tests := []struct {
		name string
		goos string
		path string
		want []string
	}{
		{name: "unix nested", goos: "darwin", path: "/dir/file.bin", want: []string{"/dir/file.bin", "/dir"}},
		{name: "unix root child", goos: "linux", path: "/file.bin", want: []string{"/file.bin", "/"}},
		{name: "windows", goos: "windows", path: "/dir/file.bin", want: []string{"/dir/file.bin"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := mountInvalidationPaths(tc.path, tc.goos)
			if strings.Join(got, "|") != strings.Join(tc.want, "|") {
				t.Fatalf("paths = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSubscribeInvalidationsNotifiesAndUnsubscribes(t *testing.T) {
	fs := &invalidationStubFS{}
	type notification struct {
		path   string
		action uint32
	}
	notifications := make(chan notification, 4)
	stop := subscribeInvalidations(fs, func(path string, action uint32) bool {
		notifications <- notification{path: path, action: action}
		return true
	})

	fs.emitInvalidation("/dir/file.bin")
	wantPaths := mountInvalidationPaths("/dir/file.bin", runtime.GOOS)
	for i, wantPath := range wantPaths {
		var got notification
		select {
		case got = <-notifications:
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for notification[%d] path=%q", i, wantPath)
		}
		if got.path != wantPath {
			t.Fatalf("notification[%d].path = %q, want %q", i, got.path, wantPath)
		}
		wantAction := uint32(fuse.NOTIFY_CREATE | fuse.NOTIFY_TRUNCATE)
		if got.action != wantAction {
			t.Fatalf("notification[%d].action = %#x, want %#x", i, got.action, wantAction)
		}
	}

	stop()
	stop()
	if !fs.unsubscribed {
		t.Fatal("invalidation source was not unsubscribed")
	}
	fs.emitInvalidation("/ignored.bin")
	select {
	case got := <-notifications:
		t.Fatalf("notification after unsubscribe = %+v", got)
	default:
	}
}

func TestSubscribeInvalidationsDoesNotBlockPublisherAndCloseWaitsForNotify(t *testing.T) {
	fs := &invalidationStubFS{}
	entered := make(chan struct{})
	release := make(chan struct{})
	var enteredOnce sync.Once
	stop := subscribeInvalidations(fs, func(string, uint32) bool {
		enteredOnce.Do(func() { close(entered) })
		<-release
		return true
	})

	published := make(chan struct{})
	go func() {
		fs.emitInvalidation("/slow.bin")
		close(published)
	}()
	select {
	case <-published:
	case <-time.After(time.Second):
		t.Fatal("invalidation publisher blocked on notify")
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("notify worker did not start")
	}

	stopped := make(chan struct{})
	go func() {
		stop()
		close(stopped)
	}()
	select {
	case <-stopped:
		t.Fatal("unsubscribe returned while notify was still running")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("unsubscribe did not finish after notify returned")
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

func TestMountOptionsUseOnlyConfiguredPlatformOptions(t *testing.T) {
	opts := Options{PlatformOptions: map[string][]string{
		"darwin":  {"auto_xattr"},
		"windows": {"FileInfoTimeout=1000"},
	}}
	for _, test := range []struct {
		goos string
		want string
		have string
	}{
		{goos: "darwin", want: "auto_xattr", have: "FileInfoTimeout=1000"},
		{goos: "windows", want: "FileInfoTimeout=1000", have: "auto_xattr"},
	} {
		got := mountOptionsForGOOS(opts, test.goos)
		if !hasMountOption(got, test.want) || hasMountOption(got, test.have) {
			t.Fatalf("%s mount options %v want %q without %q", test.goos, got, test.want, test.have)
		}
	}
}

func TestAdapterStatfsUsesConfiguredSpace(t *testing.T) {
	ad := newAdapter(&stubSpaceFS{
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
	fs := &stubSpaceFS{
		space: drive.Space{Total: 3 << 40, Free: 2 << 40},
	}
	ad := newAdapter(fs, StatfsOptions{})

	var stat fuse.Statfs_t
	if errc := ad.Statfs("/", &stat); errc != 0 {
		t.Fatalf("Statfs err = %d, want 0", errc)
	}
	if stat.Blocks != (512<<30)/4096 {
		t.Fatalf("initial Statfs blocks = %d, want immediate default %d", stat.Blocks, (512<<30)/4096)
	}
	deadline := time.Now().Add(time.Second)
	for stat.Blocks != (3<<40)/4096 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
		if errc := ad.Statfs("/", &stat); errc != 0 {
			t.Fatalf("refreshed Statfs err = %d, want 0", errc)
		}
	}
	if stat.Blocks != (3<<40)/4096 || stat.Bavail != (2<<40)/4096 {
		t.Fatalf("refreshed Statfs = blocks %d available %d, want %d/%d", stat.Blocks, stat.Bavail, (3<<40)/4096, (2<<40)/4096)
	}
	if calls := fs.SpaceCalls(); calls != 1 {
		t.Fatalf("Space calls = %d, want cached single call", calls)
	}
}

func TestAdapterInitSignalsReady(t *testing.T) {
	ad := newAdapter(stubFS{}, StatfsOptions{})
	select {
	case <-ad.ready:
		t.Fatal("adapter reported ready before FUSE Init")
	default:
	}
	ad.Init()
	select {
	case <-ad.ready:
	case <-time.After(time.Second):
		t.Fatal("adapter did not report ready after FUSE Init")
	}
	ad.Init()
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

func TestAdapterPassesStableReadSessionAndReleasesIt(t *testing.T) {
	fs := &readHintFS{stubFS: stubFS{entries: map[string]drive.Entry{
		"/file.txt": {ID: "file-id", Name: "file.txt", Size: 2},
	}}}
	ad := newAdapter(fs, StatfsOptions{})
	errno, fh := ad.Open("/file.txt", 0)
	if errno != 0 {
		t.Fatalf("Open errno = %d", errno)
	}
	if got := ad.Read("/file.txt", make([]byte, 1), 0, fh); got != 1 {
		t.Fatalf("first Read = %d, want 1", got)
	}
	if got := ad.Read("/file.txt", make([]byte, 1), 1, fh); got != 1 {
		t.Fatalf("second Read = %d, want 1", got)
	}
	ad.Release("/file.txt", fh)

	fs.mu.Lock()
	defer fs.mu.Unlock()
	if len(fs.hints) != 2 {
		t.Fatalf("hints = %v, want two", fs.hints)
	}
	if fs.hints[0].SessionID == 0 || fs.hints[1].SessionID != fs.hints[0].SessionID {
		t.Fatalf("session ids = %d/%d, want one non-zero session", fs.hints[0].SessionID, fs.hints[1].SessionID)
	}
	if fs.hints[0].RequestID != 1 || fs.hints[1].RequestID != 2 {
		t.Fatalf("request ids = %d/%d, want 1/2", fs.hints[0].RequestID, fs.hints[1].RequestID)
	}
	if len(fs.released) != 1 || fs.released[0] != fs.hints[0].SessionID {
		t.Fatalf("released = %v, want session %d", fs.released, fs.hints[0].SessionID)
	}
}

func TestAdapterDefersReadSessionReleaseUntilConcurrentReadsFinish(t *testing.T) {
	ad := newAdapter(stubFS{}, StatfsOptions{})
	fh := ad.nextHandle("/file.txt")
	first, finishFirst := ad.beginHandleRead(fh)
	second, finishSecond := ad.beginHandleRead(fh)
	if first.Concurrent || !second.Concurrent {
		t.Fatalf("concurrent flags = %v/%v, want false/true", first.Concurrent, second.Concurrent)
	}
	if session := ad.releaseHandle(fh); session != 0 {
		t.Fatalf("Release returned active session %d", session)
	}
	if session := finishSecond(); session != 0 {
		t.Fatalf("second completion released session with first active: %d", session)
	}
	if session := finishFirst(); session != first.SessionID {
		t.Fatalf("last completion released session %d, want %d", session, first.SessionID)
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

func TestAdapterReadOnlyFlushDoesNotTouchBackend(t *testing.T) {
	fs := &createRouteFS{stubFS: stubFS{entries: map[string]drive.Entry{
		"/file.txt": {ID: "file", Name: "file.txt", Size: 5},
	}}}
	ad := newAdapter(fs, StatfsOptions{})

	errc, fh := ad.Open("/file.txt", 0)
	if errc != 0 || fh == 0 {
		t.Fatalf("Open err=%d fh=%d, want success", errc, fh)
	}
	if errc := ad.Flush("/file.txt", fh); errc != 0 {
		t.Fatalf("Flush read-only err=%d, want 0", errc)
	}
	if len(fs.flushes) != 0 {
		t.Fatalf("backend flushes = %v, want none for read-only handle", fs.flushes)
	}
}

func TestAdapterWritableFlushTouchesBackend(t *testing.T) {
	fs := &createRouteFS{}
	ad := newAdapter(fs, StatfsOptions{})

	errc, fh := ad.Create("/file.txt", 1, fuse.S_IFREG|0o644)
	if errc != 0 || fh == 0 {
		t.Fatalf("Create err=%d fh=%d, want success", errc, fh)
	}
	if errc := ad.Flush("/file.txt", fh); errc != 0 {
		t.Fatalf("Flush writable err=%d, want 0", errc)
	}
	if got := strings.Join(fs.flushes, ","); got != "/file.txt" {
		t.Fatalf("backend flushes = %q, want /file.txt", got)
	}
}

func TestAdapterXattrsInMemory(t *testing.T) {
	ad := newAdapter(stubFS{entries: map[string]drive.Entry{
		"/": {ID: "root", Name: "", IsDir: true, ModTime: time.Unix(1, 0)},
	}}, StatfsOptions{})

	if errc, _ := ad.Getxattr("/", "com.apple.FinderInfo"); errc != -fuse.ENOATTR {
		t.Fatalf("Getxattr FinderInfo err = %d, want ENOATTR", errc)
	}
	if errc, _ := ad.Getxattr("/", "user.foo"); errc != -fuse.ENOATTR {
		t.Fatalf("Getxattr unknown err = %d, want ENOATTR", errc)
	}
	if errc := ad.Setxattr("/", "com.apple.FinderInfo", []byte("finder"), 0); errc != 0 {
		t.Fatalf("Setxattr FinderInfo err = %d, want 0", errc)
	}
	if errc, got := ad.Getxattr("/", "com.apple.FinderInfo"); errc != len("finder") || string(got) != "finder" {
		t.Fatalf("Getxattr FinderInfo err=%d value=%q, want len/value", errc, got)
	}
	if errc := ad.Setxattr("/", "user.foo", []byte("bar"), 0); errc != 0 {
		t.Fatalf("Setxattr err = %d, want 0", errc)
	}
	names := map[string]bool{}
	if errc := ad.Listxattr("/", func(name string) bool {
		names[name] = true
		return true
	}); errc != 0 {
		t.Fatalf("Listxattr err = %d, want 0", errc)
	}
	if !names["com.apple.FinderInfo"] || !names["user.foo"] {
		t.Fatalf("Listxattr names = %v, want FinderInfo and user.foo", names)
	}
	if errc := ad.Removexattr("/", "user.foo"); errc != 0 {
		t.Fatalf("Removexattr err = %d, want 0", errc)
	}
	if errc, _ := ad.Getxattr("/", "user.foo"); errc != -fuse.ENOATTR {
		t.Fatalf("Getxattr removed err = %d, want ENOATTR", errc)
	}
}

func TestAdapterXattrsRenameAndRemove(t *testing.T) {
	ad := newAdapter(stubFS{entries: map[string]drive.Entry{
		"/": {ID: "root", Name: "", IsDir: true},
	}}, StatfsOptions{})

	if errc := ad.Setxattr("/dir/file", "user.foo", []byte("bar"), fuse.XATTR_CREATE); errc != 0 {
		t.Fatalf("Setxattr err = %d, want 0", errc)
	}
	ad.renameXattrs("/dir", "/renamed")
	if errc, got := ad.Getxattr("/renamed/file", "user.foo"); errc != len("bar") || string(got) != "bar" {
		t.Fatalf("Getxattr renamed err=%d value=%q, want len/value", errc, got)
	}
	if errc, _ := ad.Getxattr("/dir/file", "user.foo"); errc != -fuse.ENOATTR {
		t.Fatalf("Getxattr old path err=%d, want ENOATTR", errc)
	}
	ad.removeXattrs("/renamed")
	if errc, _ := ad.Getxattr("/renamed/file", "user.foo"); errc != -fuse.ENOATTR {
		t.Fatalf("Getxattr removed subtree err=%d, want ENOATTR", errc)
	}
}

func TestAdapterAppleCopySourceXattrPreparesFinderDirectoryCopy(t *testing.T) {
	fs := &copyPrepareFS{}
	ad := newAdapter(fs, StatfsOptions{})

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

func TestAdapterResourceForkIsEmptyNoop(t *testing.T) {
	ad := newAdapter(stubFS{entries: map[string]drive.Entry{
		"/": {ID: "root", Name: "", IsDir: true},
	}}, StatfsOptions{})
	const name = "com.apple.ResourceFork"

	if errc := ad.Setxattr("/", name, []byte("ignored"), 0); errc != 0 {
		t.Fatalf("Setxattr ResourceFork err = %d, want 0", errc)
	}
	errc, value := ad.Getxattr("/", name)
	if errc != len("ignored") || string(value) != "ignored" {
		t.Fatalf("Getxattr ResourceFork err=%d value=%q, want len/value", errc, value)
	}
	if errc := ad.Removexattr("/", name); errc != 0 {
		t.Fatalf("Removexattr ResourceFork err = %d, want 0", errc)
	}
	if errc, value := ad.Getxattr("/", name); errc != -fuse.ENOATTR || len(value) != 0 {
		t.Fatalf("Getxattr removed ResourceFork err=%d len=%d, want ENOATTR/0", errc, len(value))
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
	if errc, got := ad.Getxattr("/missing", "x"); errc != 0 || len(got) != 0 {
		t.Fatalf("Getxattr missing stored err=%d len=%d, want 0/0", errc, len(got))
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

func TestAdapterCrossMountRenameReturnsEXDEV(t *testing.T) {
	ad := newAdapter(crossMountRenameFS{}, StatfsOptions{})
	if errc := ad.Rename("/a/file.txt", "/b/file.txt"); errc != -fuse.EXDEV {
		t.Fatalf("Rename cross mount err = %d, want EXDEV", errc)
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
	fsA, err := vfs.New(localfs.New(t.TempDir()), vfs.Options{StorageDir: filepath.Join(t.TempDir(), "a")})
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
