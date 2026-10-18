package quark

import (
	"context"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

// newOfflineDriverForTest starts a fake Quark host and returns a driver bound
// to it.
func newOfflineDriverForTest(t *testing.T, fake *offlineTestServer) *Driver {
	t.Helper()
	server := httptest.NewServer(fake.handler())
	t.Cleanup(server.Close)
	return New("k=v", Options{BaseURL: server.URL, V2URL: server.URL})
}

// TestQuarkOfflineLayoutIncludesRootFolder verifies the staged relative path is
// relative to the staging directory, so a resource folder keeps its own name.
func TestQuarkOfflineLayoutIncludesRootFolder(t *testing.T) {
	t.Parallel()

	fake := &offlineTestServer{
		t: t, taskID: "task-root-folder", downloadURL: "https://cdn.example/x",
		sortTree: map[string][]map[string]any{
			"dir-1": {
				{"fid": "fid-1", "file_name": "release", "file": false},
			},
			"fid-1": {
				{"fid": "fid-ep1", "file_name": "ep1.mkv", "size": 1000, "file": true},
			},
		},
		resultList: []map[string]any{{"fid": "fid-ep1", "file_name": "ep1.mkv", "size": 1000}},
	}
	driver := newOfflineDriverForTest(t, fake)

	if _, err := driver.CreateOfflineDownload(context.Background(), drive.OfflineDownloadRequest{
		SourceKind: drive.OfflineSourceMagnet, Source: "magnet:?xt=urn:btih:root-folder", Destination: "/",
	}); err != nil {
		t.Fatalf("CreateOfflineDownload: %v", err)
	}
	observation, err := driver.GetOfflineDownload(context.Background(), "task-root-folder")
	if err != nil {
		t.Fatalf("GetOfflineDownload: %v", err)
	}
	if len(observation.Files) != 1 || observation.Files[0].Path != "release/ep1.mkv" {
		t.Fatalf("files = %+v, want release/ep1.mkv", observation.Files)
	}
}

// TestQuarkOfflineLayoutSingleFileRoot verifies a single-file output root is
// named by its own file name rather than being treated as a directory.
func TestQuarkOfflineLayoutSingleFileRoot(t *testing.T) {
	t.Parallel()

	fake := &offlineTestServer{
		t: t, taskID: "task-single-root", rootFid: "fid-single", downloadURL: "https://cdn.example/x",
		sortTree: map[string][]map[string]any{
			"dir-1": {
				{"fid": "fid-single", "file_name": "single.mkv", "size": 7, "file": true},
			},
		},
		resultList: []map[string]any{{"fid": "fid-single", "file_name": "single.mkv", "size": 7}},
	}
	driver := newOfflineDriverForTest(t, fake)

	if _, err := driver.CreateOfflineDownload(context.Background(), drive.OfflineDownloadRequest{
		SourceKind: drive.OfflineSourceMagnet, Source: "magnet:?xt=urn:btih:single-root", Destination: "/",
	}); err != nil {
		t.Fatalf("CreateOfflineDownload: %v", err)
	}
	observation, err := driver.GetOfflineDownload(context.Background(), "task-single-root")
	if err != nil {
		t.Fatalf("GetOfflineDownload: %v", err)
	}
	if len(observation.Files) != 1 || observation.Files[0].Path != "single.mkv" {
		t.Fatalf("files = %+v, want single.mkv", observation.Files)
	}
}

// TestQuarkOfflineLayoutPropagatesWalkError verifies a failed directory listing
// in the output tree fails the observation instead of publishing a partial
// available result set.
func TestQuarkOfflineLayoutPropagatesWalkError(t *testing.T) {
	t.Parallel()

	fake := &offlineTestServer{
		t: t, taskID: "task-walk-error", downloadURL: "https://cdn.example/x",
		sortTree: map[string][]map[string]any{
			"dir-1": {{"fid": "fid-1", "file_name": "release", "file": false}},
			"fid-1": {{"fid": "dir-sub", "file_name": "sub", "file": false}},
		},
		sortFail:   map[string]bool{"dir-sub": true},
		resultList: []map[string]any{{"fid": "fid-ep1", "file_name": "ep1.mkv", "size": 1000}},
	}
	driver := newOfflineDriverForTest(t, fake)

	if _, err := driver.CreateOfflineDownload(context.Background(), drive.OfflineDownloadRequest{
		SourceKind: drive.OfflineSourceMagnet, Source: "magnet:?xt=urn:btih:walk-error", Destination: "/",
	}); err != nil {
		t.Fatalf("CreateOfflineDownload: %v", err)
	}
	if _, err := driver.GetOfflineDownload(context.Background(), "task-walk-error"); err == nil {
		t.Fatal("an incomplete tree walk was published as an available result set")
	}
}

// TestQuarkOfflineLayoutRejectsMissingRootInParent verifies a lagging parent
// listing is not silently accepted with an empty prefix that drops the
// resource folder.
func TestQuarkOfflineLayoutRejectsMissingRootInParent(t *testing.T) {
	t.Parallel()

	fake := &offlineTestServer{
		t: t, taskID: "task-lagging-parent", downloadURL: "https://cdn.example/x",
		sortTree: map[string][]map[string]any{
			"fid-1": {{"fid": "fid-ep1", "file_name": "ep1.mkv", "size": 1000, "file": true}},
		},
		resultList: []map[string]any{{"fid": "fid-ep1", "file_name": "ep1.mkv", "size": 1000}},
	}
	driver := newOfflineDriverForTest(t, fake)

	if _, err := driver.CreateOfflineDownload(context.Background(), drive.OfflineDownloadRequest{
		SourceKind: drive.OfflineSourceMagnet, Source: "magnet:?xt=urn:btih:lagging-parent", Destination: "/",
	}); err != nil {
		t.Fatalf("CreateOfflineDownload: %v", err)
	}
	if _, err := driver.GetOfflineDownload(context.Background(), "task-lagging-parent"); err == nil {
		t.Fatal("a missing root in the known parent was accepted as a complete layout")
	}
}

// TestQuarkOfflineLayoutPreservesClassifiedError verifies a nonretryable
// provider error during the tree walk is not reclassified as a transient
// provider-unavailable error.
func TestQuarkOfflineLayoutPreservesClassifiedError(t *testing.T) {
	t.Parallel()

	fake := &offlineTestServer{
		t: t, taskID: "task-auth-error", downloadURL: "https://cdn.example/x",
		sortCode:   map[string]int{"dir-1": 31001},
		resultList: []map[string]any{{"fid": "fid-ep1", "file_name": "ep1.mkv", "size": 1000}},
	}
	driver := newOfflineDriverForTest(t, fake)

	if _, err := driver.CreateOfflineDownload(context.Background(), drive.OfflineDownloadRequest{
		SourceKind: drive.OfflineSourceMagnet, Source: "magnet:?xt=urn:btih:auth-error", Destination: "/",
	}); err != nil {
		t.Fatalf("CreateOfflineDownload: %v", err)
	}
	_, err := driver.GetOfflineDownload(context.Background(), "task-auth-error")
	offline, ok := drive.AsOfflineError(err)
	if !ok || offline.Code != drive.OfflineErrAuthRequired || offline.Retryable {
		t.Fatalf("error = %v, want nonretryable offline_auth_required", err)
	}
}

// TestQuarkOfflineLayoutRejectsCycle verifies a directory cycle is reported as
// an error instead of silently truncating the walk at the depth limit.
func TestQuarkOfflineLayoutRejectsCycle(t *testing.T) {
	t.Parallel()

	fake := &offlineTestServer{
		t: t, taskID: "task-cycle", downloadURL: "https://cdn.example/x",
		sortTree: map[string][]map[string]any{
			"dir-1": {{"fid": "fid-1", "file_name": "release", "file": false}},
			"fid-1": {{"fid": "dir-a", "file_name": "a", "file": false}},
			"dir-a": {{"fid": "dir-b", "file_name": "b", "file": false}},
			"dir-b": {{"fid": "dir-a", "file_name": "a", "file": false}},
		},
		resultList: []map[string]any{{"fid": "fid-ep1", "file_name": "ep1.mkv", "size": 1000}},
	}
	driver := newOfflineDriverForTest(t, fake)

	if _, err := driver.CreateOfflineDownload(context.Background(), drive.OfflineDownloadRequest{
		SourceKind: drive.OfflineSourceMagnet, Source: "magnet:?xt=urn:btih:cycle", Destination: "/",
	}); err != nil {
		t.Fatalf("CreateOfflineDownload: %v", err)
	}
	_, err := driver.GetOfflineDownload(context.Background(), "task-cycle")
	if err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("error = %v, want a cycle error", err)
	}
}

// TestQuarkOfflineLayoutRestoresFromPersistedIdentity verifies a fresh driver
// that only receives persisted task metadata resolves the same paths and
// discovers the same omitted files as the driver that created the task.
func TestQuarkOfflineLayoutRestoresFromPersistedIdentity(t *testing.T) {
	t.Parallel()

	fake := &offlineTestServer{
		t: t, taskID: "task-restore", downloadURL: "https://cdn.example/x",
		sortTree: map[string][]map[string]any{
			"dir-1": {{"fid": "fid-1", "file_name": "release", "file": false}},
			"fid-1": {
				{"fid": "fid-ep1", "file_name": "ep1.mkv", "size": 1000, "file": true},
				{"fid": "fid-readme", "file_name": "readme.txt", "size": 5, "file": true},
			},
		},
		resultList: []map[string]any{{"fid": "fid-ep1", "file_name": "ep1.mkv", "size": 1000}},
	}
	server := httptest.NewServer(fake.handler())
	t.Cleanup(server.Close)

	creator := New("k=v", Options{BaseURL: server.URL, V2URL: server.URL})
	if _, err := creator.CreateOfflineDownload(context.Background(), drive.OfflineDownloadRequest{
		SourceKind: drive.OfflineSourceMagnet, Source: "magnet:?xt=urn:btih:restore", Destination: "/",
	}); err != nil {
		t.Fatalf("CreateOfflineDownload: %v", err)
	}
	want, err := creator.GetOfflineDownload(context.Background(), "task-restore")
	if err != nil {
		t.Fatalf("creator GetOfflineDownload: %v", err)
	}

	fresh := New("k=v", Options{BaseURL: server.URL, V2URL: server.URL})
	before, err := fresh.GetOfflineDownload(context.Background(), "task-restore")
	if err != nil {
		t.Fatalf("fresh GetOfflineDownload: %v", err)
	}
	if len(before.Files) != 1 || before.Files[0].Path != "" {
		t.Fatalf("fresh driver unexpectedly knew the layout: %+v", before.Files)
	}
	fresh.RestoreOfflineLayout(drive.OfflineLayout{
		TaskID: "task-restore", RootID: "fid-1", ParentID: "dir-1", Available: true,
	})
	restored, err := fresh.GetOfflineDownload(context.Background(), "task-restore")
	if err != nil {
		t.Fatalf("restored GetOfflineDownload: %v", err)
	}
	paths := map[string]string{}
	for _, file := range restored.Files {
		paths[file.RemoteID] = file.Path
	}
	if paths["fid-ep1"] != "release/ep1.mkv" {
		t.Fatalf("restored ep1 path = %q, want release/ep1.mkv", paths["fid-ep1"])
	}
	if paths["fid-readme"] != "release/readme.txt" {
		t.Fatalf("restored discovered readme path = %q, want release/readme.txt", paths["fid-readme"])
	}
	if len(want.Files) != len(restored.Files) {
		t.Fatalf("restored files = %+v, want the same set as %+v", restored.Files, want.Files)
	}
}

// TestQuarkOfflineLayoutTerminalFailureWins verifies a cached availability
// signal cannot mask a failed or canceled terminal observation, even when the
// layout lookup itself would fail.
func TestQuarkOfflineLayoutTerminalFailureWins(t *testing.T) {
	t.Parallel()

	fake := &offlineTestServer{
		t: t, taskID: "task-terminal-failure", downloadURL: "https://cdn.example/x",
		taskState:  3,
		sortFail:   map[string]bool{"dir-1": true},
		resultList: []map[string]any{{"fid": "fid-ep1", "file_name": "ep1.mkv", "size": 1000}},
	}
	driver := newOfflineDriverForTest(t, fake)

	created, err := driver.CreateOfflineDownload(context.Background(), drive.OfflineDownloadRequest{
		SourceKind: drive.OfflineSourceMagnet, Source: "magnet:?xt=urn:btih:terminal-failure", Destination: "/",
	})
	if err != nil {
		t.Fatalf("CreateOfflineDownload: %v", err)
	}
	if !created.Available {
		t.Fatal("instant resource should carry cached availability")
	}
	observation, err := driver.GetOfflineDownload(context.Background(), "task-terminal-failure")
	if err != nil {
		t.Fatalf("terminal failure was masked by a layout error: %v", err)
	}
	if observation.State != drive.OfflineStateFailed {
		t.Fatalf("state = %q, want failed", observation.State)
	}
	if observation.Available {
		t.Fatal("a failed observation must not report availability")
	}
}

// TestQuarkOfflineLayoutRestoreClearsAvailability verifies an in-place retry's
// persisted restore clears old availability instead of OR-ing it back, so a
// partial retained list is not treated as complete.
func TestQuarkOfflineLayoutRestoreClearsAvailability(t *testing.T) {
	t.Parallel()

	fake := &offlineTestServer{
		t: t, taskID: "task-retry-partial", downloadURL: "https://cdn.example/x",
		sortTree: map[string][]map[string]any{
			"dir-1": {{"fid": "fid-1", "file_name": "release", "file": false}},
			"fid-1": {{"fid": "fid-partial", "file_name": "partial.bin", "size": 3, "file": true}},
		},
		resultList: []map[string]any{{"fid": "fid-partial", "file_name": "partial.bin", "size": 3}},
	}
	driver := newOfflineDriverForTest(t, fake)

	created, err := driver.CreateOfflineDownload(context.Background(), drive.OfflineDownloadRequest{
		SourceKind: drive.OfflineSourceMagnet, Source: "magnet:?xt=urn:btih:retry-partial", Destination: "/",
	})
	if err != nil {
		t.Fatalf("CreateOfflineDownload: %v", err)
	}
	if !created.Available {
		t.Fatal("instant resource should start available")
	}

	fake.queued = true
	fake.sortFail = map[string]bool{"dir-1": true}
	driver.RestoreOfflineLayout(drive.OfflineLayout{
		TaskID: "task-retry-partial", RootID: "fid-1", ParentID: "dir-1", Available: false,
	})
	observation, err := driver.GetOfflineDownload(context.Background(), "task-retry-partial")
	if err != nil {
		t.Fatalf("GetOfflineDownload: %v", err)
	}
	if observation.Available {
		t.Fatal("restored false availability was OR-ed with the old true value")
	}
	if len(observation.Files) != 1 || observation.Files[0].Path != "" {
		t.Fatalf("partial layout was resolved without availability: %+v", observation.Files)
	}
}

// TestQuarkOfflineLayoutPersistsActualParent verifies the parent persisted for
// recovery is the directory that actually contains the produced root, not the
// requested staging identifier.
func TestQuarkOfflineLayoutPersistsActualParent(t *testing.T) {
	t.Parallel()

	fake := &offlineTestServer{
		t: t, taskID: "task-parent", rootFid: "fid-1", submitDirFid: "layout-parent",
		downloadURL: "https://cdn.example/x",
		sortTree: map[string][]map[string]any{
			"layout-parent": {{"fid": "fid-1", "file_name": "release", "file": false}},
			"fid-1":         {{"fid": "fid-ep1", "file_name": "ep1.mkv", "size": 1000, "file": true}},
		},
		resultList: []map[string]any{{"fid": "fid-ep1", "file_name": "ep1.mkv", "size": 1000}},
	}
	server := httptest.NewServer(fake.handler())
	t.Cleanup(server.Close)
	creator := New("k=v", Options{BaseURL: server.URL, V2URL: server.URL})

	created, err := creator.CreateOfflineDownload(context.Background(), drive.OfflineDownloadRequest{
		SourceKind: drive.OfflineSourceMagnet, Source: "magnet:?xt=urn:btih:parent", Destination: "/",
	})
	if err != nil {
		t.Fatalf("CreateOfflineDownload: %v", err)
	}
	if created.Destination != "layout-parent" {
		t.Fatalf("persisted destination = %q, want the actual layout parent", created.Destination)
	}

	fresh := New("k=v", Options{BaseURL: server.URL, V2URL: server.URL})
	fresh.RestoreOfflineLayout(drive.OfflineLayout{
		TaskID: created.ID, RootID: created.RemoteID, ParentID: created.Destination, Available: created.Available,
	})
	observation, err := fresh.GetOfflineDownload(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("fresh GetOfflineDownload: %v", err)
	}
	if len(observation.Files) != 1 || observation.Files[0].Path != "release/ep1.mkv" {
		t.Fatalf("fresh files = %+v, want release/ep1.mkv", observation.Files)
	}
}

// TestQuarkOfflineLayoutRejectsExcessiveDepth verifies a tree deeper than the
// bound fails instead of returning a partial tree as available.
func TestQuarkOfflineLayoutRejectsExcessiveDepth(t *testing.T) {
	t.Parallel()

	tree := map[string][]map[string]any{
		"dir-1": {{"fid": "fid-1", "file_name": "release", "file": false}},
	}
	const depth = 34
	for index := 1; index <= depth; index++ {
		child := map[string]any{"fid": fmt.Sprintf("fid-%d", index+1), "file_name": fmt.Sprintf("d%d", index), "file": false}
		if index == depth {
			child = map[string]any{"fid": fmt.Sprintf("file-%d", index), "file_name": "leaf.bin", "size": 1, "file": true}
		}
		tree[fmt.Sprintf("fid-%d", index)] = []map[string]any{child}
	}
	fake := &offlineTestServer{
		t: t, taskID: "task-depth", downloadURL: "https://cdn.example/x",
		sortTree:   tree,
		resultList: []map[string]any{{"fid": "file-34", "file_name": "leaf.bin", "size": 1}},
	}
	driver := newOfflineDriverForTest(t, fake)

	if _, err := driver.CreateOfflineDownload(context.Background(), drive.OfflineDownloadRequest{
		SourceKind: drive.OfflineSourceMagnet, Source: "magnet:?xt=urn:btih:depth", Destination: "/",
	}); err != nil {
		t.Fatalf("CreateOfflineDownload: %v", err)
	}
	if _, err := driver.GetOfflineDownload(context.Background(), "task-depth"); err == nil {
		t.Fatal("an over-deep tree was published as an available result set")
	}
}
