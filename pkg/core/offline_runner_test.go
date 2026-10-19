package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/task"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

// fakeOfflineProvider is a deterministic provider for the offline lifecycle
// test. It embeds the shared fake driver (so it is a valid VFS backend) and
// scripts provider observations per created task.
type fakeOfflineProvider struct {
	*drive.FakeDriver

	mu              sync.Mutex
	nextID          int
	accelerated     bool
	issueSession    bool
	selection       bool
	states          []drive.OfflineState
	files           []drive.OfflineFile
	filesEveryState bool
	// availableAt and filesAt override p.available / filesEveryState per
	// scripted observation, so a test can make availability (and the result
	// list) appear on a later poll.
	availableAt []bool
	filesAt     []bool
	// retryStates overrides the script used after an in-place retry.
	retryStates []drive.OfflineState
	// getErrs injects one error into the next GetOfflineDownload calls.
	getErrs []error
	// createFiles makes the create response carry the scripted result files.
	createFiles bool
	// repeatLast keeps returning the script's final observation; when false the
	// script is consumed and the task then reports as disappeared.
	repeatLast  bool
	createFID   string
	available   bool
	failCreate  *drive.OfflineError
	preview     drive.OfflinePreview
	downloadURL string

	created      []drive.OfflineDownloadRequest
	observations map[string][]drive.OfflineDownload
	cancelled    []string
	retried      []string
	urlRequests  []drive.OfflineFetchRequest
	restored     []drive.OfflineLayout

	// hideResultsFor hides the result files from the first N listings of their
	// parent directory, emulating an eventually consistent provider listing.
	hideResultsFor   int
	listMu           sync.Mutex
	resultsListCalls int
}

func newFakeOfflineProvider() *fakeOfflineProvider {
	return &fakeOfflineProvider{
		FakeDriver:   drive.NewFakeDriver(),
		selection:    true,
		repeatLast:   true,
		states:       []drive.OfflineState{drive.OfflineStateQueued, drive.OfflineStateSucceeded},
		observations: map[string][]drive.OfflineDownload{},
		downloadURL:  "http://unused",
	}
}

func (p *fakeOfflineProvider) Capabilities() []drive.Capability {
	caps := append([]drive.Capability(nil), p.FakeDriver.Capabilities()...)
	caps = append(caps, drive.CapabilityOfflineDownload, drive.CapabilityOfflineInspect)
	if p.selection {
		caps = append(caps, drive.CapabilityOfflineSelection)
	}
	if p.accelerated {
		caps = append(caps, drive.CapabilityAcceleratedFetch)
	}
	return caps
}

func (p *fakeOfflineProvider) OfflineSourceKinds() []drive.OfflineSourceKind {
	return []drive.OfflineSourceKind{drive.OfflineSourceMagnet, drive.OfflineSourceHTTP}
}

func (p *fakeOfflineProvider) script(id string) []drive.OfflineDownload {
	return p.scriptStates(id, p.states)
}

func (p *fakeOfflineProvider) scriptStates(id string, states []drive.OfflineState) []drive.OfflineDownload {
	if len(states) == 0 {
		states = []drive.OfflineState{drive.OfflineStateQueued, drive.OfflineStateSucceeded}
	}
	out := make([]drive.OfflineDownload, 0, len(states))
	for index, state := range states {
		available := p.available
		if p.availableAt != nil {
			available = index < len(p.availableAt) && p.availableAt[index]
		}
		obs := drive.OfflineDownload{ID: id, State: state, Available: available}
		withFiles := state == drive.OfflineStateSucceeded || p.filesEveryState
		if p.filesAt != nil {
			withFiles = index < len(p.filesAt) && p.filesAt[index]
		}
		if withFiles {
			obs.Files = append([]drive.OfflineFile(nil), p.files...)
		}
		out = append(out, obs)
	}
	return out
}

func (p *fakeOfflineProvider) CreateOfflineDownload(_ context.Context, req drive.OfflineDownloadRequest) (drive.OfflineDownload, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.created = append(p.created, req)
	if p.failCreate != nil {
		return drive.OfflineDownload{}, p.failCreate
	}
	p.nextID++
	id := fmt.Sprintf("ptask-%d", p.nextID)
	p.observations[id] = p.script(id)
	created := drive.OfflineDownload{ID: id, State: drive.OfflineStateQueued, RemoteID: p.createFID, Available: p.available}
	if p.createFiles {
		created.Files = append([]drive.OfflineFile(nil), p.files...)
	}
	return created, nil
}

func (p *fakeOfflineProvider) GetOfflineDownload(_ context.Context, id string) (drive.OfflineDownload, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.getErrs) > 0 {
		err := p.getErrs[0]
		p.getErrs = p.getErrs[1:]
		if err != nil {
			return drive.OfflineDownload{}, err
		}
	}
	seq := p.observations[id]
	if len(seq) == 0 {
		return drive.OfflineDownload{}, drive.NewOfflineError(drive.OfflineErrProviderTaskMissing, "", "task gone")
	}
	obs := seq[0]
	if len(seq) > 1 || !p.repeatLast {
		p.observations[id] = seq[1:]
	}
	return obs, nil
}

func (p *fakeOfflineProvider) CancelOfflineDownload(_ context.Context, id string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cancelled = append(p.cancelled, id)
	return nil
}

func (p *fakeOfflineProvider) RetryOfflineDownload(_ context.Context, id string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.retried = append(p.retried, id)
	states := p.retryStates
	if states == nil {
		states = []drive.OfflineState{drive.OfflineStateQueued, drive.OfflineStateSucceeded}
	}
	p.observations[id] = p.scriptStates(id, states)
	return nil
}

func (p *fakeOfflineProvider) RestoreOfflineLayout(layout drive.OfflineLayout) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.restored = append(p.restored, layout)
}

func (p *fakeOfflineProvider) InspectOfflineDownload(context.Context, drive.OfflineDownloadRequest) (drive.OfflinePreview, error) {
	return p.preview, nil
}

func (p *fakeOfflineProvider) OfflineDownloadURL(_ context.Context, req drive.OfflineFetchRequest) (drive.OfflineDownloadTarget, error) {
	p.mu.Lock()
	p.urlRequests = append(p.urlRequests, req)
	available := p.accelerated && p.issueSession
	p.mu.Unlock()
	if req.Accelerated && !available {
		return drive.OfflineDownloadTarget{}, drive.NewOfflineError(drive.OfflineErrProvider, "", "no accelerated session")
	}
	return drive.OfflineDownloadTarget{URL: p.downloadURL}, nil
}

func (p *fakeOfflineProvider) createdRequests() []drive.OfflineDownloadRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]drive.OfflineDownloadRequest(nil), p.created...)
}

// newOfflineCore builds a Core over a single-mount namespace backed by the fake
// provider.
func newOfflineCore(t *testing.T, provider *fakeOfflineProvider, mount string, opts ...vfs.Options) *Core {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	options := vfs.Options{Name: mount, StorageDir: filepath.Join(t.TempDir(), "cache")}
	if len(opts) > 0 {
		options = opts[0]
		options.Name = mount
		if options.StorageDir == "" {
			options.StorageDir = filepath.Join(t.TempDir(), "cache")
		}
	}
	fs, err := vfs.New(provider, options)
	if err != nil {
		t.Fatalf("vfs.New: %v", err)
	}
	ns, err := vfs.NewNamespace([]vfs.Mount{{Name: mount, FS: fs}})
	if err != nil {
		t.Fatalf("vfs.NewNamespace: %v", err)
	}
	t.Cleanup(func() { stopTestVFS(t, ns) })
	ns.Start(ctx)
	c := newTestCore(t, ns)
	c.offlinePollInterval = 5 * time.Millisecond
	return c
}

func cloudOnlyRequest(source, parseTo string) task.OperationRequest {
	return task.OperationRequest{
		Operation: task.OperationDownload,
		Offline:   &task.OfflineOperationRequest{Source: source, ParseTo: parseTo},
	}
}

func TestOfflineDownloadLifecycle(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	provider := newFakeOfflineProvider()
	provider.files = []drive.OfflineFile{{Name: "ep1.mkv", Size: 3, RemoteID: "0/downloads/ep1.mkv"}}
	provider.states = []drive.OfflineState{drive.OfflineStateQueued, drive.OfflineStateRunning, drive.OfflineStateSucceeded}
	if err := provider.Seed(map[string]string{"downloads/": "", "downloads/ep1.mkv": "abc"}); err != nil {
		t.Fatal(err)
	}
	c := newOfflineCore(t, provider, "fake")

	created, err := c.CreateOperation(ctx, cloudOnlyRequest("magnet:?xt=urn:btih:abc", "/fake/downloads"))
	if err != nil {
		t.Fatalf("CreateOperation: %v", err)
	}
	if created.Type != task.TypeOfflineDownload {
		t.Fatalf("type = %q, want offline_download", created.Type)
	}
	done := waitCoreTask(t, c, created.ID)
	if done.State != task.StateSucceeded {
		t.Fatalf("task state = %s, want succeeded (%v)", done.State, done.Error)
	}
	detail, ok := decodeOfflineDetail(done.Detail)
	if !ok {
		t.Fatal("task has no offline detail")
	}
	if detail.ProviderID != "ptask-1" {
		t.Fatalf("provider id = %q, want ptask-1", detail.ProviderID)
	}
	if detail.Observation == nil || detail.Observation.State != string(drive.OfflineStateSucceeded) {
		t.Fatalf("observation = %+v", detail.Observation)
	}
	if !detail.Observation.DestinationVerified {
		t.Fatal("destination should be verified when the entry exists")
	}
	if len(done.Tracking.Items) != 1 || !strings.HasSuffix(done.Tracking.Items[0].Path, "/downloads/ep1.mkv") {
		t.Fatalf("result items = %+v", done.Tracking.Items)
	}
	requests := provider.createdRequests()
	if len(requests) != 1 {
		t.Fatalf("provider create calls = %d, want 1", len(requests))
	}
	if requests[0].Source != "magnet:?xt=urn:btih:abc" || requests[0].Destination != "/downloads" {
		t.Fatalf("create request = %+v", requests[0])
	}

	// A terminal task does not satisfy de-duplication: a new submission makes a
	// new task.
	again, err := c.CreateOperation(ctx, cloudOnlyRequest("magnet:?xt=urn:btih:abc", "/fake/downloads"))
	if err != nil {
		t.Fatalf("second CreateOperation: %v", err)
	}
	if again.ID == created.ID {
		t.Fatal("a terminal task must not be reused")
	}
}

// logOfflineDedupState reports what active de-duplication sees: the offline
// tasks the lookup would list, plus the de-duplication fingerprint of each task
// involved. It runs only on the failure path, so a rare flake names its cause
// instead of just the outcome.
func logOfflineDedupState(t *testing.T, c *Core, ids ...string) {
	t.Helper()
	ctx := context.Background()
	listed, err := c.ListTasks(ctx, task.Filter{Types: []task.Type{task.TypeOfflineDownload, task.TypeOfflineFetch}})
	if err != nil {
		t.Logf("de-dup ListTasks: %v", err)
	} else {
		t.Logf("de-dup ListTasks sees %d offline task(s):", len(listed))
		for _, item := range listed {
			t.Logf("  listed id=%q type=%s state=%s terminal=%v", item.ID, item.Type, item.State, offlineTaskTerminal(item.State))
		}
	}
	for _, id := range ids {
		item, err := c.GetTask(ctx, id)
		if err != nil {
			t.Logf("  task %q: GetTask: %v", id, err)
			continue
		}
		detail, ok := decodeOfflineDetail(item.Detail)
		if !ok {
			t.Logf("  task %q: state=%s detail undecodable (%d entries)", id, item.State, len(item.Detail))
			continue
		}
		t.Logf("  task %q: state=%s terminal=%v fingerprint=%q source=%q kind=%q identity=%q parse_mount=%q parse_dir=%q selection=%+v",
			id, item.State, offlineTaskTerminal(item.State), offlineFingerprint(detail),
			detail.Source, detail.SourceKind, detail.SourceIdentity, detail.ParseMount, detail.ParseDir, detail.Selection)
	}
}

func TestOfflineActiveDeduplicationAndNew(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	provider := newFakeOfflineProvider()
	// A never-terminal script keeps the first task active.
	provider.states = []drive.OfflineState{drive.OfflineStateQueued, drive.OfflineStateRunning}
	if err := provider.Seed(map[string]string{"downloads/": ""}); err != nil {
		t.Fatal(err)
	}
	c := newOfflineCore(t, provider, "fake")

	first, err := c.CreateOperation(ctx, cloudOnlyRequest("magnet:?xt=urn:btih:abc", "/fake/downloads"))
	if err != nil {
		t.Fatalf("first create: %v", err)
	}
	waitCoreTaskUntil(t, c, first.ID, func(item task.Task) bool { return item.State == task.StateRunning })

	second, err := c.CreateOperation(ctx, cloudOnlyRequest("magnet:?xt=urn:btih:abc", "/fake/downloads"))
	if err != nil {
		t.Fatalf("second create: %v", err)
	}
	if second.ID != first.ID {
		logOfflineDedupState(t, c, first.ID, second.ID)
		t.Fatalf("active de-duplication returned %q, want %q", second.ID, first.ID)
	}
	if requests := provider.createdRequests(); len(requests) != 1 {
		logOfflineDedupState(t, c, first.ID, second.ID)
		t.Fatalf("de-duplicated submission created %d provider tasks: %+v", len(requests), requests)
	}

	req := cloudOnlyRequest("magnet:?xt=urn:btih:abc", "/fake/downloads")
	req.Offline.New = true
	third, err := c.CreateOperation(ctx, req)
	if err != nil {
		t.Fatalf("--new create: %v", err)
	}
	if third.ID == first.ID {
		logOfflineDedupState(t, c, first.ID, third.ID)
		t.Fatal("--new must bypass de-duplication")
	}

	_ = c.CancelTask(ctx, first.ID)
	_ = c.CancelTask(ctx, third.ID)
}

func TestOfflineFetchLocalTransferAndCleanup(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	download := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("hello fetch"))
	}))
	defer download.Close()

	provider := newFakeOfflineProvider()
	provider.downloadURL = download.URL
	provider.files = []drive.OfflineFile{{Name: "out.bin", Size: 11, RemoteID: "0/downloads/out.bin"}}
	provider.states = []drive.OfflineState{drive.OfflineStateQueued, drive.OfflineStateSucceeded}
	if err := provider.Seed(map[string]string{"downloads/": "", "downloads/out.bin": "remote-bytes"}); err != nil {
		t.Fatal(err)
	}
	c := newOfflineCore(t, provider, "fake")

	destination := filepath.Join(t.TempDir(), "downloaded.bin")
	created, err := c.CreateOperation(ctx, task.OperationRequest{
		Operation: task.OperationDownload,
		Offline: &task.OfflineOperationRequest{
			Source:     "magnet:?xt=urn:btih:abc",
			ParseTo:    "/fake/downloads",
			DownloadTo: destination,
		},
	})
	if err != nil {
		t.Fatalf("CreateOperation: %v", err)
	}
	if created.Type != task.TypeOfflineFetch {
		t.Fatalf("type = %q, want offline_fetch", created.Type)
	}
	done := waitCoreTask(t, c, created.ID)
	if done.State != task.StateSucceeded {
		t.Fatalf("fetch state = %s, want succeeded (%v)", done.State, done.Error)
	}
	data, err := os.ReadFile(destination)
	if err != nil {
		t.Fatalf("read destination: %v", err)
	}
	// The staged file is read through the Core's VFS reader (the driver's tuned
	// download path), so the destination holds the staged bytes.
	if string(data) != "remote-bytes" {
		t.Fatalf("destination = %q, want remote-bytes", data)
	}
	// The staged cloud copy is removed by default.
	if !containsCall(provider.FakeCalls(), "Remove:0/downloads/out.bin") {
		t.Fatalf("cloud copy was not removed: %v", provider.FakeCalls())
	}
}

// TestOfflineFetchIntoDirectory verifies a single-file fetch into a directory
// destination writes the file under that directory by name, instead of trying
// to open the directory as a file.
func TestOfflineFetchIntoDirectory(t *testing.T) {
	t.Parallel()

	download := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("video-bytes"))
	}))
	// Close after the parallel subtests finish, not when the parent returns.
	t.Cleanup(download.Close)

	for _, tc := range []struct {
		name     string
		existing bool
		trailing bool
	}{
		{name: "existing directory", existing: true},
		{name: "trailing separator creates directory", trailing: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()

			provider := newFakeOfflineProvider()
			provider.downloadURL = download.URL
			provider.files = []drive.OfflineFile{{Name: "movie.mp4", Size: int64(len("remote")), RemoteID: "0/downloads/movie.mp4"}}
			provider.states = []drive.OfflineState{drive.OfflineStateQueued, drive.OfflineStateSucceeded}
			if err := provider.Seed(map[string]string{"downloads/": "", "downloads/movie.mp4": "remote"}); err != nil {
				t.Fatal(err)
			}
			c := newOfflineCore(t, provider, "fake")

			root := t.TempDir()
			destination := filepath.Join(root, "out")
			if tc.existing {
				if err := os.MkdirAll(destination, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			if tc.trailing {
				destination += string(os.PathSeparator)
			}

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
			if done.State != task.StateSucceeded {
				t.Fatalf("state = %s (%v)", done.State, done.Error)
			}
			data, err := os.ReadFile(filepath.Join(root, "out", "movie.mp4"))
			if err != nil || string(data) != "remote" {
				t.Fatalf("written file = %q err=%v", data, err)
			}
		})
	}
}

func TestOfflineFetchKeepCloud(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	download := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("keep me"))
	}))
	defer download.Close()

	provider := newFakeOfflineProvider()
	provider.downloadURL = download.URL
	provider.files = []drive.OfflineFile{{Name: "keep.bin", Size: 7, RemoteID: "0/downloads/keep.bin"}}
	provider.states = []drive.OfflineState{drive.OfflineStateQueued, drive.OfflineStateSucceeded}
	if err := provider.Seed(map[string]string{"downloads/": "", "downloads/keep.bin": "remote"}); err != nil {
		t.Fatal(err)
	}
	c := newOfflineCore(t, provider, "fake")

	created, err := c.CreateOperation(ctx, task.OperationRequest{
		Operation: task.OperationDownload,
		Offline: &task.OfflineOperationRequest{
			Source:     "magnet:?xt=urn:btih:abc",
			ParseTo:    "/fake/downloads",
			DownloadTo: filepath.Join(t.TempDir(), "keep.bin"),
			KeepCloud:  true,
		},
	})
	if err != nil {
		t.Fatalf("CreateOperation: %v", err)
	}
	waitCoreTask(t, c, created.ID)
	if containsCall(provider.FakeCalls(), "Remove:0/downloads/keep.bin") {
		t.Fatal("--keep-cloud must not remove the staged copy")
	}
}

func TestOfflineCancelAndRetry(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	provider := newFakeOfflineProvider()
	provider.states = []drive.OfflineState{drive.OfflineStateQueued, drive.OfflineStateRunning}
	if err := provider.Seed(map[string]string{"downloads/": ""}); err != nil {
		t.Fatal(err)
	}
	c := newOfflineCore(t, provider, "fake")

	created, err := c.CreateOperation(ctx, cloudOnlyRequest("magnet:?xt=urn:btih:abc", "/fake/downloads"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	waitCoreTaskUntil(t, c, created.ID, func(item task.Task) bool { return item.State == task.StateRunning })
	// Cancel only once the run has submitted the provider task. The task flips
	// to Running before the run body executes, so cancelling immediately raced
	// the submit: when the cancel wins, the run never creates a provider task
	// and there is legitimately nothing to remove.
	waitCoreCondition(t, func() bool { return len(provider.createdRequests()) == 1 })
	if err := c.CancelTask(ctx, created.ID); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	waitCoreTaskUntil(t, c, created.ID, func(item task.Task) bool { return item.State == task.StateCanceled })
	// The runner removes the provider task from its own goroutine, after the
	// canonical state flips, so wait for that call instead of reading it once
	// (reading it immediately raced the runner under load).
	waitCoreCondition(t, func() bool {
		provider.mu.Lock()
		defer provider.mu.Unlock()
		return len(provider.cancelled) > 0 && provider.cancelled[0] == "ptask-1"
	})

	// Retry re-queues the provider task in place and advances the generation.
	if err := c.RetryTask(ctx, created.ID); err != nil {
		t.Fatalf("retry: %v", err)
	}
	provider.mu.Lock()
	retried := append([]string(nil), provider.retried...)
	provider.mu.Unlock()
	if len(retried) == 0 || retried[0] != "ptask-1" {
		t.Fatalf("provider retries = %v, want in-place ptask-1", retried)
	}
	waitCoreTask(t, c, created.ID)
}

func TestOfflineRetrySucceededRejected(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	provider := newFakeOfflineProvider()
	if err := provider.Seed(map[string]string{"downloads/": "", "downloads/ep1.mkv": "abc"}); err != nil {
		t.Fatal(err)
	}
	provider.files = []drive.OfflineFile{{Name: "ep1.mkv", Size: 3, RemoteID: "0/downloads/ep1.mkv"}}
	c := newOfflineCore(t, provider, "fake")

	created, err := c.CreateOperation(ctx, cloudOnlyRequest("magnet:?xt=urn:btih:abc", "/fake/downloads"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	waitCoreTask(t, c, created.ID)
	if err := c.RetryTask(ctx, created.ID); err == nil {
		t.Fatal("retrying a succeeded task must be rejected")
	}
}

func TestOfflineInspectCreatesNoTask(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	provider := newFakeOfflineProvider()
	provider.preview = drive.OfflinePreview{
		Title:       "Example",
		TotalSize:   200,
		Files:       []drive.OfflineFile{{Name: "a.mkv", Size: 150}, {Name: "b.mkv", Size: 50}},
		InstantSave: true,
	}
	if err := provider.Seed(map[string]string{"downloads/": ""}); err != nil {
		t.Fatal(err)
	}
	c := newOfflineCore(t, provider, "fake")

	preview, err := c.InspectOffline(ctx, task.OfflineOperationRequest{Source: "magnet:?xt=urn:btih:abc", ParseTo: "/fake/downloads"})
	if err != nil {
		t.Fatalf("InspectOffline: %v", err)
	}
	if len(preview.Files) != 2 || preview.TotalSize != 200 {
		t.Fatalf("preview = %+v", preview)
	}
	if len(provider.createdRequests()) != 0 {
		t.Fatal("inspection must not create a provider task")
	}
	tasks, err := c.ListOfflineTasks(ctx, task.Filter{})
	if err != nil {
		t.Fatalf("ListOfflineTasks: %v", err)
	}
	if len(tasks) != 0 {
		t.Fatalf("inspection created %d tasks, want 0", len(tasks))
	}
}

func TestOfflineWaitTimeoutLeavesTaskRunning(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	provider := newFakeOfflineProvider()
	provider.states = []drive.OfflineState{drive.OfflineStateQueued, drive.OfflineStateRunning}
	if err := provider.Seed(map[string]string{"downloads/": ""}); err != nil {
		t.Fatal(err)
	}
	c := newOfflineCore(t, provider, "fake")

	created, err := c.CreateOperation(ctx, cloudOnlyRequest("magnet:?xt=urn:btih:abc", "/fake/downloads"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	_, err = c.WaitOfflineTask(ctx, created.ID, 40*time.Millisecond)
	var offline *drive.OfflineError
	if !errors.As(err, &offline) || offline.Code != drive.OfflineErrWaitTimeout {
		t.Fatalf("wait error = %v, want offline_wait_timeout", err)
	}
	current, err := c.GetTask(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.State != task.StateRunning {
		t.Fatalf("wait timeout must leave the task running, got %s", current.State)
	}
	_ = c.CancelTask(ctx, created.ID)
}

func TestOfflineCreationRejections(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	provider := newFakeOfflineProvider()
	provider.selection = false
	if err := provider.Seed(map[string]string{"downloads/": ""}); err != nil {
		t.Fatal(err)
	}
	c := newOfflineCore(t, provider, "fake")

	cases := []struct {
		name  string
		req   task.OperationRequest
		check func(error) bool
	}{
		{
			name:  "unknown mount",
			req:   cloudOnlyRequest("magnet:?xt=urn:btih:abc", "/nope/downloads"),
			check: func(err error) bool { return errors.Is(err, ErrOfflineMountNotFound) },
		},
		{
			name:  "missing parse directory",
			req:   cloudOnlyRequest("magnet:?xt=urn:btih:abc", "/fake/nope"),
			check: func(err error) bool { return errors.Is(err, ErrOfflineParseDestinationMissing) },
		},
		{
			name: "unsupported selection",
			req: task.OperationRequest{
				Operation: task.OperationDownload,
				Offline: &task.OfflineOperationRequest{
					Source:    "magnet:?xt=urn:btih:abc",
					ParseTo:   "/fake/downloads",
					Selection: task.OfflineSelection{Mode: task.OfflineSelectionLargest, Largest: 1},
				},
			},
			check: func(err error) bool { return errors.Is(err, ErrOfflineUnsupported) },
		},
		{
			name: "same destination",
			req: task.OperationRequest{
				Operation: task.OperationDownload,
				Offline: &task.OfflineOperationRequest{
					Source:     "magnet:?xt=urn:btih:abc",
					ParseTo:    "/fake/downloads",
					DownloadTo: "/fake/downloads/.",
				},
			},
			check: func(err error) bool { return errors.Is(err, ErrOfflineSameDestination) },
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := c.CreateOperation(ctx, tc.req)
			if err == nil {
				t.Fatalf("%s: expected rejection", tc.name)
			}
			if !tc.check(err) {
				t.Fatalf("%s: error = %v", tc.name, err)
			}
		})
	}
	// None of the rejections created a provider task.
	if len(provider.createdRequests()) != 0 {
		t.Fatalf("rejections created %d provider tasks", len(provider.createdRequests()))
	}
}

func TestOfflineEncryptedParseDestinationRejected(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	provider := newFakeOfflineProvider()
	if err := provider.Seed(map[string]string{"downloads/": ""}); err != nil {
		t.Fatal(err)
	}
	c := newOfflineCore(t, provider, "fake", vfs.Options{Encrypted: true})

	_, err := c.CreateOperation(ctx, cloudOnlyRequest("magnet:?xt=urn:btih:abc", "/fake/downloads"))
	if !errors.Is(err, ErrOfflineEncryptedParseDestination) {
		t.Fatalf("error = %v, want encrypted-parse-destination rejection", err)
	}
}

func TestOfflineMissingParseDirectoryMkdir(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	provider := newFakeOfflineProvider()
	provider.states = []drive.OfflineState{drive.OfflineStateQueued, drive.OfflineStateRunning}
	if err := provider.Seed(map[string]string{"downloads/": ""}); err != nil {
		t.Fatal(err)
	}
	c := newOfflineCore(t, provider, "fake")

	req := cloudOnlyRequest("magnet:?xt=urn:btih:abc", "/fake/newdir")
	req.Offline.Mkdir = true
	created, err := c.CreateOperation(ctx, req)
	if err != nil {
		t.Fatalf("create with --mkdir: %v", err)
	}
	if created.ID == "" {
		t.Fatal("expected a task")
	}
	// The created provider directory id is captured at creation and handed to
	// the provider, so the adapter never re-resolves a just-created directory
	// through an eventually consistent listing.
	detail, ok := decodeOfflineDetail(created.Detail)
	if !ok || detail.ParseRemoteID == "" {
		t.Fatalf("parse destination id was not captured: %+v", detail)
	}
	waitCoreTaskUntil(t, c, created.ID, func(item task.Task) bool { return item.State == task.StateRunning })
	deadline := time.Now().Add(2 * time.Second)
	for len(provider.createdRequests()) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	requests := provider.createdRequests()
	if len(requests) != 1 || requests[0].DestinationID != detail.ParseRemoteID {
		t.Fatalf("provider request did not carry the resolved destination id: %+v", requests)
	}
	_ = c.CancelTask(ctx, created.ID)
}

func TestOfflineRunActiveGuard(t *testing.T) {
	t.Parallel()

	c := &Core{}
	if !c.beginOfflineRun("t1") {
		t.Fatal("first registration should succeed")
	}
	if c.beginOfflineRun("t1") {
		t.Fatal("second registration for the same task must fail")
	}
	c.endOfflineRun("t1")
	if !c.beginOfflineRun("t1") {
		t.Fatal("registration after end should succeed")
	}
	c.endOfflineRun("t1")
}

func TestOfflineRecoverable(t *testing.T) {
	t.Parallel()

	if !offlineRecoverable(task.StateFailed, &task.Error{Code: "interrupted"}) {
		t.Fatal("an interrupted failed task must be recoverable")
	}
	if offlineRecoverable(task.StateSucceeded, nil) {
		t.Fatal("a succeeded task must not be recoverable")
	}
	if !offlineRecoverable(task.StateRunning, nil) {
		t.Fatal("a running task must be recoverable")
	}
}

// TestOfflineFetchAccelerated verifies the accelerated path obtains a download
// URL before the provider task completes, using the fid reported at submit.
func TestOfflineFetchAccelerated(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	download := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("accelerated"))
	}))
	defer download.Close()

	provider := newFakeOfflineProvider()
	provider.accelerated = true
	provider.issueSession = true
	provider.downloadURL = download.URL
	provider.createFID = "0/downloads/accel.bin"
	provider.available = true
	provider.filesEveryState = true
	provider.files = []drive.OfflineFile{{Name: "accel.bin", Size: 11, RemoteID: "0/downloads/accel.bin"}}
	// A never-terminal script proves the transfer does not wait for success
	// once the provider reports the staged results are available.
	provider.states = []drive.OfflineState{drive.OfflineStateQueued, drive.OfflineStateRunning}
	if err := provider.Seed(map[string]string{"downloads/": "", "downloads/accel.bin": "remote"}); err != nil {
		t.Fatal(err)
	}
	c := newOfflineCore(t, provider, "fake")

	destination := filepath.Join(t.TempDir(), "accel.bin")
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
	data, err := os.ReadFile(destination)
	if err != nil || string(data) != "accelerated" {
		t.Fatalf("destination = %q err=%v", data, err)
	}
	provider.mu.Lock()
	requests := append([]drive.OfflineFetchRequest(nil), provider.urlRequests...)
	provider.mu.Unlock()
	accelerated := false
	for _, request := range requests {
		if request.Accelerated {
			accelerated = true
		}
	}
	if !accelerated {
		t.Fatalf("expected an accelerated download URL request, got %+v", requests)
	}
}

// TestOfflineFetchAcceleratedFallback verifies a provider that declares
// accelerated fetch but issues no session still completes through the normal
// download URL after the task finishes.
func TestOfflineFetchAcceleratedFallback(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	download := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("normal"))
	}))
	defer download.Close()

	provider := newFakeOfflineProvider()
	provider.accelerated = true
	provider.issueSession = false
	provider.downloadURL = download.URL
	provider.files = []drive.OfflineFile{{Name: "fallback.bin", Size: 6, RemoteID: "0/downloads/fallback.bin"}}
	provider.states = []drive.OfflineState{drive.OfflineStateQueued, drive.OfflineStateSucceeded}
	// The staged entry is not readable through the VFS here, so the transfer
	// must fall back to the provider download URL.
	if err := provider.Seed(map[string]string{"downloads/": ""}); err != nil {
		t.Fatal(err)
	}
	c := newOfflineCore(t, provider, "fake")

	destination := filepath.Join(t.TempDir(), "fallback.bin")
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
		t.Fatalf("state = %s (%v), want success via normal URL", done.State, done.Error)
	}
	data, err := os.ReadFile(destination)
	if err != nil || string(data) != "normal" {
		t.Fatalf("destination = %q err=%v", data, err)
	}
}

func TestOfflineFetchRefreshesDownloadURL(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	var calls int32
	download := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte("refreshed"))
	}))
	defer download.Close()

	provider := newFakeOfflineProvider()
	provider.downloadURL = download.URL
	provider.files = []drive.OfflineFile{{Name: "refresh.bin", Size: 9, RemoteID: "0/downloads/refresh.bin"}}
	provider.states = []drive.OfflineState{drive.OfflineStateQueued, drive.OfflineStateSucceeded}
	// The staged entry is not readable through the VFS here, so the transfer
	// must use the (first failing, then refreshed) provider download URL.
	if err := provider.Seed(map[string]string{"downloads/": ""}); err != nil {
		t.Fatal(err)
	}
	c := newOfflineCore(t, provider, "fake")

	destination := filepath.Join(t.TempDir(), "refresh.bin")
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
	data, err := os.ReadFile(destination)
	if err != nil || string(data) != "refreshed" {
		t.Fatalf("destination = %q err=%v", data, err)
	}
	provider.mu.Lock()
	requests := len(provider.urlRequests)
	provider.mu.Unlock()
	if requests < 2 {
		t.Fatalf("expected a refreshed download URL, got %d requests", requests)
	}
}

// TestOfflineRestartRecovery resumes a persisted non-terminal offline task from
// its journaled provider identifier without re-submitting the source.
func TestOfflineRestartRecovery(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	provider := newFakeOfflineProvider()
	provider.files = []drive.OfflineFile{{Name: "ep1.mkv", Size: 3, RemoteID: "0/downloads/ep1.mkv"}}
	if err := provider.Seed(map[string]string{"downloads/": "", "downloads/ep1.mkv": "abc"}); err != nil {
		t.Fatal(err)
	}
	provider.mu.Lock()
	provider.observations["ptask-1"] = []drive.OfflineDownload{
		{ID: "ptask-1", State: drive.OfflineStateRunning},
		{ID: "ptask-1", State: drive.OfflineStateSucceeded, Files: provider.files},
	}
	provider.mu.Unlock()

	stateDir := t.TempDir()
	detail := offlineDetail{
		Source:         "magnet:?xt=urn:btih:abc",
		SourceKind:     "magnet",
		SourceIdentity: "magnet:btih:abc",
		ParseMount:     "fake",
		ParseDir:       "/downloads",
		Selection:      offlineDetailSelection{Mode: "all"},
		ProviderID:     "ptask-1",
		Phase:          offlinePhaseStaging,
	}
	taskBefore := task.Task{
		ID:           "offline-restart",
		Type:         task.TypeOfflineDownload,
		Operation:    task.OperationDownload,
		State:        task.StateRunning,
		Capabilities: task.Capabilities{Persistent: true, Cancelable: true},
		Detail:       map[string]any{task.OfflineDetailKey: detail},
	}
	writeJournalEntry(t, stateDir, taskBefore)

	ctx2, cancel := context.WithCancel(ctx)
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
	ns.Start(ctx2)
	c := &Core{fs: ns, runtimeLayout: RuntimeLayout{StateDir: stateDir}, offlinePollInterval: 5 * time.Millisecond}
	t.Cleanup(func() { _ = c.Close(context.Background()) })

	// Building the task manager replays the journal and runs recovery.
	if _, err := c.taskManager(); err != nil {
		t.Fatal(err)
	}
	done := waitCoreTask(t, c, "offline-restart")
	if done.State != task.StateSucceeded {
		t.Fatalf("recovered task state = %s (%v)", done.State, done.Error)
	}
	if requests := provider.createdRequests(); len(requests) != 0 {
		t.Fatalf("recovery re-submitted the source: %+v", requests)
	}
	recovered, ok := decodeOfflineDetail(done.Detail)
	if !ok || recovered.ProviderID != "ptask-1" {
		t.Fatalf("recovered detail = %+v", recovered)
	}
}

// writeJournalEntry writes a single persisted task record as the task journal
// replay would read it.
func writeJournalEntry(t *testing.T, stateDir string, item task.Task) {
	t.Helper()
	dir := filepath.Join(stateDir, "tasks")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	entry := map[string]any{"version": 1, "op": "update", "id": item.ID, "task": item}
	data, err := json.Marshal(entry)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tasks.jsonl"), append(data, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestOfflineMissingTaskReconciliation covers the disappeared-provider-task
// fallback: success when the destination entry can still be resolved, and a
// typed failure once the bounded window elapses without one.
func TestOfflineMissingTaskReconciliation(t *testing.T) {
	t.Parallel()

	t.Run("destination resolves", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()

		provider := newFakeOfflineProvider()
		provider.files = []drive.OfflineFile{{Name: "gone.bin", Size: 4, RemoteID: "0/downloads/gone.bin"}}
		provider.filesEveryState = true
		provider.repeatLast = false
		provider.states = []drive.OfflineState{drive.OfflineStateQueued, drive.OfflineStateRunning}
		if err := provider.Seed(map[string]string{"downloads/": "", "downloads/gone.bin": "data"}); err != nil {
			t.Fatal(err)
		}
		c := newOfflineCore(t, provider, "fake")
		c.offlineMissingWindow = 20 * time.Millisecond

		created, err := c.CreateOperation(ctx, cloudOnlyRequest("magnet:?xt=urn:btih:abc", "/fake/downloads"))
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		done := waitCoreTask(t, c, created.ID)
		if done.State != task.StateSucceeded {
			t.Fatalf("state = %s (%v), want succeeded via destination resolution", done.State, done.Error)
		}
	})

	t.Run("no destination entry fails", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()

		provider := newFakeOfflineProvider()
		provider.repeatLast = false
		provider.states = []drive.OfflineState{drive.OfflineStateQueued}
		if err := provider.Seed(map[string]string{"downloads/": ""}); err != nil {
			t.Fatal(err)
		}
		c := newOfflineCore(t, provider, "fake")
		c.offlineMissingWindow = 20 * time.Millisecond

		created, err := c.CreateOperation(ctx, cloudOnlyRequest("magnet:?xt=urn:btih:abc", "/fake/downloads"))
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		done := waitCoreTask(t, c, created.ID)
		if done.State != task.StateFailed {
			t.Fatalf("state = %s, want failed", done.State)
		}
		if done.Error == nil || done.Error.Code != string(drive.OfflineErrProviderTaskMissing) {
			t.Fatalf("error = %+v, want offline_provider_task_missing", done.Error)
		}
	})
}

// TestOfflineTaskAPISurface verifies offline tasks participate in the existing
// task snapshot, filter, and event contracts (task-api spec).
func TestOfflineTaskAPISurface(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	provider := newFakeOfflineProvider()
	provider.files = []drive.OfflineFile{{Name: "ep1.mkv", Size: 3, RemoteID: "0/downloads/ep1.mkv"}}
	provider.states = []drive.OfflineState{drive.OfflineStateQueued, drive.OfflineStateRunning, drive.OfflineStateSucceeded}
	if err := provider.Seed(map[string]string{"downloads/": "", "downloads/ep1.mkv": "abc"}); err != nil {
		t.Fatal(err)
	}
	c := newOfflineCore(t, provider, "fake")

	manager, err := c.taskManager()
	if err != nil {
		t.Fatal(err)
	}
	sub := manager.Subscribe(task.Filter{Types: []task.Type{task.TypeOfflineDownload, task.TypeOfflineFetch}})
	defer sub.Close()

	created, err := c.CreateOperation(ctx, cloudOnlyRequest("magnet:?xt=urn:btih:abc", "/fake/downloads"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	done := waitCoreTask(t, c, created.ID)

	listed, err := c.ListTasks(ctx, task.Filter{Types: []task.Type{task.TypeOfflineDownload}})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].ID != created.ID {
		t.Fatalf("filtered list = %+v", listed)
	}
	items, err := c.ListTaskItems(ctx, created.ID, task.ItemFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].State != task.ItemStateSucceeded {
		t.Fatalf("items = %+v", items)
	}
	if done.State != task.StateSucceeded {
		t.Fatalf("state = %s", done.State)
	}

	events, err := sub.ReadAvailable()
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	sawTask := false
	var last task.State
	for _, event := range events {
		if event.TaskID == created.ID && event.Task != nil {
			sawTask = true
			last = event.Task.State
		}
	}
	if !sawTask {
		t.Fatalf("no offline task event observed: %d events", len(events))
	}
	if last != done.State {
		t.Fatalf("last event state %s != snapshot state %s", last, done.State)
	}
}

func containsCall(calls []string, want string) bool {
	for _, call := range calls {
		if strings.HasPrefix(call, want) {
			return true
		}
	}
	return false
}

// List hides the staged result files for the first hideResultsFor listings of
// their parent directory, emulating a provider whose listing lags its own task.
func (p *fakeOfflineProvider) List(ctx context.Context, parentID string) ([]drive.Entry, error) {
	entries, err := p.FakeDriver.List(ctx, parentID)
	if err != nil || p.hideResultsFor <= 0 || !p.resultsParent(parentID) {
		return entries, err
	}
	p.listMu.Lock()
	p.resultsListCalls++
	visible := p.resultsListCalls > p.hideResultsFor
	p.listMu.Unlock()
	if visible {
		return entries, nil
	}
	hidden := map[string]struct{}{}
	for _, file := range p.files {
		if file.Name != "" {
			hidden[file.Name] = struct{}{}
		}
	}
	out := make([]drive.Entry, 0, len(entries))
	for _, entry := range entries {
		if _, ok := hidden[entry.Name]; ok {
			continue
		}
		out = append(out, entry)
	}
	return out, nil
}

// resultsParent reports whether parentID is the directory holding the scripted
// result files (their remote ids are parent-prefixed).
func (p *fakeOfflineProvider) resultsParent(parentID string) bool {
	for _, file := range p.files {
		if file.RemoteID == "" {
			continue
		}
		if idx := strings.LastIndex(file.RemoteID, "/"); idx > 0 && file.RemoteID[:idx] == parentID {
			return true
		}
	}
	return false
}

// TestOfflineVerifyDestinationRetriesLaggingListing verifies the destination is
// re-checked while the provider's listing catches up, so a finished cloud task
// is not recorded as unverified just because the directory was stale.
func TestOfflineVerifyDestinationRetriesLaggingListing(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	provider := newFakeOfflineProvider()
	provider.files = []drive.OfflineFile{{Name: "lag.bin", Size: 3, RemoteID: "0/downloads/lag.bin"}}
	provider.states = []drive.OfflineState{drive.OfflineStateQueued, drive.OfflineStateSucceeded}
	// The listing hides the file for the first two reads, so only a retrying
	// verifier can see it.
	provider.hideResultsFor = 2
	if err := provider.Seed(map[string]string{"downloads/": "", "downloads/lag.bin": "abc"}); err != nil {
		t.Fatal(err)
	}
	c := newOfflineCore(t, provider, "fake")

	created, err := c.CreateOperation(ctx, cloudOnlyRequest("magnet:?xt=urn:btih:abc", "/fake/downloads"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	done := waitCoreTask(t, c, created.ID)
	if done.State != task.StateSucceeded {
		t.Fatalf("state = %s (%v)", done.State, done.Error)
	}
	detail := offlineDetailFromTask(t, done)
	if detail.Observation == nil || !detail.Observation.DestinationVerified {
		t.Fatalf("observation = %+v, want a verified destination after the listing caught up", detail.Observation)
	}
}

// TestOfflineVerifyDestinationUnverifiedWhenListingNeverCatchesUp pins the
// other side: a listing that never shows the reported files leaves the
// observation unverified instead of failing the task.
func TestOfflineVerifyDestinationUnverifiedWhenListingNeverCatchesUp(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	provider := newFakeOfflineProvider()
	provider.files = []drive.OfflineFile{{Name: "never.bin", Size: 3, RemoteID: "0/downloads/never.bin"}}
	provider.states = []drive.OfflineState{drive.OfflineStateQueued, drive.OfflineStateSucceeded}
	provider.hideResultsFor = offlineVerifyAttempts + 5
	if err := provider.Seed(map[string]string{"downloads/": "", "downloads/never.bin": "abc"}); err != nil {
		t.Fatal(err)
	}
	c := newOfflineCore(t, provider, "fake")

	created, err := c.CreateOperation(ctx, cloudOnlyRequest("magnet:?xt=urn:btih:abc", "/fake/downloads"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	done := waitCoreTask(t, c, created.ID)
	if done.State != task.StateSucceeded {
		t.Fatalf("state = %s (%v), want success with an unverified destination", done.State, done.Error)
	}
	detail := offlineDetailFromTask(t, done)
	if detail.Observation != nil && detail.Observation.DestinationVerified {
		t.Fatalf("observation = %+v, want an unverified destination", detail.Observation)
	}
}

// TestOfflineFetchAcceleratedObservesFilesFirst covers a provider that reports
// the staged files only from its task endpoint (as Quark does): the fetch must
// observe once before asking for an accelerated URL, so the transfer can start
// while the provider task is still running.
func TestOfflineFetchAcceleratedObservesFilesFirst(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	download := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("accelerated"))
	}))
	defer download.Close()

	provider := newFakeOfflineProvider()
	provider.accelerated = true
	provider.issueSession = true
	provider.downloadURL = download.URL
	provider.files = []drive.OfflineFile{{Name: "accel2.bin", Size: 11, RemoteID: "0/downloads/accel2.bin"}}
	provider.available = true
	// The files are reported from the first observation, and the task never
	// reaches a terminal state: only a pre-terminal accelerated URL can finish.
	provider.filesEveryState = true
	provider.states = []drive.OfflineState{drive.OfflineStateQueued, drive.OfflineStateRunning}
	if err := provider.Seed(map[string]string{"downloads/": ""}); err != nil {
		t.Fatal(err)
	}
	c := newOfflineCore(t, provider, "fake")

	destination := filepath.Join(t.TempDir(), "accel2.bin")
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
	data, err := os.ReadFile(destination)
	if err != nil || string(data) != "accelerated" {
		t.Fatalf("destination = %q err=%v", data, err)
	}
	provider.mu.Lock()
	requests := append([]drive.OfflineFetchRequest(nil), provider.urlRequests...)
	provider.mu.Unlock()
	if len(requests) == 0 {
		t.Fatal("no download URL was requested")
	}
	if !requests[0].Accelerated {
		t.Fatalf("first URL request = %+v, want the accelerated path", requests[0])
	}
	if len(requests[0].Files) != 1 || requests[0].Files[0].RemoteID != "0/downloads/accel2.bin" {
		t.Fatalf("accelerated URL files = %+v, want the file observed from the task", requests[0].Files)
	}
}

// offlineDetailFromTask decodes the offline detail a finished task carries.
func offlineDetailFromTask(t *testing.T, item task.Task) offlineDetail {
	t.Helper()
	raw, err := json.Marshal(item.Detail[task.OfflineDetailKey])
	if err != nil {
		t.Fatal(err)
	}
	var detail offlineDetail
	if err := json.Unmarshal(raw, &detail); err != nil {
		t.Fatal(err)
	}
	return detail
}
