package p115open

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	sdk "github.com/OpenListTeam/115-sdk-go"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/drivers/internal/p115offline"
)

type openLocalTransport struct {
	endpoint *url.URL
	base     http.RoundTripper
}

func (r openLocalTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.URL.Scheme = r.endpoint.Scheme
	clone.URL.Host = r.endpoint.Host
	clone.Host = r.endpoint.Host
	return r.base.RoundTrip(clone)
}

func newLocalOpenDriver(t *testing.T, handler http.HandlerFunc) *Driver {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	endpoint, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	d := New(Options{RootID: "root"})
	d.cl = sdk.New()
	d.cl.SetHttpClient(&http.Client{Transport: openLocalTransport{endpoint: endpoint, base: server.Client().Transport}})
	return d
}

func writeOpenJSON(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Error(err)
	}
}

func TestP115OpenOfflineCodeMapping(t *testing.T) {
	t.Parallel()
	tests := []struct {
		code int64
		want drive.OfflineErrorCode
	}{
		{code: 10010, want: drive.OfflineErrQuotaExceeded},
		{code: 10004, want: drive.OfflineErrSourceInvalid},
		{code: 10008, want: drive.OfflineErrDuplicate},
		{code: 99, want: drive.OfflineErrAuthRequired},
		{code: 40100001, want: drive.OfflineErrAuthRequired},
		{code: 12345, want: drive.OfflineErrProvider},
	}
	for _, test := range tests {
		got := p115OpenOfflineCode(test.code, "message")
		offline, ok := drive.AsOfflineError(got)
		if !ok || offline.Code != test.want {
			t.Fatalf("code %d = %v, want %s", test.code, got, test.want)
		}
	}
}

func TestP115OpenOfflineAddSuccessAndPerItemError(t *testing.T) {
	t.Parallel()
	var addResponse any = map[string]any{"state": true, "data": []map[string]any{{
		"state": true, "code": 0, "info_hash": "hash-1",
	}}}
	d := newLocalOpenDriver(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/open/offline/add_task_urls" {
			t.Errorf("unexpected path %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		writeOpenJSON(t, w, addResponse)
	})
	created, err := d.offline().CreateOfflineDownload(t.Context(), drive.OfflineDownloadRequest{
		Source:        "magnet:?xt=urn:btih:hash-1",
		DestinationID: "dir",
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.ID != "hash-1" || created.Destination != "dir" {
		t.Fatalf("created = %+v", created)
	}

	addResponse = map[string]any{"state": true, "data": []map[string]any{{
		"state": false, "code": 10008, "message": "task existed",
	}}}
	_, err = d.offline().CreateOfflineDownload(t.Context(), drive.OfflineDownloadRequest{
		Source:        "magnet:?xt=urn:btih:duplicate",
		DestinationID: "dir",
	})
	offline, ok := drive.AsOfflineError(err)
	if !ok || offline.Code != drive.OfflineErrDuplicate {
		t.Fatalf("error = %v, want duplicate", err)
	}
}

func TestP115OpenOfflineLifecycle(t *testing.T) {
	t.Parallel()
	status := 0
	percent := 5
	deleted := false
	d := newLocalOpenDriver(t, func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		switch r.URL.Path {
		case "/open/offline/get_task_list":
			page := r.FormValue("page")
			var tasks []map[string]any
			if page == "2" {
				tasks = []map[string]any{{
					"info_hash": "hash-1", "name": "Resource", "size": 30,
					"status": status, "percentDone": percent,
					"file_id": "root", "wp_path_id": "dir",
				}}
			}
			writeOpenJSON(t, w, map[string]any{"state": true, "data": map[string]any{
				"page": 1, "page_count": 2, "count": 1, "tasks": tasks,
			}})
		case "/open/offline/del_task":
			deleted = true
			writeOpenJSON(t, w, map[string]any{"state": true, "data": []any{}})
		case "/open/ufile/files":
			cid := r.URL.Query().Get("cid")
			var files []map[string]any
			switch cid {
			case "dir":
				files = []map[string]any{{"fid": "root", "pid": "dir", "fc": "0", "fn": "Resource"}}
			case "root":
				files = []map[string]any{
					{"fid": "f2", "pid": "root", "fc": "1", "fn": "ep2.mkv", "fs": 20},
					{"fid": "sub", "pid": "root", "fc": "0", "fn": "sub"},
				}
			case "sub":
				files = []map[string]any{{"fid": "f1", "pid": "sub", "fc": "1", "fn": "ep1.mkv", "fs": 10}}
			default:
				t.Errorf("unexpected cid %q", cid)
			}
			writeOpenJSON(t, w, map[string]any{"state": true, "count": len(files), "data": files})
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			http.NotFound(w, r)
		}
	})
	adapter := d.offline()
	queued, err := adapter.GetOfflineDownload(t.Context(), "hash-1")
	if err != nil {
		t.Fatal(err)
	}
	if queued.State != drive.OfflineStateQueued || queued.Progress.Percent != 5 {
		t.Fatalf("queued = %+v", queued)
	}
	status = 1
	percent = 50
	running, err := adapter.GetOfflineDownload(t.Context(), "hash-1")
	if err != nil {
		t.Fatal(err)
	}
	if running.State != drive.OfflineStateRunning {
		t.Fatalf("running = %+v", running)
	}
	status = 2
	percent = 100
	done, err := adapter.GetOfflineDownload(t.Context(), "hash-1")
	if err != nil {
		t.Fatal(err)
	}
	if done.State != drive.OfflineStateSucceeded || !done.Available {
		t.Fatalf("done = %+v", done)
	}
	wantPaths := []string{"Resource/ep2.mkv", "Resource/sub/ep1.mkv"}
	if len(done.Files) != len(wantPaths) {
		t.Fatalf("files = %+v", done.Files)
	}
	for index, want := range wantPaths {
		if done.Files[index].Path != want {
			t.Fatalf("file %d path = %q, want %q", index, done.Files[index].Path, want)
		}
	}
	if err := adapter.CancelOfflineDownload(t.Context(), "hash-1"); err != nil {
		t.Fatal(err)
	}
	if !deleted {
		t.Fatal("delete task was not called")
	}
}

func TestP115OpenOfflineSingleFile(t *testing.T) {
	t.Parallel()
	d := newLocalOpenDriver(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/open/offline/get_task_list":
			writeOpenJSON(t, w, map[string]any{"state": true, "data": map[string]any{
				"page": 1, "page_count": 1, "count": 1,
				"tasks": []map[string]any{{"info_hash": "hash-1", "status": 2, "file_id": "file-1", "wp_path_id": "dir"}},
			}})
		case "/open/ufile/files":
			cid := r.URL.Query().Get("cid")
			files := []map[string]any{}
			if cid == "dir" {
				files = []map[string]any{{"fid": "file-1", "pid": "dir", "fc": "1", "fn": "movie.mkv", "fs": 42}}
			}
			writeOpenJSON(t, w, map[string]any{"state": true, "count": len(files), "data": files})
		default:
			http.NotFound(w, r)
		}
	})
	got, err := d.offline().GetOfflineDownload(t.Context(), "hash-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Files) != 1 || got.Files[0].Path != "movie.mkv" || got.Files[0].Size != 42 {
		t.Fatalf("files = %+v", got.Files)
	}
}

func TestP115OpenOfflineMissingTask(t *testing.T) {
	t.Parallel()
	d := newLocalOpenDriver(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/open/offline/get_task_list" {
			http.NotFound(w, r)
			return
		}
		writeOpenJSON(t, w, map[string]any{"state": true, "data": map[string]any{
			"page": 1, "page_count": 1, "count": 0, "tasks": []any{},
		}})
	})
	_, err := d.offline().GetOfflineDownload(t.Context(), "missing")
	offline, ok := drive.AsOfflineError(err)
	if !ok || offline.Code != drive.OfflineErrProviderTaskMissing {
		t.Fatalf("error = %v, want task missing", err)
	}
}

func TestP115OpenOfflineDownloadTarget(t *testing.T) {
	t.Parallel()
	d := newLocalOpenDriver(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/open/folder/get_info":
			writeOpenJSON(t, w, map[string]any{"state": true, "data": map[string]any{
				"file_id": "file-1", "file_name": "x", "pick_code": "pc-1",
				"paths": []map[string]any{{"file_id": "root", "file_name": "root"}, {"file_id": "dir", "file_name": "dir"}},
			}})
		case "/open/ufile/downurl":
			writeOpenJSON(t, w, map[string]any{"state": true, "data": map[string]any{
				"file-1": map[string]any{"file_name": "x", "file_size": 42, "pick_code": "pc-1", "url": map[string]any{"url": "https://cdn.example/file"}},
			}})
		default:
			http.NotFound(w, r)
		}
	})
	got, err := d.offline().OfflineDownloadURL(t.Context(), drive.OfflineFetchRequest{
		Files: []drive.OfflineFile{{RemoteID: "file-1"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.URL != "https://cdn.example/file" || got.Size != 42 || got.Headers["User-Agent"] != downloadUserAgent {
		t.Fatalf("target = %+v", got)
	}
}

func TestP115OpenRemoveResolvesParent(t *testing.T) {
	t.Parallel()
	var parentID string
	d := newLocalOpenDriver(t, func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		switch r.URL.Path {
		case "/open/folder/get_info":
			writeOpenJSON(t, w, map[string]any{"state": true, "data": map[string]any{
				"file_id": "file-1", "file_name": "x", "pick_code": "pc-1",
				"paths": []map[string]any{{"file_id": "root", "file_name": "root"}, {"file_id": "dir", "file_name": "dir"}},
			}})
		case "/open/ufile/delete":
			parentID = r.FormValue("parent_id")
			writeOpenJSON(t, w, map[string]any{"state": true, "data": []any{}})
		default:
			http.NotFound(w, r)
		}
	})
	if err := d.Remove(t.Context(), drive.Entry{ID: "file-1"}); err != nil {
		t.Fatal(err)
	}
	if parentID != "dir" {
		t.Fatalf("parent_id = %q, want dir", parentID)
	}
}

func TestP115OpenOfflineErrorMapping(t *testing.T) {
	t.Parallel()
	got := p115OpenOfflineError(errors.New("network down"), true)
	offline, ok := drive.AsOfflineError(got)
	if !ok || offline.Code != drive.OfflineErrProviderUnavailable || !offline.Retryable {
		t.Fatalf("error = %v, want retryable provider unavailable", got)
	}
	got = p115OpenOfflineError(&sdk.Error{Code: 10010, Message: "quota"}, false)
	offline, ok = drive.AsOfflineError(got)
	if !ok || offline.Code != drive.OfflineErrQuotaExceeded {
		t.Fatalf("error = %v, want quota", got)
	}
}

var _ p115offline.Backend = p115OpenOfflineBackend{}
