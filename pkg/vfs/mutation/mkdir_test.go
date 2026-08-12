package mutation

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

// mkdirFixture provides stub MkdirResolver/MkdirRemote/MkdirView.
type mkdirFixture struct {
	resolveEntry drive.Entry
	resolveErr   error
	// resolveErrs, when set, is consumed one error per Resolve call
	// (overrides resolveErr); used for call-indexed second-resolve tests.
	resolveErrs []error
	parentEntry drive.Entry
	parentName  string
	parentErr   error

	listEntries []drive.Entry
	listErr     error
	mkdirErr    error
	mkdirResult drive.Entry

	restored        drive.Entry
	restoredOK      bool
	restoreAncestor string
	underRestored   bool
	committedPath   string
	cached          int
	order           []string
}

func (f *mkdirFixture) Resolve(_ context.Context, _ string) (drive.Entry, error) {
	f.order = append(f.order, "resolve")
	if len(f.resolveErrs) > 0 {
		err := f.resolveErrs[0]
		f.resolveErrs = f.resolveErrs[1:]
		return drive.Entry{}, err
	}
	return f.resolveEntry, f.resolveErr
}

func (f *mkdirFixture) Parent(_ context.Context, _ string) (drive.Entry, string, error) {
	f.order = append(f.order, "parent")
	return f.parentEntry, f.parentName, f.parentErr
}

func (f *mkdirFixture) List(_ context.Context, _ string) ([]drive.Entry, error) {
	f.order = append(f.order, "list")
	return f.listEntries, f.listErr
}

func (f *mkdirFixture) Mkdir(_ context.Context, _, _ string) (drive.Entry, error) {
	f.order = append(f.order, "remote_mkdir")
	return f.mkdirResult, f.mkdirErr
}

func (f *mkdirFixture) RestoreDeleted(string) (drive.Entry, bool) {
	f.order = append(f.order, "restore")
	return f.restored, f.restoredOK
}

func (f *mkdirFixture) RestoreDeletedAncestor(path string) {
	f.order = append(f.order, "restore_ancestor")
	f.restoreAncestor = path
}

func (f *mkdirFixture) IsUnderRestoredDir(string) bool {
	f.order = append(f.order, "under_restored")
	return f.underRestored
}

func (f *mkdirFixture) CommitMkdir(path string, _ drive.Entry) {
	f.order = append(f.order, "commit")
	f.committedPath = path
}

func (f *mkdirFixture) CacheListedChildren(string, []drive.Entry) {
	f.order = append(f.order, "cache_children")
	f.cached++
}

func (f *mkdirFixture) coordinator() *MkdirCoordinator {
	return NewMkdirCoordinator(f, f, f)
}

func newMkdirFixture() *mkdirFixture {
	return &mkdirFixture{
		// By default the target does not exist yet (resolve reports the
		// stable not-found sentinel).
		resolveErr:  drive.ErrNotFound,
		parentEntry: drive.Entry{ID: "parent"},
		parentName:  "newdir",
		mkdirResult: drive.Entry{ID: "new-id", ParentID: "parent", Name: "newdir", IsDir: true},
	}
}

// TestMkdirCommitsExactlyOnce: a fresh directory creates remotely and
// commits exactly once with the normalized path.
func TestMkdirCommitsExactlyOnce(t *testing.T) {
	fx := newMkdirFixture()
	entry, err := fx.coordinator().Mkdir(context.Background(), "/a/newdir//")
	if err != nil {
		t.Fatal(err)
	}
	if entry.ID != "new-id" {
		t.Fatalf("entry = %+v, want remote result", entry)
	}
	if fx.committedPath != "/a/newdir" {
		t.Fatalf("committed path = %q, want normalized /a/newdir", fx.committedPath)
	}
	if fx.cached != 0 {
		t.Fatalf("cache calls = %d, want 0 on fresh mkdir", fx.cached)
	}
}

// TestMkdirNeverCommitsOnRemoteFailure: a failing remote Mkdir commits
// nothing.
func TestMkdirNeverCommitsOnRemoteFailure(t *testing.T) {
	fx := newMkdirFixture()
	fx.mkdirErr = errors.New("remote boom")
	if _, err := fx.coordinator().Mkdir(context.Background(), "/newdir"); err == nil {
		t.Fatal("want remote mkdir error")
	}
	if fx.committedPath != "" {
		t.Fatalf("committed %q on failure, want none", fx.committedPath)
	}
}

// TestMkdirExistingDirectoryReturnsAsIs: an already-visible directory is
// returned without any remote call.
func TestMkdirExistingDirectoryReturnsAsIs(t *testing.T) {
	fx := newMkdirFixture()
	fx.resolveErr = nil
	fx.resolveEntry = drive.Entry{ID: "existing", IsDir: true}
	entry, err := fx.coordinator().Mkdir(context.Background(), "/dir")
	if err != nil {
		t.Fatal(err)
	}
	if entry.ID != "existing" {
		t.Fatalf("entry = %+v, want existing", entry)
	}
	if len(fx.order) != 1 {
		t.Fatalf("calls = %v, want only resolve", fx.order)
	}
}

// TestMkdirRestoredDeleted: a path marked deleted is restored without a
// remote call.
func TestMkdirRestoredDeleted(t *testing.T) {
	fx := newMkdirFixture()
	fx.restored = drive.Entry{ID: "restored-id", IsDir: true}
	fx.restoredOK = true
	entry, err := fx.coordinator().Mkdir(context.Background(), "/dir")
	if err != nil {
		t.Fatal(err)
	}
	if entry.ID != "restored-id" {
		t.Fatalf("entry = %+v, want restored", entry)
	}
	for _, step := range fx.order {
		if step == "remote_mkdir" || step == "commit" {
			t.Fatalf("unexpected %q on restore path", step)
		}
	}
}

// TestMkdirAlreadyExistsRecovery: an already-exists error recovers the
// existing directory, caching its siblings, and commits exactly once.
func TestMkdirAlreadyExistsRecovery(t *testing.T) {
	fx := newMkdirFixture()
	fx.mkdirErr = errors.New("already exists")
	fx.listEntries = []drive.Entry{
		{ID: "file", ParentID: "parent", Name: "file.txt"},
		{ID: "dir", ParentID: "parent", Name: "newdir", IsDir: true},
	}
	entry, err := fx.coordinator().Mkdir(context.Background(), "/newdir")
	if err != nil {
		t.Fatal(err)
	}
	if entry.ID != "dir" {
		t.Fatalf("entry = %+v, want recovered existing dir", entry)
	}
	if fx.cached != 1 {
		t.Fatalf("cache calls = %d, want 1", fx.cached)
	}
	if fx.committedPath != "/newdir" {
		t.Fatalf("committed path = %q, want /newdir", fx.committedPath)
	}
}

// TestMkdirOrder: the success path runs resolve -> restore probe ->
// ancestor -> under-restored -> parent -> remote mkdir -> commit.
func TestMkdirOrder(t *testing.T) {
	fx := newMkdirFixture()
	if _, err := fx.coordinator().Mkdir(context.Background(), "/newdir"); err != nil {
		t.Fatal(err)
	}
	want := []string{"resolve", "restore", "restore_ancestor", "under_restored", "parent", "remote_mkdir", "commit"}
	if len(fx.order) != len(want) {
		t.Fatalf("order = %v, want %v", fx.order, want)
	}
	for i := range want {
		if fx.order[i] != want[i] {
			t.Fatalf("order[%d] = %q, want %q (full %v)", i, fx.order[i], want[i], fx.order)
		}
	}
}

// TestMkdirNonNotFoundResolveHasNoSideEffects: a resolve error that is NOT
// not-found (cancel/auth/network) returns immediately with zero overlay,
// parent, mkdir, or commit calls.
func TestMkdirNonNotFoundResolveHasNoSideEffects(t *testing.T) {
	fx := newMkdirFixture()
	fx.resolveErr = errors.New("auth failed")
	if _, err := fx.coordinator().Mkdir(context.Background(), "/newdir"); err == nil {
		t.Fatal("want resolve error")
	}
	for _, step := range fx.order {
		switch step {
		case "restore", "restore_ancestor", "under_restored", "parent", "remote_mkdir", "commit":
			t.Fatalf("unexpected %q after non-not-found resolve", step)
		}
	}
}

// TestMkdirWrappedNotFoundProceeds: a WRAPPED drive.ErrNotFound still
// classifies as not-found and proceeds to create.
func TestMkdirWrappedNotFoundProceeds(t *testing.T) {
	fx := newMkdirFixture()
	fx.resolveErr = fmt.Errorf("backend: %w", drive.ErrNotFound)
	entry, err := fx.coordinator().Mkdir(context.Background(), "/newdir")
	if err != nil {
		t.Fatal(err)
	}
	if entry.ID != "new-id" {
		t.Fatalf("entry = %+v, want created", entry)
	}
}

// TestMkdirRestoredAncestorNonNotFoundReturns: the second resolve (after
// restoring a deleted ancestor) returning a non-not-found error aborts
// with zero mkdir/commit side effects.
func TestMkdirRestoredAncestorNonNotFoundReturns(t *testing.T) {
	fx := newMkdirFixture()
	fx.underRestored = true
	fx.resolveErrs = []error{drive.ErrNotFound, errors.New("auth failed")}
	if _, err := fx.coordinator().Mkdir(context.Background(), "/newdir"); err == nil {
		t.Fatal("want second resolve error")
	}
	for _, step := range fx.order {
		if step == "remote_mkdir" || step == "commit" {
			t.Fatalf("unexpected %q after second-resolve error", step)
		}
	}
}

// TestMkdirRestoredAncestorFindsFile: the second resolve under a restored
// ancestor finding a plain file reports exists-not-directory.
func TestMkdirRestoredAncestorFindsFile(t *testing.T) {
	fx := newMkdirFixture()
	fx.underRestored = true
	// First resolve: not found. Second resolve: succeeds with a plain file.
	fx.resolveErr = nil
	fx.resolveErrs = []error{drive.ErrNotFound}
	fx.resolveEntry = drive.Entry{ID: "file-id", Name: "newdir"}
	if _, err := fx.coordinator().Mkdir(context.Background(), "/newdir"); err == nil {
		t.Fatal("want exists-not-directory error")
	}
	for _, step := range fx.order {
		if step == "remote_mkdir" || step == "commit" {
			t.Fatalf("unexpected %q when restored ancestor holds a file", step)
		}
	}
}

// TestMkdirRestoredAncestorNotFoundProceeds: the second resolve under a
// restored ancestor reporting not-found continues to create.
func TestMkdirRestoredAncestorNotFoundProceeds(t *testing.T) {
	fx := newMkdirFixture()
	fx.underRestored = true
	fx.resolveErrs = []error{drive.ErrNotFound, drive.ErrNotFound}
	entry, err := fx.coordinator().Mkdir(context.Background(), "/newdir")
	if err != nil {
		t.Fatal(err)
	}
	if entry.ID != "new-id" {
		t.Fatalf("entry = %+v, want created", entry)
	}
}
