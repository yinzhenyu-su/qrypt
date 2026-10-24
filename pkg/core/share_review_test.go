package core

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	shareapi "github.com/yinzhenyu/qrypt/pkg/share"
	"github.com/yinzhenyu/qrypt/pkg/task"
)

// TestShareFetchDirectDownloadNeedsNoSaveCapability pins the capability split:
// a direct fetch only needs ShareReceive + ShareFetch, while a cloud save still
// requires ShareSave.
func TestShareFetchDirectDownloadNeedsNoSaveCapability(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("payload"))
	}))
	defer server.Close()

	provider := newFakeReceiveProvider()
	provider.caps = []drive.Capability{
		drive.CapabilityWriter,
		drive.CapabilityShareReceive,
		drive.CapabilityShareFetch,
	}
	provider.downloadURL = server.URL
	provider.entries = []drive.ShareEntry{{Entry: drive.Entry{ID: "f1", Name: "a.bin", Size: 7}, Ticket: "t1"}}
	c := newReceiveCore(t, map[string]*fakeReceiveProvider{"fake": provider}, nil)

	destination := filepath.Join(t.TempDir(), "a.bin")
	created := createShareFetch(t, c, "https://fake.test/s/abc", "", destination, 0, 0)
	done := waitCoreTask(t, c, created.ID)
	if done.State != task.StateSucceeded {
		t.Fatalf("state = %s (%v)", done.State, done.Error)
	}
	data, err := os.ReadFile(destination)
	if err != nil || string(data) != "payload" {
		t.Fatalf("destination = %q err=%v", data, err)
	}

	// The same provider cannot act as a cloud-save target.
	_, err = c.CreateTask(context.Background(), task.Request{
		Type:  task.TypeShareSave,
		Share: &task.ShareOperationRequest{Link: "https://fake.test/s/abc", To: "/fake/dest"},
	})
	if !errors.Is(err, ErrShareUnsupported) {
		t.Fatalf("save error = %v, want ErrShareUnsupported", err)
	}
}

// TestShareReceivePasscodeFailsBeforeTaskCreation pins pre-flight link opening:
// a missing or wrong passcode must be a request error, not a failed task.
func TestShareReceivePasscodeFailsBeforeTaskCreation(t *testing.T) {
	t.Parallel()

	provider := newFakeReceiveProvider()
	seedReceiveFixture(provider)
	provider.openErr = drive.NewShareError(drive.ShareErrPasscodeRequired, "41008", "需要提取码")
	c := newReceiveCore(t, map[string]*fakeReceiveProvider{"fake": provider}, nil)

	_, err := c.CreateTask(context.Background(), task.Request{
		Type:  task.TypeShareSave,
		Share: &task.ShareOperationRequest{Link: "https://fake.test/s/abc", To: "/fake/dest"},
	})
	if !drive.IsShareErrorCode(err, drive.ShareErrPasscodeRequired) {
		t.Fatalf("error = %v, want share_passcode_required", err)
	}
	manager, managerErr := c.taskManager()
	if managerErr != nil {
		t.Fatal(managerErr)
	}
	tasks, listErr := manager.ListTasks(context.Background(), task.Filter{})
	if listErr != nil {
		t.Fatal(listErr)
	}
	if len(tasks) != 0 {
		t.Fatalf("a failed passcode created %d tasks", len(tasks))
	}

	// Retrying with the explicit passcode succeeds and forwards it.
	if err := provider.Seed(map[string]string{"dest/": ""}); err != nil {
		t.Fatal(err)
	}
	provider.mu.Lock()
	provider.openErr = nil
	provider.mu.Unlock()
	created := createShareSave(t, c, "https://fake.test/s/abc", "/fake/dest", task.OfflineSelection{})
	if created.ID == "" {
		t.Fatal("retry with a passcode created no task")
	}
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if provider.lastLink.Passcode != "" {
		t.Fatalf("link passcode unexpectedly set: %q", provider.lastLink.Passcode)
	}
}

func TestShareReceiveExplicitPasscodeIsForwarded(t *testing.T) {
	t.Parallel()

	provider := newFakeReceiveProvider()
	if err := provider.Seed(map[string]string{"dest/": ""}); err != nil {
		t.Fatal(err)
	}
	seedReceiveFixture(provider)
	c := newReceiveCore(t, map[string]*fakeReceiveProvider{"fake": provider}, nil)

	created, err := c.CreateTask(context.Background(), task.Request{
		Type: task.TypeShareSave,
		Share: &task.ShareOperationRequest{
			Link:     "https://fake.test/s/abc",
			Passcode: "ab12",
			To:       "/fake/dest",
		},
	})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	waitCoreTask(t, c, created.ID)
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if provider.lastLink.Passcode != "ab12" {
		t.Fatalf("passcode sent to provider = %q", provider.lastLink.Passcode)
	}
}

// TestShareReceiveRejectsUnsupportedSelection pins the policy gate: a provider
// that cannot enumerate names/sizes rejects any selector other than all before
// a task exists.
func TestShareReceiveRejectsUnsupportedSelection(t *testing.T) {
	t.Parallel()

	provider := newFakeReceiveProvider()
	provider.policy.Selection = false
	if err := provider.Seed(map[string]string{"dest/": ""}); err != nil {
		t.Fatal(err)
	}
	seedReceiveFixture(provider)
	c := newReceiveCore(t, map[string]*fakeReceiveProvider{"fake": provider}, nil)

	_, err := c.CreateTask(context.Background(), task.Request{
		Type: task.TypeShareSave,
		Share: &task.ShareOperationRequest{
			Link:      "https://fake.test/s/abc",
			To:        "/fake/dest",
			Selection: task.OfflineSelection{Mode: task.OfflineSelectionLargest, Largest: 1},
		},
	})
	if !drive.IsShareErrorCode(err, drive.ShareErrUnsupported) {
		t.Fatalf("error = %v, want share_unsupported", err)
	}
	manager, managerErr := c.taskManager()
	if managerErr != nil {
		t.Fatal(managerErr)
	}
	tasks, listErr := manager.ListTasks(context.Background(), task.Filter{})
	if listErr != nil {
		t.Fatal(listErr)
	}
	if len(tasks) != 0 {
		t.Fatalf("unsupported selection created %d tasks", len(tasks))
	}
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if provider.openCalls != 0 || provider.listCalls != 0 || len(provider.saveCalls) != 0 {
		t.Fatalf("provider was called: open=%d list=%d save=%d", provider.openCalls, provider.listCalls, len(provider.saveCalls))
	}
}

// TestShareSaveUnverifiableOutcomeFails pins the verification floor: an outcome
// whose saved entries carry no name or id cannot be proven to exist and must
// fail instead of being reported as success.
func TestShareSaveUnverifiableOutcomeFails(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		wantCode string
		outcome  func() drive.ShareSaveOutcome
	}{
		{
			name:     "saved entry without identity",
			wantCode: string(drive.ShareErrDestinationUnverified),
			outcome: func() drive.ShareSaveOutcome {
				return drive.ShareSaveOutcome{
					State: drive.SaveStateSucceeded,
					Items: []drive.ShareSaveItem{{
						SourceID: "f1", SourceName: "a.bin", Saved: &drive.Entry{},
					}},
				}
			},
		},
		{
			name: "no per-entry results at all",
			outcome: func() drive.ShareSaveOutcome {
				return drive.ShareSaveOutcome{State: drive.SaveStateSucceeded}
			},
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			provider := newFakeReceiveProvider()
			if err := provider.Seed(map[string]string{"dest/": ""}); err != nil {
				t.Fatal(err)
			}
			seedReceiveFixture(provider)
			provider.outcome = func(drive.ShareSaveRequest) (drive.ShareSaveOutcome, error) {
				return tc.outcome(), nil
			}
			c := newReceiveCore(t, map[string]*fakeReceiveProvider{"fake": provider}, nil)

			created := createShareSave(t, c, "https://fake.test/s/abc", "/fake/dest", task.OfflineSelection{})
			done := waitCoreTask(t, c, created.ID)
			if done.State != task.StateFailed {
				t.Fatalf("state = %s, want failed", done.State)
			}
			if done.Error == nil {
				t.Fatalf("state = %s, want a failure", done.State)
			}
			if tc.wantCode != "" && done.Error.Code != tc.wantCode {
				t.Fatalf("error = %+v, want %s", done.Error, tc.wantCode)
			}
		})
	}
}

// TestCreateShareLinkValidatesExplicitMount pins that --mount is not silently
// ignored: it must agree with the mount the paths already name.
func TestCreateShareLinkValidatesExplicitMount(t *testing.T) {
	t.Parallel()

	provider := newFakeShareProvider()
	if err := provider.Seed(map[string]string{"a.bin": "content"}); err != nil {
		t.Fatal(err)
	}
	c := newShareCore(t, map[string]*fakeShareProvider{"fake": provider})

	if _, err := c.CreateShareLink(context.Background(), ShareLinkCreateRequest{
		Paths: []string{"/fake/a.bin"},
		Mount: "other",
	}); !errors.Is(err, ErrShareCrossMount) {
		t.Fatalf("error = %v, want ErrShareCrossMount", err)
	}
	created, err := c.CreateShareLink(context.Background(), ShareLinkCreateRequest{
		Paths: []string{"/fake/a.bin"},
		Mount: "fake",
	})
	if err != nil {
		t.Fatalf("matching mount: %v", err)
	}
	if created.LinkID == "" {
		t.Fatalf("created = %+v", created)
	}
}

func TestShareReceiveWrongPasscodeFailsBeforeTaskCreation(t *testing.T) {
	t.Parallel()

	provider := newFakeReceiveProvider()
	seedReceiveFixture(provider)
	provider.openErr = drive.NewShareError(drive.ShareErrPasscodeWrong, "41007", "提取码错误")
	c := newReceiveCore(t, map[string]*fakeReceiveProvider{"fake": provider}, nil)

	_, err := c.CreateTask(context.Background(), task.Request{
		Type:  task.TypeShareSave,
		Share: &task.ShareOperationRequest{Link: "https://fake.test/s/abc", Passcode: "wrong", To: "/fake/dest"},
	})
	if !drive.IsShareErrorCode(err, drive.ShareErrPasscodeWrong) {
		t.Fatalf("error = %v, want share_passcode_wrong", err)
	}
	if strings.Contains(err.Error(), "提取码错误") {
		t.Fatalf("provider text leaked: %v", err)
	}
	manager, managerErr := c.taskManager()
	if managerErr != nil {
		t.Fatal(managerErr)
	}
	tasks, listErr := manager.ListTasks(context.Background(), task.Filter{})
	if listErr != nil {
		t.Fatal(listErr)
	}
	if len(tasks) != 0 {
		t.Fatalf("wrong passcode created %d tasks", len(tasks))
	}
}

// TestShareSaveReconcilesIncompleteProviderOutcome pins the core-side floor for
// providers that report only successes: every selected entry still becomes an
// item, and the task is partially failed rather than fully succeeded.
func TestShareSaveReconcilesIncompleteProviderOutcome(t *testing.T) {
	t.Parallel()

	provider := newFakeReceiveProvider()
	if err := provider.Seed(map[string]string{"dest/": ""}); err != nil {
		t.Fatal(err)
	}
	seedReceiveFixture(provider)
	provider.outcome = func(req drive.ShareSaveRequest) (drive.ShareSaveOutcome, error) {
		// Report only the first entry, as Quark's asynchronous task does.
		first := req.Entries[0]
		provider.seed(req.Dest.Entry, first.Name)
		provider.recordSaved(first.Name)
		return drive.ShareSaveOutcome{
			State: drive.SaveStateSucceeded,
			Items: []drive.ShareSaveItem{{
				SourceID: first.ID, SourceName: first.Name, SourceSize: first.Size,
				Saved: &drive.Entry{ID: "0/dest/" + first.Name, Name: first.Name, ParentID: req.Dest.Entry.ID},
			}},
		}, nil
	}
	c := newReceiveCore(t, map[string]*fakeReceiveProvider{"fake": provider}, nil)

	created := createShareSave(t, c, "https://fake.test/s/abc", "/fake/dest", task.OfflineSelection{})
	done := waitCoreTask(t, c, created.ID)
	if done.State != task.StatePartialFailed {
		t.Fatalf("state = %s, want partial", done.State)
	}
	if len(done.Tracking.Items) != 2 {
		t.Fatalf("items = %+v, want one per selected entry", done.Tracking.Items)
	}
	if done.Tracking.Items[0].State != task.ItemStateSucceeded || done.Tracking.Items[1].State != task.ItemStateFailed {
		t.Fatalf("item states = %+v", done.Tracking.Items)
	}
	if done.Tracking.Items[1].Error == nil {
		t.Fatalf("missing entry has no error: %+v", done.Tracking.Items[1])
	}
}

// TestShareFingerprintIncludesLocalDestination pins the fetch de-duplication
// identity: two receives with different local destinations must not share a
// task, while save-only details keep their existing identity.
func TestShareFingerprintIncludesLocalDestination(t *testing.T) {
	t.Parallel()

	base := shareDetail{
		LinkID:     "abc",
		Mount:      "quark",
		ToMount:    "quark",
		ToDir:      "/inbox",
		DownloadTo: "/local/a",
	}
	same := base
	if shareReceiveFingerprint(base) != shareReceiveFingerprint(same) {
		t.Fatal("identical details must share a fingerprint")
	}
	other := base
	other.DownloadTo = "/local/b"
	if shareReceiveFingerprint(base) == shareReceiveFingerprint(other) {
		t.Fatal("different local destinations must not share a fingerprint")
	}

	save := base
	save.DownloadTo = ""
	otherSave := save
	otherSave.ToDir = "/other"
	if shareReceiveFingerprint(save) == shareReceiveFingerprint(otherSave) {
		t.Fatal("different cloud destinations must not share a fingerprint")
	}
}

// TestShareFetchResumesPersistedSaveHandle pins restart behavior: a task whose
// provider save was submitted but whose per-entry result was not persisted must
// poll the existing handle instead of submitting a second save.
func TestShareFetchResumesPersistedSaveHandle(t *testing.T) {
	t.Parallel()

	provider := newFakeReceiveProvider()
	if err := provider.Seed(map[string]string{"dest/": ""}); err != nil {
		t.Fatal(err)
	}
	seedReceiveFixture(provider)
	provider.handle = "save:1"
	provider.getRunning = 1 << 30
	c := newReceiveCore(t, map[string]*fakeReceiveProvider{"fake": provider}, nil)

	downloadDir := t.TempDir() + string(filepath.Separator)
	created := createShareFetch(t, c, "https://fake.test/s/abc", "/fake/dest", downloadDir, 0, 0)
	waitForShare(t, "the provider save handle", func() bool {
		current, err := c.GetTask(context.Background(), created.ID)
		if err != nil {
			return false
		}
		detail, ok := decodeShareDetail(current.Detail)
		provider.mu.Lock()
		calls := provider.getCalls
		provider.mu.Unlock()
		return ok && detail.SaveHandle != "" && calls > 0
	})
	if err := c.CancelTask(context.Background(), created.ID); err != nil {
		t.Fatalf("CancelTask: %v", err)
	}
	waitShareState(t, c, created.ID, task.StateCanceled)

	// A process that dies mid-save leaves a failed task; simulate that state so
	// the retry path is eligible (a canceled task is deliberately not
	// retryable).
	manager, err := c.taskManager()
	if err != nil {
		t.Fatal(err)
	}
	if _, updated := manager.UpdateTask(created.ID, func(item *task.Task) {
		item.State = task.StateFailed
		item.Error = &task.Error{Code: "interrupted", Message: "restarted before the save result was recorded"}
		item.Capabilities.Retryable = true
	}); !updated {
		t.Fatal("could not simulate a failed task")
	}

	provider.mu.Lock()
	provider.getRunning = 0
	// Recovery must not depend on the link still being openable or listable.
	provider.openErr = drive.NewShareError(drive.ShareErrExpired, "", "link expired")
	provider.listErr = drive.NewShareError(drive.ShareErrProvider, "", "listing unavailable")
	openCalls, listCalls := provider.openCalls, provider.listCalls
	saveCalls := len(provider.saveCalls)
	provider.mu.Unlock()
	if saveCalls != 1 {
		t.Fatalf("save calls before retry = %d, want 1", saveCalls)
	}

	if err := c.RetryTask(context.Background(), created.ID); err != nil {
		t.Fatalf("RetryTask: %v", err)
	}
	done := waitCoreTerminal(t, c, created.ID)
	if done.State != task.StateSucceeded {
		t.Fatalf("state = %s error = %+v detail = %+v", done.State, done.Error, done.Detail)
	}
	if done.Progress.Phase != task.PhaseComplete {
		t.Fatalf("phase = %s", done.Progress.Phase)
	}
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if len(provider.saveCalls) != 1 {
		t.Fatalf("save calls after retry = %d, want 1 (the handle must be resumed)", len(provider.saveCalls))
	}
	if provider.openCalls != openCalls || provider.listCalls != listCalls {
		t.Fatalf("resume reopened the share: open %d->%d list %d->%d", openCalls, provider.openCalls, listCalls, provider.listCalls)
	}
	if len(done.Tracking.Items) != 2 {
		t.Fatalf("items = %+v, want one per selected entry", done.Tracking.Items)
	}
}

// TestShareFetchKeepsPartialSaveFailures pins the combined result: a successful
// download of the saved entries must not hide entries the provider failed to
// save.
func TestShareFetchKeepsPartialSaveFailures(t *testing.T) {
	t.Parallel()

	provider := newFakeReceiveProvider()
	if err := provider.Seed(map[string]string{"dest/": ""}); err != nil {
		t.Fatal(err)
	}
	seedReceiveFixture(provider)
	provider.outcome = provider.partialOutcome
	c := newReceiveCore(t, map[string]*fakeReceiveProvider{"fake": provider}, nil)

	downloadDir := t.TempDir() + string(filepath.Separator)
	created := createShareFetch(t, c, "https://fake.test/s/abc", "/fake/dest", downloadDir, 0, 0)
	done := waitCoreTask(t, c, created.ID)
	if done.State != task.StatePartialFailed {
		t.Fatalf("state = %s (%v), want partial", done.State, done.Error)
	}
	if len(done.Tracking.Items) != 2 {
		t.Fatalf("items = %+v, want the failed save and the downloaded entry", done.Tracking.Items)
	}
	if done.Tracking.Items[0].State != task.ItemStateFailed {
		t.Fatalf("save failure item = %+v", done.Tracking.Items[0])
	}
	if done.Tracking.Items[1].State != task.ItemStateSucceeded {
		t.Fatalf("download item = %+v", done.Tracking.Items[1])
	}
}

// TestShareFetchResumeSkipsCompletedDownloads pins that a resumed download does
// not fetch a target whose result is already persisted.
func TestShareFetchResumeSkipsCompletedDownloads(t *testing.T) {
	t.Parallel()

	provider := newFakeReceiveProvider()
	provider.entries = []drive.ShareEntry{{Entry: drive.Entry{ID: "f1", Name: "a.bin", Size: 7}, Ticket: "t1"}}
	c := newReceiveCore(t, map[string]*fakeReceiveProvider{"fake": provider}, nil)

	destination := t.TempDir() + string(filepath.Separator)
	run := &shareRun{
		core: c,
		detail: shareDetail{
			Link:       "https://fake.test/s/abc",
			Mount:      "fake",
			DownloadTo: destination,
			Transferred: []shareSavedEntry{
				{Name: "a.bin", RemoteID: "f1", Size: 7},
			},
		},
		driver:   provider,
		receiver: provider,
	}
	if err := run.downloadPhase(context.Background(), func(func(*task.Task)) {}, false); err != nil {
		t.Fatalf("downloadPhase: %v", err)
	}
	if len(run.detail.Transferred) != 1 || run.detail.Transferred[0].ErrorCode != "" {
		t.Fatalf("persisted result was overwritten: %+v", run.detail.Transferred)
	}
}

func waitCoreTerminal(t *testing.T, c *Core, id string) task.Task {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	var last task.Task
	for time.Now().Before(deadline) {
		current, err := c.GetTask(context.Background(), id)
		if err == nil {
			last = current
			if current.State == task.StateSucceeded || current.State == task.StateFailed ||
				current.State == task.StatePartialFailed || current.State == task.StateCanceled {
				return current
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("task %s did not reach a terminal state: %+v", id, last)
	return last
}

// TestShareFetchTransientPollErrorKeepsHandle pins that only a missing handle
// triggers a resubmit. A timeout, throttle, or transport error must leave the
// provider handle in durable detail so a retry resumes the same task.
func TestShareFetchTransientPollErrorKeepsHandle(t *testing.T) {
	t.Parallel()

	provider := newFakeReceiveProvider()
	if err := provider.Seed(map[string]string{"dest/": ""}); err != nil {
		t.Fatal(err)
	}
	seedReceiveFixture(provider)
	provider.handle = "save:1"
	provider.getRunning = 1 << 30
	c := newReceiveCore(t, map[string]*fakeReceiveProvider{"fake": provider}, nil)

	downloadDir := t.TempDir() + string(filepath.Separator)
	created := createShareFetch(t, c, "https://fake.test/s/abc", "/fake/dest", downloadDir, 0, 0)
	waitForShare(t, "the provider save handle", func() bool {
		current, err := c.GetTask(context.Background(), created.ID)
		if err != nil {
			return false
		}
		detail, ok := decodeShareDetail(current.Detail)
		provider.mu.Lock()
		calls := provider.getCalls
		provider.mu.Unlock()
		return ok && detail.SaveHandle != "" && calls > 0
	})
	if err := c.CancelTask(context.Background(), created.ID); err != nil {
		t.Fatalf("CancelTask: %v", err)
	}
	waitShareState(t, c, created.ID, task.StateCanceled)

	manager, err := c.taskManager()
	if err != nil {
		t.Fatal(err)
	}
	if _, updated := manager.UpdateTask(created.ID, func(item *task.Task) {
		item.State = task.StateFailed
		item.Error = &task.Error{Code: "interrupted", Message: "restarted before the save result was recorded"}
		item.Capabilities.Retryable = true
	}); !updated {
		t.Fatal("could not simulate a failed task")
	}

	provider.mu.Lock()
	provider.getRunning = 0
	provider.getErr = drive.NewShareError(drive.ShareErrRateLimited, "", "slow down")
	provider.mu.Unlock()

	if err := c.RetryTask(context.Background(), created.ID); err != nil {
		t.Fatalf("RetryTask: %v", err)
	}
	failed := waitCoreTerminal(t, c, created.ID)
	if failed.State != task.StateFailed || failed.Error == nil || failed.Error.Code != string(drive.ShareErrRateLimited) {
		t.Fatalf("transient failure = %s/%+v", failed.State, failed.Error)
	}
	detail, ok := decodeShareDetail(failed.Detail)
	if !ok || detail.SaveHandle != "save:1" {
		t.Fatalf("handle was cleared after a transient error: %+v", detail)
	}
	provider.mu.Lock()
	if len(provider.saveCalls) != 1 {
		t.Fatalf("save calls = %d, want 1 (no resubmit)", len(provider.saveCalls))
	}
	provider.getErr = nil
	provider.mu.Unlock()

	if err := c.RetryTask(context.Background(), created.ID); err != nil {
		t.Fatalf("RetryTask after clearing the error: %v", err)
	}
	done := waitCoreTerminal(t, c, created.ID)
	if done.State != task.StateSucceeded {
		t.Fatalf("state = %s error = %+v", done.State, done.Error)
	}
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if len(provider.saveCalls) != 1 {
		t.Fatalf("save calls = %d, want 1 after resuming the handle", len(provider.saveCalls))
	}
}

// TestShareSaveResumesHandleWithoutOpeningLink covers the save-only path: a
// persisted provider handle is polled from durable detail even when the link can
// no longer be opened or listed.
func TestShareSaveResumesHandleWithoutOpeningLink(t *testing.T) {
	t.Parallel()

	provider := newFakeReceiveProvider()
	if err := provider.Seed(map[string]string{"dest/": ""}); err != nil {
		t.Fatal(err)
	}
	seedReceiveFixture(provider)
	provider.handle = "save:1"
	provider.getRunning = 1 << 30
	c := newReceiveCore(t, map[string]*fakeReceiveProvider{"fake": provider}, nil)

	created := createShareSave(t, c, "https://fake.test/s/abc", "/fake/dest", task.OfflineSelection{})
	waitForShare(t, "the provider save handle", func() bool {
		current, err := c.GetTask(context.Background(), created.ID)
		if err != nil {
			return false
		}
		detail, ok := decodeShareDetail(current.Detail)
		provider.mu.Lock()
		calls := provider.getCalls
		provider.mu.Unlock()
		return ok && detail.SaveHandle != "" && calls > 0
	})
	if err := c.CancelTask(context.Background(), created.ID); err != nil {
		t.Fatalf("CancelTask: %v", err)
	}
	waitShareState(t, c, created.ID, task.StateCanceled)

	manager, err := c.taskManager()
	if err != nil {
		t.Fatal(err)
	}
	if _, updated := manager.UpdateTask(created.ID, func(item *task.Task) {
		item.State = task.StateFailed
		item.Error = &task.Error{Code: "interrupted", Message: "restarted before the save result was recorded"}
		item.Capabilities.Retryable = true
	}); !updated {
		t.Fatal("could not simulate a failed task")
	}

	provider.mu.Lock()
	provider.getRunning = 0
	provider.openErr = drive.NewShareError(drive.ShareErrExpired, "", "link expired")
	provider.listErr = drive.NewShareError(drive.ShareErrProvider, "", "listing unavailable")
	openCalls, listCalls := provider.openCalls, provider.listCalls
	provider.mu.Unlock()

	if err := c.RetryTask(context.Background(), created.ID); err != nil {
		t.Fatalf("RetryTask: %v", err)
	}
	done := waitCoreTerminal(t, c, created.ID)
	if done.State != task.StateSucceeded {
		t.Fatalf("state = %s error = %+v", done.State, done.Error)
	}
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if len(provider.saveCalls) != 1 {
		t.Fatalf("save calls = %d, want 1", len(provider.saveCalls))
	}
	if provider.openCalls != openCalls || provider.listCalls != listCalls {
		t.Fatalf("resume reopened the share: open %d->%d list %d->%d", openCalls, provider.openCalls, listCalls, provider.listCalls)
	}
}

func TestResolveShareLinkCandidates(t *testing.T) {
	t.Parallel()

	alpha, beta := newFakeReceiveProvider(), newFakeReceiveProvider()
	c := newReceiveCore(t, map[string]*fakeReceiveProvider{"beta": beta, "alpha": alpha}, nil)

	result, err := c.ResolveShareLink(context.Background(), shareapi.ResolveRequest{Link: "https://fake.test/s/abc"})
	if err != nil {
		t.Fatalf("ResolveShareLink: %v", err)
	}
	if len(result.Candidates) != 2 || result.Candidates[0] != "alpha" || result.Candidates[1] != "beta" {
		t.Fatalf("candidates = %+v, want sorted alpha, beta", result.Candidates)
	}
	if result.Link.LinkID != "abc" {
		t.Fatalf("link = %+v", result.Link)
	}

	if _, err := c.ResolveShareLink(context.Background(), shareapi.ResolveRequest{Link: "https://elsewhere.test/s/abc"}); !errors.Is(err, ErrShareLinkInvalid) {
		t.Fatalf("unknown link error = %v, want ErrShareLinkInvalid", err)
	}
}
