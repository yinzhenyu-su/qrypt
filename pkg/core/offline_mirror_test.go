package core

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/task"
)

// TestOfflineFetchTransfersWhenAvailableBeforeTerminal verifies a fetch starts
// transferring once the provider reports the staged results are available, even
// though the task never reaches a terminal state.
func TestOfflineFetchTransfersWhenAvailableBeforeTerminal(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	download := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("available"))
	}))
	defer download.Close()

	provider := newFakeOfflineProvider()
	provider.downloadURL = download.URL
	provider.available = true
	provider.filesEveryState = true
	provider.files = []drive.OfflineFile{{Name: "a.bin", Size: 9, RemoteID: "0/downloads/a.bin"}}
	provider.states = []drive.OfflineState{drive.OfflineStateQueued, drive.OfflineStateRunning}
	if err := provider.Seed(map[string]string{"downloads/": ""}); err != nil {
		t.Fatal(err)
	}
	c := newOfflineCore(t, provider, "fake")

	destination := filepath.Join(t.TempDir(), "a.bin")
	created, err := c.CreateOperation(ctx, task.OperationRequest{
		Operation: task.OperationDownload,
		Offline: &task.OfflineOperationRequest{
			Source:     "magnet:?xt=urn:btih:abc",
			ParseTo:    "/fake/downloads",
			DownloadTo: destination,
		},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	done := waitCoreTask(t, c, created.ID)
	if done.State != task.StateSucceeded {
		t.Fatalf("state = %s (%v)", done.State, done.Error)
	}
	if data, err := os.ReadFile(destination); err != nil || string(data) != "available" {
		t.Fatalf("destination = %q err=%v", data, err)
	}
}

// TestOfflineFetchMirrorsStagedLayout verifies a fetch recreates the provider's
// folder structure under the destination instead of flattening to file names.
func TestOfflineFetchMirrorsStagedLayout(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	provider := newFakeOfflineProvider()
	provider.files = []drive.OfflineFile{
		{Index: 0, Name: "ep1.mkv", Size: 3, RemoteID: "0/downloads/release/ep1.mkv", Path: "release/ep1.mkv"},
		{Index: 1, Name: "ep2.mkv", Size: 4, RemoteID: "0/downloads/release/sub/ep2.mkv", Path: "release/sub/ep2.mkv"},
	}
	provider.states = []drive.OfflineState{drive.OfflineStateQueued, drive.OfflineStateSucceeded}
	if err := provider.Seed(map[string]string{
		"downloads/":                    "",
		"downloads/release/":            "",
		"downloads/release/ep1.mkv":     "aaa",
		"downloads/release/sub/":        "",
		"downloads/release/sub/ep2.mkv": "bbbb",
	}); err != nil {
		t.Fatal(err)
	}
	c := newOfflineCore(t, provider, "fake")

	root := t.TempDir()
	created, err := c.CreateOperation(ctx, task.OperationRequest{
		Operation: task.OperationDownload,
		Offline: &task.OfflineOperationRequest{
			Source:     "magnet:?xt=urn:btih:abc",
			ParseTo:    "/fake/downloads",
			DownloadTo: root,
			New:        true,
		},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	done := waitCoreTask(t, c, created.ID)
	if done.State != task.StateSucceeded {
		t.Fatalf("state = %s (%v)", done.State, done.Error)
	}
	if got, err := os.ReadFile(filepath.Join(root, "release", "ep1.mkv")); err != nil || string(got) != "aaa" {
		t.Fatalf("ep1 = %q err=%v", got, err)
	}
	if got, err := os.ReadFile(filepath.Join(root, "release", "sub", "ep2.mkv")); err != nil || string(got) != "bbbb" {
		t.Fatalf("ep2 = %q err=%v", got, err)
	}
}

// TestOfflineFetchFlatWritesByName verifies --flat places every file by its own
// name even when the provider reports a staged path.
func TestOfflineFetchFlatWritesByName(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	provider := newFakeOfflineProvider()
	provider.files = []drive.OfflineFile{
		{Index: 0, Name: "ep1.mkv", Size: 3, RemoteID: "0/downloads/release/ep1.mkv", Path: "release/ep1.mkv"},
		{Index: 1, Name: "ep2.mkv", Size: 4, RemoteID: "0/downloads/release/sub/ep2.mkv", Path: "release/sub/ep2.mkv"},
	}
	provider.states = []drive.OfflineState{drive.OfflineStateQueued, drive.OfflineStateSucceeded}
	if err := provider.Seed(map[string]string{
		"downloads/":                    "",
		"downloads/release/":            "",
		"downloads/release/ep1.mkv":     "aaa",
		"downloads/release/sub/":        "",
		"downloads/release/sub/ep2.mkv": "bbbb",
	}); err != nil {
		t.Fatal(err)
	}
	c := newOfflineCore(t, provider, "fake")

	root := t.TempDir()
	created, err := c.CreateOperation(ctx, task.OperationRequest{
		Operation: task.OperationDownload,
		Offline: &task.OfflineOperationRequest{
			Source:     "magnet:?xt=urn:btih:abc",
			ParseTo:    "/fake/downloads",
			DownloadTo: root,
			Flat:       true,
			New:        true,
		},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	done := waitCoreTask(t, c, created.ID)
	if done.State != task.StateSucceeded {
		t.Fatalf("state = %s (%v)", done.State, done.Error)
	}
	if got, err := os.ReadFile(filepath.Join(root, "ep1.mkv")); err != nil || string(got) != "aaa" {
		t.Fatalf("flat ep1 = %q err=%v", got, err)
	}
	if _, err := os.Stat(filepath.Join(root, "release")); !os.IsNotExist(err) {
		t.Fatalf("flat layout must not create the release folder: %v", err)
	}
}

// TestOfflineFetchRejectsUnsafeStagedPath verifies a traversing staged path
// fails the task with offline_path_invalid and writes nothing.
func TestOfflineFetchRejectsUnsafeStagedPath(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	provider := newFakeOfflineProvider()
	provider.files = []drive.OfflineFile{{Name: "evil", Size: 4, RemoteID: "0/downloads/evil", Path: "../evil"}}
	provider.states = []drive.OfflineState{drive.OfflineStateQueued, drive.OfflineStateSucceeded}
	if err := provider.Seed(map[string]string{"downloads/": ""}); err != nil {
		t.Fatal(err)
	}
	c := newOfflineCore(t, provider, "fake")

	root := t.TempDir()
	created, err := c.CreateOperation(ctx, task.OperationRequest{
		Operation: task.OperationDownload,
		Offline: &task.OfflineOperationRequest{
			Source:     "magnet:?xt=urn:btih:abc",
			ParseTo:    "/fake/downloads",
			DownloadTo: root,
			New:        true,
		},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	done := waitCoreTask(t, c, created.ID)
	if done.State != task.StateFailed {
		t.Fatalf("state = %s, want failed (%v)", done.State, done.Error)
	}
	if done.Error == nil || done.Error.Code != string(drive.OfflineErrPathInvalid) {
		t.Fatalf("error = %+v, want offline_path_invalid", done.Error)
	}
	if entries, err := os.ReadDir(root); err != nil || len(entries) != 0 {
		t.Fatalf("destination entries = %v err=%v, want none", entries, err)
	}
}

// TestOfflineFetchRejectsCollidingPaths verifies two results that resolve to the
// same destination fail instead of overwriting each other.
func TestOfflineFetchRejectsCollidingPaths(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	provider := newFakeOfflineProvider()
	provider.files = []drive.OfflineFile{
		{Index: 0, Name: "a", Size: 3, RemoteID: "0/downloads/same", Path: "same"},
		{Index: 1, Name: "b", Size: 4, RemoteID: "0/downloads/other", Path: "same"},
	}
	provider.states = []drive.OfflineState{drive.OfflineStateQueued, drive.OfflineStateSucceeded}
	if err := provider.Seed(map[string]string{"downloads/": ""}); err != nil {
		t.Fatal(err)
	}
	c := newOfflineCore(t, provider, "fake")

	root := t.TempDir()
	created, err := c.CreateOperation(ctx, task.OperationRequest{
		Operation: task.OperationDownload,
		Offline: &task.OfflineOperationRequest{
			Source:     "magnet:?xt=urn:btih:abc",
			ParseTo:    "/fake/downloads",
			DownloadTo: root,
			New:        true,
		},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	done := waitCoreTask(t, c, created.ID)
	if done.State != task.StateFailed {
		t.Fatalf("state = %s, want failed (%v)", done.State, done.Error)
	}
	if done.Error == nil || done.Error.Code != string(drive.OfflineErrPathInvalid) {
		t.Fatalf("error = %+v, want offline_path_invalid", done.Error)
	}
}

// TestOfflineFetchRejectsSymlinkedAncestor verifies a staged path that resolves
// through an existing symlink leaving the destination root is refused.
func TestOfflineFetchRejectsSymlinkedAncestor(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	provider := newFakeOfflineProvider()
	provider.files = []drive.OfflineFile{{Name: "f", Size: 3, RemoteID: "0/downloads/f", Path: "link/f"}}
	provider.states = []drive.OfflineState{drive.OfflineStateQueued, drive.OfflineStateSucceeded}
	if err := provider.Seed(map[string]string{"downloads/": ""}); err != nil {
		t.Fatal(err)
	}
	c := newOfflineCore(t, provider, "fake")

	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	created, err := c.CreateOperation(ctx, task.OperationRequest{
		Operation: task.OperationDownload,
		Offline: &task.OfflineOperationRequest{
			Source:     "magnet:?xt=urn:btih:abc",
			ParseTo:    "/fake/downloads",
			DownloadTo: root,
			New:        true,
		},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	done := waitCoreTask(t, c, created.ID)
	if done.State != task.StateFailed {
		t.Fatalf("state = %s, want failed (%v)", done.State, done.Error)
	}
	if done.Error == nil || done.Error.Code != string(drive.OfflineErrPathInvalid) {
		t.Fatalf("error = %+v, want offline_path_invalid", done.Error)
	}
	if entries, err := os.ReadDir(outside); err != nil || len(entries) != 0 {
		t.Fatalf("outside entries = %v err=%v, want none", entries, err)
	}
}

// TestOfflineFetchSingleFileDestinationRejectsLayout verifies a single-file
// destination cannot hold a directory layout.
func TestOfflineFetchSingleFileDestinationRejectsLayout(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	provider := newFakeOfflineProvider()
	provider.files = []drive.OfflineFile{{Name: "f", Size: 3, RemoteID: "0/downloads/f", Path: "folder/f"}}
	provider.states = []drive.OfflineState{drive.OfflineStateQueued, drive.OfflineStateSucceeded}
	if err := provider.Seed(map[string]string{"downloads/": ""}); err != nil {
		t.Fatal(err)
	}
	c := newOfflineCore(t, provider, "fake")

	destination := filepath.Join(t.TempDir(), "out.bin")
	created, err := c.CreateOperation(ctx, task.OperationRequest{
		Operation: task.OperationDownload,
		Offline: &task.OfflineOperationRequest{
			Source:     "magnet:?xt=urn:btih:abc",
			ParseTo:    "/fake/downloads",
			DownloadTo: destination,
			New:        true,
		},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	done := waitCoreTask(t, c, created.ID)
	if done.State != task.StateFailed {
		t.Fatalf("state = %s, want failed (%v)", done.State, done.Error)
	}
	if done.Error == nil || done.Error.Code != string(drive.OfflineErrPathInvalid) {
		t.Fatalf("error = %+v, want offline_path_invalid", done.Error)
	}
}

// TestOfflineFetchNestedNamespaceDestination verifies a namespace destination
// creates and writes each nested directory level.
func TestOfflineFetchNestedNamespaceDestination(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	provider := newFakeOfflineProvider()
	provider.files = []drive.OfflineFile{{Index: 0, Name: "ep1.mkv", Size: 3, RemoteID: "0/downloads/release/ep1.mkv", Path: "release/ep1.mkv"}}
	provider.states = []drive.OfflineState{drive.OfflineStateQueued, drive.OfflineStateSucceeded}
	if err := provider.Seed(map[string]string{
		"downloads/":                "",
		"downloads/release/":        "",
		"downloads/release/ep1.mkv": "aaa",
	}); err != nil {
		t.Fatal(err)
	}
	c := newOfflineCore(t, provider, "fake")

	created, err := c.CreateOperation(ctx, task.OperationRequest{
		Operation: task.OperationDownload,
		Offline: &task.OfflineOperationRequest{
			Source:     "magnet:?xt=urn:btih:abc",
			ParseTo:    "/fake/downloads",
			DownloadTo: "/fake/out/",
			New:        true,
		},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	done := waitCoreTask(t, c, created.ID)
	if done.State != task.StateSucceeded {
		t.Fatalf("state = %s (%v)", done.State, done.Error)
	}
	entry, err := c.fs.Stat(ctx, "/fake/out/release/ep1.mkv")
	if err != nil || entry.Size != 3 {
		t.Fatalf("nested namespace destination = %+v err=%v", entry, err)
	}
}

// TestOfflineReconcileMissingMatchesNestedPath verifies destination
// reconciliation matches a nested staged path, not just a flat name.
func TestOfflineReconcileMissingMatchesNestedPath(t *testing.T) {
	t.Parallel()

	provider := newFakeOfflineProvider()
	provider.repeatLast = false
	provider.filesEveryState = true
	provider.files = []drive.OfflineFile{{Name: "ep1.mkv", Size: 3, RemoteID: "0/downloads/release/ep1.mkv", Path: "release/ep1.mkv"}}
	provider.states = []drive.OfflineState{drive.OfflineStateRunning}
	if err := provider.Seed(map[string]string{
		"downloads/":                "",
		"downloads/release/":        "",
		"downloads/release/ep1.mkv": "aaa",
	}); err != nil {
		t.Fatal(err)
	}
	c := newOfflineCore(t, provider, "fake")
	created, err := c.CreateOperation(context.Background(), cloudOnlyRequest("magnet:?xt=urn:btih:nested-missing", "/fake/downloads"))
	if err != nil {
		t.Fatal(err)
	}
	done := waitCoreTask(t, c, created.ID)
	if done.State != task.StateSucceeded {
		t.Fatalf("state = %s (%+v), want success from the resolved nested destination", done.State, done.Error)
	}
}

// TestOfflineFetchRemovesStagedRootFolder verifies the resource folder a magnet
// made the provider create is removed once its files are, including nested
// folders.
func TestOfflineFetchRemovesStagedRootFolder(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	provider := newFakeOfflineProvider()
	provider.createFID = "0/downloads/release"
	provider.files = []drive.OfflineFile{
		{Index: 0, Name: "ep1.mkv", Size: 3, RemoteID: "0/downloads/release/ep1.mkv", Path: "release/ep1.mkv"},
		{Index: 1, Name: "ep2.mkv", Size: 4, RemoteID: "0/downloads/release/sub/ep2.mkv", Path: "release/sub/ep2.mkv"},
	}
	provider.states = []drive.OfflineState{drive.OfflineStateQueued, drive.OfflineStateSucceeded}
	if err := provider.Seed(map[string]string{
		"downloads/":                    "",
		"downloads/release/":            "",
		"downloads/release/ep1.mkv":     "aaa",
		"downloads/release/sub/":        "",
		"downloads/release/sub/ep2.mkv": "bbbb",
	}); err != nil {
		t.Fatal(err)
	}
	c := newOfflineCore(t, provider, "fake")

	created, err := c.CreateOperation(ctx, task.OperationRequest{
		Operation: task.OperationDownload,
		Offline: &task.OfflineOperationRequest{
			Source:     "magnet:?xt=urn:btih:abc",
			ParseTo:    "/fake/downloads",
			DownloadTo: t.TempDir(),
			New:        true,
		},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	done := waitCoreTask(t, c, created.ID)
	if done.State != task.StateSucceeded {
		t.Fatalf("state = %s (%v)", done.State, done.Error)
	}
	if !hasExactCall(provider.FakeCalls(), "Remove:0/downloads/release") {
		t.Fatalf("staged root folder was not removed: %v", provider.FakeCalls())
	}
}

// TestOfflineFetchKeepsStagedFolderWithUnrelatedEntry verifies a resource folder
// that still holds an entry this fetch did not create is left untouched.
func TestOfflineFetchKeepsStagedFolderWithUnrelatedEntry(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	provider := newFakeOfflineProvider()
	provider.createFID = "0/downloads/release"
	provider.files = []drive.OfflineFile{
		{Index: 0, Name: "ep1.mkv", Size: 3, RemoteID: "0/downloads/release/ep1.mkv", Path: "release/ep1.mkv"},
	}
	provider.states = []drive.OfflineState{drive.OfflineStateQueued, drive.OfflineStateSucceeded}
	if err := provider.Seed(map[string]string{
		"downloads/":                  "",
		"downloads/release/":          "",
		"downloads/release/ep1.mkv":   "aaa",
		"downloads/release/other.txt": "keep me",
	}); err != nil {
		t.Fatal(err)
	}
	c := newOfflineCore(t, provider, "fake")

	created, err := c.CreateOperation(ctx, task.OperationRequest{
		Operation: task.OperationDownload,
		Offline: &task.OfflineOperationRequest{
			Source:     "magnet:?xt=urn:btih:abc",
			ParseTo:    "/fake/downloads",
			DownloadTo: t.TempDir(),
			New:        true,
		},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	done := waitCoreTask(t, c, created.ID)
	if done.State != task.StateSucceeded {
		t.Fatalf("state = %s (%v)", done.State, done.Error)
	}
	if !containsCall(provider.FakeCalls(), "Remove:0/downloads/release/ep1.mkv") {
		t.Fatalf("staged file was not removed: %v", provider.FakeCalls())
	}
	if hasExactCall(provider.FakeCalls(), "Remove:0/downloads/release") {
		t.Fatalf("folder holding an unrelated entry was removed: %v", provider.FakeCalls())
	}
}

// TestOfflineFetchNeverRemovesParseDestination verifies files staged directly in
// the parse destination do not cause the parse destination to be removed.
func TestOfflineFetchNeverRemovesParseDestination(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	provider := newFakeOfflineProvider()
	provider.createFID = "0/downloads/out.bin"
	provider.files = []drive.OfflineFile{{Name: "out.bin", Size: 3, RemoteID: "0/downloads/out.bin"}}
	provider.states = []drive.OfflineState{drive.OfflineStateQueued, drive.OfflineStateSucceeded}
	if err := provider.Seed(map[string]string{"downloads/": "", "downloads/out.bin": "aaa"}); err != nil {
		t.Fatal(err)
	}
	c := newOfflineCore(t, provider, "fake")

	created, err := c.CreateOperation(ctx, task.OperationRequest{
		Operation: task.OperationDownload,
		Offline: &task.OfflineOperationRequest{
			Source:     "magnet:?xt=urn:btih:abc",
			ParseTo:    "/fake/downloads",
			DownloadTo: t.TempDir(),
			New:        true,
		},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	done := waitCoreTask(t, c, created.ID)
	if done.State != task.StateSucceeded {
		t.Fatalf("state = %s (%v)", done.State, done.Error)
	}
	if !containsCall(provider.FakeCalls(), "Remove:0/downloads/out.bin") {
		t.Fatalf("staged file was not removed: %v", provider.FakeCalls())
	}
	if hasExactCall(provider.FakeCalls(), "Remove:0/downloads") {
		t.Fatalf("parse destination was removed: %v", provider.FakeCalls())
	}
}

// TestOfflineFetchKeepCloudKeepsStagedFolder verifies --keep-cloud keeps both the
// files and their resource folder.
func TestOfflineFetchKeepCloudKeepsStagedFolder(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	provider := newFakeOfflineProvider()
	provider.createFID = "0/downloads/release"
	provider.files = []drive.OfflineFile{
		{Index: 0, Name: "ep1.mkv", Size: 3, RemoteID: "0/downloads/release/ep1.mkv", Path: "release/ep1.mkv"},
	}
	provider.states = []drive.OfflineState{drive.OfflineStateQueued, drive.OfflineStateSucceeded}
	if err := provider.Seed(map[string]string{
		"downloads/":                "",
		"downloads/release/":        "",
		"downloads/release/ep1.mkv": "aaa",
	}); err != nil {
		t.Fatal(err)
	}
	c := newOfflineCore(t, provider, "fake")

	created, err := c.CreateOperation(ctx, task.OperationRequest{
		Operation: task.OperationDownload,
		Offline: &task.OfflineOperationRequest{
			Source:     "magnet:?xt=urn:btih:abc",
			ParseTo:    "/fake/downloads",
			DownloadTo: t.TempDir(),
			KeepCloud:  true,
			New:        true,
		},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	done := waitCoreTask(t, c, created.ID)
	if done.State != task.StateSucceeded {
		t.Fatalf("state = %s (%v)", done.State, done.Error)
	}
	if containsCall(provider.FakeCalls(), "Remove:0/downloads/release/ep1.mkv") ||
		hasExactCall(provider.FakeCalls(), "Remove:0/downloads/release") {
		t.Fatalf("--keep-cloud must keep the staged copy: %v", provider.FakeCalls())
	}
}

// hasExactCall reports whether the fake recorded exactly this call. containsCall
// matches by prefix, which a folder id and a child file id share.
func hasExactCall(calls []string, want string) bool {
	for _, call := range calls {
		if call == want {
			return true
		}
	}
	return false
}

func TestSanitizeOfflineRelPath(t *testing.T) {
	t.Parallel()

	valid := []string{"file.mp4", "release/ep1.mkv", "a/b/c/d.txt", "第一会所/ep1.mkv"}
	for _, raw := range valid {
		if _, err := sanitizeOfflineRelPath(raw); err != nil {
			t.Fatalf("sanitizeOfflineRelPath(%q) = %v, want ok", raw, err)
		}
	}
	invalid := []string{
		"", "/abs/path", `\abs\path`, "C:/drive", "../escape", "a/../b", "a//b",
		"a/./b", "con", "NUL.txt", "trailing.", "trailing ", "bad\x00name", "reserved:name",
	}
	for _, raw := range invalid {
		if _, err := sanitizeOfflineRelPath(raw); err == nil {
			t.Fatalf("sanitizeOfflineRelPath(%q) accepted an unsafe path", raw)
		}
	}
	if _, err := sanitizeOfflineComponent("dir/file"); err == nil {
		t.Fatal("sanitizeOfflineComponent accepted a separator")
	}
	if got, err := sanitizeOfflineComponent("plain.mp4"); err != nil || got != "plain.mp4" {
		t.Fatalf("sanitizeOfflineComponent = %q err=%v", got, err)
	}
}

// TestOfflineDetailRetainsStagedPathAndRoot verifies the staged path and output
// root survive a JSON round trip, and that an older snapshot without them still
// decodes.
func TestOfflineDetailRetainsStagedPathAndRoot(t *testing.T) {
	t.Parallel()

	detail := offlineDetail{
		Source:         "magnet:?xt=urn:btih:abc",
		ProviderID:     "ptask-1",
		ProviderRootID: "root-fid",
		ResultFiles:    []offlineResultFile{{Index: 0, Name: "ep1.mkv", Size: 3, RemoteID: "fid-1", Path: "release/ep1.mkv"}},
	}
	data, err := json.Marshal(detail)
	if err != nil {
		t.Fatal(err)
	}
	decoded, ok := decodeOfflineDetail(map[string]any{task.OfflineDetailKey: json.RawMessage(data)})
	if !ok {
		t.Fatal("detail did not decode")
	}
	if decoded.ProviderRootID != "root-fid" || len(decoded.ResultFiles) != 1 || decoded.ResultFiles[0].Path != "release/ep1.mkv" {
		t.Fatalf("decoded detail = %+v", decoded)
	}

	legacy := map[string]any{task.OfflineDetailKey: map[string]any{
		"source":       "magnet:?xt=urn:btih:abc",
		"result_files": []any{map[string]any{"name": "ep1.mkv", "remote_id": "fid-1"}},
	}}
	old, ok := decodeOfflineDetail(legacy)
	if !ok {
		t.Fatal("legacy detail did not decode")
	}
	if len(old.ResultFiles) != 1 || old.ResultFiles[0].Path != "" {
		t.Fatalf("legacy detail = %+v, want an empty path", old.ResultFiles)
	}
}

// TestOfflineResultItemsExposeStagedPath verifies task items carry the staged
// relative path when the provider reports one.
func TestOfflineResultItemsExposeStagedPath(t *testing.T) {
	t.Parallel()

	detail := offlineDetail{
		ParseDir:   "/downloads",
		ParseMount: "fake",
		ResultFiles: []offlineResultFile{
			{Index: 0, Name: "ep1.mkv", Size: 3, RemoteID: "fid-1", Path: "release/ep1.mkv"},
		},
	}
	items := offlineResultItems(detail)
	if len(items) != 1 {
		t.Fatalf("items = %+v", items)
	}
	if items[0].SourcePath != "release/ep1.mkv" {
		t.Fatalf("item source path = %q, want the staged relative path", items[0].SourcePath)
	}
	if !strings.HasSuffix(items[0].Path, "/downloads/release/ep1.mkv") {
		t.Fatalf("item path = %q, want the staged cloud path", items[0].Path)
	}
}

// TestOfflinePathInvalidIsNotRetryable pins the new error code's classification.
func TestOfflinePathInvalidIsNotRetryable(t *testing.T) {
	t.Parallel()

	if drive.OfflineRetryable(drive.OfflineErrPathInvalid) {
		t.Fatal("offline_path_invalid must not be retryable")
	}
	if string(drive.OfflineErrPathInvalid) != "offline_path_invalid" {
		t.Fatalf("code = %q", drive.OfflineErrPathInvalid)
	}
}
