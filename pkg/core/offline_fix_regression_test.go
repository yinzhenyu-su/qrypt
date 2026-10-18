package core

import (
	"context"
	"errors"
	"fmt"
	"io"
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

// signalWriter closes landed once a write at offset 16 lands, so a test can
// force a later chunk to finish before an earlier one fails.
type signalWriter struct {
	offlineWriter
	landed chan struct{}
}

func (w signalWriter) WriteAt(p []byte, off int64) (int, error) {
	n, err := w.offlineWriter.WriteAt(p, off)
	if off == 16 {
		close(w.landed)
	}
	return n, err
}

func TestOfflineURLFetchSparseFailureRestartsCleanly(t *testing.T) {
	t.Parallel()

	content := contentBytes(24)
	landed := make(chan struct{})
	var failedFirst bool
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start, end, _ := parseTestRange(r.Header.Get("Range"), 24)
		if start == 0 {
			mu.Lock()
			fail := !failedFirst
			failedFirst = true
			mu.Unlock()
			if fail {
				<-landed
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/24", start, end))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(content[start : end+1])
	}))
	defer server.Close()

	dest := filepath.Join(t.TempDir(), "sparse.bin")
	writer, err := openLocalWriter(dest)
	if err != nil {
		t.Fatal(err)
	}
	fetch := offlineURLFetch{
		client: http.DefaultClient,
		target: drive.OfflineDownloadTarget{URL: server.URL},
		writer: signalWriter{writer, landed},
		plan:   offlineChunkPlan{chunk: 8, workers: 3},
	}
	first, err := fetch.run(context.Background(), 0, 24)
	if err == nil {
		t.Fatal("expected the first chunk to fail")
	}
	if first.written != 0 {
		t.Fatalf("failed attempt reported %d contiguous bytes, want 0", first.written)
	}
	_ = writer.Close()

	// Reopening must not trust the file size: later chunks landed beyond a
	// hole. A fresh attempt has to start at zero and overwrite everything.
	resumed, err := openLocalWriter(dest)
	if err != nil {
		t.Fatal(err)
	}
	defer resumed.Close()
	if resumed.Offset() != 0 {
		t.Fatalf("reopened writer offset = %d, want 0", resumed.Offset())
	}
	fetch.writer = resumed
	second, err := fetch.run(context.Background(), resumed.Offset(), 24)
	if err != nil {
		t.Fatalf("restart: %v", err)
	}
	if !second.done {
		t.Fatalf("restart result = %+v, want done", second)
	}
	data, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(content) {
		t.Fatalf("destination mismatch after restart: %q", data)
	}
}

func TestOfflineURLFetchDoesNotTrustExistingDestination(t *testing.T) {
	t.Parallel()

	content := contentBytes(24)
	server, _ := rangeServer(t, content, -1)
	dest := filepath.Join(t.TempDir(), "existing.bin")
	if err := os.WriteFile(dest, []byte("XXXXXXXXXXXXXXXXXXXXXXXX"), 0o600); err != nil {
		t.Fatal(err)
	}
	writer, err := openLocalWriter(dest)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	fetch := offlineURLFetch{
		client: http.DefaultClient,
		target: drive.OfflineDownloadTarget{URL: server.URL},
		writer: writer,
		plan:   offlineChunkPlan{chunk: 8, workers: 2},
	}
	result, err := fetch.run(context.Background(), writer.Offset(), 24)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !result.done {
		t.Fatalf("result = %+v, want done", result)
	}
	data, _ := os.ReadFile(dest)
	if string(data) != string(content) {
		t.Fatalf("existing destination accepted as complete: %q", data)
	}
}

func TestOfflineURLFetchRejectsPremature416(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Range", "bytes */0")
		w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
	}))
	defer server.Close()

	dest := filepath.Join(t.TempDir(), "short.bin")
	writer, err := openLocalWriter(dest)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	fetch := offlineURLFetch{
		client: http.DefaultClient,
		target: drive.OfflineDownloadTarget{URL: server.URL},
		writer: writer,
		plan:   offlineChunkPlan{chunk: 8, workers: 1},
	}
	result, err := fetch.run(context.Background(), 0, 8)
	if err == nil && result.done {
		t.Fatalf("premature 416 accepted as complete: %+v", result)
	}
}

func TestOfflineURLFetchAccepts416OnlyAtEOF(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Range", "bytes */8")
		w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
	}))
	defer server.Close()

	dest := filepath.Join(t.TempDir(), "eof.bin")
	writer, err := openLocalWriter(dest)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	fetch := offlineURLFetch{
		client: http.DefaultClient,
		target: drive.OfflineDownloadTarget{URL: server.URL},
		writer: writer,
		plan:   offlineChunkPlan{chunk: 8, workers: 1},
	}
	result, err := fetch.runSingleRange(context.Background(), 8, 8, 8)
	if err != nil {
		t.Fatalf("416 at EOF: %v", err)
	}
	if !result.done {
		t.Fatalf("result = %+v, want done", result)
	}
	if _, err := fetch.runSingleRange(context.Background(), 0, 8, 8); err == nil {
		t.Fatal("416 before EOF must fail instead of completing an empty file")
	}
}

func TestOfflineURLFetchRejectsMismatchedPartialRange(t *testing.T) {
	t.Parallel()

	content := contentBytes(24)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Range", "bytes 8-15/24")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(content[8:16])
	}))
	defer server.Close()

	dest := filepath.Join(t.TempDir(), "wrong-range.bin")
	writer, err := openLocalWriter(dest)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	fetch := offlineURLFetch{
		client: http.DefaultClient,
		target: drive.OfflineDownloadTarget{URL: server.URL},
		writer: writer,
		plan:   offlineChunkPlan{chunk: 8, workers: 1},
	}
	if _, err := fetch.run(context.Background(), 0, 24); err == nil {
		t.Fatal("a response for the wrong range must not be written as the requested one")
	}
}

// flushFailFS makes Flush fail, emulating a namespace finalization error.
type flushFailFS struct {
	BuiltFileSystem
	flushed bool
}

func (f *flushFailFS) Capabilities() []vfs.Capability {
	return []vfs.Capability{vfs.CapabilityStreamRead}
}
func (f *flushFailFS) ReadStream(context.Context, string) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("payload")), nil
}
func (f *flushFailFS) RefreshPath(string)                   {}
func (f *flushFailFS) Create(context.Context, string) error { return nil }
func (f *flushFailFS) WriteAt(_ context.Context, _ string, b []byte, _ int64) (int, error) {
	return len(b), nil
}
func (f *flushFailFS) Flush(context.Context, string) error {
	f.flushed = true
	return fmt.Errorf("injected staging fsync failure")
}

func TestOfflineTransferFromStagingPropagatesFinalizeError(t *testing.T) {
	t.Parallel()

	fs := &flushFailFS{}
	run := offlineRun{core: &Core{fs: fs}, detail: offlineDetail{ParseDir: "/staged", DownloadMount: "target"}}
	_, handled, err := run.transferFromStaging(context.Background(), nil, offlineFetchTarget{
		target: "/target/result",
		file:   offlineResultFile{Name: "source", Size: 7},
	}, 0)
	if !handled {
		t.Fatal("staged transfer was not handled")
	}
	if !fs.flushed {
		t.Fatal("transfer did not reach finalization")
	}
	if err == nil {
		t.Fatal("finalization failure was discarded")
	}
}

func TestOfflineShutdownDoesNotCancelProviderTask(t *testing.T) {
	provider := newFakeOfflineProvider()
	provider.states = []drive.OfflineState{drive.OfflineStateRunning}
	if err := provider.Seed(map[string]string{"downloads/": ""}); err != nil {
		t.Fatal(err)
	}
	c := newOfflineCore(t, provider, "fake")
	created, err := c.CreateOperation(context.Background(), cloudOnlyRequest("magnet:?xt=urn:btih:shutdown", "/fake/downloads"))
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		provider.mu.Lock()
		ready := len(provider.created) > 0
		provider.mu.Unlock()
		if ready {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("provider task was not created")
		}
		time.Sleep(time.Millisecond)
	}

	c.tasks.Close()
	got, err := c.GetTask(context.Background(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	provider.mu.Lock()
	cancelled := append([]string(nil), provider.cancelled...)
	provider.mu.Unlock()
	if len(cancelled) != 0 {
		t.Fatalf("shutdown canceled provider task %v", cancelled)
	}
	if got.State == task.StateCanceled {
		t.Fatalf("shutdown reported canceled instead of interrupted: %+v", got)
	}
	if !offlineRecoverable(got.State, got.Error) {
		t.Fatalf("shutdown task is not recoverable: state=%s error=%+v", got.State, got.Error)
	}
}

func TestOfflineProviderCancelMapsToCanonicalCanceled(t *testing.T) {
	t.Parallel()

	provider := newFakeOfflineProvider()
	provider.states = []drive.OfflineState{drive.OfflineStateCanceled}
	if err := provider.Seed(map[string]string{"downloads/": ""}); err != nil {
		t.Fatal(err)
	}
	c := newOfflineCore(t, provider, "fake")
	created, err := c.CreateOperation(context.Background(), cloudOnlyRequest("magnet:?xt=urn:btih:provider-cancel", "/fake/downloads"))
	if err != nil {
		t.Fatal(err)
	}
	done := waitCoreTask(t, c, created.ID)
	if done.State != task.StateCanceled {
		t.Fatalf("state = %s (%+v), want canceled", done.State, done.Error)
	}
	if done.Error == nil || done.Error.Code != string(drive.OfflineStateCanceled) {
		t.Fatalf("error = %+v, want the provider-canceled code", done.Error)
	}
}

func TestOfflineJoinDestinationUsesNativeAndNamespaceRules(t *testing.T) {
	t.Parallel()

	if got, want := joinDestination("./downloads/", "movie.bin"), filepath.Join("downloads", "movie.bin"); got != want {
		t.Fatalf("local join = %q, want %q", got, want)
	}
	if got := joinNamespaceDestination("/fake/dir/", "movie.bin"); got != "/fake/dir/movie.bin" {
		t.Fatalf("namespace join = %q", got)
	}

	fs, err := vfs.New(drive.NewFakeDriver(), vfs.Options{StorageDir: filepath.Join(t.TempDir(), "cache")})
	if err != nil {
		t.Fatal(err)
	}
	namespace, err := vfs.NewNamespace([]vfs.Mount{{Name: "fake", FS: fs}})
	if err != nil {
		t.Fatal(err)
	}
	run := &offlineRun{
		core: &Core{fs: namespace},
		detail: offlineDetail{
			DownloadTo:    "/fake/dir/",
			DownloadMount: "fake",
			DownloadDir:   "/dir",
			ResultFiles: []offlineResultFile{{
				Name: "movie.bin", Size: 7,
			}},
		},
	}
	targets, asDirectory, err := run.fetchTargets(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !asDirectory || len(targets) != 1 || targets[0].target != "/fake/dir/movie.bin" {
		t.Fatalf("namespace targets = %+v asDirectory=%v", targets, asDirectory)
	}

	run.detail = offlineDetail{
		DownloadTo: "downloads/",
		ResultFiles: []offlineResultFile{{
			Name: "movie.bin", Size: 7,
		}},
	}
	targets, asDirectory, err = run.fetchTargets(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !asDirectory || len(targets) != 1 || targets[0].target != filepath.Join("downloads", "movie.bin") {
		t.Fatalf("local targets = %+v asDirectory=%v", targets, asDirectory)
	}
}

func TestOfflineReconcileMissingPreservesResultFiles(t *testing.T) {
	t.Parallel()

	provider := newFakeOfflineProvider()
	provider.repeatLast = false
	provider.filesEveryState = true
	provider.files = []drive.OfflineFile{{Name: "movie.bin", Size: 6, RemoteID: "0/downloads/movie.bin"}}
	provider.states = []drive.OfflineState{drive.OfflineStateRunning}
	if err := provider.Seed(map[string]string{"downloads/": "", "downloads/movie.bin": "remote"}); err != nil {
		t.Fatal(err)
	}
	c := newOfflineCore(t, provider, "fake")
	created, err := c.CreateOperation(context.Background(), cloudOnlyRequest("magnet:?xt=urn:btih:missing", "/fake/downloads"))
	if err != nil {
		t.Fatal(err)
	}
	done := waitCoreTask(t, c, created.ID)
	if done.State != task.StateSucceeded {
		t.Fatalf("state = %s (%+v), want success from the resolved destination", done.State, done.Error)
	}
	detail, ok := decodeOfflineDetail(done.Detail)
	if !ok {
		t.Fatal("missing offline detail")
	}
	if len(detail.ResultFiles) != 1 || detail.ResultFiles[0].RemoteID != "0/downloads/movie.bin" || detail.ResultFiles[0].Size != 6 {
		t.Fatalf("result files = %+v, want the retained identity and size", detail.ResultFiles)
	}
	if len(done.Tracking.Items) != 1 || done.Tracking.Items[0].RemoteID != "0/downloads/movie.bin" {
		t.Fatalf("items = %+v, want the retained result item", done.Tracking.Items)
	}
}

func TestOfflineFetchDoesNotClaimUnrelatedParseFiles(t *testing.T) {
	t.Parallel()

	provider := newFakeOfflineProvider()
	// The provider reports no files even though the parse directory holds an
	// unrelated entry. It must not become a fetch result.
	if err := provider.Seed(map[string]string{"downloads/": "", "downloads/unrelated.bin": "other"}); err != nil {
		t.Fatal(err)
	}
	c := newOfflineCore(t, provider, "fake")
	destination := t.TempDir()
	created, err := c.CreateOperation(context.Background(), task.OperationRequest{
		Operation: task.OperationDownload,
		Offline: &task.OfflineOperationRequest{
			Source:     "magnet:?xt=urn:btih:unrelated",
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
		t.Fatalf("state = %s, want failed when the provider reports no files", done.State)
	}
	if containsCall(provider.FakeCalls(), "Remove:0/downloads/unrelated.bin") {
		t.Fatal("an unrelated parse-directory entry was treated as an owned result and deleted")
	}
	if entries, err := os.ReadDir(destination); err != nil || len(entries) != 0 {
		t.Fatalf("destination entries = %v err=%v, want none", entries, err)
	}
}

func TestOfflineExplicitIdempotencyKeyContract(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	provider := newFakeOfflineProvider()
	provider.states = []drive.OfflineState{drive.OfflineStateQueued, drive.OfflineStateRunning}
	if err := provider.Seed(map[string]string{"downloads/": ""}); err != nil {
		t.Fatal(err)
	}
	c := newOfflineCore(t, provider, "fake")

	destA := filepath.Join(t.TempDir(), "a.bin")
	destB := filepath.Join(t.TempDir(), "b.bin")
	request := func(source, key, downloadTo string, keepCloud bool) task.OperationRequest {
		return task.OperationRequest{
			Operation:      task.OperationDownload,
			IdempotencyKey: key,
			Offline: &task.OfflineOperationRequest{
				Source:     source,
				ParseTo:    "/fake/downloads",
				DownloadTo: downloadTo,
				KeepCloud:  keepCloud,
			},
		}
	}
	source := "magnet:?xt=urn:btih:keyed"
	first, err := c.CreateOperation(ctx, request(source, "key-1", destA, false))
	if err != nil {
		t.Fatal(err)
	}
	same, err := c.CreateOperation(ctx, request(source, "key-1", destA, false))
	if err != nil {
		t.Fatalf("same key/content: %v", err)
	}
	if same.ID != first.ID {
		t.Fatalf("same key/content returned %q, want %q", same.ID, first.ID)
	}
	if _, err := c.CreateOperation(ctx, request(source, "key-1", destB, false)); !errors.Is(err, task.ErrIdempotencyConflict) {
		t.Fatalf("same key/different destination error = %v, want idempotency conflict", err)
	}
	if _, err := c.CreateOperation(ctx, request(source, "key-1", destA, true)); !errors.Is(err, task.ErrIdempotencyConflict) {
		t.Fatalf("same key/different cleanup error = %v, want idempotency conflict", err)
	}

	// A fresh key with no matching active work follows the manager contract
	// and creates a task.
	freshSource := "magnet:?xt=urn:btih:keyed-fresh"
	fresh, err := c.CreateOperation(ctx, request(freshSource, "key-2", destA, false))
	if err != nil {
		t.Fatalf("fresh key: %v", err)
	}
	if fresh.ID == first.ID {
		t.Fatal("a fresh key for different work must not reuse the first task")
	}
	freshAgain, err := c.CreateOperation(ctx, request(freshSource, "key-2", destA, false))
	if err != nil || freshAgain.ID != fresh.ID {
		t.Fatalf("fresh key retry = %q err=%v, want %q", freshAgain.ID, err, fresh.ID)
	}

	// Active de-duplication still applies to a keyed request: it reuses the
	// matching active task and binds the new key, so a later same-key request
	// with different content conflicts.
	bindSource := "magnet:?xt=urn:btih:key-bind"
	unkeyed, err := c.CreateOperation(ctx, request(bindSource, "", destA, false))
	if err != nil {
		t.Fatal(err)
	}
	bound, err := c.CreateOperation(ctx, request(bindSource, "key-3", destA, false))
	if err != nil {
		t.Fatalf("key binding: %v", err)
	}
	if bound.ID != unkeyed.ID {
		t.Fatalf("active de-duplication returned %q for keyed request, want %q", bound.ID, unkeyed.ID)
	}
	if !task.HasIdempotencyKey(bound, "key-3") {
		t.Fatalf("bound keys = %v, want key-3", task.BoundIdempotencyKeys(bound))
	}
	if bound.IdempotencyKey != "" {
		t.Fatalf("binding replaced the empty primary key: %q", bound.IdempotencyKey)
	}
	if _, err := c.CreateOperation(ctx, request(bindSource, "key-3", destB, false)); !errors.Is(err, task.ErrIdempotencyConflict) {
		t.Fatalf("bound key/different destination error = %v, want idempotency conflict", err)
	}

	_ = c.CancelTask(ctx, first.ID)
	_ = c.CancelTask(ctx, fresh.ID)
	_ = c.CancelTask(ctx, unkeyed.ID)
}

func TestOfflineSecondaryIdempotencyKeyBinding(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	provider := newFakeOfflineProvider()
	provider.states = []drive.OfflineState{drive.OfflineStateQueued, drive.OfflineStateRunning}
	if err := provider.Seed(map[string]string{"downloads/": ""}); err != nil {
		t.Fatal(err)
	}
	c := newOfflineCore(t, provider, "fake")
	destA := filepath.Join(t.TempDir(), "a.bin")
	destB := filepath.Join(t.TempDir(), "b.bin")
	request := func(key, destination string) task.OperationRequest {
		return task.OperationRequest{
			Operation:      task.OperationDownload,
			IdempotencyKey: key,
			Offline: &task.OfflineOperationRequest{
				Source:     "magnet:?xt=urn:btih:secondary-key",
				ParseTo:    "/fake/downloads",
				DownloadTo: destination,
			},
		}
	}

	first, err := c.CreateOperation(ctx, request("key-1", destA))
	if err != nil {
		t.Fatal(err)
	}
	second, err := c.CreateOperation(ctx, request("key-2", destA))
	if err != nil || second.ID != first.ID {
		t.Fatalf("active de-duplication: second=%s first=%s err=%v", second.ID, first.ID, err)
	}
	if second.IdempotencyKey != "key-1" {
		t.Fatalf("binding replaced the primary key: %q", second.IdempotencyKey)
	}
	if !task.HasIdempotencyKey(second, "key-2") {
		t.Fatalf("bound keys = %v, want key-1 and key-2", task.BoundIdempotencyKeys(second))
	}
	if _, err := c.CreateOperation(ctx, request("key-2", destB)); !errors.Is(err, task.ErrIdempotencyConflict) {
		t.Fatalf("second key was never bound: altered request error=%v", err)
	}

	// A later alias keeps the fingerprint of its own request, so replaying the
	// identical request reuses the task even though another alias points at a
	// different destination.
	alias, err := c.CreateOperation(ctx, request("key-3", destB))
	if err != nil || alias.ID != first.ID {
		t.Fatalf("third key active de-duplication = %q err=%v, want %q", alias.ID, err, first.ID)
	}
	aliasAgain, err := c.CreateOperation(ctx, request("key-3", destB))
	if err != nil || aliasAgain.ID != first.ID {
		t.Fatalf("identical alias replay = %q err=%v, want %q", aliasAgain.ID, err, first.ID)
	}
	if _, err := c.CreateOperation(ctx, request("key-3", destA)); !errors.Is(err, task.ErrIdempotencyConflict) {
		t.Fatalf("alias with different content error=%v, want conflict", err)
	}

	if err := c.CancelTask(ctx, first.ID); err != nil {
		t.Fatal(err)
	}
	waitCoreTask(t, c, first.ID)
	replayed, err := c.CreateOperation(ctx, request("key-2", destA))
	if err != nil {
		t.Fatal(err)
	}
	if replayed.ID != first.ID {
		t.Fatalf("secondary key replay after termination created %s instead of %s", replayed.ID, first.ID)
	}
}

func TestOfflineConcurrentCreateDeduplicates(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	provider := newFakeOfflineProvider()
	provider.states = []drive.OfflineState{drive.OfflineStateQueued, drive.OfflineStateRunning}
	if err := provider.Seed(map[string]string{"downloads/": ""}); err != nil {
		t.Fatal(err)
	}
	c := newOfflineCore(t, provider, "fake")

	type result struct {
		id  string
		err error
	}
	start := make(chan struct{})
	results := make(chan result, 2)
	for range 2 {
		go func() {
			<-start
			created, err := c.CreateOperation(ctx, cloudOnlyRequest("magnet:?xt=urn:btih:concurrent", "/fake/downloads"))
			results <- result{id: created.ID, err: err}
		}()
	}
	close(start)
	first, second := <-results, <-results
	if first.err != nil || second.err != nil {
		t.Fatalf("creates = %v / %v", first.err, second.err)
	}
	if first.id != second.id {
		t.Fatalf("concurrent creates returned %q and %q", first.id, second.id)
	}
	deadline := time.Now().Add(2 * time.Second)
	for len(provider.createdRequests()) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("provider task was never created")
		}
		time.Sleep(time.Millisecond)
	}
	if got := len(provider.createdRequests()); got != 1 {
		t.Fatalf("concurrent creates produced %d provider tasks, want 1", got)
	}
	_ = c.CancelTask(ctx, first.id)
}
