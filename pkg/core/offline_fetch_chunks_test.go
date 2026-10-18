package core

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/task"
)

// rangeServer serves fixed content and honours bounded Range requests, failing
// the chunk starting at failAt on the first attempt when set.
func rangeServer(t *testing.T, content []byte, failAt int64) (*httptest.Server, func() []string) {
	t.Helper()
	var (
		mu       sync.Mutex
		ranges   []string
		failed   bool
		chunkLen = 8
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		value := r.Header.Get("Range")
		mu.Lock()
		ranges = append(ranges, value)
		fail := failAt >= 0 && !failed && value == fmt.Sprintf("bytes=%d-%d", failAt, failAt+int64(chunkLen)-1)
		if fail {
			failed = true
		}
		mu.Unlock()
		if fail {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		start, end, err := parseTestRange(value, int64(len(content)))
		if err != nil {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(content)
			return
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(content)))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(content[start : end+1])
	}))
	t.Cleanup(server.Close)
	return server, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), ranges...)
	}
}

// parseTestRange parses a bounded "bytes=start-end" header, clamping end to the
// content length. A non-bounded header is rejected so the handler can answer
// with a full body.
func parseTestRange(value string, size int64) (int64, int64, error) {
	spec, ok := strings.CutPrefix(value, "bytes=")
	if !ok || spec == "" {
		return 0, 0, fmt.Errorf("no range")
	}
	startText, endText, ok := strings.Cut(spec, "-")
	if !ok || startText == "" || endText == "" {
		return 0, 0, fmt.Errorf("unbounded range")
	}
	start, err := strconv.ParseInt(startText, 10, 64)
	if err != nil {
		return 0, 0, err
	}
	end, err := strconv.ParseInt(endText, 10, 64)
	if err != nil {
		return 0, 0, err
	}
	if start >= size {
		return 0, 0, fmt.Errorf("out of range")
	}
	if end >= size {
		end = size - 1
	}
	return start, end, nil
}

func contentBytes(n int) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = byte('a' + i%26)
	}
	return out
}

// TestOfflineURLFetchChunksWithBoundedRanges drives the chunked copier directly
// with a small plan and asserts every request carries a bounded range.
func TestOfflineURLFetchChunksWithBoundedRanges(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	content := contentBytes(40)
	server, ranges := rangeServer(t, content, -1)

	destination := filepath.Join(t.TempDir(), "chunked.bin")
	writer, err := openLocalWriter(destination)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()

	fetch := offlineURLFetch{
		client: http.DefaultClient,
		target: drive.OfflineDownloadTarget{URL: server.URL},
		writer: writer,
		plan:   offlineChunkPlan{chunk: 8, workers: 3},
	}
	result, err := fetch.run(ctx, 0, int64(len(content)))
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !result.done || result.written != int64(len(content)) {
		t.Fatalf("result = %+v, want %d contiguous bytes and done", result, len(content))
	}
	data, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(content) {
		t.Fatalf("destination mismatch: %q", data)
	}
	got := ranges()
	if len(got) != 5 {
		t.Fatalf("requests = %v, want one per 8-byte chunk", got)
	}
	for _, value := range got {
		start, end, err := parseTestRange(value, int64(len(content)))
		if err != nil {
			t.Fatalf("range %q is not a bounded range: %v", value, err)
		}
		if end-start+1 != 8 && (start != 32 || end != 39) {
			t.Fatalf("range %q is not chunk sized", value)
		}
	}
}

// TestOfflineURLFetchResumesFromContiguousPrefix verifies a failed chunk does
// not skip earlier gaps: the retry resumes at the last contiguous byte.
func TestOfflineURLFetchResumesFromContiguousPrefix(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	content := contentBytes(40)
	server, _ := rangeServer(t, content, 16)

	destination := filepath.Join(t.TempDir(), "resume.bin")
	writer, err := openLocalWriter(destination)
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
	first, err := fetch.run(ctx, 0, int64(len(content)))
	if err == nil {
		t.Fatal("first attempt must fail on the served chunk error")
	}
	if first.written != 16 {
		t.Fatalf("first attempt wrote %d contiguous bytes, want 16", first.written)
	}
	second, err := fetch.run(ctx, first.written, int64(len(content)))
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if !second.done {
		t.Fatalf("resume result = %+v, want done", second)
	}
	data, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(content) {
		t.Fatalf("destination mismatch after resume: %q", data)
	}
}

// TestOfflineURLFetchRangeIgnoringServer covers a provider that answers every
// range request with the whole body.
func TestOfflineURLFetchRangeIgnoringServer(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	content := contentBytes(40)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(content)
	}))
	defer server.Close()

	destination := filepath.Join(t.TempDir(), "plain.bin")
	writer, err := openLocalWriter(destination)
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
	result, err := fetch.run(ctx, 0, int64(len(content)))
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !result.done {
		t.Fatalf("result = %+v, want done", result)
	}
	data, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(content) {
		t.Fatalf("destination mismatch: %q", data)
	}
}

// TestOfflineURLFetchUnknownSizeHonoursRanges covers a provider that honours
// bounded ranges but discloses no total size.
func TestOfflineURLFetchUnknownSizeHonoursRanges(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	content := contentBytes(20)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start, end, err := parseTestRange(r.Header.Get("Range"), int64(len(content)))
		if err != nil {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(content)
			return
		}
		// A 206 without Content-Range, so the total size stays unknown.
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(content[start : end+1])
	}))
	defer server.Close()

	destination := filepath.Join(t.TempDir(), "unknown.bin")
	writer, err := openLocalWriter(destination)
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
	result, err := fetch.run(ctx, 0, 0)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !result.done {
		t.Fatalf("result = %+v, want done", result)
	}
	data, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(content) {
		t.Fatalf("destination mismatch: %q", data)
	}
}

// TestOfflineFetchSendsBoundedRangeRequests is the end-to-end regression guard:
// a fetch must never issue an unbounded request, because providers may serve
// those at a fraction of the real speed.
func TestOfflineFetchSendsBoundedRangeRequests(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	content := contentBytes(48)
	server, ranges := rangeServer(t, content, -1)

	provider := newFakeOfflineProvider()
	provider.downloadURL = server.URL
	provider.files = []drive.OfflineFile{{Name: "bounded.bin", Size: int64(len(content)), RemoteID: "0/downloads/bounded.bin"}}
	provider.states = []drive.OfflineState{drive.OfflineStateQueued, drive.OfflineStateSucceeded}
	// No staged entry: the transfer must use the provider download URL.
	if err := provider.Seed(map[string]string{"downloads/": ""}); err != nil {
		t.Fatal(err)
	}
	c := newOfflineCore(t, provider, "fake")

	destination := filepath.Join(t.TempDir(), "bounded.bin")
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
	if err != nil || string(data) != string(content) {
		t.Fatalf("destination = %q err=%v", data, err)
	}
	requests := ranges()
	if len(requests) == 0 {
		t.Fatal("the transfer made no range request")
	}
	for _, value := range requests {
		if _, _, err := parseTestRange(value, int64(len(content))); err != nil {
			t.Fatalf("request %q is not a bounded range: %v", value, err)
		}
	}
}

// TestOfflineParseDestinationMountRootUsesProviderID verifies a mount root is
// reported to the provider as its own directory fid rather than the namespace's
// synthetic "/MOUNT" id, which providers silently ignore.
func TestOfflineParseDestinationMountRootUsesProviderID(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	provider := newFakeOfflineProvider()
	provider.files = []drive.OfflineFile{{Name: "root.bin", Size: 3, RemoteID: "0/root.bin"}}
	provider.states = []drive.OfflineState{drive.OfflineStateQueued, drive.OfflineStateSucceeded}
	if err := provider.Seed(map[string]string{"": "seed"}); err != nil {
		t.Fatal(err)
	}
	c := newOfflineCore(t, provider, "fake")

	created, err := c.CreateOperation(ctx, task.OperationRequest{
		Operation: task.OperationDownload,
		Offline: &task.OfflineOperationRequest{
			Source:  "magnet:?xt=urn:btih:abc",
			ParseTo: "/fake",
		},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	_ = waitCoreTask(t, c, created.ID)

	requests := provider.createdRequests()
	if len(requests) == 0 {
		t.Fatal("provider was never asked to create the task")
	}
	// The fake driver resolves "/" to its root id, which is "0" by default.
	const wantRootID = "0"
	if got := requests[0].DestinationID; got != wantRootID {
		t.Fatalf("DestinationID = %q, want the provider root id %q", got, wantRootID)
	}

	raw, err := json.Marshal(created.Detail[task.OfflineDetailKey])
	if err != nil {
		t.Fatal(err)
	}
	var detail offlineDetail
	if err := json.Unmarshal(raw, &detail); err != nil {
		t.Fatal(err)
	}
	if detail.ParseRemoteID != wantRootID {
		t.Fatalf("parse_remote_id = %q, want the provider root id %q", detail.ParseRemoteID, wantRootID)
	}
}

// TestOfflineTransferPlanFollowsPersistedTuning verifies the caller's tuning
// reaches the chunk plan, so a fetch created with --workers/--chunk-size keeps
// them (including after a recovery, which reads the persisted detail).
func TestOfflineTransferPlanFollowsPersistedTuning(t *testing.T) {
	t.Parallel()

	defaults := defaultOfflineChunkPlan()
	run := &offlineRun{detail: offlineDetail{}}
	if got := run.transferPlan(); got != defaults {
		t.Fatalf("plan without tuning = %+v, want the defaults %+v", got, defaults)
	}

	run.detail.TransferWorkers = 2
	run.detail.TransferChunk = task.MinOfflineTransferChunk
	if got := run.transferPlan(); got.workers != 2 || got.chunk != task.MinOfflineTransferChunk {
		t.Fatalf("plan = %+v, want the persisted tuning", got)
	}
}

// TestOfflineFetchPersistsTransferTuning verifies a created fetch carries the
// requested tuning in its durable detail (and its task JSON).
func TestOfflineFetchPersistsTransferTuning(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	download := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("tuned"))
	}))
	defer download.Close()

	provider := newFakeOfflineProvider()
	provider.downloadURL = download.URL
	provider.files = []drive.OfflineFile{{Name: "tuned.bin", Size: 5, RemoteID: "0/downloads/tuned.bin"}}
	provider.states = []drive.OfflineState{drive.OfflineStateQueued, drive.OfflineStateSucceeded}
	if err := provider.Seed(map[string]string{"downloads/": ""}); err != nil {
		t.Fatal(err)
	}
	c := newOfflineCore(t, provider, "fake")

	destination := filepath.Join(t.TempDir(), "tuned.bin")
	created, err := c.CreateOperation(ctx, task.OperationRequest{
		Operation: task.OperationDownload,
		Offline: &task.OfflineOperationRequest{
			Source:     "magnet:?xt=urn:btih:abc",
			ParseTo:    "/fake/downloads",
			DownloadTo: destination,
			Workers:    3,
			ChunkSize:  1 << 20,
		},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	detail := offlineDetailFromTask(t, created)
	if detail.TransferWorkers != 3 || detail.TransferChunk != 1<<20 {
		t.Fatalf("detail tuning = %d/%d, want 3/%d", detail.TransferWorkers, detail.TransferChunk, 1<<20)
	}
	done := waitCoreTask(t, c, created.ID)
	if done.State != task.StateSucceeded {
		t.Fatalf("state = %s (%v)", done.State, done.Error)
	}
	data, err := os.ReadFile(destination)
	if err != nil || string(data) != "tuned" {
		t.Fatalf("destination = %q err=%v", data, err)
	}
}
