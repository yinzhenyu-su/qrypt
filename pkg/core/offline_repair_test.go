package core

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/task"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

// TestOfflineRepairRejectsLeafSymlink verifies a final destination that is a
// symbolic link is refused before any bytes are written, for both a directory
// destination and a single-file destination.
func TestOfflineRepairRejectsLeafSymlink(t *testing.T) {
	t.Parallel()

	for _, directory := range []bool{true, false} {
		t.Run(map[bool]string{true: "directory", false: "single-file"}[directory], func(t *testing.T) {
			t.Parallel()

			provider := newFakeOfflineProvider()
			if err := provider.Seed(map[string]string{"downloads/": "", "downloads/a.bin": "new"}); err != nil {
				t.Fatal(err)
			}
			c := newOfflineCore(t, provider, "fake")
			root := t.TempDir()
			outside := filepath.Join(t.TempDir(), "victim")
			if err := os.WriteFile(outside, []byte("untouched"), 0o600); err != nil {
				t.Fatal(err)
			}
			leaf := filepath.Join(root, "a.bin")
			if err := os.Symlink(outside, leaf); err != nil {
				t.Skipf("symlinks unavailable: %v", err)
			}
			destination := root
			if !directory {
				destination = leaf
			}
			run := &offlineRun{
				core:   c,
				driver: provider,
				fetch:  true,
				detail: offlineDetail{
					ParseMount: "fake",
					ParseDir:   "/downloads",
					DownloadTo: destination,
					ResultFiles: []offlineResultFile{{
						Name: "a.bin", Size: 3, RemoteID: "0/downloads/a.bin",
					}},
				},
			}
			err := run.transferPhase(context.Background(), func(func(*task.Task)) {})
			if !errors.Is(err, ErrOfflinePathInvalid) {
				t.Fatalf("error = %v, want offline_path_invalid", err)
			}
			data, readErr := os.ReadFile(outside)
			if readErr != nil || string(data) != "untouched" {
				t.Fatalf("external destination changed: %q err=%v", data, readErr)
			}
		})
	}
}

// TestOfflineRepairLateUnsafeTargetWritesNothing verifies every target is
// preflighted before the first target is written.
func TestOfflineRepairLateUnsafeTargetWritesNothing(t *testing.T) {
	t.Parallel()

	provider := newFakeOfflineProvider()
	if err := provider.Seed(map[string]string{
		"downloads/":      "",
		"downloads/a.bin": "aaa",
		"downloads/z.bin": "zzz",
	}); err != nil {
		t.Fatal(err)
	}
	c := newOfflineCore(t, provider, "fake")
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "victim")
	if err := os.WriteFile(outside, []byte("untouched"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "z.bin")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	run := &offlineRun{
		core:   c,
		driver: provider,
		fetch:  true,
		detail: offlineDetail{
			ParseMount: "fake",
			ParseDir:   "/downloads",
			DownloadTo: root,
			ResultFiles: []offlineResultFile{
				{Name: "a.bin", Size: 3, RemoteID: "0/downloads/a.bin"},
				{Index: 1, Name: "z.bin", Size: 3, RemoteID: "0/downloads/z.bin"},
			},
		},
	}
	err := run.transferPhase(context.Background(), func(func(*task.Task)) {})
	if !errors.Is(err, ErrOfflinePathInvalid) {
		t.Fatalf("error = %v, want offline_path_invalid", err)
	}
	if _, statErr := os.Stat(filepath.Join(root, "a.bin")); !os.IsNotExist(statErr) {
		t.Fatalf("first target was written before the unsafe later target was discovered: %v", statErr)
	}
	data, readErr := os.ReadFile(outside)
	if readErr != nil || string(data) != "untouched" {
		t.Fatalf("external destination changed: %q err=%v", data, readErr)
	}
}

// TestOfflineRepairRejectsFileDirectoryCollision verifies a result that is a
// file at a path another result needs as a directory fails before writing.
func TestOfflineRepairRejectsFileDirectoryCollision(t *testing.T) {
	t.Parallel()

	provider := newFakeOfflineProvider()
	if err := provider.Seed(map[string]string{"downloads/": ""}); err != nil {
		t.Fatal(err)
	}
	c := newOfflineCore(t, provider, "fake")
	run := &offlineRun{
		core:   c,
		driver: provider,
		fetch:  true,
		detail: offlineDetail{
			ParseMount: "fake",
			ParseDir:   "/downloads",
			DownloadTo: t.TempDir(),
			ResultFiles: []offlineResultFile{
				{Name: "x", Size: 1, RemoteID: "0/downloads/x"},
				{Name: "y", Size: 1, RemoteID: "0/downloads/x/y", Path: "x/y"},
			},
		},
	}
	err := run.transferPhase(context.Background(), func(func(*task.Task)) {})
	if !errors.Is(err, ErrOfflinePathInvalid) {
		t.Fatalf("error = %v, want offline_path_invalid", err)
	}
}

// TestOfflineRepairCaseAliasFollowsDestinationFilesystem verifies alias
// handling against the actual destination volume: a case-insensitive volume
// rejects the duplicate, while a case-sensitive volume writes both files.
func TestOfflineRepairCaseAliasFollowsDestinationFilesystem(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	caseInsensitive := testVolumeCaseInsensitive(t, root)
	provider := newFakeOfflineProvider()
	if err := provider.Seed(map[string]string{
		"downloads/":         "",
		"downloads/Same.bin": "AA",
		"downloads/same.bin": "bb",
	}); err != nil {
		t.Fatal(err)
	}
	c := newOfflineCore(t, provider, "fake")
	run := &offlineRun{
		core:   c,
		driver: provider,
		fetch:  true,
		detail: offlineDetail{
			ParseMount: "fake",
			ParseDir:   "/downloads",
			DownloadTo: root,
			ResultFiles: []offlineResultFile{
				{Name: "Same.bin", Size: 2, RemoteID: "0/downloads/Same.bin"},
				{Name: "same.bin", Size: 2, RemoteID: "0/downloads/same.bin"},
			},
		},
	}
	err := run.transferPhase(context.Background(), func(func(*task.Task)) {})
	if caseInsensitive {
		if !errors.Is(err, ErrOfflinePathInvalid) {
			t.Fatalf("case-insensitive volume: error = %v, want offline_path_invalid", err)
		}
		if entries, readErr := os.ReadDir(root); readErr != nil || len(entries) != 0 {
			t.Fatalf("case-insensitive volume entries = %v err=%v, want none", entries, readErr)
		}
		return
	}
	if err != nil {
		t.Fatalf("case-sensitive volume: %v", err)
	}
	if data, readErr := os.ReadFile(filepath.Join(root, "Same.bin")); readErr != nil || string(data) != "AA" {
		t.Fatalf("Same.bin = %q err=%v", data, readErr)
	}
	if data, readErr := os.ReadFile(filepath.Join(root, "same.bin")); readErr != nil || string(data) != "bb" {
		t.Fatalf("same.bin = %q err=%v", data, readErr)
	}
}

// TestOfflineRepairUnicodeAliasKeepsExistingContent verifies canonical-
// equivalent names are rejected when the actual destination volume aliases
// them, without touching an existing destination file.
func TestOfflineRepairUnicodeAliasKeepsExistingContent(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	first, second := "\u00e9.bin", "e\u0301.bin"
	if err := os.WriteFile(filepath.Join(root, first), []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	one, err := os.Stat(filepath.Join(root, first))
	if err != nil {
		t.Fatal(err)
	}
	two, err := os.Stat(filepath.Join(root, second))
	if err != nil || !os.SameFile(one, two) {
		t.Skip("destination filesystem distinguishes Unicode canonical forms")
	}

	provider := newFakeOfflineProvider()
	if err := provider.Seed(map[string]string{
		"downloads/":          "",
		"downloads/" + first:  "aaa",
		"downloads/" + second: "bbb",
	}); err != nil {
		t.Fatal(err)
	}
	c := newOfflineCore(t, provider, "fake")
	run := &offlineRun{
		core:   c,
		driver: provider,
		fetch:  true,
		detail: offlineDetail{
			ParseMount: "fake",
			ParseDir:   "/downloads",
			DownloadTo: root,
			ResultFiles: []offlineResultFile{
				{Name: first, Size: 3, RemoteID: "0/downloads/" + first},
				{Index: 1, Name: second, Size: 3, RemoteID: "0/downloads/" + second},
			},
		},
	}
	err = run.transferPhase(context.Background(), func(func(*task.Task)) {})
	if !errors.Is(err, ErrOfflinePathInvalid) {
		t.Fatalf("error = %v, want offline_path_invalid", err)
	}
	if data, readErr := os.ReadFile(filepath.Join(root, first)); readErr != nil || string(data) != "original" {
		t.Fatalf("existing destination content changed: %q err=%v", data, readErr)
	}
	if entries, readErr := os.ReadDir(root); readErr != nil || len(entries) != 1 {
		t.Fatalf("destination entries = %v err=%v, want only the original file", entries, readErr)
	}
}

// TestOfflineFetchAvailabilityOnLaterObservation verifies a fetch starts
// transferring when an availability signal arrives after the first poll, even
// though the provider task never reaches a terminal state.
func TestOfflineFetchAvailabilityOnLaterObservation(t *testing.T) {
	t.Parallel()

	provider := newFakeOfflineProvider()
	provider.files = []drive.OfflineFile{{Name: "later.bin", Size: 5, RemoteID: "0/downloads/later.bin"}}
	provider.states = []drive.OfflineState{drive.OfflineStateQueued, drive.OfflineStateRunning}
	provider.availableAt = []bool{false, true}
	provider.filesAt = []bool{false, true}
	if err := provider.Seed(map[string]string{"downloads/": "", "downloads/later.bin": "later"}); err != nil {
		t.Fatal(err)
	}
	c := newOfflineCore(t, provider, "fake")

	destination := filepath.Join(t.TempDir(), "later.bin")
	created, err := c.CreateOperation(context.Background(), task.OperationRequest{
		Operation: task.OperationDownload,
		Offline: &task.OfflineOperationRequest{
			Source:     "magnet:?xt=urn:btih:later-available",
			ParseTo:    "/fake/downloads",
			DownloadTo: destination,
			New:        true,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	done := waitCoreTask(t, c, created.ID)
	if done.State != task.StateSucceeded {
		t.Fatalf("state = %s (%+v), want success from later availability", done.State, done.Error)
	}
	if data, err := os.ReadFile(destination); err != nil || string(data) != "later" {
		t.Fatalf("destination = %q err=%v", data, err)
	}
}

// TestOfflineFetchLatestTerminalFailureWins verifies a stale create-time
// availability signal cannot hide a failed or canceled latest observation.
func TestOfflineFetchLatestTerminalFailureWins(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		state drive.OfflineState
		want  task.State
	}{
		{name: "failed", state: drive.OfflineStateFailed, want: task.StateFailed},
		{name: "canceled", state: drive.OfflineStateCanceled, want: task.StateCanceled},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			provider := newFakeOfflineProvider()
			provider.available = true
			provider.createFiles = true
			provider.filesEveryState = true
			provider.files = []drive.OfflineFile{{Name: "stale.bin", Size: 3, RemoteID: "0/downloads/stale.bin"}}
			provider.states = []drive.OfflineState{tc.state}
			if err := provider.Seed(map[string]string{"downloads/": "", "downloads/stale.bin": "abc"}); err != nil {
				t.Fatal(err)
			}
			c := newOfflineCore(t, provider, "fake")
			root := t.TempDir()

			created, err := c.CreateOperation(context.Background(), task.OperationRequest{
				Operation: task.OperationDownload,
				Offline: &task.OfflineOperationRequest{
					Source:     "magnet:?xt=urn:btih:stale-available",
					ParseTo:    "/fake/downloads",
					DownloadTo: root,
					New:        true,
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			done := waitCoreTask(t, c, created.ID)
			if done.State != tc.want {
				t.Fatalf("state = %s (%+v), want %s", done.State, done.Error, tc.want)
			}
			if containsCall(provider.FakeCalls(), "Remove:0/downloads/stale.bin") {
				t.Fatal("a failed/canceled task cleaned up its staged cloud copy")
			}
			if entries, err := os.ReadDir(root); err != nil || len(entries) != 0 {
				t.Fatalf("destination entries = %v err=%v, want none", entries, err)
			}
		})
	}
}

// TestOfflineFetchObserveErrorRetries verifies a single transient observation
// failure during the availability probe does not turn into a no-files failure.
func TestOfflineFetchObserveErrorRetries(t *testing.T) {
	t.Parallel()

	provider := newFakeOfflineProvider()
	provider.available = true
	provider.files = []drive.OfflineFile{{Name: "retry.bin", Size: 5, RemoteID: "0/downloads/retry.bin"}}
	provider.states = []drive.OfflineState{drive.OfflineStateRunning, drive.OfflineStateSucceeded}
	provider.filesAt = []bool{false, true}
	provider.getErrs = []error{drive.NewOfflineError(drive.OfflineErrProviderUnavailable, "", "transient lookup failure")}
	if err := provider.Seed(map[string]string{"downloads/": "", "downloads/retry.bin": "retry"}); err != nil {
		t.Fatal(err)
	}
	c := newOfflineCore(t, provider, "fake")

	destination := filepath.Join(t.TempDir(), "retry.bin")
	created, err := c.CreateOperation(context.Background(), task.OperationRequest{
		Operation: task.OperationDownload,
		Offline: &task.OfflineOperationRequest{
			Source:     "magnet:?xt=urn:btih:observe-retry",
			ParseTo:    "/fake/downloads",
			DownloadTo: destination,
			New:        true,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	done := waitCoreTask(t, c, created.ID)
	if done.State != task.StateSucceeded {
		t.Fatalf("state = %s (%+v), want success after a transient lookup retry", done.State, done.Error)
	}
	if data, err := os.ReadFile(destination); err != nil || string(data) != "retry" {
		t.Fatalf("destination = %q err=%v", data, err)
	}
}

// TestOfflineFetchIncompleteLayoutDoesNotReconcilePartial verifies a transient
// provider-unavailable error after a partial pre-availability observation is
// not turned into synthetic success for that subset.
func TestOfflineFetchIncompleteLayoutDoesNotReconcilePartial(t *testing.T) {
	t.Parallel()

	provider := newFakeOfflineProvider()
	provider.files = []drive.OfflineFile{{Name: "partial.bin", Size: 3, RemoteID: "0/downloads/partial.bin"}}
	provider.filesEveryState = true
	provider.states = []drive.OfflineState{drive.OfflineStateRunning}
	provider.getErrs = make([]error, 10)
	provider.getErrs[0] = nil // let the first observation return the partial list
	for index := 1; index < len(provider.getErrs); index++ {
		provider.getErrs[index] = drive.NewOfflineError(drive.OfflineErrProviderUnavailable, "", "layout walk incomplete")
	}
	if err := provider.Seed(map[string]string{"downloads/": "", "downloads/partial.bin": "abc"}); err != nil {
		t.Fatal(err)
	}
	c := newOfflineCore(t, provider, "fake")
	destination := filepath.Join(t.TempDir(), "partial.bin")

	created, err := c.CreateOperation(context.Background(), task.OperationRequest{
		Operation: task.OperationDownload,
		Offline: &task.OfflineOperationRequest{
			Source:     "magnet:?xt=urn:btih:partial-layout",
			ParseTo:    "/fake/downloads",
			DownloadTo: destination,
			New:        true,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	done := waitCoreTask(t, c, created.ID)
	if done.State != task.StateFailed {
		t.Fatalf("state = %s (%+v), want failed instead of synthetic partial success", done.State, done.Error)
	}
	if _, err := os.Stat(destination); !os.IsNotExist(err) {
		t.Fatalf("partial result was transferred: %v", err)
	}
	if containsCall(provider.FakeCalls(), "Remove:0/downloads/partial.bin") {
		t.Fatal("partial result was cleaned up after a synthetic success")
	}
}

// TestOfflineTransferRootReplacementStaysConfined verifies the transfer pins
// the destination root once: renaming the root and replacing it with a symlink
// between files cannot redirect the remaining writes outside.
func TestOfflineTransferRootReplacementStaysConfined(t *testing.T) {
	t.Parallel()

	provider := newFakeOfflineProvider()
	if err := provider.Seed(map[string]string{
		"downloads/":      "",
		"downloads/a.bin": "aaa",
		"downloads/b.bin": "bbb",
	}); err != nil {
		t.Fatal(err)
	}
	c := newOfflineCore(t, provider, "fake")
	root := t.TempDir()
	outside := t.TempDir()
	run := &offlineRun{
		core:   c,
		driver: provider,
		fetch:  true,
		detail: offlineDetail{
			ParseMount: "fake",
			ParseDir:   "/downloads",
			DownloadTo: root,
			ResultFiles: []offlineResultFile{
				{Name: "a.bin", Size: 3, RemoteID: "0/downloads/a.bin"},
				{Index: 1, Name: "b.bin", Size: 3, RemoteID: "0/downloads/b.bin"},
			},
		},
	}
	// Block on the first progress update: the first file is still open and the
	// second file has not been opened, so the root replacement below is
	// deterministic rather than a timing race.
	firstBytes := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	update := func(apply func(*task.Task)) {
		item := &task.Task{}
		apply(item)
		if item.Progress.TransferBytesDone > 0 {
			once.Do(func() {
				close(firstBytes)
				<-release
			})
		}
	}
	done := make(chan error, 1)
	go func() {
		done <- run.transferPhase(context.Background(), update)
	}()
	select {
	case <-firstBytes:
	case <-time.After(5 * time.Second):
		close(release)
		<-done
		t.Fatal("transfer did not report the first file bytes")
	}
	moved := root + "-moved"
	if err := os.Rename(root, moved); err != nil {
		close(release)
		<-done
		t.Skipf("cannot rename an open destination root: %v", err)
	}
	if err := os.Symlink(outside, root); err != nil {
		close(release)
		<-done
		t.Skipf("symlinks unavailable: %v", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("transferPhase: %v", err)
	}
	if entries, err := os.ReadDir(outside); err != nil || len(entries) != 0 {
		t.Fatalf("outside entries = %v err=%v, want none", entries, err)
	}
	for _, name := range []string{"a.bin", "b.bin"} {
		if _, err := os.Stat(filepath.Join(moved, name)); err != nil {
			t.Fatalf("pinned destination is missing %s: %v", name, err)
		}
	}
}

// TestOfflineLocalWriterAbortUsesPinnedRoot verifies a failed Close keeps the
// pinned root open so Abort removes through it, never through the replaced
// destination pathname.
func TestOfflineLocalWriterAbortUsesPinnedRoot(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	outside := t.TempDir()
	writer, err := openLocalWriter(filepath.Join(root, "f.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.WriteAt([]byte("data"), 0); err != nil {
		t.Fatal(err)
	}
	local := writer.(*offlineLocalWriter)
	// Force both Sync and Close to fail, as a disk error would.
	_ = local.file.Close()

	moved := root + "-moved"
	if err := os.Rename(root, moved); err != nil {
		t.Skipf("cannot rename an open destination root: %v", err)
	}
	if err := os.Symlink(outside, root); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := os.WriteFile(filepath.Join(outside, "f.bin"), []byte("sentinel"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err == nil {
		t.Fatal("Close unexpectedly succeeded on a closed descriptor")
	}
	if err := writer.Abort(); err != nil {
		t.Fatalf("Abort: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(outside, "f.bin")); err != nil || string(data) != "sentinel" {
		t.Fatalf("outside file was touched: %q err=%v", data, err)
	}
	if _, err := os.Stat(filepath.Join(moved, "f.bin")); !os.IsNotExist(err) {
		t.Fatalf("pinned partial file was not removed: %v", err)
	}
}

// TestOfflineRetryClearsAvailabilityBeforeUsingPartialResults verifies an
// in-place retry hands the driver a false availability and does not transfer
// the partial list retained by the failed generation.
func TestOfflineRetryClearsAvailabilityBeforeUsingPartialResults(t *testing.T) {
	t.Parallel()

	provider := newFakeOfflineProvider()
	provider.available = true
	provider.createFiles = true
	provider.filesEveryState = true
	provider.files = []drive.OfflineFile{{Name: "partial.bin", Size: 3, RemoteID: "0/downloads/partial.bin"}}
	provider.states = []drive.OfflineState{drive.OfflineStateFailed}
	if err := provider.Seed(map[string]string{"downloads/": "", "downloads/partial.bin": "abc"}); err != nil {
		t.Fatal(err)
	}
	c := newOfflineCore(t, provider, "fake")
	destination := filepath.Join(t.TempDir(), "partial.bin")

	created, err := c.CreateOperation(context.Background(), task.OperationRequest{
		Operation: task.OperationDownload,
		Offline: &task.OfflineOperationRequest{
			Source:     "magnet:?xt=urn:btih:retry-partial",
			ParseTo:    "/fake/downloads",
			DownloadTo: destination,
			New:        true,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	done := waitCoreTask(t, c, created.ID)
	if done.State != task.StateFailed {
		t.Fatalf("first generation state = %s, want failed", done.State)
	}

	provider.mu.Lock()
	provider.available = false
	provider.retryStates = []drive.OfflineState{drive.OfflineStateRunning}
	provider.restored = nil
	provider.mu.Unlock()
	if err := c.RetryTask(context.Background(), created.ID); err != nil {
		t.Fatalf("retry: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		provider.mu.Lock()
		restored := append([]drive.OfflineLayout(nil), provider.restored...)
		provider.mu.Unlock()
		if len(restored) > 0 {
			if restored[len(restored)-1].Available {
				t.Fatalf("retry restored availability=true: %+v", restored[len(restored)-1])
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("retry did not restore the persisted layout")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if _, err := os.Stat(destination); !os.IsNotExist(err) {
		t.Fatalf("partial result was transferred after retry: %v", err)
	}
	if containsCall(provider.FakeCalls(), "Remove:0/downloads/partial.bin") {
		t.Fatal("partial result was cleaned up after retry")
	}
	_ = c.CancelTask(context.Background(), created.ID)
}

// testVolumeCaseInsensitive reports whether dir's filesystem treats a
// differently-cased name as the same entry.
func testVolumeCaseInsensitive(t *testing.T, dir string) bool {
	t.Helper()
	name := fmt.Sprintf("CaseProbe-%d", os.Getpid())
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Remove(path) }()
	_, err := os.Stat(filepath.Join(dir, strings.ToUpper(name)))
	return err == nil
}

// TestOfflineRestoreLayoutFromPersistedDetail verifies a recovered fetch hands
// the persisted provider output identity to the driver before observing.
func TestOfflineRestoreLayoutFromPersistedDetail(t *testing.T) {
	t.Parallel()

	provider := newFakeOfflineProvider()
	provider.files = []drive.OfflineFile{{Name: "movie.bin", Size: 3, RemoteID: "0/downloads/movie.bin"}}
	if err := provider.Seed(map[string]string{"downloads/": "", "downloads/movie.bin": "abc"}); err != nil {
		t.Fatal(err)
	}
	provider.mu.Lock()
	provider.observations["ptask-restored"] = []drive.OfflineDownload{
		{ID: "ptask-restored", State: drive.OfflineStateRunning, Available: true, Files: provider.files},
	}
	provider.mu.Unlock()

	stateDir := t.TempDir()
	destination := filepath.Join(t.TempDir(), "movie.bin")
	detail := offlineDetail{
		Source:         "magnet:?xt=urn:btih:restored",
		SourceKind:     "magnet",
		SourceIdentity: "magnet:btih:restored",
		ParseMount:     "fake",
		ParseDir:       "/downloads",
		ParseRemoteID:  "parent-fid",
		Selection:      offlineDetailSelection{Mode: "all"},
		ProviderID:     "ptask-restored",
		ProviderRootID: "root-fid",
		Available:      true,
		DownloadTo:     destination,
		Phase:          offlinePhaseStaging,
	}
	taskBefore := task.Task{
		ID:           "offline-restore-layout",
		Type:         task.TypeOfflineFetch,
		Operation:    task.OperationDownload,
		State:        task.StateRunning,
		Capabilities: task.Capabilities{Persistent: true, Cancelable: true},
		Detail:       map[string]any{task.OfflineDetailKey: detail},
	}
	writeJournalEntry(t, stateDir, taskBefore)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	fs, err := vfs.New(provider, vfs.Options{Name: "fake", StorageDir: filepath.Join(t.TempDir(), "cache")})
	if err != nil {
		t.Fatal(err)
	}
	ns, err := vfs.NewNamespace([]vfs.Mount{{Name: "fake", FS: fs}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stopTestVFS(t, ns) })
	ns.Start(ctx)
	c := &Core{fs: ns, runtimeLayout: RuntimeLayout{StateDir: stateDir}, offlinePollInterval: 5 * time.Millisecond}
	t.Cleanup(func() { _ = c.Close(context.Background()) })
	if _, err := c.taskManager(); err != nil {
		t.Fatal(err)
	}

	done := waitCoreTask(t, c, "offline-restore-layout")
	if done.State != task.StateSucceeded {
		t.Fatalf("recovered fetch state = %s (%+v), want success", done.State, done.Error)
	}
	if data, err := os.ReadFile(destination); err != nil || string(data) != "abc" {
		t.Fatalf("destination = %q err=%v", data, err)
	}
	provider.mu.Lock()
	restored := append([]drive.OfflineLayout(nil), provider.restored...)
	provider.mu.Unlock()
	if len(restored) != 1 {
		t.Fatalf("restored layouts = %+v, want one", restored)
	}
	want := drive.OfflineLayout{TaskID: "ptask-restored", RootID: "root-fid", ParentID: "parent-fid", Available: true}
	if restored[0] != want {
		t.Fatalf("restored layout = %+v, want %+v", restored[0], want)
	}
}
