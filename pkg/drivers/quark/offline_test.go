package quark

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

// offlineTestServer is a fake Quark host that records the decrypted offline
// requests and answers with provider-shaped responses.
type offlineTestServer struct {
	t *testing.T

	parse       map[string]any
	submit      map[string]any
	taskStatus  map[string]any
	downloadReq map[string]any

	taskID      string
	session     string
	downloadURL string
}

func (s *offlineTestServer) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/offline/download/parse":
			s.parse = s.decodeWG(r)
			writeJSON(s.t, w, map[string]any{
				"status": 200, "code": 0,
				"token": "parse-token_m", "finished": true,
				"title": "Example", "total_size": 1234,
				"files": []map[string]any{
					{"file_no": 0, "file_name": "ep1.mkv", "size": 1000},
					{"file_no": 1, "file_name": "ep2.mkv", "size": 234},
				},
				"resource_info": map[string]any{"download_task_finish": 1},
			})
		case "/offline/download/submit":
			s.submit = s.decodeWG(r)
			data := map[string]any{"task_id": s.taskID, "fid": "fid-1", "dir_fid": "dir-1"}
			if s.session != "" {
				data["speedup_dl_info"] = map[string]any{"speedup_session": s.session}
			}
			writeJSON(s.t, w, map[string]any{"status": 200, "code": 0, "data": data})
		case "/file/system_path":
			// The provider reports the directory through path_files; only some
			// versions also echo a top-level fid.
			writeJSON(s.t, w, map[string]any{"status": 200, "code": 0, "data": map[string]any{
				"path":       "来自：BT磁力链下载",
				"path_files": []map[string]any{{"fid": "system-dir", "pdir_fid": "0"}},
			}})
		case "/task":
			writeJSON(s.t, w, map[string]any{
				"status": 200, "code": 0,
				"data": map[string]any{
					"task_id": r.URL.Query().Get("task_id"), "status": 2, "progress": 1000,
					"offdl_result": map[string]any{"list": []map[string]any{
						{"fid": "fid-1", "file_name": "ep1.mkv", "size": 1000},
					}},
				},
			})
		case "/file/download":
			_ = json.NewDecoder(r.Body).Decode(&s.downloadReq)
			writeJSON(s.t, w, map[string]any{"status": 200, "code": 0, "data": []map[string]any{{"download_url": s.downloadURL}}})
		case "/task/status":
			_ = json.NewDecoder(r.Body).Decode(&s.taskStatus)
			writeJSON(s.t, w, map[string]any{"status": 200, "code": 0})
		default:
			s.t.Fatalf("unexpected quark path %s", r.URL.Path)
		}
	})
}

func (s *offlineTestServer) decodeWG(r *http.Request) map[string]any {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		s.t.Fatalf("read body: %v", err)
	}
	if r.Header.Get("X-U-Content-Encoding") != "wg" {
		s.t.Fatalf("offline request %s missing wg marker", r.URL.Path)
	}
	plain, err := wgDecrypt(string(body), nil)
	if err != nil {
		s.t.Fatalf("decrypt wg body: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(plain, &decoded); err != nil {
		s.t.Fatalf("decode wg body: %v", err)
	}
	return decoded
}

// TestCreateOfflineDownloadParseAndSubmit verifies the unified parse + submit
// path for a magnet source, including the wg-encrypted request bodies.
func TestCreateOfflineDownloadParseAndSubmit(t *testing.T) {
	t.Parallel()

	fake := &offlineTestServer{t: t, taskID: "task-42", downloadURL: "https://cdn.example/x"}
	server := httptest.NewServer(fake.handler())
	defer server.Close()
	driver := New("k=v", Options{BaseURL: server.URL, V2URL: server.URL})

	got, err := driver.CreateOfflineDownload(context.Background(), drive.OfflineDownloadRequest{
		SourceKind:  drive.OfflineSourceMagnet,
		Source:      "magnet:?xt=urn:btih:abcdef",
		Destination: "/",
	})
	if err != nil {
		t.Fatalf("CreateOfflineDownload: %v", err)
	}
	if got.ID != "task-42" {
		t.Fatalf("task id = %q, want task-42", got.ID)
	}
	if got.State != drive.OfflineStateQueued {
		t.Fatalf("state = %q, want queued", got.State)
	}
	if fake.parse["parse_mode"] != float64(0) {
		t.Fatalf("parse_mode = %v, want 0", fake.parse["parse_mode"])
	}
	if fake.parse["url"] != "magnet:?xt=urn:btih:abcdef" {
		t.Fatalf("parse url = %v", fake.parse["url"])
	}
	if fake.submit["pdir_fid"] != "0" {
		t.Fatalf("submit pdir_fid = %v, want root id 0", fake.submit["pdir_fid"])
	}
	selected, ok := fake.submit["selected_files"].([]any)
	if !ok || len(selected) != 2 {
		t.Fatalf("selected_files = %v, want both files", fake.submit["selected_files"])
	}
}

// TestCreateOfflineDownloadDefaultStagingDirectory verifies an empty parse
// destination resolves through the provider's system path.
func TestCreateOfflineDownloadDefaultStagingDirectory(t *testing.T) {
	t.Parallel()

	fake := &offlineTestServer{t: t, taskID: "task-1", downloadURL: "https://cdn.example/x"}
	server := httptest.NewServer(fake.handler())
	defer server.Close()
	driver := New("k=v", Options{BaseURL: server.URL, V2URL: server.URL})

	if _, err := driver.CreateOfflineDownload(context.Background(), drive.OfflineDownloadRequest{
		SourceKind: drive.OfflineSourceMagnet,
		Source:     "magnet:?xt=urn:btih:abcdef",
	}); err != nil {
		t.Fatalf("CreateOfflineDownload: %v", err)
	}
	if fake.submit["pdir_fid"] != "system-dir" {
		t.Fatalf("submit pdir_fid = %v, want system-dir", fake.submit["pdir_fid"])
	}
}

// TestOfflineStateMapping pins the provider status → canonical state mapping.
func TestOfflineStateMapping(t *testing.T) {
	t.Parallel()

	cases := []struct {
		status int
		want   drive.OfflineState
	}{
		{0, drive.OfflineStateQueued},
		{1, drive.OfflineStateRunning},
		{2, drive.OfflineStateSucceeded},
		{3, drive.OfflineStateFailed},
		{4, drive.OfflineStateCanceled},
	}
	for _, tc := range cases {
		if got := quarkOfflineState(tc.status); got != tc.want {
			t.Fatalf("quarkOfflineState(%d) = %q, want %q", tc.status, got, tc.want)
		}
	}
}

// TestOfflineDownloadAcceleratedAndFallback verifies the accelerated session is
// used when present and degrees to the normal URL when it is absent.
func TestOfflineDownloadAcceleratedAndFallback(t *testing.T) {
	t.Parallel()

	fake := &offlineTestServer{t: t, taskID: "task-7", session: "session-xyz", downloadURL: "https://cdn.example/accel"}
	server := httptest.NewServer(fake.handler())
	defer server.Close()
	driver := New("k=v", Options{BaseURL: server.URL, V2URL: server.URL})

	if _, err := driver.CreateOfflineDownload(context.Background(), drive.OfflineDownloadRequest{
		SourceKind:  drive.OfflineSourceMagnet,
		Source:      "magnet:?xt=urn:btih:abcdef",
		Destination: "/",
		Accelerated: true,
	}); err != nil {
		t.Fatalf("CreateOfflineDownload: %v", err)
	}
	if fake.submit["parse_mode"] != float64(parseModeSpeedup) {
		t.Fatalf("submit parse_mode = %v, want %d", fake.submit["parse_mode"], parseModeSpeedup)
	}
	target, err := driver.OfflineDownloadURL(context.Background(), drive.OfflineFetchRequest{
		ID:          "task-7",
		Files:       []drive.OfflineFile{{RemoteID: "fid-1"}},
		Accelerated: true,
	})
	if err != nil {
		t.Fatalf("OfflineDownloadURL accelerated: %v", err)
	}
	if target.URL != "https://cdn.example/accel" {
		t.Fatalf("accelerated url = %q", target.URL)
	}
	if fake.downloadReq["speedup_session"] != "session-xyz" {
		t.Fatalf("download request session = %v", fake.downloadReq["speedup_session"])
	}

	// No session: an accelerated request degrades to a typed error the caller
	// interprets as "wait for completion".
	if _, err := driver.OfflineDownloadURL(context.Background(), drive.OfflineFetchRequest{
		ID:          "other-task",
		Files:       []drive.OfflineFile{{RemoteID: "fid-1"}},
		Accelerated: true,
	}); err == nil {
		t.Fatal("accelerated URL without session should degrade with an error")
	}
	// The normal path still works.
	if _, err := driver.OfflineDownloadURL(context.Background(), drive.OfflineFetchRequest{
		ID:    "other-task",
		Files: []drive.OfflineFile{{RemoteID: "fid-1"}},
	}); err != nil {
		t.Fatalf("OfflineDownloadURL normal: %v", err)
	}
}

// TestInspectOfflinePreview verifies inspection parses files without creating a
// task.
func TestInspectOfflinePreview(t *testing.T) {
	t.Parallel()

	fake := &offlineTestServer{t: t, taskID: "task-x", downloadURL: "https://cdn.example/x"}
	server := httptest.NewServer(fake.handler())
	defer server.Close()
	driver := New("k=v", Options{BaseURL: server.URL, V2URL: server.URL})

	preview, err := driver.InspectOfflineDownload(context.Background(), drive.OfflineDownloadRequest{
		SourceKind: drive.OfflineSourceMagnet,
		Source:     "magnet:?xt=urn:btih:abcdef",
	})
	if err != nil {
		t.Fatalf("InspectOfflineDownload: %v", err)
	}
	if len(preview.Files) != 2 || preview.TotalSize != 1234 {
		t.Fatalf("preview = %+v", preview)
	}
	if !preview.InstantSave {
		t.Fatal("instant save prediction should be true")
	}
	if fake.submit != nil {
		t.Fatal("inspection must not submit a task")
	}
}

// TestOfflineDownloadURLHeaders verifies the signed CDN URL is returned with
// the cookie and browser-like headers the CDN requires (a bare request gets a
// 403).
func TestOfflineDownloadURLHeaders(t *testing.T) {
	t.Parallel()

	fake := &offlineTestServer{t: t, taskID: "task-1", downloadURL: "https://cdn.example/u"}
	server := httptest.NewServer(fake.handler())
	defer server.Close()
	driver := New("k=v", Options{BaseURL: server.URL, V2URL: server.URL})

	target, err := driver.OfflineDownloadURL(context.Background(), drive.OfflineFetchRequest{
		ID:    "task-1",
		Files: []drive.OfflineFile{{RemoteID: "fid-1"}},
	})
	if err != nil {
		t.Fatalf("OfflineDownloadURL: %v", err)
	}
	if target.Headers["Cookie"] == "" {
		t.Fatalf("download target must carry the cookie: %+v", target.Headers)
	}
	if target.Headers["User-Agent"] == "" || target.Headers["Referer"] == "" {
		t.Fatalf("download target must carry browser headers: %+v", target.Headers)
	}
}

// TestOfflineCancelAndRetry verifies the op_type used for delete and in-place
// retry.
func TestOfflineCancelAndRetry(t *testing.T) {
	t.Parallel()

	fake := &offlineTestServer{t: t, taskID: "task-1", downloadURL: "https://cdn.example/x"}
	server := httptest.NewServer(fake.handler())
	defer server.Close()
	driver := New("k=v", Options{BaseURL: server.URL, V2URL: server.URL})

	if err := driver.CancelOfflineDownload(context.Background(), "task-1"); err != nil {
		t.Fatalf("CancelOfflineDownload: %v", err)
	}
	if fake.taskStatus["op_type"] != float64(0) {
		t.Fatalf("cancel op_type = %v, want 0", fake.taskStatus["op_type"])
	}
	if err := driver.RetryOfflineDownload(context.Background(), "task-1"); err != nil {
		t.Fatalf("RetryOfflineDownload: %v", err)
	}
	if fake.taskStatus["op_type"] != float64(1) {
		t.Fatalf("retry op_type = %v, want 1", fake.taskStatus["op_type"])
	}
}

// TestOfflineSystemPathDirectoryFid pins both response shapes the provider uses
// for the default staging directory.
func TestOfflineSystemPathDirectoryFid(t *testing.T) {
	t.Parallel()

	raw := []byte(`{"status":200,"code":0,"data":{"path":"来自：BT磁力链下载","path_files":[{"fid":"dir-a"},{"fid":"dir-b"}]}}`)
	var resp offlineSystemPathResp
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got := resp.dirFid(); got != "dir-b" {
		t.Fatalf("path_files fid = %q, want the last entry dir-b", got)
	}

	var legacy offlineSystemPathResp
	if err := json.Unmarshal([]byte(`{"status":200,"code":0,"data":{"fid":"dir-c"}}`), &legacy); err != nil {
		t.Fatalf("unmarshal legacy: %v", err)
	}
	if got := legacy.dirFid(); got != "dir-c" {
		t.Fatalf("legacy fid = %q, want dir-c", got)
	}

	var empty offlineSystemPathResp
	if err := json.Unmarshal([]byte(`{"status":200,"code":0,"data":{}}`), &empty); err != nil {
		t.Fatalf("unmarshal empty: %v", err)
	}
	if got := empty.dirFid(); got != "" {
		t.Fatalf("empty fid = %q, want empty", got)
	}
}
