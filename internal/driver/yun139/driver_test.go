package yun139

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/osutil"
)

// testAuth encodes an authorization string like base64(":account:token").
func testAuth(account, token string) string {
	return base64.StdEncoding.EncodeToString([]byte(":" + account + ":" + token))
}

func testAuthExpiring(account string, expiry time.Time) string {
	token := fmt.Sprintf("token|a|b|%d", expiry.UnixMilli())
	return testAuth(account, token)
}

func writeJSON(t *testing.T, w http.ResponseWriter, v interface{}) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Fatal(err)
	}
}

func TestRegister(t *testing.T) {
	drv, err := drive.New("yun139", drive.Params{
		"authorization": testAuth("test", "token"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if drv == nil {
		t.Fatal("driver is nil")
	}
	_ = drv
}

func TestRegisterMissingAuth(t *testing.T) {
	_, err := drive.New("yun139", drive.Params{})
	if err == nil {
		t.Fatal("expected error for missing authorization")
	}
}

func TestInit(t *testing.T) {
	// Route policy server.
	routeServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, map[string]interface{}{
			"data": map[string]interface{}{
				"routePolicyList": []map[string]interface{}{
					{"modName": "personal", "httpsUrl": "https://personal-cloud.example.com"},
				},
			},
		})
	}))
	defer routeServer.Close()

	// Override route URL by making the client point to our server.
	// We can't easily mock, so just test that New + Drop works.
	drv := New(testAuth("test", "token"), "/", "")
	_ = drv.Drop(context.Background())
}

// fakePersonalServer creates a test server that handles /file/list etc.
func fakePersonalServer(t *testing.T, handler func(w http.ResponseWriter, r *http.Request)) (*httptest.Server, *Driver) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		handler(w, r)
	}))

	drv := New(testAuth("test", "token"), "/", "")
	// Bypass route discovery by setting the host directly.
	drv.cl.mu.Lock()
	drv.cl.personalCloudHost = server.URL
	drv.cl.mu.Unlock()
	return server, drv
}

func useUserAPIServer(t *testing.T, server *httptest.Server) {
	t.Helper()
	old := userAPIBaseURL
	userAPIBaseURL = server.URL
	t.Cleanup(func() {
		userAPIBaseURL = old
	})
}

func TestList(t *testing.T) {
	server, drv := fakePersonalServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/file/list" {
			writeJSON(t, w, map[string]interface{}{
				"success": true,
				"data": map[string]interface{}{
					"nextPageCursor": "",
					"items": []map[string]interface{}{
						{
							"fileId":    "f1",
							"name":      "doc.txt",
							"type":      "file",
							"size":      1024,
							"updatedAt": "2024-01-15T10:30:00.000+08:00",
						},
						{
							"fileId":    "d1",
							"name":      "folder1",
							"type":      "folder",
							"size":      0,
							"updatedAt": "2024-01-15T10:30:00.000+08:00",
						},
					},
				},
			})
		}
	})
	defer server.Close()

	entries, err := drv.List(context.Background(), "/")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	if entries[0].ID != "f1" || entries[0].Name != "doc.txt" || entries[0].IsDir || entries[0].Size != 1024 {
		t.Errorf("unexpected file entry: %+v", entries[0])
	}
	if entries[1].ID != "d1" || entries[1].Name != "folder1" || !entries[1].IsDir {
		t.Errorf("unexpected dir entry: %+v", entries[1])
	}
}

func TestListPaginated(t *testing.T) {
	var callCount int
	server, drv := fakePersonalServer(t, func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if callCount == 1 {
			writeJSON(t, w, map[string]interface{}{
				"success": true,
				"data": map[string]interface{}{
					"nextPageCursor": "cursor2",
					"items": []map[string]interface{}{
						{"fileId": "f1", "name": "a.txt", "type": "file", "size": 1, "updatedAt": "2024-01-15T10:30:00.000+08:00"},
					},
				},
			})
		} else {
			writeJSON(t, w, map[string]interface{}{
				"success": true,
				"data": map[string]interface{}{
					"nextPageCursor": "",
					"items": []map[string]interface{}{
						{"fileId": "f2", "name": "b.txt", "type": "file", "size": 2, "updatedAt": "2024-01-15T10:30:00.000+08:00"},
					},
				},
			})
		}
	})
	defer server.Close()

	entries, err := drv.List(context.Background(), "/")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	if callCount != 2 {
		t.Fatalf("expected 2 list calls, got %d", callCount)
	}
}

func TestListFailed(t *testing.T) {
	server, drv := fakePersonalServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/refresh" {
			w.Header().Set("Content-Type", "application/xml")
			_, _ = w.Write([]byte(`<root><return>1</return><desc>refresh failed</desc></root>`))
			return
		}
		w.WriteHeader(http.StatusOK)
		writeJSON(t, w, map[string]interface{}{
			"success": false,
			"message": "auth expired",
		})
	})
	defer server.Close()
	drv.cl.authRefreshURL = server.URL + "/refresh"

	_, err := drv.List(context.Background(), "/")
	if err == nil || !strings.Contains(err.Error(), "auth expired") {
		t.Fatalf("expected auth expired error, got %v", err)
	}
}

func TestInitRefreshesExpiringAuthorizationAndPersistsState(t *testing.T) {
	refreshServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(`<root><return>0</return><token>new-token|a|b|` + fmt.Sprintf("%d", time.Now().Add(30*24*time.Hour).UnixMilli()) + `</token></root>`))
	}))
	defer refreshServer.Close()

	drv := New(testAuthExpiring("test", time.Now().Add(time.Hour)), "/", "")
	drv.cl.authRefreshURL = refreshServer.URL
	drv.cl.personalCloudHost = "https://personal.example.com"
	store := drive.NewFileStateStore(filepath.Join(t.TempDir(), "driver"))
	drv.InstallStateStore(store)

	if err := drv.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	var state authState
	if err := store.LoadJSON("yun139_auth.json", &state); err != nil {
		t.Fatal(err)
	}
	if state.Authorization == "" || state.Authorization == testAuthExpiring("test", time.Now().Add(time.Hour)) {
		t.Fatalf("authorization state was not updated: %+v", state)
	}
	if drv.authSource != "refresh" {
		t.Fatalf("authSource = %q, want refresh", drv.authSource)
	}
}

func TestLoadAuthStateOverridesConfigAuthorization(t *testing.T) {
	store := drive.NewFileStateStore(filepath.Join(t.TempDir(), "driver"))
	stored := testAuth("stored", "stored-token")
	if err := store.SaveJSON("yun139_auth.json", authState{
		Authorization: stored,
		UpdatedAt:     time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	drv := New(testAuth("config", "config-token"), "/", "")
	drv.InstallStateStore(store)
	drv.loadAuthState()
	if got := drv.cl.getAuthorization(); got != stored {
		t.Fatalf("authorization = %q, want stored", got)
	}
	if drv.authSource != "state" {
		t.Fatalf("authSource = %q, want state", drv.authSource)
	}
}

func TestPersonalPostRefreshesAndRetriesOnAuthExpired(t *testing.T) {
	var listCalls int
	server, drv := fakePersonalServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/refresh":
			w.Header().Set("Content-Type", "application/xml")
			_, _ = w.Write([]byte(`<root><return>0</return><token>fresh-token|a|b|` + fmt.Sprintf("%d", time.Now().Add(30*24*time.Hour).UnixMilli()) + `</token></root>`))
		case "/file/list":
			listCalls++
			if listCalls == 1 {
				writeJSON(t, w, map[string]interface{}{"success": false, "message": "auth expired"})
				return
			}
			writeJSON(t, w, map[string]interface{}{
				"success": true,
				"data": map[string]interface{}{
					"nextPageCursor": "",
					"items":          []map[string]interface{}{},
				},
			})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	})
	defer server.Close()
	drv.cl.authRefreshURL = server.URL + "/refresh"
	store := drive.NewFileStateStore(filepath.Join(t.TempDir(), "driver"))
	drv.InstallStateStore(store)

	entries, err := drv.List(context.Background(), "/")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("entries = %d, want 0", len(entries))
	}
	if listCalls != 2 {
		t.Fatalf("listCalls = %d, want 2", listCalls)
	}
	var state authState
	if err := store.LoadJSON("yun139_auth.json", &state); err != nil {
		t.Fatal(err)
	}
	if state.Authorization == "" {
		t.Fatal("expected refreshed authorization in state")
	}
}

func TestRead(t *testing.T) {
	downloadServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusPartialContent)
		w.Write([]byte("world"))
	}))
	defer downloadServer.Close()

	server, drv := fakePersonalServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/file/getDownloadUrl" {
			writeJSON(t, w, map[string]interface{}{
				"success": true,
				"data": map[string]interface{}{
					"url": downloadServer.URL,
				},
			})
		}
	})
	defer server.Close()

	rc, err := drv.Read(context.Background(), drive.Entry{ID: "f1"}, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "world" {
		t.Fatalf("got %q, want %q", string(data), "world")
	}
}

func TestReadRange(t *testing.T) {
	downloadServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") != "bytes=6-10" {
			t.Errorf("unexpected range: %s", r.Header.Get("Range"))
		}
		w.WriteHeader(http.StatusPartialContent)
		w.Write([]byte("world"))
	}))
	defer downloadServer.Close()

	server, drv := fakePersonalServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, map[string]interface{}{
			"success": true,
			"data":    map[string]interface{}{"url": downloadServer.URL},
		})
	})
	defer server.Close()

	rc, err := drv.Read(context.Background(), drive.Entry{ID: "f1"}, 6, 5)
	if err != nil {
		t.Fatal(err)
	}
	rc.Close()
}

func TestMkdir(t *testing.T) {
	server, drv := fakePersonalServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, map[string]interface{}{
			"success": true,
			"data":    map[string]interface{}{"fileId": "new-dir", "name": "testdir", "type": "folder"},
		})
	})
	defer server.Close()

	entry, err := drv.Mkdir(context.Background(), "/", "testdir")
	if err != nil {
		t.Fatal(err)
	}
	if entry.ID != "new-dir" || !entry.IsDir {
		t.Errorf("unexpected entry: %+v", entry)
	}
	if entry.ModTime.IsZero() {
		t.Fatal("mkdir entry modtime is zero")
	}
}

func TestMove(t *testing.T) {
	server, drv := fakePersonalServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, map[string]interface{}{"success": true, "code": "0"})
	})
	defer server.Close()

	err := drv.Move(context.Background(), drive.Entry{ID: "f1"}, "dst-folder")
	if err != nil {
		t.Fatal(err)
	}
}

func TestRename(t *testing.T) {
	server, drv := fakePersonalServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, map[string]interface{}{"success": true, "code": "0"})
	})
	defer server.Close()

	err := drv.Rename(context.Background(), drive.Entry{ID: "f1"}, "newname.txt")
	if err != nil {
		t.Fatal(err)
	}
}

func TestRemove(t *testing.T) {
	server, drv := fakePersonalServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, map[string]interface{}{"success": true, "code": "0"})
	})
	defer server.Close()

	err := drv.Remove(context.Background(), drive.Entry{ID: "f1"})
	if err != nil {
		t.Fatal(err)
	}
}

func TestSpace(t *testing.T) {
	var gotBody struct {
		UserDomainID      string `json:"userDomainId"`
		CommonAccountInfo struct {
			Account     string `json:"account"`
			AccountType int    `json:"accountType"`
		} `json:"commonAccountInfo"`
	}
	server, drv := fakePersonalServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/user/disk/quota/detail" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatal(err)
		}
		writeJSON(t, w, map[string]interface{}{
			"success": true,
			"code":    "0000",
			"message": "请求成功",
			"data": map[string]interface{}{
				"freeDiskSize": 39984,
				"diskSize":     40960,
			},
		})
	})
	defer server.Close()
	useUserAPIServer(t, server)
	if _, _, err := drv.cl.decodeAuth(); err != nil {
		t.Fatal(err)
	}
	drv.cl.mu.Lock()
	drv.cl.userDomainID = "domain-1"
	drv.cl.mu.Unlock()

	space, err := drv.Space(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if space.Total != 40*osutil.GiB || space.Free != 39984*osutil.MiB {
		t.Fatalf("space = %+v, want total=%d free=%d", space, 40*osutil.GiB, 39984*osutil.MiB)
	}
	if gotBody.UserDomainID != "domain-1" {
		t.Fatalf("userDomainId = %q, want domain-1", gotBody.UserDomainID)
	}
	if gotBody.CommonAccountInfo.Account != "test" || gotBody.CommonAccountInfo.AccountType != 1 {
		t.Fatalf("commonAccountInfo = %+v", gotBody.CommonAccountInfo)
	}
}

func TestSpaceFailedSetsLastError(t *testing.T) {
	server, drv := fakePersonalServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, map[string]interface{}{
			"success": false,
			"code":    "500",
			"message": "quota failed",
		})
	})
	defer server.Close()
	useUserAPIServer(t, server)

	_, err := drv.Space(context.Background())
	if err == nil || !strings.Contains(err.Error(), "quota failed") {
		t.Fatalf("expected quota failed error, got %v", err)
	}
	if !strings.Contains(drv.getLastError(), "quota failed") {
		t.Fatalf("lastError = %q, want quota failed", drv.getLastError())
	}
}

func TestPutSmallFile(t *testing.T) {
	var createCalled, completeCalled bool
	uploadServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer uploadServer.Close()

	server, drv := fakePersonalServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/file/create":
			createCalled = true
			writeJSON(t, w, map[string]interface{}{
				"success": true,
				"data": map[string]interface{}{
					"fileId":   "new-file",
					"exist":    false,
					"uploadId": "upload-1",
					"partInfos": []map[string]interface{}{
						{"partNumber": 1, "uploadUrl": uploadServer.URL},
					},
				},
			})
		case "/file/complete":
			completeCalled = true
			writeJSON(t, w, map[string]interface{}{
				"success": true,
				"data":    map[string]interface{}{"fileId": "new-file"},
			})
		}
	})
	defer server.Close()

	entry, err := drv.Put(context.Background(), "/", "test.bin", 5, strings.NewReader("hello"))
	if err != nil {
		t.Fatal(err)
	}
	if entry.ID != "new-file" || entry.Name != "test.bin" || entry.Size != 5 {
		t.Errorf("unexpected entry: %+v", entry)
	}
	if entry.ModTime.IsZero() {
		t.Fatal("put entry modtime is zero")
	}
	if !createCalled {
		t.Error("create not called")
	}
	if !completeCalled {
		t.Error("complete not called")
	}
}

func TestPutFileUsesLocalFile(t *testing.T) {
	var uploaded string
	uploadServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ContentLength != int64(len("hello from staging")) {
			t.Errorf("ContentLength = %d, want %d", r.ContentLength, len("hello from staging"))
		}
		if len(r.TransferEncoding) != 0 {
			t.Errorf("unexpected transfer encoding: %v", r.TransferEncoding)
		}
		data, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		uploaded = string(data)
		w.WriteHeader(http.StatusOK)
	}))
	defer uploadServer.Close()

	server, drv := fakePersonalServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/file/create":
			writeJSON(t, w, map[string]interface{}{
				"success": true,
				"data": map[string]interface{}{
					"fileId":   "new-file",
					"exist":    false,
					"uploadId": "upload-1",
					"partInfos": []map[string]interface{}{
						{"partNumber": 1, "uploadUrl": uploadServer.URL},
					},
				},
			})
		case "/file/complete":
			writeJSON(t, w, map[string]interface{}{
				"success": true,
				"data":    map[string]interface{}{"fileId": "new-file"},
			})
		}
	})
	defer server.Close()

	localPath := t.TempDir() + "/payload.bin"
	if err := os.WriteFile(localPath, []byte("hello from staging"), 0o600); err != nil {
		t.Fatal(err)
	}
	entry, err := drv.PutFile(context.Background(), "/", "test.bin", int64(len("hello from staging")), localPath)
	if err != nil {
		t.Fatal(err)
	}
	if entry.ID != "new-file" || entry.Name != "test.bin" || entry.Size != int64(len("hello from staging")) {
		t.Errorf("unexpected entry: %+v", entry)
	}
	if uploaded != "hello from staging" {
		t.Fatalf("unexpected uploaded body: %q", uploaded)
	}
}

func TestPutDuplicate(t *testing.T) {
	server, drv := fakePersonalServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, map[string]interface{}{
			"success": true,
			"data": map[string]interface{}{
				"fileId": "existing-file",
				"exist":  true,
			},
		})
	})
	defer server.Close()

	entry, err := drv.Put(context.Background(), "/", "dup.txt", 3, strings.NewReader("abc"))
	if err != nil {
		t.Fatal(err)
	}
	if entry.ID != "existing-file" {
		t.Errorf("expected existing file ID, got %s", entry.ID)
	}
	if entry.ModTime.IsZero() {
		t.Fatal("duplicate put entry modtime is zero")
	}
}

func TestResolvePath(t *testing.T) {
	d := &Driver{rootID: "/root-id"}
	path, err := d.ResolvePath(context.Background(), "/")
	if err != nil {
		t.Fatal(err)
	}
	if path != "/root-id" {
		t.Errorf("got %q, want %q", path, "/root-id")
	}
}

func TestCalcPartSize(t *testing.T) {
	tests := []struct {
		size int64
		want int64
	}{
		{0, 4 << 20},
		{50 << 20, 4 << 20},
		{200 << 20, 10 << 20},
		{800 << 20, 20 << 20},
		{2 << 30, 50 << 20},
	}
	for _, tt := range tests {
		got := calcPartSize(tt.size)
		if got != tt.want {
			t.Errorf("calcPartSize(%d) = %d, want %d", tt.size, got, tt.want)
		}
	}
}

func TestUploadPartsUsesNativeBandwidthLimiter(t *testing.T) {
	uploadServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Fatalf("unexpected upload method: %s", r.Method)
		}
		_, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer uploadServer.Close()

	localPath := filepath.Join(t.TempDir(), "slow.bin")
	if err := os.WriteFile(localPath, []byte("slow"), 0o600); err != nil {
		t.Fatal(err)
	}

	drv := New(testAuth("test", "token"), "/", "")
	drv.InstallBandwidthLimiter(drive.NewBandwidthLimiter(drive.BandwidthLimits{UploadBytesPerSecond: 1}))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	var uploadResp personalUploadResp
	uploadResp.Data.PartInfos = []personalPartInfo{{PartNumber: 1, UploadUrl: uploadServer.URL}}
	err := drv.uploadParts(ctx, uploadResp, []partMeta{{PartNumber: 1, PartSize: 4}}, 4, 4, localPath)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("uploadParts error = %v, want context deadline exceeded", err)
	}
}

func TestToEntry(t *testing.T) {
	item := personalItem{
		FileId:    "f1",
		Name:      "file.txt",
		Type:      "file",
		Size:      100,
		UpdatedAt: "2024-01-15T10:30:00.000+08:00",
	}
	entry := toEntry(item)
	if entry.ID != "f1" || entry.Name != "file.txt" || entry.IsDir || entry.Size != 100 {
		t.Errorf("unexpected entry: %+v", entry)
	}
	if entry.ModTime.IsZero() {
		t.Error("mod time is zero")
	}
	want := time.Date(2024, 1, 15, 10, 30, 0, 0, time.FixedZone("", 8*60*60))
	if !entry.ModTime.Equal(want) {
		t.Fatalf("mod time = %s, want %s", entry.ModTime, want)
	}

	createdOnly := personalItem{
		FileId:    "f2",
		Name:      "created.txt",
		Type:      "file",
		CreatedAt: "2024-01-14T09:20:00.000+08:00",
	}
	createdEntry := toEntry(createdOnly)
	if createdEntry.ModTime.IsZero() {
		t.Fatal("created_at fallback modtime is zero")
	}

	invalid := personalItem{FileId: "bad", Name: "bad.txt", Type: "file", UpdatedAt: "not-a-time"}
	invalidEntry := toEntry(invalid)
	if !invalidEntry.ModTime.IsZero() {
		t.Fatalf("invalid modtime = %s, want zero", invalidEntry.ModTime)
	}

	folder := personalItem{FileId: "d1", Name: "dir", Type: "folder"}
	dirEntry := toEntry(folder)
	if !dirEntry.IsDir {
		t.Error("expected IsDir")
	}
}

func TestYun139DebugSnapshot(t *testing.T) {
	d := New("auth", "/Docs", "")
	d.rootID = "root-id"
	snapshot, err := d.DebugSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Driver != "yun139" {
		t.Fatalf("driver = %q, want yun139", snapshot.Driver)
	}
	if snapshot.Health != "ok" {
		t.Fatalf("health = %q, want ok", snapshot.Health)
	}
	if snapshot.Stats["root_path"] != "/Docs" {
		t.Fatalf("unexpected stats: %+v", snapshot.Stats)
	}
	if snapshot.Stats["root_id"] != "root-id" {
		t.Fatalf("unexpected stats: %+v", snapshot.Stats)
	}
	if snapshot.Extra["credential_source"] != "config" {
		t.Fatalf("credential_source = %v, want config", snapshot.Extra["credential_source"])
	}
}

func TestYun139DebugSnapshotDegraded(t *testing.T) {
	d := New("auth", "/Docs", "")
	d.setLastError(fmt.Errorf("simulated API error"))
	snapshot, err := d.DebugSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Health != "degraded" {
		t.Fatalf("health = %q, want degraded", snapshot.Health)
	}
	if snapshot.Extra["last_error"] != "simulated API error" {
		t.Fatalf("last_error = %v, want simulated API error", snapshot.Extra["last_error"])
	}
}
