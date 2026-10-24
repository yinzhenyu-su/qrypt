package p115

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	driver115 "github.com/SheltonZhu/115driver/pkg/driver"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/drivers/internal/p115offline"
)

type fakeP115OfflineAPI struct {
	addHashes []string
	addErr    error
	addCalls  []string
	addOpts   []driver115.OfflineOption

	list      []driver115.OfflineTaskResp
	listCalls []int64
	err       error

	deleteErr   error
	deleteCalls []string

	file    *driver115.File
	fileErr error
	info    *driver115.DownloadInfo
	infoErr error
}

func (f *fakeP115OfflineAPI) AddOfflineTaskURIs(uris []string, saveDirID string, opts ...driver115.OfflineOption) ([]string, error) {
	f.addCalls = append(f.addCalls, saveDirID)
	f.addOpts = append(f.addOpts, opts...)
	if f.addErr != nil {
		return nil, f.addErr
	}
	return f.addHashes, nil
}

func (f *fakeP115OfflineAPI) ListOfflineTask(page int64) (driver115.OfflineTaskResp, error) {
	f.listCalls = append(f.listCalls, page)
	if f.err != nil {
		return driver115.OfflineTaskResp{}, f.err
	}
	if len(f.list) == 0 {
		return driver115.OfflineTaskResp{}, nil
	}
	return f.list[0], nil
}

func (f *fakeP115OfflineAPI) DeleteOfflineTasks(hashes []string, deleteFiles bool) error {
	f.deleteCalls = append(f.deleteCalls, hashes...)
	return f.deleteErr
}

func (f *fakeP115OfflineAPI) GetFile(string) (*driver115.File, error) {
	if f.fileErr != nil {
		return nil, f.fileErr
	}
	return f.file, nil
}

func (f *fakeP115OfflineAPI) DownloadWithUA(string, string) (*driver115.DownloadInfo, error) {
	if f.infoErr != nil {
		return nil, f.infoErr
	}
	return f.info, nil
}

func TestP115OfflineErrorMapping(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		err   error
		want  drive.OfflineErrorCode
		retry bool
	}{
		{name: "quota", err: driver115.ErrOfflineNoTimes, want: drive.OfflineErrQuotaExceeded},
		{name: "invalid source", err: driver115.ErrOfflineInvalidLink, want: drive.OfflineErrSourceInvalid},
		{name: "duplicate", err: driver115.ErrOfflineTaskExisted, want: drive.OfflineErrDuplicate},
		{name: "auth", err: driver115.ErrNotLogin, want: drive.OfflineErrAuthRequired},
		{name: "unknown create", err: errors.New("boom"), want: drive.OfflineErrProvider},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := p115OfflineError(test.err, false)
			offline, ok := drive.AsOfflineError(got)
			if !ok || offline.Code != test.want || offline.Retryable != test.retry {
				t.Fatalf("error = %v, want %s (retryable=%v)", got, test.want, test.retry)
			}
		})
	}
	got := p115OfflineError(errors.New("temporary"), true)
	offline, ok := drive.AsOfflineError(got)
	if !ok || offline.Code != drive.OfflineErrProviderUnavailable || !offline.Retryable {
		t.Fatalf("observation error = %v, want retryable provider unavailable", got)
	}
	raw := errors.New(`{"data":"ciphertext","errcode":10010,"error_msg":"quota","state":false}: unexpected error`)
	got = p115OfflineError(raw, false)
	offline, ok = drive.AsOfflineError(got)
	if !ok || offline.Code != drive.OfflineErrQuotaExceeded || offline.Message != "115: offline quota is exhausted" {
		t.Fatalf("raw provider error = %v, want quota", got)
	}
}

func TestP115OfflineAddRejectsEmptyHash(t *testing.T) {
	t.Parallel()
	d := New(Options{})
	api := &fakeP115OfflineAPI{addHashes: []string{""}}
	adapter := p115offline.New(p115OfflineBackend{driver: d, api: api})
	_, err := adapter.CreateOfflineDownload(t.Context(), drive.OfflineDownloadRequest{
		Source:        "magnet:?xt=urn:btih:hash",
		DestinationID: "dir",
	})
	offline, ok := drive.AsOfflineError(err)
	if !ok || offline.Code != drive.OfflineErrProvider {
		t.Fatalf("error = %v, want provider error", err)
	}
	if len(api.addCalls) != 1 || api.addCalls[0] != "dir" {
		t.Fatalf("add calls = %v", api.addCalls)
	}
}

func TestP115OfflineLifecycle(t *testing.T) {
	t.Parallel()
	d := newLocalSDKDriver(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/files" {
			t.Errorf("unexpected path %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		cid := r.URL.Query().Get("cid")
		var files []map[string]any
		switch cid {
		case "dir":
			files = []map[string]any{{"cid": "root", "pid": "dir", "n": "Resource"}}
		case "root":
			files = []map[string]any{
				{"fid": "f2", "cid": "root", "n": "ep2.mkv", "s": "20"},
				{"cid": "sub", "pid": "root", "n": "sub"},
			}
		case "sub":
			files = []map[string]any{{"fid": "f1", "cid": "sub", "n": "ep1.mkv", "s": "10"}}
		default:
			t.Errorf("unexpected cid %q", cid)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"state": true, "cid": cid, "count": len(files), "data": files})
	})
	api := &fakeP115OfflineAPI{
		addHashes: []string{"hash-1"},
		list: []driver115.OfflineTaskResp{{
			BasicResp: driver115.BasicResp{State: true},
			PageCount: 1,
			Tasks: []*driver115.OfflineTask{{
				InfoHash: "hash-1", Name: "Resource", Size: 30, Status: 0, Percent: 5,
				FileId: "root", DirId: "dir",
			}},
		}},
	}
	adapter := p115offline.New(p115OfflineBackend{driver: d, api: api})
	created, err := adapter.CreateOfflineDownload(t.Context(), drive.OfflineDownloadRequest{
		SourceKind:    drive.OfflineSourceMagnet,
		Source:        "magnet:?xt=urn:btih:hash-1",
		DestinationID: "dir",
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.ID != "hash-1" || created.Destination != "dir" {
		t.Fatalf("created = %+v", created)
	}
	if len(api.addOpts) == 0 {
		t.Fatal("offline add did not pass the app version option")
	}
	queued, err := adapter.GetOfflineDownload(t.Context(), "hash-1")
	if err != nil {
		t.Fatal(err)
	}
	if queued.State != drive.OfflineStateQueued {
		t.Fatalf("queued = %+v", queued)
	}
	if len(api.listCalls) == 0 || api.listCalls[0] != 0 {
		t.Fatalf("first provider page = %v, want zero-based page 0", api.listCalls)
	}
	api.list[0].Tasks[0].Status = 1
	running, err := adapter.GetOfflineDownload(t.Context(), "hash-1")
	if err != nil {
		t.Fatal(err)
	}
	if running.State != drive.OfflineStateRunning {
		t.Fatalf("running = %+v", running)
	}
	api.list[0].Tasks[0].Status = 2
	api.list[0].Tasks[0].Percent = 100
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
}

func TestP115OfflineSingleFile(t *testing.T) {
	t.Parallel()
	d := newLocalSDKDriver(t, func(w http.ResponseWriter, r *http.Request) {
		cid := r.URL.Query().Get("cid")
		files := []map[string]any{}
		if cid == "dir" {
			files = []map[string]any{{"fid": "file-1", "cid": "dir", "n": "movie.mkv", "s": "42"}}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"state": true, "cid": cid, "count": len(files), "data": files})
	})
	api := &fakeP115OfflineAPI{list: []driver115.OfflineTaskResp{{
		BasicResp: driver115.BasicResp{State: true},
		PageCount: 1,
		Tasks: []*driver115.OfflineTask{{
			InfoHash: "hash-1", Status: 2, FileId: "file-1", DirId: "dir",
		}},
	}}}
	got, err := p115offline.New(p115OfflineBackend{driver: d, api: api}).GetOfflineDownload(t.Context(), "hash-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Files) != 1 || got.Files[0].Path != "movie.mkv" || got.Files[0].Size != 42 {
		t.Fatalf("files = %+v", got.Files)
	}
}

func TestP115OfflineFakeServerAddDuplicate(t *testing.T) {
	t.Parallel()
	d := newLocalSDKDriver(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/lixianssp/" {
			t.Errorf("unexpected path %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"state":false,"errno":10008,"error":"task existed"}`))
	})
	d.cl.UserID = 1
	_, err := d.offline().CreateOfflineDownload(t.Context(), drive.OfflineDownloadRequest{
		Source:        "magnet:?xt=urn:btih:duplicate",
		DestinationID: "dir",
	})
	offline, ok := drive.AsOfflineError(err)
	if !ok || offline.Code != drive.OfflineErrDuplicate {
		t.Fatalf("error = %v, want duplicate", err)
	}
}

func TestP115OfflineFakeServerListAndDelete(t *testing.T) {
	t.Parallel()
	var deleted bool
	d := newLocalSDKDriver(t, func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		switch r.URL.Query().Get("ac") {
		case "task_lists":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"state": true, "page_count": 1, "count": 1,
				"tasks": []map[string]any{{
					"info_hash": "hash-1", "name": "Resource", "size": 30,
					"status": 1, "percentDone": 50, "file_id": "root", "wp_path_id": "dir",
				}},
			})
		case "task_del":
			deleted = true
			_ = json.NewEncoder(w).Encode(map[string]any{"state": true})
		default:
			t.Errorf("unexpected action %q", r.URL.Query().Get("ac"))
		}
	})
	adapter := d.offline()
	got, err := adapter.GetOfflineDownload(t.Context(), "hash-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.State != drive.OfflineStateRunning || got.Progress.Percent != 50 {
		t.Fatalf("observation = %+v", got)
	}
	if err := adapter.CancelOfflineDownload(t.Context(), "hash-1"); err != nil {
		t.Fatal(err)
	}
	if !deleted {
		t.Fatal("task_del was not called")
	}
}

func TestP115OfflineDownloadTarget(t *testing.T) {
	t.Parallel()
	api := &fakeP115OfflineAPI{
		file: &driver115.File{PickCode: "pc-1"},
		info: &driver115.DownloadInfo{
			Url:      driver115.FileDownloadUrl{Url: "https://cdn.example/file"},
			FileSize: 42,
			Header:   http.Header{"User-Agent": {"115Browser"}},
		},
	}
	adapter := p115offline.New(p115OfflineBackend{driver: New(Options{}), api: api})
	got, err := adapter.OfflineDownloadURL(t.Context(), drive.OfflineFetchRequest{
		Files: []drive.OfflineFile{{RemoteID: "file-1"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.URL != "https://cdn.example/file" || got.Size != 42 || got.Headers["User-Agent"] != "115Browser" {
		t.Fatalf("target = %+v", got)
	}
}

func TestP115OfflineObservationErrorIsRetryable(t *testing.T) {
	t.Parallel()
	api := &fakeP115OfflineAPI{err: errors.New("network down")}
	adapter := p115offline.New(p115OfflineBackend{driver: New(Options{}), api: api})
	_, err := adapter.GetOfflineDownload(t.Context(), "hash-1")
	offline, ok := drive.AsOfflineError(err)
	if !ok || offline.Code != drive.OfflineErrProviderUnavailable || !offline.Retryable {
		t.Fatalf("error = %v, want retryable provider unavailable", err)
	}
}

var _ p115OfflineAPI = (*fakeP115OfflineAPI)(nil)
