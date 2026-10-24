package core

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
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

// fakeReceiveProvider is an in-memory provider that implements the receive half
// of the share contract: link parsing, opening, browsing, and a provider-side
// save with configurable outcomes.
type fakeReceiveProvider struct {
	*drive.FakeDriver

	mu     sync.Mutex
	caps   []drive.Capability
	policy drive.SharePolicy

	entries []drive.ShareEntry

	openCalls int
	lastLink  drive.ShareLink
	listCalls int
	saveCalls []drive.ShareSaveRequest
	getCalls  int
	// getRunning keeps GetShareSave in the running state for N calls, emulating
	// a save that is still in flight when a process restarts.
	getRunning int
	getErr     error
	listErr    error

	lastSave []drive.ShareEntry
	dest     drive.Entry

	// handle, when non-empty, makes SaveShareEntries return a running outcome
	// that GetShareSave resolves later.
	handle string
	// outcome, when set, replaces the default "save everything" behaviour.
	outcome func(req drive.ShareSaveRequest) (drive.ShareSaveOutcome, error)
	openErr error
	saveErr error
	// saveBlock, when non-nil, parks SaveShareEntries until ctx is done or the
	// channel is closed.
	saveBlock chan struct{}
	// downloadURL is handed out by ShareDownloadTargets for every entry.
	downloadURL string
	// deleted records provider removals (cleanup assertions).
	deleted []string
	// linkPrefix is the URL prefix this fixture provider recognizes.
	linkPrefix string
	// parseDisabled makes the provider look like a different backend that does
	// not recognize the fixture link.
	parseDisabled bool
	// hideLists makes the first N destination listings omit the saved entries,
	// emulating eventual consistency.
	hideLists   int
	verifyCalls int
	savedNames  map[string]struct{}
}

func newFakeReceiveProvider() *fakeReceiveProvider {
	return &fakeReceiveProvider{
		FakeDriver: drive.NewFakeDriver(),
		caps: []drive.Capability{
			drive.CapabilityWriter,
			drive.CapabilityShareReceive,
			drive.CapabilityShareSave,
			drive.CapabilityShareFetch,
		},
		policy: drive.SharePolicy{
			Passcode:  drive.PolicyOptional,
			Expiries:  drive.NeutralShareExpiries(),
			List:      true,
			Update:    true,
			Selection: true,
		},
	}
}

func (p *fakeReceiveProvider) Capabilities() []drive.Capability { return p.caps }
func (p *fakeReceiveProvider) SharePolicy() drive.SharePolicy   { return p.policy }

// ParseShareLink recognizes https://fake.test/s/<id>.
func (p *fakeReceiveProvider) ParseShareLink(raw string) (drive.ShareLink, bool) {
	if p.parseDisabled {
		return drive.ShareLink{}, false
	}
	prefix := p.linkPrefix
	if prefix == "" {
		prefix = "https://fake.test/s/"
	}
	if !strings.HasPrefix(raw, prefix) {
		return drive.ShareLink{}, false
	}
	id := strings.TrimPrefix(raw, prefix)
	if id == "" || strings.Contains(id, "/") {
		return drive.ShareLink{}, false
	}
	return drive.ShareLink{URL: raw, LinkID: id}, true
}

func (p *fakeReceiveProvider) OpenShare(ctx context.Context, link drive.ShareLink) (drive.ShareSession, error) {
	p.mu.Lock()
	p.openCalls++
	p.lastLink = link
	p.mu.Unlock()
	if p.openErr != nil {
		return drive.ShareSession{}, p.openErr
	}
	return drive.ShareSession{
		Link:         link,
		SessionToken: "token",
		Share:        drive.Share{NativeID: "native-" + link.LinkID, LinkID: link.LinkID, Title: "fixture"},
	}, nil
}

func (p *fakeReceiveProvider) ListShareEntries(ctx context.Context, session drive.ShareSession, dir drive.SharePath, cursor string) (drive.ShareEntryPage, error) {
	p.mu.Lock()
	p.listCalls++
	entries := append([]drive.ShareEntry(nil), p.entries...)
	listErr := p.listErr
	p.mu.Unlock()
	if listErr != nil {
		return drive.ShareEntryPage{}, listErr
	}
	return drive.ShareEntryPage{Entries: entries}, nil
}

func (p *fakeReceiveProvider) SaveShareEntries(ctx context.Context, req drive.ShareSaveRequest) (drive.ShareSaveOutcome, error) {
	p.mu.Lock()
	p.saveCalls = append(p.saveCalls, req)
	p.lastSave = append([]drive.ShareEntry(nil), req.Entries...)
	p.dest = req.Dest.Entry
	handle := p.handle
	outcome := p.outcome
	saveErr := p.saveErr
	p.mu.Unlock()

	if saveErr != nil {
		return drive.ShareSaveOutcome{}, saveErr
	}
	if p.saveBlock != nil {
		select {
		case <-ctx.Done():
			return drive.ShareSaveOutcome{}, ctx.Err()
		case <-p.saveBlock:
		}
	}
	if outcome != nil {
		return outcome(req)
	}
	if handle != "" {
		return drive.ShareSaveOutcome{Handle: handle, State: drive.SaveStateRunning}, nil
	}
	return p.successOutcome(req.Dest.Entry, req.Entries), nil
}

func (p *fakeReceiveProvider) Remove(ctx context.Context, entry drive.Entry) error {
	p.mu.Lock()
	p.deleted = append(p.deleted, entry.ID)
	p.mu.Unlock()
	return p.FakeDriver.Remove(ctx, entry)
}

func (p *fakeReceiveProvider) ShareDownloadTargets(ctx context.Context, req drive.ShareFetchRequest) ([]drive.DownloadTarget, error) {
	targets := make([]drive.DownloadTarget, 0, len(req.Entries))
	for _, entry := range req.Entries {
		targets = append(targets, drive.DownloadTarget{URL: p.downloadURL, Size: entry.Size})
	}
	return targets, nil
}

func (p *fakeReceiveProvider) GetShareSave(ctx context.Context, handle string, entries []drive.ShareEntry) (drive.ShareSaveOutcome, error) {
	p.mu.Lock()
	p.getCalls++
	running := p.getRunning
	if running > 0 {
		p.getRunning--
	}
	getErr := p.getErr
	dest, saved := p.dest, append([]drive.ShareEntry(nil), p.lastSave...)
	if len(saved) == 0 {
		saved = append([]drive.ShareEntry(nil), entries...)
	}
	p.mu.Unlock()
	if getErr != nil {
		return drive.ShareSaveOutcome{}, getErr
	}
	if handle == "" {
		return drive.ShareSaveOutcome{}, drive.NewShareError(drive.ShareErrNotFound, "", "no handle")
	}
	if running > 0 {
		return drive.ShareSaveOutcome{Handle: handle, State: drive.SaveStateRunning}, nil
	}
	return p.successOutcome(dest, saved), nil
}

// successOutcome seeds the saved entries into the fake tree and reports success.
func (p *fakeReceiveProvider) successOutcome(dest drive.Entry, entries []drive.ShareEntry) drive.ShareSaveOutcome {
	items := make([]drive.ShareSaveItem, 0, len(entries))
	for _, entry := range entries {
		p.seed(dest, entry.Name)
		p.recordSaved(entry.Name)
		items = append(items, drive.ShareSaveItem{
			SourceID:   entry.ID,
			SourceName: entry.Name,
			SourceSize: entry.Size,
			Saved:      &drive.Entry{ID: dest.ID + "/" + entry.Name, Name: entry.Name, Size: entry.Size, ParentID: dest.ID},
		})
	}
	return drive.ShareSaveOutcome{State: drive.SaveStateSucceeded, Progress: 1, Items: items}
}

// List emulates an eventually consistent destination: the first hideLists calls
// omit entries the save already created.
func (p *fakeReceiveProvider) List(ctx context.Context, parentID string) ([]drive.Entry, error) {
	entries, err := p.FakeDriver.List(ctx, parentID)
	p.mu.Lock()
	p.verifyCalls++
	hide := p.verifyCalls <= p.hideLists
	hidden := p.savedNames
	p.mu.Unlock()
	if err != nil || !hide || len(hidden) == 0 {
		return entries, err
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

func (p *fakeReceiveProvider) recordSaved(name string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.savedNames == nil {
		p.savedNames = map[string]struct{}{}
	}
	p.savedNames[name] = struct{}{}
}

func (p *fakeReceiveProvider) seed(dest drive.Entry, name string) {
	path := strings.TrimPrefix(dest.ID, "0/")
	if path == "0" {
		path = ""
	}
	if path != "" {
		path += "/"
	}
	_ = p.Seed(map[string]string{path + name: "saved"})
}

// partialOutcome saves the first entry and rejects the rest.
func (p *fakeReceiveProvider) partialOutcome(req drive.ShareSaveRequest) (drive.ShareSaveOutcome, error) {
	items := make([]drive.ShareSaveItem, 0, len(req.Entries))
	for i, entry := range req.Entries {
		if i == 0 {
			p.seed(req.Dest.Entry, entry.Name)
			p.recordSaved(entry.Name)
			items = append(items, drive.ShareSaveItem{
				SourceID: entry.ID, SourceName: entry.Name, SourceSize: entry.Size,
				Saved: &drive.Entry{ID: req.Dest.Entry.ID + "/" + entry.Name, Name: entry.Name, ParentID: req.Dest.Entry.ID},
			})
			continue
		}
		items = append(items, drive.ShareSaveItem{
			SourceID: entry.ID, SourceName: entry.Name, SourceSize: entry.Size,
			Error: drive.NewShareError(drive.ShareErrQuota, "32003", "no space"),
		})
	}
	return drive.ShareSaveOutcome{State: drive.SaveStatePartial, Items: items}, nil
}

func newReceiveCore(t *testing.T, specs map[string]*fakeReceiveProvider, encrypted map[string]bool) *Core {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	mounts := make([]vfs.Mount, 0, len(specs))
	for _, name := range sortedMountNames(specs) {
		fs, err := vfs.New(specs[name], vfs.Options{
			Name:       name,
			StorageDir: filepath.Join(t.TempDir(), name+"-cache"),
			Encrypted:  encrypted[name],
		})
		if err != nil {
			t.Fatalf("vfs.New: %v", err)
		}
		mounts = append(mounts, vfs.Mount{Name: name, FS: fs})
	}
	ns, err := vfs.NewNamespace(mounts)
	if err != nil {
		t.Fatalf("vfs.NewNamespace: %v", err)
	}
	t.Cleanup(func() { stopTestVFS(t, ns) })
	ns.Start(ctx)
	c := newTestCore(t, ns)
	c.sharePollInterval = time.Millisecond
	c.shareVerifyInterval = time.Millisecond
	return c
}

func sortedMountNames(specs map[string]*fakeReceiveProvider) []string {
	names := make([]string, 0, len(specs))
	for name := range specs {
		names = append(names, name)
	}
	for i := 1; i < len(names); i++ {
		for j := i; j > 0 && names[j] < names[j-1]; j-- {
			names[j], names[j-1] = names[j-1], names[j]
		}
	}
	return names
}

func createShareSave(t *testing.T, c *Core, link, to string, selection task.OfflineSelection) task.Task {
	t.Helper()
	created, err := c.CreateTask(context.Background(), task.Request{
		Type:  task.TypeShareSave,
		Share: &task.ShareOperationRequest{Link: link, To: to, Selection: selection},
	})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	return created
}

func seedReceiveFixture(provider *fakeReceiveProvider) {
	provider.entries = []drive.ShareEntry{
		{Entry: drive.Entry{ID: "f1", Name: "small.bin", Size: 10}, Ticket: "t1"},
		{Entry: drive.Entry{ID: "f2", Name: "large.bin", Size: 50}, Ticket: "t2"},
	}
}

func TestShareSaveLifecycle(t *testing.T) {
	t.Parallel()

	provider := newFakeReceiveProvider()
	if err := provider.Seed(map[string]string{"pics/": "", "pics/a.jpg": "x", "dest/": ""}); err != nil {
		t.Fatal(err)
	}
	seedReceiveFixture(provider)
	c := newReceiveCore(t, map[string]*fakeReceiveProvider{"fake": provider}, nil)

	created := createShareSave(t, c, "https://fake.test/s/abc", "/fake/dest", task.OfflineSelection{})
	if created.Type != task.TypeShareSave || created.Operation != task.OperationShare {
		t.Fatalf("created task = %s/%s", created.Type, created.Operation)
	}
	done := waitCoreTask(t, c, created.ID)
	if done.State != task.StateSucceeded {
		t.Fatalf("state = %s (%v)", done.State, done.Error)
	}
	if len(provider.saveCalls) != 1 || len(provider.saveCalls[0].Entries) != 2 {
		t.Fatalf("save calls = %+v", provider.saveCalls)
	}
	if provider.saveCalls[0].Dest.Entry.ID == "" {
		t.Fatal("destination entry was not resolved")
	}
	detail, ok := decodeShareDetail(done.Detail)
	if !ok {
		t.Fatal("task has no share detail")
	}
	if detail.Observation == nil || !detail.Observation.DestinationVerified {
		t.Fatalf("observation = %+v, want verified", detail.Observation)
	}
	if len(detail.Saved) != 2 || detail.Saved[0].RemoteID == "" {
		t.Fatalf("saved entries = %+v", detail.Saved)
	}
	if len(done.Tracking.Items) != 2 {
		t.Fatalf("tracking items = %d, want 2", len(done.Tracking.Items))
	}
	for _, item := range done.Tracking.Items {
		if item.State != task.ItemStateSucceeded {
			t.Fatalf("item %s state = %s", item.ItemID, item.State)
		}
	}
}

func TestShareSaveAsyncHandle(t *testing.T) {
	t.Parallel()

	provider := newFakeReceiveProvider()
	if err := provider.Seed(map[string]string{"dest/": ""}); err != nil {
		t.Fatal(err)
	}
	seedReceiveFixture(provider)
	provider.handle = "save-1"
	c := newReceiveCore(t, map[string]*fakeReceiveProvider{"fake": provider}, nil)

	created := createShareSave(t, c, "https://fake.test/s/abc", "/fake/dest", task.OfflineSelection{})
	done := waitCoreTask(t, c, created.ID)
	if done.State != task.StateSucceeded {
		t.Fatalf("state = %s (%v)", done.State, done.Error)
	}
	if provider.getCalls == 0 {
		t.Fatal("provider save handle was not polled")
	}
	detail, _ := decodeShareDetail(done.Detail)
	if detail.SaveHandle != "save-1" {
		t.Fatalf("save handle = %q, want save-1", detail.SaveHandle)
	}
}

func TestShareSavePartialOutcome(t *testing.T) {
	t.Parallel()

	provider := newFakeReceiveProvider()
	if err := provider.Seed(map[string]string{"dest/": ""}); err != nil {
		t.Fatal(err)
	}
	seedReceiveFixture(provider)
	provider.outcome = provider.partialOutcome
	c := newReceiveCore(t, map[string]*fakeReceiveProvider{"fake": provider}, nil)

	created := createShareSave(t, c, "https://fake.test/s/abc", "/fake/dest", task.OfflineSelection{})
	done := waitCoreTask(t, c, created.ID)
	if done.State != task.StatePartialFailed {
		t.Fatalf("state = %s (%v), want partial_failed", done.State, done.Error)
	}
	if len(done.Tracking.Items) != 2 {
		t.Fatalf("tracking items = %d", len(done.Tracking.Items))
	}
	failed := 0
	for _, item := range done.Tracking.Items {
		if item.State == task.ItemStateFailed {
			failed++
			if item.Error == nil || item.Error.Code != string(drive.ShareErrQuota) {
				t.Fatalf("failed item error = %+v", item.Error)
			}
		}
	}
	if failed != 1 {
		t.Fatalf("failed items = %d, want 1", failed)
	}
	detail, _ := decodeShareDetail(done.Detail)
	if len(detail.Saved) != 2 || detail.Saved[1].ErrorCode != string(drive.ShareErrQuota) {
		t.Fatalf("saved detail = %+v", detail.Saved)
	}
}

func TestShareSaveUnverifiedDestination(t *testing.T) {
	t.Parallel()

	provider := newFakeReceiveProvider()
	if err := provider.Seed(map[string]string{"dest/": ""}); err != nil {
		t.Fatal(err)
	}
	seedReceiveFixture(provider)
	provider.outcome = func(req drive.ShareSaveRequest) (drive.ShareSaveOutcome, error) {
		items := make([]drive.ShareSaveItem, 0, len(req.Entries))
		for _, entry := range req.Entries {
			items = append(items, drive.ShareSaveItem{
				SourceID: entry.ID, SourceName: entry.Name,
				Saved: &drive.Entry{Name: entry.Name, ParentID: req.Dest.Entry.ID},
			})
		}
		return drive.ShareSaveOutcome{State: drive.SaveStateSucceeded, Items: items}, nil
	}
	c := newReceiveCore(t, map[string]*fakeReceiveProvider{"fake": provider}, nil)

	created := createShareSave(t, c, "https://fake.test/s/abc", "/fake/dest", task.OfflineSelection{})
	done := waitCoreTask(t, c, created.ID)
	if done.State != task.StateFailed {
		t.Fatalf("state = %s, want failed", done.State)
	}
	if done.Error == nil || done.Error.Code != string(drive.ShareErrDestinationUnverified) {
		t.Fatalf("error = %+v, want share_destination_unverified", done.Error)
	}
}

func TestShareSaveLaggingListingStillVerifies(t *testing.T) {
	t.Parallel()

	provider := newFakeReceiveProvider()
	if err := provider.Seed(map[string]string{"dest/": ""}); err != nil {
		t.Fatal(err)
	}
	seedReceiveFixture(provider)
	provider.hideLists = 2
	c := newReceiveCore(t, map[string]*fakeReceiveProvider{"fake": provider}, nil)

	created := createShareSave(t, c, "https://fake.test/s/abc", "/fake/dest", task.OfflineSelection{})
	done := waitCoreTask(t, c, created.ID)
	if done.State != task.StateSucceeded {
		t.Fatalf("state = %s (%v), want succeeded", done.State, done.Error)
	}
	if provider.verifyCalls <= provider.hideLists {
		t.Fatalf("verification did not retry: calls = %d", provider.verifyCalls)
	}
}

func TestShareSaveSelection(t *testing.T) {
	t.Parallel()

	provider := newFakeReceiveProvider()
	if err := provider.Seed(map[string]string{"dest/": ""}); err != nil {
		t.Fatal(err)
	}
	seedReceiveFixture(provider)
	c := newReceiveCore(t, map[string]*fakeReceiveProvider{"fake": provider}, nil)

	created := createShareSave(t, c, "https://fake.test/s/abc", "/fake/dest", task.OfflineSelection{
		Mode: task.OfflineSelectionLargest, Largest: 1,
	})
	done := waitCoreTask(t, c, created.ID)
	if done.State != task.StateSucceeded {
		t.Fatalf("state = %s (%v)", done.State, done.Error)
	}
	if len(provider.saveCalls) != 1 || len(provider.saveCalls[0].Entries) != 1 {
		t.Fatalf("save calls = %+v", provider.saveCalls)
	}
	if provider.saveCalls[0].Entries[0].Name != "large.bin" {
		t.Fatalf("selected %q, want large.bin", provider.saveCalls[0].Entries[0].Name)
	}
}

func TestShareSaveMkdirDestination(t *testing.T) {
	t.Parallel()

	provider := newFakeReceiveProvider()
	if err := provider.Seed(map[string]string{"dest/": ""}); err != nil {
		t.Fatal(err)
	}
	seedReceiveFixture(provider)
	c := newReceiveCore(t, map[string]*fakeReceiveProvider{"fake": provider}, nil)

	if _, err := c.CreateTask(context.Background(), task.Request{
		Type:  task.TypeShareSave,
		Share: &task.ShareOperationRequest{Link: "https://fake.test/s/abc", To: "/fake/missing"},
	}); !errors.Is(err, ErrShareDestinationMissing) {
		t.Fatalf("error = %v, want ErrShareDestinationMissing", err)
	}

	created, err := c.CreateTask(context.Background(), task.Request{
		Type:  task.TypeShareSave,
		Share: &task.ShareOperationRequest{Link: "https://fake.test/s/abc", To: "/fake/missing", Mkdir: true},
	})
	if err != nil {
		t.Fatalf("CreateTask with --mkdir: %v", err)
	}
	done := waitCoreTask(t, c, created.ID)
	if done.State != task.StateSucceeded {
		t.Fatalf("state = %s (%v)", done.State, done.Error)
	}
	detail, _ := decodeShareDetail(done.Detail)
	if detail.ToDir != "/missing" {
		t.Fatalf("to dir = %q", detail.ToDir)
	}
}

func TestShareSaveCreatesThroughOperationSurface(t *testing.T) {
	t.Parallel()

	provider := newFakeReceiveProvider()
	if err := provider.Seed(map[string]string{"dest/": ""}); err != nil {
		t.Fatal(err)
	}
	seedReceiveFixture(provider)
	c := newReceiveCore(t, map[string]*fakeReceiveProvider{"fake": provider}, nil)

	created, err := c.CreateOperation(context.Background(), task.OperationRequest{
		Operation: task.OperationShare,
		Share:     &task.ShareOperationRequest{Link: "https://fake.test/s/abc", To: "/fake/dest"},
	})
	if err != nil {
		t.Fatalf("CreateOperation: %v", err)
	}
	if created.Type != task.TypeShareSave || created.Operation != task.OperationShare {
		t.Fatalf("created = %s/%s", created.Type, created.Operation)
	}
	done := waitCoreTask(t, c, created.ID)
	if done.State != task.StateSucceeded {
		t.Fatalf("state = %s (%v)", done.State, done.Error)
	}
}

func TestShareFetchCreatesThroughOperationSurface(t *testing.T) {
	t.Parallel()

	provider := newFakeReceiveProvider()
	if err := provider.Seed(map[string]string{"dest/": ""}); err != nil {
		t.Fatal(err)
	}
	seedReceiveFixture(provider)
	c := newReceiveCore(t, map[string]*fakeReceiveProvider{"fake": provider}, nil)

	created, err := c.CreateOperation(context.Background(), task.OperationRequest{
		Operation: task.OperationDownload,
		Share: &task.ShareOperationRequest{
			Link: "https://fake.test/s/abc", To: "/fake/dest", DownloadTo: t.TempDir(),
		},
	})
	if err != nil {
		t.Fatalf("CreateOperation: %v", err)
	}
	if created.Type != task.TypeShareFetch || created.Operation != task.OperationDownload {
		t.Fatalf("created = %s/%s", created.Type, created.Operation)
	}
	_ = c.CancelTask(context.Background(), created.ID)
}

func TestShareSaveRejectsCrossProviderDestination(t *testing.T) {
	t.Parallel()

	local := newFakeReceiveProvider()
	other := newFakeReceiveProvider()
	other.parseDisabled = true
	if err := local.Seed(map[string]string{"dest/": ""}); err != nil {
		t.Fatal(err)
	}
	if err := other.Seed(map[string]string{"dest/": ""}); err != nil {
		t.Fatal(err)
	}
	c := newReceiveCore(t, map[string]*fakeReceiveProvider{"alpha": local, "beta": other}, nil)

	_, err := c.CreateTask(context.Background(), task.Request{
		Type:  task.TypeShareSave,
		Share: &task.ShareOperationRequest{Link: "https://fake.test/s/abc", To: "/beta/dest"},
	})
	if !errors.Is(err, ErrShareCrossProviderUnsupported) {
		t.Fatalf("error = %v, want ErrShareCrossProviderUnsupported", err)
	}
	if len(local.saveCalls) != 0 || len(other.saveCalls) != 0 {
		t.Fatal("a provider was called for a rejected cross-provider request")
	}
}

func TestShareSaveRejectsAmbiguousMount(t *testing.T) {
	t.Parallel()

	first, second := newFakeReceiveProvider(), newFakeReceiveProvider()
	c := newReceiveCore(t, map[string]*fakeReceiveProvider{"alpha": first, "beta": second}, nil)

	_, err := c.CreateTask(context.Background(), task.Request{
		Type:  task.TypeShareSave,
		Share: &task.ShareOperationRequest{Link: "https://fake.test/s/abc"},
	})
	if !errors.Is(err, ErrShareMountRequired) {
		t.Fatalf("error = %v, want ErrShareMountRequired", err)
	}
	if !strings.Contains(err.Error(), "--mount") {
		t.Fatalf("ambiguous error must tell the caller how to choose: %v", err)
	}
}

func TestShareSaveRejectsEncryptedDestination(t *testing.T) {
	t.Parallel()

	provider := newFakeReceiveProvider()
	if err := provider.Seed(map[string]string{"dest/": ""}); err != nil {
		t.Fatal(err)
	}
	c := newReceiveCore(t, map[string]*fakeReceiveProvider{"vault": provider}, map[string]bool{"vault": true})

	_, err := c.CreateTask(context.Background(), task.Request{
		Type:  task.TypeShareSave,
		Share: &task.ShareOperationRequest{Link: "https://fake.test/s/abc", To: "/vault/dest"},
	})
	if !errors.Is(err, ErrShareEncryptedDestination) {
		t.Fatalf("error = %v, want ErrShareEncryptedDestination", err)
	}
	if len(provider.saveCalls) != 0 {
		t.Fatal("encrypted destination reached the provider")
	}
}

func TestShareSaveRejectsMountMismatch(t *testing.T) {
	t.Parallel()

	alpha, beta := newFakeReceiveProvider(), newFakeReceiveProvider()
	beta.parseDisabled = true
	if err := alpha.Seed(map[string]string{"dest/": ""}); err != nil {
		t.Fatal(err)
	}
	c := newReceiveCore(t, map[string]*fakeReceiveProvider{"alpha": alpha, "beta": beta}, nil)

	_, err := c.CreateTask(context.Background(), task.Request{
		Type:  task.TypeShareSave,
		Share: &task.ShareOperationRequest{Link: "https://fake.test/s/abc", Mount: "beta"},
	})
	if !errors.Is(err, ErrShareLinkProviderMismatch) {
		t.Fatalf("error = %v, want ErrShareLinkProviderMismatch", err)
	}
}

func TestShareSaveRejectsUnsupportedMount(t *testing.T) {
	t.Parallel()

	provider := newFakeReceiveProvider()
	provider.caps = []drive.Capability{drive.CapabilityWriter}
	if err := provider.Seed(map[string]string{"dest/": ""}); err != nil {
		t.Fatal(err)
	}
	c := newReceiveCore(t, map[string]*fakeReceiveProvider{"fake": provider}, nil)

	_, err := c.CreateTask(context.Background(), task.Request{
		Type:  task.TypeShareSave,
		Share: &task.ShareOperationRequest{Link: "https://fake.test/s/abc", To: "/fake/dest"},
	})
	if !errors.Is(err, ErrShareUnsupported) {
		t.Fatalf("error = %v, want ErrShareUnsupported", err)
	}
	if len(provider.saveCalls) != 0 {
		t.Fatal("unsupported provider was called")
	}
}

func TestShareSaveRejectsUnknownLink(t *testing.T) {
	t.Parallel()

	provider := newFakeReceiveProvider()
	c := newReceiveCore(t, map[string]*fakeReceiveProvider{"fake": provider}, nil)

	_, err := c.CreateTask(context.Background(), task.Request{
		Type:  task.TypeShareSave,
		Share: &task.ShareOperationRequest{Link: "https://elsewhere.test/s/abc", To: "/fake/dest"},
	})
	if !errors.Is(err, ErrShareLinkInvalid) {
		t.Fatalf("error = %v, want ErrShareLinkInvalid", err)
	}
}

func TestShareSaveProviderErrorIsClassified(t *testing.T) {
	t.Parallel()

	provider := newFakeReceiveProvider()
	if err := provider.Seed(map[string]string{"dest/": ""}); err != nil {
		t.Fatal(err)
	}
	seedReceiveFixture(provider)
	provider.saveErr = drive.NewShareError(drive.ShareErrOwnSave, "41017", "own share")
	c := newReceiveCore(t, map[string]*fakeReceiveProvider{"fake": provider}, nil)

	created := createShareSave(t, c, "https://fake.test/s/abc", "/fake/dest", task.OfflineSelection{})
	done := waitCoreTask(t, c, created.ID)
	if done.State != task.StateFailed {
		t.Fatalf("state = %s", done.State)
	}
	if done.Error == nil || done.Error.Code != string(drive.ShareErrOwnSave) {
		t.Fatalf("error = %+v, want share_own_save", done.Error)
	}
}

func waitForShare(t *testing.T, what string, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func waitShareState(t *testing.T, c *Core, id string, want task.State) task.Task {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	var last task.Task
	for time.Now().Before(deadline) {
		current, err := c.GetTask(context.Background(), id)
		if err == nil {
			last = current
			if current.State == want {
				return current
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("task %s state = %s, want %s", id, last.State, want)
	return last
}

func TestShareSaveCancelStopsObservation(t *testing.T) {
	t.Parallel()

	provider := newFakeReceiveProvider()
	if err := provider.Seed(map[string]string{"dest/": ""}); err != nil {
		t.Fatal(err)
	}
	seedReceiveFixture(provider)
	provider.saveBlock = make(chan struct{})
	c := newReceiveCore(t, map[string]*fakeReceiveProvider{"fake": provider}, nil)

	created := createShareSave(t, c, "https://fake.test/s/abc", "/fake/dest", task.OfflineSelection{})
	waitForShare(t, "the provider save to start", func() bool {
		provider.mu.Lock()
		defer provider.mu.Unlock()
		return len(provider.saveCalls) == 1
	})
	if err := c.CancelTask(context.Background(), created.ID); err != nil {
		t.Fatalf("CancelTask: %v", err)
	}
	done := waitCoreTask(t, c, created.ID)
	if done.State != task.StateCanceled {
		t.Fatalf("state = %s, want canceled", done.State)
	}
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if len(provider.saveCalls) != 1 {
		t.Fatalf("save calls = %d, want 1 (no retry after cancel)", len(provider.saveCalls))
	}
}

func TestShareSaveRetryAfterProviderFailure(t *testing.T) {
	t.Parallel()

	provider := newFakeReceiveProvider()
	if err := provider.Seed(map[string]string{"dest/": ""}); err != nil {
		t.Fatal(err)
	}
	seedReceiveFixture(provider)
	provider.saveErr = drive.NewShareError(drive.ShareErrRateLimited, "", "slow down")
	c := newReceiveCore(t, map[string]*fakeReceiveProvider{"fake": provider}, nil)

	created := createShareSave(t, c, "https://fake.test/s/abc", "/fake/dest", task.OfflineSelection{})
	failed := waitCoreTask(t, c, created.ID)
	if failed.State != task.StateFailed || failed.Error == nil || failed.Error.Code != string(drive.ShareErrRateLimited) {
		t.Fatalf("failed = %s/%+v", failed.State, failed.Error)
	}
	if !failed.Capabilities.Retryable {
		t.Fatal("a rate-limited receive must be user-retryable")
	}
	generation := failed.ExecutionGeneration

	provider.mu.Lock()
	provider.saveErr = nil
	provider.mu.Unlock()
	if err := c.RetryTask(context.Background(), created.ID); err != nil {
		t.Fatalf("RetryTask: %v", err)
	}
	done := waitShareState(t, c, created.ID, task.StateSucceeded)
	if done.ExecutionGeneration <= generation {
		t.Fatalf("generation = %d, want > %d", done.ExecutionGeneration, generation)
	}
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if len(provider.saveCalls) != 2 {
		t.Fatalf("save calls = %d, want 2", len(provider.saveCalls))
	}
}

func TestShareSaveRetryAfterUnverifiedDoesNotSaveAgain(t *testing.T) {
	t.Parallel()

	provider := newFakeReceiveProvider()
	if err := provider.Seed(map[string]string{"dest/": ""}); err != nil {
		t.Fatal(err)
	}
	seedReceiveFixture(provider)
	provider.hideLists = 100
	c := newReceiveCore(t, map[string]*fakeReceiveProvider{"fake": provider}, nil)

	created := createShareSave(t, c, "https://fake.test/s/abc", "/fake/dest", task.OfflineSelection{})
	failed := waitCoreTask(t, c, created.ID)
	if failed.State != task.StateFailed || failed.Error == nil || failed.Error.Code != string(drive.ShareErrDestinationUnverified) {
		t.Fatalf("failed = %s/%+v", failed.State, failed.Error)
	}

	provider.mu.Lock()
	provider.hideLists = 0
	provider.mu.Unlock()
	if err := c.RetryTask(context.Background(), created.ID); err != nil {
		t.Fatalf("RetryTask: %v", err)
	}
	done := waitShareState(t, c, created.ID, task.StateSucceeded)
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if len(provider.saveCalls) != 1 {
		t.Fatalf("save calls = %d, want 1 (a completed save must not repeat)", len(provider.saveCalls))
	}
	if done.Tracking.Items == nil {
		t.Fatal("retried task lost its item projection")
	}
}

func TestRecoverShareTaskResumesVerification(t *testing.T) {
	t.Parallel()

	provider := newFakeReceiveProvider()
	if err := provider.Seed(map[string]string{"dest/": ""}); err != nil {
		t.Fatal(err)
	}
	seedReceiveFixture(provider)
	provider.hideLists = 100
	c := newReceiveCore(t, map[string]*fakeReceiveProvider{"fake": provider}, nil)

	created := createShareSave(t, c, "https://fake.test/s/abc", "/fake/dest", task.OfflineSelection{})
	failed := waitCoreTask(t, c, created.ID)
	if failed.State != task.StateFailed {
		t.Fatalf("state = %s", failed.State)
	}

	manager, err := c.taskManager()
	if err != nil {
		t.Fatal(err)
	}
	if _, updated := manager.UpdateTask(created.ID, func(item *task.Task) {
		item.State = task.StateFailed
		item.Error = &task.Error{Code: "interrupted", Message: "crashed mid-verify"}
	}); !updated {
		t.Fatal("could not simulate an interrupted task")
	}
	provider.mu.Lock()
	provider.hideLists = 0
	provider.mu.Unlock()

	c.recoverShareSaveTasks(context.Background(), manager)
	done := waitShareState(t, c, created.ID, task.StateSucceeded)
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if len(provider.saveCalls) != 1 {
		t.Fatalf("save calls = %d, want 1", len(provider.saveCalls))
	}
	if done.Tracking.Items == nil {
		t.Fatal("recovered task lost its item projection")
	}
}

func TestInspectShareLink(t *testing.T) {
	t.Parallel()

	provider := newFakeReceiveProvider()
	seedReceiveFixture(provider)
	c := newReceiveCore(t, map[string]*fakeReceiveProvider{"fake": provider}, nil)

	result, err := c.InspectShareLink(context.Background(), ShareInspectRequest{Link: "https://fake.test/s/abc"})
	if err != nil {
		t.Fatalf("InspectShareLink: %v", err)
	}
	if result.Share.Title != "fixture" || len(result.Entries) != 2 {
		t.Fatalf("result = %+v", result)
	}
	if result.Link.LinkID != "abc" {
		t.Fatalf("link id = %q", result.Link.LinkID)
	}
	manager, err := c.taskManager()
	if err != nil {
		t.Fatal(err)
	}
	tasks, err := manager.ListTasks(context.Background(), task.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 0 {
		t.Fatalf("inspect created %d tasks, want 0", len(tasks))
	}
}

func TestInspectShareLinkEmptyRootIsGone(t *testing.T) {
	t.Parallel()

	provider := newFakeReceiveProvider()
	c := newReceiveCore(t, map[string]*fakeReceiveProvider{"fake": provider}, nil)

	_, err := c.InspectShareLink(context.Background(), ShareInspectRequest{Link: "https://fake.test/s/abc"})
	if !drive.IsShareErrorCode(err, drive.ShareErrNotFound) {
		t.Fatalf("error = %v, want share_not_found", err)
	}
}

func TestInspectShareLinkPasscodeError(t *testing.T) {
	t.Parallel()

	provider := newFakeReceiveProvider()
	provider.openErr = drive.NewShareError(drive.ShareErrPasscodeRequired, "41008", "需要提取码")
	c := newReceiveCore(t, map[string]*fakeReceiveProvider{"fake": provider}, nil)

	_, err := c.InspectShareLink(context.Background(), ShareInspectRequest{Link: "https://fake.test/s/abc"})
	if !drive.IsShareErrorCode(err, drive.ShareErrPasscodeRequired) {
		t.Fatalf("error = %v, want share_passcode_required", err)
	}
}

func TestShareSaveDeduplicatesActiveTask(t *testing.T) {
	t.Parallel()

	provider := newFakeReceiveProvider()
	if err := provider.Seed(map[string]string{"dest/": "", "dest2/": ""}); err != nil {
		t.Fatal(err)
	}
	seedReceiveFixture(provider)
	provider.saveBlock = make(chan struct{})
	c := newReceiveCore(t, map[string]*fakeReceiveProvider{"fake": provider}, nil)

	ctx := context.Background()
	first := createShareSave(t, c, "https://fake.test/s/abc", "/fake/dest", task.OfflineSelection{})
	waitForShare(t, "the first save to start", func() bool {
		provider.mu.Lock()
		defer provider.mu.Unlock()
		return len(provider.saveCalls) == 1
	})
	second := createShareSave(t, c, "https://fake.test/s/abc", "/fake/dest", task.OfflineSelection{})
	if second.ID != first.ID {
		t.Fatalf("duplicate submission created %s, want %s", second.ID, first.ID)
	}

	differentDest := createShareSave(t, c, "https://fake.test/s/abc", "/fake/dest2", task.OfflineSelection{})
	if differentDest.ID == first.ID {
		t.Fatal("a different destination reused the active task")
	}

	forced, err := c.CreateTask(ctx, task.Request{
		Type:  task.TypeShareSave,
		Share: &task.ShareOperationRequest{Link: "https://fake.test/s/abc", To: "/fake/dest", New: true},
	})
	if err != nil {
		t.Fatalf("CreateTask --new: %v", err)
	}
	if forced.ID == first.ID {
		t.Fatal("--new reused the active task")
	}

	keyed, err := c.CreateTask(ctx, task.Request{
		Type:           task.TypeShareSave,
		Share:          &task.ShareOperationRequest{Link: "https://fake.test/s/abc", To: "/fake/dest"},
		IdempotencyKey: "same-key",
	})
	if err != nil {
		t.Fatalf("CreateTask keyed: %v", err)
	}
	keyedAgain, err := c.CreateTask(ctx, task.Request{
		Type:           task.TypeShareSave,
		Share:          &task.ShareOperationRequest{Link: "https://fake.test/s/abc", To: "/fake/dest"},
		IdempotencyKey: "same-key",
	})
	if err != nil {
		t.Fatalf("CreateTask keyed twice: %v", err)
	}
	if keyedAgain.ID != keyed.ID {
		t.Fatalf("keyed submission created %s, want %s", keyedAgain.ID, keyed.ID)
	}
}

func TestShareSaveCompletedTaskAllowsNewSubmission(t *testing.T) {
	t.Parallel()

	provider := newFakeReceiveProvider()
	if err := provider.Seed(map[string]string{"dest/": ""}); err != nil {
		t.Fatal(err)
	}
	seedReceiveFixture(provider)
	c := newReceiveCore(t, map[string]*fakeReceiveProvider{"fake": provider}, nil)

	first := createShareSave(t, c, "https://fake.test/s/abc", "/fake/dest", task.OfflineSelection{})
	waitShareState(t, c, first.ID, task.StateSucceeded)
	second := createShareSave(t, c, "https://fake.test/s/abc", "/fake/dest", task.OfflineSelection{})
	if second.ID == first.ID {
		t.Fatal("a completed receive was reused instead of creating a new task")
	}
}

func createShareFetch(t *testing.T, c *Core, link, to, downloadTo string, workers int, chunk int64) task.Task {
	t.Helper()
	request := task.Request{
		Type: task.TypeShareFetch,
		Share: &task.ShareOperationRequest{
			Link: link, To: to, DownloadTo: downloadTo,
			Workers: workers, ChunkSize: chunk,
		},
	}
	created, err := c.CreateTask(context.Background(), request)
	if err != nil {
		t.Fatalf("CreateTask fetch: %v", err)
	}
	return created
}

func TestShareFetchDirectDownload(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("payload"))
	}))
	defer server.Close()

	provider := newFakeReceiveProvider()
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
	if provider.openCalls == 0 {
		t.Fatal("direct download did not open the share")
	}
}

func TestShareFetchSaveThenDownloadAndCleanup(t *testing.T) {
	t.Parallel()

	provider := newFakeReceiveProvider()
	if err := provider.Seed(map[string]string{"dest/": ""}); err != nil {
		t.Fatal(err)
	}
	seedReceiveFixture(provider)
	c := newReceiveCore(t, map[string]*fakeReceiveProvider{"fake": provider}, nil)

	downloadDir := t.TempDir()
	created := createShareFetch(t, c, "https://fake.test/s/abc", "/fake/dest", downloadDir, 2, 1<<20)
	done := waitCoreTask(t, c, created.ID)
	if done.State != task.StateSucceeded {
		t.Fatalf("state = %s (%v)", done.State, done.Error)
	}
	for _, name := range []string{"small.bin", "large.bin"} {
		data, err := os.ReadFile(filepath.Join(downloadDir, name))
		if err != nil || string(data) != "saved" {
			t.Fatalf("%s = %q err=%v", name, data, err)
		}
	}
	entries, err := provider.List(context.Background(), "0/dest")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("cloud copy was not cleaned up: %+v", entries)
	}
	detail, _ := decodeShareDetail(done.Detail)
	if detail.DownloadMount != "" || detail.DownloadDir != "" {
		t.Fatalf("local destination recorded as namespace: %+v", detail)
	}
	if len(detail.Transferred) != 2 {
		t.Fatalf("transferred = %d, want 2", len(detail.Transferred))
	}
}

func TestShareFetchKeepCloud(t *testing.T) {
	t.Parallel()

	provider := newFakeReceiveProvider()
	if err := provider.Seed(map[string]string{"dest/": ""}); err != nil {
		t.Fatal(err)
	}
	seedReceiveFixture(provider)
	c := newReceiveCore(t, map[string]*fakeReceiveProvider{"fake": provider}, nil)

	downloadDir := t.TempDir()
	created, err := c.CreateTask(context.Background(), task.Request{
		Type: task.TypeShareFetch,
		Share: &task.ShareOperationRequest{
			Link: "https://fake.test/s/abc", To: "/fake/dest", DownloadTo: downloadDir, KeepCloud: true,
		},
	})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	done := waitCoreTask(t, c, created.ID)
	if done.State != task.StateSucceeded {
		t.Fatalf("state = %s (%v)", done.State, done.Error)
	}
	entries, err := provider.List(context.Background(), "0/dest")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("--keep-cloud removed entries: %+v", entries)
	}
}

func TestShareFetchDirectDownloadDeletesNothing(t *testing.T) {
	t.Parallel()

	provider := newFakeReceiveProvider()
	provider.downloadURL = "http://127.0.0.1:1/unused"
	seedReceiveFixture(provider)
	c := newReceiveCore(t, map[string]*fakeReceiveProvider{"fake": provider}, nil)

	// The URL is unreachable, so the fetch fails; a direct download must never
	// remove provider content even so.
	destination := filepath.Join(t.TempDir(), "out.bin")
	created, err := c.CreateTask(context.Background(), task.Request{
		Type: task.TypeShareFetch,
		Share: &task.ShareOperationRequest{
			Link: "https://fake.test/s/abc", DownloadTo: destination,
		},
	})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	done := waitCoreTask(t, c, created.ID)
	if done.State != task.StateFailed {
		t.Fatalf("state = %s, want failed", done.State)
	}
	if len(provider.deleted) != 0 {
		t.Fatalf("a direct download deleted %v", provider.deleted)
	}
}

func TestShareFetchPersistsTuning(t *testing.T) {
	t.Parallel()

	provider := newFakeReceiveProvider()
	if err := provider.Seed(map[string]string{"dest/": ""}); err != nil {
		t.Fatal(err)
	}
	seedReceiveFixture(provider)
	c := newReceiveCore(t, map[string]*fakeReceiveProvider{"fake": provider}, nil)

	downloadDir := t.TempDir()
	created := createShareFetch(t, c, "https://fake.test/s/abc", "/fake/dest", downloadDir, 3, 2<<20)
	done := waitCoreTask(t, c, created.ID)
	if done.State != task.StateSucceeded {
		t.Fatalf("state = %s (%v)", done.State, done.Error)
	}
	detail, _ := decodeShareDetail(done.Detail)
	if detail.Workers != 3 || detail.Chunk != 2<<20 {
		t.Fatalf("tuning = %d/%d, want 3/%d", detail.Workers, detail.Chunk, 2<<20)
	}
}

// TestInspectShareLinkPasscodeRetry pins the explicit passcode path: the first
// attempt reports the neutral required code, and a retry carrying the passcode
// opens the link without any provider text leaking.
func TestInspectShareLinkPasscodeRetry(t *testing.T) {
	t.Parallel()

	provider := newFakeReceiveProvider()
	seedReceiveFixture(provider)
	provider.openErr = drive.NewShareError(drive.ShareErrPasscodeRequired, "41008", "需要提取码")
	c := newReceiveCore(t, map[string]*fakeReceiveProvider{"fake": provider}, nil)

	_, err := c.InspectShareLink(context.Background(), ShareInspectRequest{Link: "https://fake.test/s/abc"})
	if !drive.IsShareErrorCode(err, drive.ShareErrPasscodeRequired) {
		t.Fatalf("error = %v, want share_passcode_required", err)
	}
	if strings.Contains(err.Error(), "需要提取码") {
		t.Fatalf("provider text leaked as the only message: %v", err)
	}

	provider.mu.Lock()
	provider.openErr = nil
	provider.mu.Unlock()
	result, err := c.InspectShareLink(context.Background(), ShareInspectRequest{Link: "https://fake.test/s/abc", Passcode: "ab12"})
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if len(result.Entries) == 0 {
		t.Fatal("retry returned no entries")
	}
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if provider.lastLink.Passcode != "ab12" {
		t.Fatalf("passcode sent to provider = %q", provider.lastLink.Passcode)
	}
}
