package control

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/contracttest"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/drivers/localfs"
	"github.com/yinzhenyu/qrypt/pkg/logging"
	"github.com/yinzhenyu/qrypt/pkg/task"
	"github.com/yinzhenyu/qrypt/pkg/util"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
	"github.com/yinzhenyu/qrypt/pkg/vfs/diagnostics"
	"github.com/yinzhenyu/qrypt/pkg/vfs/readcache"
	"github.com/yinzhenyu/qrypt/pkg/vfs/upload"
	"github.com/yinzhenyu/qrypt/pkg/vfs/vfstypes"
)

type fakeSnapshotter struct {
	snapshot diagnostics.DebugSnapshot
	drivers  []diagnostics.NamedDriver
}

type fakeTaskDebugger struct {
	tasks []task.Task
	items map[string][]task.ItemResult
}

func (f fakeTaskDebugger) ListTasks(_ context.Context, filter task.Filter) ([]task.Task, error) {
	var out []task.Task
	for _, item := range f.tasks {
		if filter.Match(item) {
			out = append(out, item)
		}
		if filter.Limit > 0 && len(out) >= filter.Limit {
			break
		}
	}
	return out, nil
}

func (f fakeTaskDebugger) ListTaskItems(_ context.Context, taskID string, filter task.ItemFilter) ([]task.ItemResult, error) {
	var out []task.ItemResult
	for _, item := range f.items[taskID] {
		if filter.Match(item) {
			out = append(out, item)
		}
	}
	return out, nil
}

func (f fakeSnapshotter) DebugSnapshot() diagnostics.DebugSnapshot {
	return f.snapshot
}

type fakeResetSnapshotter struct {
	fakeSnapshotter
	resetCalled bool
}

func (f *fakeResetSnapshotter) DebugReset(ctx context.Context) error {
	f.resetCalled = true
	return nil
}

func (f fakeSnapshotter) Drivers() []diagnostics.NamedDriver {
	return f.drivers
}

func (f fakeSnapshotter) DebugActiveOps(ctx context.Context, mountNames []string) ([]diagnostics.DebugActiveMount, error) {
	return []diagnostics.DebugActiveMount{{
		Mount: "local",
		Ops: []vfstypes.DebugActiveOp{{
			OpID:      "read-1",
			Kind:      "vfs_read",
			Phase:     "read_range",
			State:     "active",
			Mount:     "local",
			Path:      "/old.txt",
			RemoteID:  "old",
			StartedAt: time.Unix(6, 0),
			UpdatedAt: time.Unix(6, 0),
		}},
	}}, nil
}

func (f fakeSnapshotter) MountHealth(ctx context.Context, mountName string) ([]diagnostics.MountHealth, error) {
	mount := mountName
	if mount == "" {
		mount = "local"
	}
	return []diagnostics.MountHealth{{
		Mount:     mount,
		OK:        true,
		Level:     drive.HealthLevelOK,
		CheckedAt: time.Unix(9, 0),
		Success:   2,
		Ops: map[string]diagnostics.MountHealthOp{
			drive.HealthOpList: {Success: 2},
		},
	}}, nil
}

type fakeSpaceDriver struct {
	drive.UnsupportedOperations
	space drive.Space
	err   error
}

func (f fakeSpaceDriver) Init(context.Context) error { return nil }
func (f fakeSpaceDriver) Drop(context.Context) error { return nil }
func (f fakeSpaceDriver) List(context.Context, string) ([]drive.Entry, error) {
	return nil, nil
}
func (f fakeSpaceDriver) Read(context.Context, drive.Entry, int64, int64) (io.ReadCloser, error) {
	return nil, nil
}
func (f fakeSpaceDriver) Space(context.Context) (drive.Space, error) {
	return f.space, f.err
}
func (f fakeSpaceDriver) Capabilities() []drive.Capability {
	return []drive.Capability{drive.CapabilitySpace}
}
func (f fakeSpaceDriver) DebugSnapshot(context.Context) (drive.DebugSnapshot, error) {
	return drive.DebugSnapshot{Driver: "fake-space", Health: drive.HealthLevelOK, GeneratedAt: time.Now()}, nil
}
func (f fakeSpaceDriver) Metrics(context.Context, time.Time) ([]drive.MetricEvent, error) {
	return nil, nil
}

func (f fakeSnapshotter) RemoteList(ctx context.Context, path string) ([]drive.Entry, error) {
	return []drive.Entry{{
		ID:       "remote-id",
		ParentID: "parent-id",
		Name:     "uploaded.txt",
		Size:     7,
		ModTime:  time.Unix(2, 0),
	}}, nil
}

func (f fakeSnapshotter) DebugResolve(ctx context.Context, path string, includeRemoteName bool) (diagnostics.DebugResolveInfo, error) {
	info := diagnostics.DebugResolveInfo{
		Path:      path,
		Parent:    "/local",
		PlainName: "file.txt",
		Mount:     "local",
		Driver:    "localfs",
		CacheID:   "cache-id",
		RemoteID:  "remote-id",
		ParentID:  "parent-id",
		Size:      7,
	}
	if path == "/local/out" {
		info.IsDir = true
		info.PlainName = "out"
	}
	if includeRemoteName {
		info.RemoteName = "encrypted-name"
	}
	return info, nil
}

func (f fakeSnapshotter) DebugConsistency(ctx context.Context, path string) (diagnostics.ConsistencyReport, error) {
	return diagnostics.ConsistencyReport{
		Path:         path,
		Parent:       "/local",
		Name:         "file.txt",
		RemoteFound:  true,
		RemoteID:     "remote-id",
		RemoteSize:   7,
		ExpectedSize: 7,
		SizeMatches:  true,
		Status:       "ok",
	}, nil
}

func (f fakeSnapshotter) DebugStaging(ctx context.Context, path string) (diagnostics.DebugStagingReport, error) {
	return diagnostics.DebugStagingReport{
		Path: path,
		Mounts: []diagnostics.DebugStagingMount{{
			Mount:        "local",
			PendingCount: 1,
			StagingCount: 1,
			Bytes:        7,
			Files: []diagnostics.DebugStagingFile{{
				Path:        "/local/file.txt",
				LocalPath:   "/tmp/file.staging",
				Pending:     true,
				Exists:      true,
				PendingSize: 7,
				StagingSize: 7,
				SizeMatches: true,
				SHA256:      "abc123",
			}},
		}},
	}, nil
}

func TestServerExposesStateAndPending(t *testing.T) {
	socketPath := testSocketPath(t)
	source := fakeSnapshotter{snapshot: diagnostics.DebugSnapshot{
		SchemaVersion: diagnostics.DebugSnapshotSchemaVersion,
		GeneratedAt:   time.Unix(1, 0),
		Kind:          "namespace",
		Process:       diagnostics.DebugProcess{PID: 1234, StartedAt: time.Unix(8, 0)},
		Mounts: []diagnostics.MountSnapshot{{
			Identity: diagnostics.MountSnapshotIdentity{
				Name:         "local",
				DriverName:   "localfs",
				Capabilities: []drive.Capability{drive.CapabilitySourceUploader},
				Driver: &drive.DebugSnapshot{
					Driver:      "localfs",
					Health:      "ok",
					GeneratedAt: time.Unix(3, 0),
				},
			},
			UploadState: diagnostics.MountSnapshotUploads{Active: []upload.UploadSnapshot{{
				OpID:          "file",
				Path:          "/file.txt",
				State:         string(drive.UploadPhaseUploading),
				BytesTotal:    10,
				BytesUploaded: 4,
				UpdatedAt:     time.Unix(4, 0),
			}}, History: []upload.UploadSnapshot{{
				OpID:          "old",
				Path:          "/old.txt",
				State:         string(drive.UploadPhaseCompleted),
				BytesTotal:    5,
				BytesUploaded: 5,
				UpdatedAt:     time.Unix(5, 0),
				CompletedAt:   time.Unix(5, 0),
				Events: []drive.MetricEvent{{
					OpID: "old", Kind: "vfs_upload", Operation: "upload", Phase: "uploading", State: "completed", OK: true, Path: "/old.txt",
				}},
			}}},
			Events: diagnostics.MountSnapshotEvents{Reads: []drive.MetricEvent{{
				OpID: "read-1", Kind: "vfs_read", Operation: "read", Phase: "read", State: "completed", OK: true,
				Path: "/old.txt", RemoteID: "old", Bytes: 5, StartedAt: time.Unix(6, 0),
			}}, Driver: []drive.MetricEvent{{
				OpID: "driver-1", Kind: "driver", Operation: "list", Phase: "request", State: "completed", OK: true,
				Path: "/old.txt", RemoteID: "old", StartedAt: time.Unix(6, 0),
			}, {
				OpID: "driver-2", Kind: "driver", Operation: "download", Phase: "request", State: "failed", OK: false,
				Path: "/old.txt", RemoteID: "old", Error: "timeout", StartedAt: time.Unix(7, 0),
			}, {
				OpID: "driver-3", Kind: "driver", Operation: "upload_part", Phase: "request", State: "completed", OK: true,
				Path: "/file.txt", Bytes: 4 * util.MiB, StartedAt: time.Now().Add(-time.Minute),
			}}},
			Overlay: diagnostics.MountSnapshotOverlay{Pending: []vfs.PendingUpload{{
				Path:       "/file.txt",
				FID:        "file",
				LocalPath:  "/tmp/file.staging",
				Size:       3,
				RetryCount: 1,
				LastError:  "boom",
			}}},
			Queues: diagnostics.MountSnapshotQueues{UploadLength: 2, UploadCap: 128},
			Cache: diagnostics.DebugCacheSnapshot{DebugReadCache: readcache.DebugReadCache{
				MaxBytes:           1024,
				ChunkCount:         1,
				Bytes:              512,
				Hits:               2,
				Misses:             1,
				WriteQueueBytes:    128,
				WriteQueueMaxBytes: 1024,
				Files:              []readcache.DebugReadCacheFile{{ID: "fid", ChunkCount: 1, Bytes: 512}, {ID: "cache-id", ChunkCount: 1, Bytes: 7}},
			}},
			Runtime: diagnostics.MountSnapshotRuntime{
				HotChunkCount: 2,
				HotChunkBytes: 2 * util.MiB,
				HotChunkLimit: 64,
				RangeHitCount: 3,
				RangeHitLimit: 1024,
			},
		}},
	}, drivers: []diagnostics.NamedDriver{{
		Name:        "local",
		Driver:      fakeSpaceDriver{space: drive.Space{Total: 2 * util.GiB, Free: 1536 * util.MiB}},
		TestEnabled: true,
	}}}
	server, err := NewServer(socketPath, source)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := server.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer server.Close(context.Background())
	if _, err := os.Stat(socketPath); err != nil {
		t.Fatalf("expected socket file: %v", err)
	}

	client, err := NewClient(socketPath)
	if err != nil {
		t.Fatal(err)
	}
	stateBody, err := client.Get(context.Background(), "/v1/state")
	if err != nil {
		t.Fatal(err)
	}
	var state diagnostics.DebugSnapshot
	if err := json.Unmarshal(stateBody, &state); err != nil {
		t.Fatal(err)
	}
	if state.Kind != "namespace" || len(state.Mounts) != 1 || state.Mounts[0].Queues.UploadLength != 2 {
		t.Fatalf("unexpected state: %+v", state)
	}
	if state.Process.PID != 1234 || state.Mounts[0].Identity.DriverName != "localfs" {
		t.Fatalf("missing state metadata: %+v", state)
	}
	if strings.Contains(string(stateBody), `"upload_history"`) ||
		strings.Contains(string(stateBody), `"upload_queue_length"`) ||
		strings.Contains(string(stateBody), `"driver_metrics"`) ||
		strings.Contains(string(stateBody), `"read_history"`) ||
		strings.Contains(string(stateBody), `"ops":`) {
		t.Fatalf("state should not expose legacy flat fields or ops: %s", stateBody)
	}

	pendingBody, err := client.Get(context.Background(), "/v1/pending")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(pendingBody), `"/local/file.txt"`) {
		t.Fatalf("expected namespace-prefixed pending path, got %s", pendingBody)
	}

	uploadsBody, err := client.Get(context.Background(), "/v1/uploads")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(uploadsBody), `"bytes_uploaded": 4`) || !strings.Contains(string(uploadsBody), `"/local/file.txt"`) {
		t.Fatalf("unexpected uploads response: %s", uploadsBody)
	}
	if strings.Contains(string(uploadsBody), "/local/old.txt") {
		t.Fatalf("uploads should not include history by default: %s", uploadsBody)
	}

	uploadHistoryBody, err := client.Get(context.Background(), "/v1/uploads?path=/local/old.txt&history=1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(uploadHistoryBody), `"/local/old.txt"`) || !strings.Contains(string(uploadHistoryBody), `"state": "`+string(drive.UploadPhaseCompleted)+`"`) {
		t.Fatalf("expected filtered upload history, got %s", uploadHistoryBody)
	}
	if !strings.Contains(string(uploadHistoryBody), `"path": "/local/old.txt"`) {
		t.Fatalf("expected namespace-prefixed upload trace path, got %s", uploadHistoryBody)
	}
	if strings.Contains(string(uploadHistoryBody), "/local/file.txt") {
		t.Fatalf("filtered upload history should not include other paths: %s", uploadHistoryBody)
	}

	opsBody, err := client.Get(context.Background(), "/v1/ops")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(opsBody), `"op_id": "read-1"`) ||
		!strings.Contains(string(opsBody), `"phase": "read_range"`) {
		t.Fatalf("unexpected active ops response: %s", opsBody)
	}

	readsBody, err := client.Get(context.Background(), "/v1/reads?path=/local/old.txt")
	if err != nil {
		t.Fatal(err)
	}

	transferBody, err := client.Get(context.Background(), "/v1/transfer/context?source=/local/file.txt&dest=/local/out/copied.txt")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(transferBody), `"compatible": true`) ||
		!strings.Contains(string(transferBody), `"destination_parent"`) ||
		!strings.Contains(string(transferBody), `"source_uploader"`) {
		t.Fatalf("unexpected transfer context response: %s", transferBody)
	}
	if !strings.Contains(string(readsBody), `"op_id": "read-1"`) ||
		!strings.Contains(string(readsBody), `"mount": "local"`) ||
		!strings.Contains(string(readsBody), `"path": "/local/old.txt"`) {
		t.Fatalf("unexpected reads response: %s", readsBody)
	}

	driverBody, err := client.Get(context.Background(), "/v1/driver")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(driverBody), `"driver": "localfs"`) || !strings.Contains(string(driverBody), `"mount": "local"`) {
		t.Fatalf("unexpected driver response: %s", driverBody)
	}
	if strings.Contains(string(driverBody), `"space"`) {
		t.Fatalf("driver response should not query space by default: %s", driverBody)
	}

	driverSpaceBody, err := client.Get(context.Background(), "/v1/driver?space=true")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(driverSpaceBody), `"bytes_total": 2147483648`) ||
		!strings.Contains(string(driverSpaceBody), `"total": "2.00 GiB"`) ||
		!strings.Contains(string(driverSpaceBody), `"free": "1.50 GiB"`) {
		t.Fatalf("unexpected driver space response: %s", driverSpaceBody)
	}
	driverMetricsBody, err := client.Get(context.Background(), "/v1/driver?operation=download&limit=1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(driverMetricsBody), `"op_id": "driver-2"`) ||
		strings.Contains(string(driverMetricsBody), `"op_id": "driver-1"`) {
		t.Fatalf("unexpected filtered driver metrics response: %s", driverMetricsBody)
	}

	testBody, err := client.PostJSON(context.Background(), "/v1/driver/test", contracttest.DriverTestRequest{Test: "crud", Mount: "local"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(testBody), `"mount": "local"`) ||
		!strings.Contains(string(testBody), "driver lacks capability") {
		t.Fatalf("unexpected driver test response: %s", testBody)
	}
	if _, err := client.PostJSON(context.Background(), "/v1/driver/test", contracttest.DriverTestRequest{Test: "crud", Mount: "missing"}); err == nil ||
		!strings.Contains(err.Error(), `mount "missing" not found`) {
		t.Fatalf("expected missing mount error, got %v", err)
	}
	disabledSource := fakeSnapshotter{drivers: []diagnostics.NamedDriver{{
		Name:   "disabled",
		Driver: fakeSpaceDriver{},
	}}}
	disabledSocket := testSocketPath(t)
	disabledServer, err := NewServer(disabledSocket, disabledSource)
	if err != nil {
		t.Fatal(err)
	}
	disabledCtx, disabledCancel := context.WithCancel(context.Background())
	defer disabledCancel()
	if err := disabledServer.Start(disabledCtx); err != nil {
		t.Fatal(err)
	}
	defer disabledServer.Close(context.Background())
	disabledClient, err := NewClient(disabledSocket)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := disabledClient.PostJSON(context.Background(), "/v1/driver/test", contracttest.DriverTestRequest{Test: "crud", Mount: "disabled"}); err == nil ||
		!strings.Contains(err.Error(), `mount "disabled" is not enabled for debug tests`) {
		t.Fatalf("expected disabled mount test error, got %v", err)
	}
	if _, err := disabledClient.PostJSON(context.Background(), "/v1/bench", contracttest.DriverTestRequest{Test: "crud", Mount: "disabled"}); err == nil ||
		!strings.Contains(err.Error(), `mount "disabled" is not enabled for debug tests`) {
		t.Fatalf("expected disabled mount benchmark error, got %v", err)
	}
	benchBody, err := client.PostJSON(context.Background(), "/v1/bench", contracttest.DriverTestRequest{Test: "crud", Mount: "local"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(benchBody), `"kind": "driver_crud_benchmark"`) ||
		!strings.Contains(string(benchBody), `"summary"`) ||
		!strings.Contains(string(benchBody), `"assessment"`) ||
		!strings.Contains(string(benchBody), `"network_probe"`) {
		t.Fatalf("unexpected driver benchmark response: %s", benchBody)
	}
	if _, err := client.PostJSON(context.Background(), "/v1/driver/bench", contracttest.DriverTestRequest{Test: "crud", Mount: "local"}); err == nil {
		t.Fatal("expected old driver benchmark endpoint to be unavailable")
	}
	if _, err := client.PostJSON(context.Background(), "/v1/bench", contracttest.DriverTestRequest{Test: "crud", Mount: "missing"}); err == nil ||
		!strings.Contains(err.Error(), `mount "missing" not found`) {
		t.Fatalf("expected missing mount benchmark error, got %v", err)
	}
	rootA, rootB := t.TempDir(), t.TempDir()
	drvA, drvB := localfs.New(rootA), localfs.New(rootB)
	if err := drvA.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := drvB.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	xferSource := fakeSnapshotter{drivers: []diagnostics.NamedDriver{
		{Name: "local", Driver: drvA, TestEnabled: true},
		{Name: "cloud", Driver: drvB, TestEnabled: true},
	}}
	xferSocket := testSocketPath(t)
	xferServer, err := NewServer(xferSocket, xferSource)
	if err != nil {
		t.Fatal(err)
	}
	xferCtx, xferCancel := context.WithCancel(context.Background())
	defer xferCancel()
	if err := xferServer.Start(xferCtx); err != nil {
		t.Fatal(err)
	}
	defer xferServer.Close(context.Background())
	xferClient, err := NewClient(xferSocket)
	if err != nil {
		t.Fatal(err)
	}
	xferBenchBody, err := xferClient.PostJSON(context.Background(), "/v1/bench", contracttest.DriverTestRequest{
		Test:    "xfer",
		Source:  "local",
		Dest:    "cloud",
		Size:    "4k",
		Samples: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(xferBenchBody), `"kind": "xfer_benchmark"`) ||
		!strings.Contains(string(xferBenchBody), `"source_mount": "local"`) ||
		!strings.Contains(string(xferBenchBody), `"dest_mount": "cloud"`) ||
		!strings.Contains(string(xferBenchBody), `"read_bps"`) {
		t.Fatalf("unexpected xfer benchmark response: %s", xferBenchBody)
	}
	disabledXferSource := fakeSnapshotter{drivers: []diagnostics.NamedDriver{
		{Name: "local", Driver: drvA, TestEnabled: true},
		{Name: "cloud", Driver: drvB},
	}}
	disabledXferSocket := testSocketPath(t)
	disabledXferServer, err := NewServer(disabledXferSocket, disabledXferSource)
	if err != nil {
		t.Fatal(err)
	}
	disabledXferCtx, disabledXferCancel := context.WithCancel(context.Background())
	defer disabledXferCancel()
	if err := disabledXferServer.Start(disabledXferCtx); err != nil {
		t.Fatal(err)
	}
	defer disabledXferServer.Close(context.Background())
	disabledXferClient, err := NewClient(disabledXferSocket)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := disabledXferClient.PostJSON(context.Background(), "/v1/driver/test", contracttest.DriverTestRequest{
		Test:   "xfer",
		Source: "local",
		Dest:   "cloud",
	}); err == nil || !strings.Contains(err.Error(), `mount "cloud" is not enabled for debug tests`) {
		t.Fatalf("expected disabled xfer mount error, got %v", err)
	}
	if _, err := client.Get(context.Background(), "/v1/driver/test?test=crud"); err == nil {
		t.Fatal("expected old driver test endpoint to be unavailable")
	}
	if _, err := client.PostJSON(context.Background(), "/v1/probe/driver", contracttest.DriverTestRequest{Test: "crud", Mount: "local"}); err == nil {
		t.Fatal("expected old driver probe endpoint to be unavailable")
	}

	mountHealthBody, err := client.Get(context.Background(), "/v1/mounts/health")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(mountHealthBody), `"mount": "local"`) ||
		!strings.Contains(string(mountHealthBody), `"level": "ok"`) ||
		!strings.Contains(string(mountHealthBody), `"success": 2`) ||
		!strings.Contains(string(mountHealthBody), `"list"`) {
		t.Fatalf("unexpected mount health response: %s", mountHealthBody)
	}

	listBody, err := client.Get(context.Background(), "/v1/list?path=/local")
	if err != nil {
		t.Fatal(err)
	}
	var list ListResponse
	if err := json.Unmarshal(listBody, &list); err != nil {
		t.Fatal(err)
	}
	if list.Source != "remote" || list.Path != "/local" || len(list.Entries) != 1 {
		t.Fatalf("unexpected list response: %+v", list)
	}
	if list.Entries[0].Name != "uploaded.txt" || list.Entries[0].Path != "/local/uploaded.txt" || list.Entries[0].ID != "remote-id" {
		t.Fatalf("unexpected list entry: %+v", list.Entries[0])
	}

	resolveBody, err := client.Get(context.Background(), "/v1/resolve?path=/local/file.txt&include_remote_name=1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(resolveBody), `"remote_name": "encrypted-name"`) || !strings.Contains(string(resolveBody), `"remote_id": "remote-id"`) {
		t.Fatalf("unexpected resolve response: %s", resolveBody)
	}
	if !strings.Contains(string(resolveBody), `"mount": "local"`) || !strings.Contains(string(resolveBody), `"cache_id": "cache-id"`) {
		t.Fatalf("resolve response missing metadata: %s", resolveBody)
	}

	cacheBody, err := client.Get(context.Background(), "/v1/cache")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(cacheBody), `"hits": 2`) || !strings.Contains(string(cacheBody), `"id": "fid"`) {
		t.Fatalf("unexpected cache response: %s", cacheBody)
	}
	cachePathBody, err := client.Get(context.Background(), "/v1/cache?path=/local/file.txt")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(cachePathBody), `"path": "/local/file.txt"`) || !strings.Contains(string(cachePathBody), `"remote_id": "remote-id"`) {
		t.Fatalf("unexpected path cache response: %s", cachePathBody)
	}
	if !strings.Contains(string(cachePathBody), `"id": "cache-id"`) || strings.Contains(string(cachePathBody), `"id": "fid"`) {
		t.Fatalf("path cache response should include only resolved file: %s", cachePathBody)
	}

	readMemoryBody, err := client.Get(context.Background(), "/v1/read-memory?mount=local&since=1000000h&limit=5")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(readMemoryBody), `"hot_chunk_bytes": 2097152`) ||
		!strings.Contains(string(readMemoryBody), `"write_queue_bytes": 128`) ||
		!strings.Contains(string(readMemoryBody), `"phase_counts"`) {
		t.Fatalf("unexpected read memory response: %s", readMemoryBody)
	}

	stagingBody, err := client.Get(context.Background(), "/v1/staging?path=/local/file.txt")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(stagingBody), `"sha256": "abc123"`) || !strings.Contains(string(stagingBody), `"size_matches": true`) {
		t.Fatalf("unexpected staging response: %s", stagingBody)
	}

	consistencyBody, err := client.Get(context.Background(), "/v1/consistency?path=/local/file.txt")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(consistencyBody), `"status": "ok"`) || !strings.Contains(string(consistencyBody), `"size_matches": true`) {
		t.Fatalf("unexpected consistency response: %s", consistencyBody)
	}

	consistencyDirBody, err := client.Get(context.Background(), "/v1/consistency?dir=/local")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(consistencyDirBody), `"reports"`) || !strings.Contains(string(consistencyDirBody), `"/local/uploaded.txt"`) {
		t.Fatalf("unexpected dir consistency response: %s", consistencyDirBody)
	}

	runtimeBody, err := client.Get(context.Background(), "/v1/runtime")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(runtimeBody), `"go_version"`) || !strings.Contains(string(runtimeBody), `"num_goroutine"`) {
		t.Fatalf("unexpected runtime response: %s", runtimeBody)
	}
	if !strings.Contains(string(runtimeBody), `"process"`) || !strings.Contains(string(runtimeBody), `"rss_available"`) {
		t.Fatalf("runtime response missing process memory: %s", runtimeBody)
	}

	uploadMemoryBody, err := client.Get(context.Background(), "/v1/upload-memory?mount=local&limit=5")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(uploadMemoryBody), `"/local/file.txt"`) ||
		!strings.Contains(string(uploadMemoryBody), `"runtime"`) ||
		!strings.Contains(string(uploadMemoryBody), `"recent_parts"`) ||
		!strings.Contains(string(uploadMemoryBody), `"op_id": "driver-3"`) {
		t.Fatalf("unexpected upload memory response: %s", uploadMemoryBody)
	}

	goroutineBody, err := client.Get(context.Background(), "/v1/goroutines")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(goroutineBody), "goroutine") {
		t.Fatalf("unexpected goroutine response: %s", goroutineBody)
	}
	stackBody, err := client.Get(context.Background(), "/v1/debug/stacks")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(stackBody), "goroutine") {
		t.Fatalf("unexpected stack response: %s", stackBody)
	}
	resetSource := &fakeResetSnapshotter{fakeSnapshotter: fakeSnapshotter{snapshot: source.snapshot}}
	resetSocket := testSocketPath(t)
	resetServer, err := NewServer(resetSocket, resetSource)
	if err != nil {
		t.Fatal(err)
	}
	resetCtx, resetCancel := context.WithCancel(context.Background())
	defer resetCancel()
	if err := resetServer.Start(resetCtx); err != nil {
		t.Fatal(err)
	}
	defer resetServer.Close(context.Background())
	resetClient, err := NewClient(resetSocket)
	if err != nil {
		t.Fatal(err)
	}
	resetBody, err := resetClient.PostJSON(context.Background(), "/v1/debug/reset", map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	if !resetSource.resetCalled || !strings.Contains(string(resetBody), `"vfs_reads"`) ||
		!strings.Contains(string(resetBody), `"debug_started_at"`) {
		t.Fatalf("unexpected reset response: called=%v body=%s", resetSource.resetCalled, resetBody)
	}
}

func TestServerExposesTasksWithItemsAndFilters(t *testing.T) {
	server, err := NewServer(testSocketPath(t), fakeSnapshotter{snapshot: diagnostics.DebugSnapshot{}})
	if err != nil {
		t.Fatal(err)
	}
	server.SetTaskDebugger(fakeTaskDebugger{
		tasks: []task.Task{{
			ID: "upload-1", Type: task.TypeUploadStreamBatch, State: task.StateWaitingInput,
			Detail: map[string]any{"recovered_from_journal": true},
		}, {
			ID: "download-1", Type: task.TypeDownload, State: task.StateSucceeded,
		}},
		items: map[string][]task.ItemResult{
			"upload-1": {{ItemID: "item-1", DestPath: "/photos/a.jpg", State: task.StateWaitingInput, ResumeOffset: 7}},
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := server.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer server.Close(context.Background())
	client, err := NewClient(server.endpoint)
	if err != nil {
		t.Fatal(err)
	}
	body, err := client.Get(context.Background(), "/v1/tasks?type=upload_stream_batch&state=waiting_input&limit=1")
	if err != nil {
		t.Fatal(err)
	}
	var response TasksResponse
	if err := json.Unmarshal(body, &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Tasks) != 1 || response.Tasks[0].Task.ID != "upload-1" || len(response.Tasks[0].Items) != 1 {
		t.Fatalf("unexpected tasks response: %s", body)
	}
	if response.Tasks[0].Items[0].ResumeOffset != 7 {
		t.Fatalf("missing item progress: %s", body)
	}
}

func TestServerExposesHTTPListenEndpoint(t *testing.T) {
	server, err := NewServer("127.0.0.1:0", fakeSnapshotter{snapshot: diagnostics.DebugSnapshot{Kind: "test"}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := server.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer server.Close(context.Background())

	listen := strings.TrimPrefix(server.ListenAddress(), "tcp:")
	client, err := NewClient("http://" + listen)
	if err != nil {
		t.Fatal(err)
	}
	body, err := client.Get(context.Background(), "/v1/health")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"ok": true`) {
		t.Fatalf("unexpected health response: %s", body)
	}
}

type degradedTaskPersistence struct{}

func (degradedTaskPersistence) TaskPersistenceError() error {
	return errors.New("task journal unavailable")
}

func TestServerHealthReportsTaskPersistenceDegraded(t *testing.T) {
	server, err := NewServer("127.0.0.1:0", fakeSnapshotter{snapshot: diagnostics.DebugSnapshot{Kind: "test"}})
	if err != nil {
		t.Fatal(err)
	}
	server.SetTaskPersistenceHealth(degradedTaskPersistence{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := server.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer server.Close(context.Background())

	client, err := NewClient("http://" + strings.TrimPrefix(server.ListenAddress(), "tcp:"))
	if err != nil {
		t.Fatal(err)
	}
	body, err := client.Get(context.Background(), "/v1/health")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"task_persistence"`) ||
		!strings.Contains(string(body), `"degraded": true`) ||
		!strings.Contains(string(body), `"task journal unavailable"`) {
		t.Fatalf("unexpected health response: %s", body)
	}
}

func TestServerFiltersDebugEndpointsByMount(t *testing.T) {
	socketPath := testSocketPath(t)
	source := fakeSnapshotter{snapshot: diagnostics.DebugSnapshot{
		SchemaVersion: diagnostics.DebugSnapshotSchemaVersion,
		GeneratedAt:   time.Unix(1, 0),
		Kind:          "namespace",
		Mounts: []diagnostics.MountSnapshot{{
			Identity: diagnostics.MountSnapshotIdentity{Name: "local", DriverName: "localfs"},
			Overlay: diagnostics.MountSnapshotOverlay{Pending: []vfs.PendingUpload{{
				Path: "/local.txt",
				FID:  "local",
				Size: 1,
			}}},
			UploadState: diagnostics.MountSnapshotUploads{Active: []upload.UploadSnapshot{{
				Path:  "/local-upload.txt",
				State: string(drive.UploadPhaseUploading),
			}}},
		}, {
			Identity: diagnostics.MountSnapshotIdentity{Name: "cloud", DriverName: "quark"},
			Overlay: diagnostics.MountSnapshotOverlay{Pending: []vfs.PendingUpload{{
				Path: "/cloud.txt",
				FID:  "cloud",
				Size: 2,
			}}},
			UploadState: diagnostics.MountSnapshotUploads{Active: []upload.UploadSnapshot{{
				Path:  "/cloud-upload.txt",
				State: string(drive.UploadPhaseUploading),
			}}},
		}},
	}}
	server, err := NewServer(socketPath, source)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := server.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer server.Close(context.Background())

	client, err := NewClient(socketPath)
	if err != nil {
		t.Fatal(err)
	}
	stateBody, err := client.Get(context.Background(), "/v1/state?mount=cloud")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(stateBody), `"name": "cloud"`) || strings.Contains(string(stateBody), `"name": "local"`) {
		t.Fatalf("unexpected filtered state: %s", stateBody)
	}
	pendingBody, err := client.Get(context.Background(), "/v1/pending?mount=cloud")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(pendingBody), `"/cloud/cloud.txt"`) || strings.Contains(string(pendingBody), `"/local/local.txt"`) {
		t.Fatalf("unexpected filtered pending: %s", pendingBody)
	}
	uploadsBody, err := client.Get(context.Background(), "/v1/uploads?mount=cloud")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(uploadsBody), `"/cloud/cloud-upload.txt"`) || strings.Contains(string(uploadsBody), `"/local/local-upload.txt"`) {
		t.Fatalf("unexpected filtered uploads: %s", uploadsBody)
	}
}

func TestDriverSpaceMarksUnsupportedWithoutError(t *testing.T) {
	source := fakeSnapshotter{drivers: []diagnostics.NamedDriver{{
		Name:   "webdav",
		Driver: fakeSpaceDriver{err: drive.ErrSpaceUnsupported},
	}}}
	server := &Server{source: source}

	spaces := server.driverSpaces(context.Background(), nil)
	space := spaces["webdav"]
	if space == nil {
		t.Fatal("missing webdav space summary")
	}
	if !space.Unsupported {
		t.Fatalf("unsupported = false, want true: %+v", space)
	}
	if space.Error != "" {
		t.Fatalf("error = %q, want empty", space.Error)
	}
	if space.Reason == "" {
		t.Fatalf("reason is empty: %+v", space)
	}
}

func TestServerExposesRecentEvents(t *testing.T) {
	oldLogger := logging.L
	testLogger, err := logging.New("debug", "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	logging.L = testLogger
	defer func() { logging.L = oldLogger }()

	socketPath := testSocketPath(t)
	server, err := NewServer(socketPath, fakeSnapshotter{snapshot: diagnostics.DebugSnapshot{}})
	if err != nil {
		t.Fatal(err)
	}
	if err := server.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer server.Close(context.Background())

	logging.L.Warnf("warn Cookie: ctoken=secret123")
	logging.L.Warnf("[FUSE] Read path=\"/local/file.txt\" errc=-5")
	logging.L.Warnf("[CACHE] put chunk failed fid=\"other\"")
	logging.L.Errorf("error msg")

	client, err := NewClient(socketPath)
	if err != nil {
		t.Fatal(err)
	}
	body, err := client.Get(context.Background(), "/v1/events?level=warn&limit=10")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "secret123") {
		t.Fatalf("event response leaked secret: %s", body)
	}
	if !strings.Contains(string(body), "Cookie: ***") || !strings.Contains(string(body), "error msg") {
		t.Fatalf("unexpected event response: %s", body)
	}
	filteredBody, err := client.Get(context.Background(), "/v1/events?level=warn&limit=10&path=/local/file.txt&component=FUSE")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(filteredBody), "[FUSE] Read") || strings.Contains(string(filteredBody), "[CACHE]") || strings.Contains(string(filteredBody), "error msg") {
		t.Fatalf("unexpected filtered event response: %s", filteredBody)
	}
}

func TestServerRemovesSocketOnClose(t *testing.T) {
	socketPath := testSocketPath(t)
	server, err := NewServer(socketPath, fakeSnapshotter{snapshot: diagnostics.DebugSnapshot{}})
	if err != nil {
		t.Fatal(err)
	}
	if err := server.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := server.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(socketPath); !os.IsNotExist(err) {
		t.Fatalf("expected socket to be removed, got %v", err)
	}
}

func testSocketPath(t *testing.T) string {
	t.Helper()
	path := filepath.Join(os.TempDir(), "qrypt-"+strconv.FormatInt(time.Now().UnixNano(), 10)+".sock")
	t.Cleanup(func() { _ = os.Remove(path) })
	return path
}
