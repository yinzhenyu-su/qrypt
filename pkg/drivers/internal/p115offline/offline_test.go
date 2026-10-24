package p115offline

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

type fakeBackend struct {
	pages  [][]Task
	tasks  []Task
	dirs   map[string][]drive.Entry
	list   []int
	addID  string
	addErr error
	addDir string

	deleteErr error
	deleted   []string

	target     drive.DownloadTarget
	targetErr  error
	targetFile string
}

func (f *fakeBackend) AddTask(_ context.Context, source, dirID string) (string, error) {
	f.addDir = dirID
	_ = source
	if f.addErr != nil {
		return "", f.addErr
	}
	return f.addID, nil
}

func (f *fakeBackend) ListTasks(_ context.Context, page int) ([]Task, int, error) {
	f.list = append(f.list, page)
	if f.pages != nil {
		if page < 1 || page > len(f.pages) {
			return nil, len(f.pages), nil
		}
		return f.pages[page-1], len(f.pages), nil
	}
	return f.tasks, 1, nil
}

func (f *fakeBackend) DeleteTask(_ context.Context, id string) error {
	f.deleted = append(f.deleted, id)
	return f.deleteErr
}

func (f *fakeBackend) ListDir(_ context.Context, dirID string) ([]drive.Entry, error) {
	entries, ok := f.dirs[dirID]
	if !ok {
		return nil, drive.ErrNotFound
	}
	return entries, nil
}

func (f *fakeBackend) DownloadTarget(_ context.Context, fileID string, size int64) (drive.DownloadTarget, error) {
	f.targetFile = fileID
	if f.targetErr != nil {
		return drive.DownloadTarget{}, f.targetErr
	}
	target := f.target
	if target.Size == 0 && size > 0 {
		target.Size = size
	}
	return target, nil
}

func (f *fakeBackend) DefaultDirID() string { return "default-dir" }

func TestStateFor(t *testing.T) {
	t.Parallel()
	tests := []struct {
		status int
		want   drive.OfflineState
		ok     bool
	}{
		{status: -1, want: drive.OfflineStateFailed, ok: true},
		{status: 0, want: drive.OfflineStateQueued, ok: true},
		{status: 1, want: drive.OfflineStateRunning, ok: true},
		{status: 2, want: drive.OfflineStateSucceeded, ok: true},
		{status: 99, ok: false},
	}
	for _, test := range tests {
		got, ok := stateFor(test.status)
		if ok != test.ok || got != test.want {
			t.Fatalf("stateFor(%d) = (%q, %v), want (%q, %v)", test.status, got, ok, test.want, test.ok)
		}
	}
}

func TestCreateOfflineDownloadUsesResolvedDestination(t *testing.T) {
	t.Parallel()
	backend := &fakeBackend{addID: "hash-1"}
	adapter := New(backend)
	created, err := adapter.CreateOfflineDownload(t.Context(), drive.OfflineDownloadRequest{
		SourceKind:    drive.OfflineSourceMagnet,
		Source:        "magnet:?xt=urn:btih:hash-1",
		Destination:   "/downloads",
		DestinationID: "dir-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.ID != "hash-1" || created.State != drive.OfflineStateQueued || created.Destination != "dir-1" {
		t.Fatalf("created = %+v", created)
	}
	if backend.addDir != "dir-1" {
		t.Fatalf("AddTask dir = %q, want dir-1", backend.addDir)
	}
}

func TestCreateOfflineDownloadUsesDefaultDestination(t *testing.T) {
	t.Parallel()
	backend := &fakeBackend{addID: "hash-2"}
	created, err := New(backend).CreateOfflineDownload(t.Context(), drive.OfflineDownloadRequest{
		Source: "https://example.com/a.iso",
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.Destination != "default-dir" || backend.addDir != "default-dir" {
		t.Fatalf("destination = %q, add dir = %q", created.Destination, backend.addDir)
	}
}

func TestCreateOfflineDownloadRejectsSelection(t *testing.T) {
	t.Parallel()
	_, err := New(&fakeBackend{addID: "hash"}).CreateOfflineDownload(t.Context(), drive.OfflineDownloadRequest{
		Source:    "magnet:?xt=urn:btih:hash",
		Selection: drive.OfflineSelection{Mode: drive.OfflineSelectionLargest, Largest: 1},
	})
	offline, ok := drive.AsOfflineError(err)
	if !ok || offline.Code != drive.OfflineErrSelectionUnsupported {
		t.Fatalf("error = %v, want selection unsupported", err)
	}
}

func TestGetOfflineDownloadTransitions(t *testing.T) {
	t.Parallel()
	backend := &fakeBackend{
		tasks: []Task{{ID: "hash-1", Status: 0, Percent: 10, ParentID: "dir", FileID: "root"}},
		dirs: map[string][]drive.Entry{
			"dir":  {{ID: "root", Name: "Resource", IsDir: true}},
			"root": {{ID: "file-1", Name: "ep1.mkv", Size: 10}},
		},
	}
	adapter := New(backend)
	queued, err := adapter.GetOfflineDownload(t.Context(), "hash-1")
	if err != nil {
		t.Fatal(err)
	}
	if queued.State != drive.OfflineStateQueued || queued.Progress.Percent != 10 || queued.Available {
		t.Fatalf("queued = %+v", queued)
	}

	backend.tasks[0].Status = 1
	backend.tasks[0].Percent = 55
	running, err := adapter.GetOfflineDownload(t.Context(), "hash-1")
	if err != nil {
		t.Fatal(err)
	}
	if running.State != drive.OfflineStateRunning || running.Progress.Percent != 55 {
		t.Fatalf("running = %+v", running)
	}

	backend.tasks[0].Status = 2
	backend.tasks[0].Percent = 100
	done, err := adapter.GetOfflineDownload(t.Context(), "hash-1")
	if err != nil {
		t.Fatal(err)
	}
	if done.State != drive.OfflineStateSucceeded || !done.Available {
		t.Fatalf("done = %+v", done)
	}
	if len(done.Files) != 1 || done.Files[0].Path != "Resource/ep1.mkv" || done.Files[0].RemoteID != "file-1" {
		t.Fatalf("files = %+v", done.Files)
	}
}

func TestFindTaskUsesCachedPage(t *testing.T) {
	t.Parallel()
	backend := &fakeBackend{pages: [][]Task{
		{{ID: "other"}},
		{{ID: "hash-1", Status: 1}},
		{{ID: "more"}},
	}}
	adapter := New(backend)
	if _, err := adapter.GetOfflineDownload(t.Context(), "hash-1"); err != nil {
		t.Fatal(err)
	}
	if got, want := fmt.Sprint(backend.list), "[1 2]"; got != want {
		t.Fatalf("first lookup pages = %s, want %s", got, want)
	}
	backend.list = nil
	if _, err := adapter.GetOfflineDownload(t.Context(), "hash-1"); err != nil {
		t.Fatal(err)
	}
	if got, want := fmt.Sprint(backend.list), "[2]"; got != want {
		t.Fatalf("cached lookup pages = %s, want %s", got, want)
	}
}

func TestGetResolvesMultiFileTree(t *testing.T) {
	t.Parallel()
	backend := &fakeBackend{
		tasks: []Task{{ID: "hash-1", Status: 2, ParentID: "dir", FileID: "root"}},
		dirs: map[string][]drive.Entry{
			"dir": {{ID: "root", Name: "Resource", IsDir: true}},
			"root": {
				{ID: "f2", Name: "ep2.mkv", Size: 20},
				{ID: "sub", Name: "sub", IsDir: true},
			},
			"sub": {{ID: "f1", Name: "ep1.mkv", Size: 10}},
		},
	}
	got, err := New(backend).GetOfflineDownload(t.Context(), "hash-1")
	if err != nil {
		t.Fatal(err)
	}
	want := []drive.OfflineFile{
		{Index: 0, Name: "ep2.mkv", Size: 20, RemoteID: "f2", Path: "Resource/ep2.mkv"},
		{Index: 1, Name: "ep1.mkv", Size: 10, RemoteID: "f1", Path: "Resource/sub/ep1.mkv"},
	}
	if len(got.Files) != len(want) {
		t.Fatalf("files = %+v, want %+v", got.Files, want)
	}
	for index := range want {
		if got.Files[index] != want[index] {
			t.Fatalf("file %d = %+v, want %+v", index, got.Files[index], want[index])
		}
	}
	if got.RemoteID != "root" || !got.Available {
		t.Fatalf("observation = %+v", got)
	}
}

func TestGetResolvesSingleFile(t *testing.T) {
	t.Parallel()
	backend := &fakeBackend{
		tasks: []Task{{ID: "hash-1", Status: 2, ParentID: "dir", FileID: "file-1"}},
		dirs: map[string][]drive.Entry{
			"dir": {{ID: "file-1", Name: "movie.mkv", Size: 42}},
		},
	}
	got, err := New(backend).GetOfflineDownload(t.Context(), "hash-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Files) != 1 || got.Files[0].Path != "movie.mkv" || got.Files[0].Size != 42 {
		t.Fatalf("files = %+v", got.Files)
	}
}

func TestGetMissingTask(t *testing.T) {
	t.Parallel()
	_, err := New(&fakeBackend{}).GetOfflineDownload(t.Context(), "missing")
	offline, ok := drive.AsOfflineError(err)
	if !ok || offline.Code != drive.OfflineErrProviderTaskMissing || !offline.Retryable {
		t.Fatalf("error = %v, want retryable task missing", err)
	}
}

func TestGetLaggingListing(t *testing.T) {
	t.Parallel()
	backend := &fakeBackend{
		tasks: []Task{{ID: "hash-1", Status: 2, ParentID: "dir", FileID: "root"}},
		dirs:  map[string][]drive.Entry{"dir": {}},
	}
	adapter := New(backend)
	_, err := adapter.GetOfflineDownload(t.Context(), "hash-1")
	offline, ok := drive.AsOfflineError(err)
	if !ok || offline.Code != drive.OfflineErrProviderUnavailable || !offline.Retryable {
		t.Fatalf("error = %v, want retryable provider unavailable", err)
	}
	backend.dirs["dir"] = []drive.Entry{{ID: "root", Name: "Resource", IsDir: true}}
	backend.dirs["root"] = []drive.Entry{{ID: "f1", Name: "ep1.mkv", Size: 1}}
	if _, err := adapter.GetOfflineDownload(t.Context(), "hash-1"); err != nil {
		t.Fatal(err)
	}
}

func TestGetEmptyResultSet(t *testing.T) {
	t.Parallel()
	backend := &fakeBackend{
		tasks: []Task{{ID: "hash-1", Status: 2, ParentID: "dir", FileID: "root"}},
		dirs: map[string][]drive.Entry{
			"dir":  {{ID: "root", Name: "Resource", IsDir: true}},
			"root": {},
		},
	}
	_, err := New(backend).GetOfflineDownload(t.Context(), "hash-1")
	offline, ok := drive.AsOfflineError(err)
	if !ok || offline.Code != drive.OfflineErrProvider || offline.Retryable {
		t.Fatalf("error = %v, want non-retryable provider error", err)
	}
}

func TestGetDuplicateInDifferentDirectory(t *testing.T) {
	t.Parallel()
	backend := &fakeBackend{
		addID: "hash-1",
		tasks: []Task{{ID: "hash-1", Status: 1, ParentID: "dir-2"}},
	}
	adapter := New(backend)
	if _, err := adapter.CreateOfflineDownload(t.Context(), drive.OfflineDownloadRequest{
		Source: "magnet:?xt=urn:btih:hash-1", DestinationID: "dir-1",
	}); err != nil {
		t.Fatal(err)
	}
	_, err := adapter.GetOfflineDownload(t.Context(), "hash-1")
	offline, ok := drive.AsOfflineError(err)
	if !ok || offline.Code != drive.OfflineErrDuplicate {
		t.Fatalf("error = %v, want duplicate", err)
	}
}

func TestRestoreOfflineLayout(t *testing.T) {
	t.Parallel()
	backend := &fakeBackend{
		tasks: []Task{{ID: "hash-1", Status: 2}},
		dirs: map[string][]drive.Entry{
			"dir":  {{ID: "root", Name: "Resource", IsDir: true}},
			"root": {{ID: "f1", Name: "ep1.mkv", Size: 1}},
		},
	}
	adapter := New(backend)
	adapter.RestoreOfflineLayout(drive.OfflineLayout{
		TaskID: "hash-1", RootID: "root", ParentID: "dir",
	})
	got, err := adapter.GetOfflineDownload(t.Context(), "hash-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Files) != 1 || got.Files[0].Path != "Resource/ep1.mkv" {
		t.Fatalf("files = %+v", got.Files)
	}
}

func TestCancelOfflineDownloadIsIdempotent(t *testing.T) {
	t.Parallel()
	backend := &fakeBackend{}
	adapter := New(backend)
	if err := adapter.CancelOfflineDownload(t.Context(), "hash-1"); err != nil {
		t.Fatal(err)
	}
	backend.deleteErr = ErrTaskNotFound
	if err := adapter.CancelOfflineDownload(t.Context(), "hash-1"); err != nil {
		t.Fatalf("missing task cancel = %v", err)
	}
	if len(backend.deleted) != 2 {
		t.Fatalf("deleted = %v", backend.deleted)
	}
}

func TestOfflineDownloadURL(t *testing.T) {
	t.Parallel()
	backend := &fakeBackend{target: drive.DownloadTarget{URL: "https://cdn/file", Headers: map[string]string{"User-Agent": "ua"}}}
	got, err := New(backend).OfflineDownloadURL(t.Context(), drive.OfflineFetchRequest{
		Files: []drive.OfflineFile{{RemoteID: "file-1", Size: 7}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.URL != "https://cdn/file" || got.Size != 7 || got.Headers["User-Agent"] != "ua" || backend.targetFile != "file-1" {
		t.Fatalf("target = %+v, backend file = %q", got, backend.targetFile)
	}
	if _, err := New(backend).OfflineDownloadURL(t.Context(), drive.OfflineFetchRequest{}); err == nil {
		t.Fatal("expected an error for an empty file list")
	}
}

func TestGetRejectsCyclicTree(t *testing.T) {
	t.Parallel()
	backend := &fakeBackend{
		tasks: []Task{{ID: "hash-1", Status: 2, ParentID: "dir", FileID: "root"}},
		dirs: map[string][]drive.Entry{
			"dir":  {{ID: "root", Name: "Resource", IsDir: true}},
			"root": {{ID: "root", Name: "loop", IsDir: true}},
		},
	}
	_, err := New(backend).GetOfflineDownload(t.Context(), "hash-1")
	offline, ok := drive.AsOfflineError(err)
	if !ok || offline.Code != drive.OfflineErrProvider || offline.Retryable {
		t.Fatalf("error = %v, want non-retryable cycle error", err)
	}
}

func TestGetRejectsDeepTree(t *testing.T) {
	t.Parallel()
	dirs := map[string][]drive.Entry{
		"dir": {{ID: "d0", Name: "d0", IsDir: true}},
	}
	for index := 0; index <= maxTreeDepth; index++ {
		current := fmt.Sprintf("d%d", index)
		next := fmt.Sprintf("d%d", index+1)
		dirs[current] = []drive.Entry{{ID: next, Name: next, IsDir: true}}
	}
	backend := &fakeBackend{
		tasks: []Task{{ID: "hash-1", Status: 2, ParentID: "dir", FileID: "d0"}},
		dirs:  dirs,
	}
	_, err := New(backend).GetOfflineDownload(t.Context(), "hash-1")
	offline, ok := drive.AsOfflineError(err)
	if !ok || offline.Code != drive.OfflineErrProvider || offline.Retryable {
		t.Fatalf("error = %v, want non-retryable depth error", err)
	}
}

func TestGetUnknownStatus(t *testing.T) {
	t.Parallel()
	backend := &fakeBackend{tasks: []Task{{ID: "hash-1", Status: 42}}}
	_, err := New(backend).GetOfflineDownload(t.Context(), "hash-1")
	if err == nil {
		t.Fatal("expected an error")
	}
	var offline *drive.OfflineError
	if !errors.As(err, &offline) || offline.Code != drive.OfflineErrProvider {
		t.Fatalf("error = %v, want provider error", err)
	}
}
