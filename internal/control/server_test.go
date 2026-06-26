package control

import (
	"context"
	"encoding/json"
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
}

func (f fakeSnapshotter) DebugSnapshot() vfs.DebugSnapshot {
	return f.snapshot
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
		RemoteID:  "remote-id",
		ParentID:  "parent-id",
		Size:      7,
	}
	if includeRemoteName {
		info.RemoteName = "encrypted-name"
	}
	return info, nil
}

func (f fakeSnapshotter) DebugTasks() []vfs.DebugTask {
	return []vfs.DebugTask{{Type: "upload", Path: "/local/file.txt", State: "uploading", OpID: "file"}}
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

func TestServerExposesStateAndPending(t *testing.T) {
	socketPath := testSocketPath(t)
	source := fakeSnapshotter{snapshot: vfs.DebugSnapshot{
		SchemaVersion: vfs.DebugSnapshotSchemaVersion,
		GeneratedAt:   time.Unix(1, 0),
		Kind:          "namespace",
		Mounts: []vfs.DebugMountSnapshot{{
			Name: "local",
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
				Files:      []vfs.DebugReadCacheFile{{ID: "fid", ChunkCount: 1, Bytes: 512}},
			},
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

	cacheBody, err := client.Get(context.Background(), "/v1/cache")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(cacheBody), `"hits": 2`) || !strings.Contains(string(cacheBody), `"id": "fid"`) {
		t.Fatalf("unexpected cache response: %s", cacheBody)
	}

	tasksBody, err := client.Get(context.Background(), "/v1/tasks")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(tasksBody), `"type": "upload"`) || !strings.Contains(string(tasksBody), `"state": "uploading"`) {
		t.Fatalf("unexpected tasks response: %s", tasksBody)
	}

	consistencyBody, err := client.Get(context.Background(), "/v1/consistency?path=/local/file.txt")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(consistencyBody), `"status": "ok"`) || !strings.Contains(string(consistencyBody), `"size_matches": true`) {
		t.Fatalf("unexpected consistency response: %s", consistencyBody)
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
