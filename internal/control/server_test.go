package control

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/internal/logging"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

type fakeSnapshotter struct {
	snapshot vfs.DebugSnapshot
	drivers  []vfs.NamedDriver
}

func (f fakeSnapshotter) DebugSnapshot() vfs.DebugSnapshot {
	return f.snapshot
}

func (f fakeSnapshotter) Drivers() []vfs.NamedDriver {
	return f.drivers
}

type fakeSpaceDriver struct {
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

func (f fakeSnapshotter) RemoteList(ctx context.Context, path string) ([]drive.Entry, error) {
	return []drive.Entry{{
		ID:       "remote-id",
		ParentID: "parent-id",
		Name:     "uploaded.txt",
		Size:     7,
		ModTime:  time.Unix(2, 0),
	}}, nil
}

func (f fakeSnapshotter) DebugResolve(ctx context.Context, path string, includeRemoteName bool) (vfs.DebugResolveInfo, error) {
	info := vfs.DebugResolveInfo{
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
	if includeRemoteName {
		info.RemoteName = "encrypted-name"
	}
	return info, nil
}

func (f fakeSnapshotter) DebugConsistency(ctx context.Context, path string) (vfs.ConsistencyReport, error) {
	return vfs.ConsistencyReport{
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

func (f fakeSnapshotter) DebugStaging(ctx context.Context, path string) (vfs.DebugStagingReport, error) {
	return vfs.DebugStagingReport{
		Path: path,
		Mounts: []vfs.DebugStagingMount{{
			Mount:        "local",
			PendingCount: 1,
			StagingCount: 1,
			Bytes:        7,
			Files: []vfs.DebugStagingFile{{
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
	source := fakeSnapshotter{snapshot: vfs.DebugSnapshot{
		SchemaVersion: vfs.DebugSnapshotSchemaVersion,
		GeneratedAt:   time.Unix(1, 0),
		Kind:          "namespace",
		Process:       vfs.DebugProcess{PID: 1234, StartedAt: time.Unix(8, 0)},
		Mounts: []vfs.DebugMountSnapshot{{
			Name:       "local",
			DriverName: "localfs",
			Uploads: []vfs.DebugUpload{{
				OpID:          "file",
				Path:          "/file.txt",
				State:         "uploading",
				BytesTotal:    10,
				BytesUploaded: 4,
				UpdatedAt:     time.Unix(4, 0),
			}},
			UploadHistory: []vfs.DebugUpload{{
				OpID:          "old",
				Path:          "/old.txt",
				State:         "completed",
				BytesTotal:    5,
				BytesUploaded: 5,
				UpdatedAt:     time.Unix(5, 0),
				CompletedAt:   time.Unix(5, 0),
			}},
			Driver: &drive.DebugSnapshot{
				Driver:      "localfs",
				Health:      "ok",
				GeneratedAt: time.Unix(3, 0),
			},
			Pending: []vfs.PendingFile{{
				Path:       "/file.txt",
				FID:        "file",
				LocalPath:  "/tmp/file.staging",
				Size:       3,
				RetryCount: 1,
				LastError:  "boom",
			}},
			UploadQueueLength: 2,
			UploadQueueCap:    128,
			ReadCache: vfs.DebugReadCache{
				MaxBytes:   1024,
				ChunkCount: 1,
				Bytes:      512,
				Hits:       2,
				Misses:     1,
				Files:      []vfs.DebugReadCacheFile{{ID: "fid", ChunkCount: 1, Bytes: 512}, {ID: "remote-id", ChunkCount: 1, Bytes: 7}},
			},
		}},
	}, drivers: []vfs.NamedDriver{{
		Name:   "local",
		Driver: fakeSpaceDriver{space: drive.Space{Total: 2 * drive.GiB, Free: 1536 * drive.MiB}},
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
	var state vfs.DebugSnapshot
	if err := json.Unmarshal(stateBody, &state); err != nil {
		t.Fatal(err)
	}
	if state.Kind != "namespace" || len(state.Mounts) != 1 || state.Mounts[0].UploadQueueLength != 2 {
		t.Fatalf("unexpected state: %+v", state)
	}
	if state.Process.PID != 1234 || state.Mounts[0].DriverName != "localfs" {
		t.Fatalf("missing state metadata: %+v", state)
	}
	if strings.Contains(string(stateBody), `"upload_history"`) || strings.Contains(string(stateBody), `"ops":`) {
		t.Fatalf("state should not inline upload history or ops: %s", stateBody)
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
	if !strings.Contains(string(uploadHistoryBody), `"/local/old.txt"`) || !strings.Contains(string(uploadHistoryBody), `"state": "completed"`) {
		t.Fatalf("expected filtered upload history, got %s", uploadHistoryBody)
	}
	if strings.Contains(string(uploadHistoryBody), "/local/file.txt") {
		t.Fatalf("filtered upload history should not include other paths: %s", uploadHistoryBody)
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
	if !strings.Contains(string(cachePathBody), `"id": "remote-id"`) || strings.Contains(string(cachePathBody), `"id": "fid"`) {
		t.Fatalf("path cache response should include only resolved file: %s", cachePathBody)
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

	goroutineBody, err := client.Get(context.Background(), "/v1/goroutines")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(goroutineBody), "goroutine") {
		t.Fatalf("unexpected goroutine response: %s", goroutineBody)
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
	server, err := NewServer(socketPath, fakeSnapshotter{snapshot: vfs.DebugSnapshot{}})
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
	server, err := NewServer(socketPath, fakeSnapshotter{snapshot: vfs.DebugSnapshot{}})
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
