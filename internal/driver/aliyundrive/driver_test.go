package aliyundrive

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

func TestFactoryRequiresRefreshToken(t *testing.T) {
	_, err := drive.New("aliyundrive", drive.Params{})
	if err == nil || !strings.Contains(err.Error(), "refresh_token") {
		t.Fatalf("expected refresh_token error, got %v", err)
	}
}

func TestFactoryRequiresDriveID(t *testing.T) {
	_, err := drive.New("aliyundrive", drive.Params{"refresh_token": "token"})
	if err == nil || !strings.Contains(err.Error(), "drive_id") {
		t.Fatalf("expected drive_id error, got %v", err)
	}
}

func TestFactoryRequiresRootID(t *testing.T) {
	_, err := drive.New("aliyundrive", drive.Params{
		"refresh_token": "token",
		"drive_id":      "drive-id",
	})
	if err == nil || !strings.Contains(err.Error(), "root_id") {
		t.Fatalf("expected root_id error, got %v", err)
	}
}

func TestFactoryCreatesDriver(t *testing.T) {
	raw, err := drive.New("aliyundrive", drive.Params{
		"refresh_token":   "token",
		"drive_id":        "drive-id",
		"root_id":         "root-id",
		"order_by":        "name",
		"order_direction": "ASC",
	})
	if err != nil {
		t.Fatal(err)
	}
	d, ok := raw.(*Driver)
	if !ok {
		t.Fatalf("driver type = %T, want *Driver", raw)
	}
	if d.driveID != "drive-id" || d.rootID != "root-id" || d.orderBy != "name" || d.orderDirection != "ASC" {
		t.Fatalf("unexpected driver config drive=%q root=%q order=%q/%q", d.driveID, d.rootID, d.orderBy, d.orderDirection)
	}
}

func TestFileEntryMapping(t *testing.T) {
	modTime := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	item := file{
		FileID:       "file-id",
		ParentFileID: "remote-parent",
		Type:         "file",
		Name:         "report.pdf",
		Size:         123,
		UpdatedAt:    &modTime,
	}
	entry := item.entry("parent")
	if entry.ID != "file-id" || entry.ParentID != "parent" || entry.Name != "report.pdf" {
		t.Fatalf("unexpected entry identity: %+v", entry)
	}
	if entry.IsDir {
		t.Fatalf("file mapped as dir: %+v", entry)
	}
	if entry.Size != 123 || !entry.ModTime.Equal(modTime) {
		t.Fatalf("unexpected entry metadata: %+v", entry)
	}
}

func TestFolderEntryMapping(t *testing.T) {
	item := file{FileID: "folder-id", ParentFileID: "parent", Type: "folder", Name: "docs"}
	entry := item.entry("")
	if !entry.IsDir || entry.ParentID != "parent" {
		t.Fatalf("unexpected folder entry: %+v", entry)
	}
}

func TestResolveIDUsesRoot(t *testing.T) {
	d := New(Options{RefreshToken: "token", RootID: "root-id"})
	for _, input := range []string{"", "0", "/"} {
		if got := d.resolveID(input); got != "root-id" {
			t.Fatalf("resolveID(%q) = %q, want root-id", input, got)
		}
	}
	if got := d.resolveID("child"); got != "child" {
		t.Fatalf("resolveID child = %q", got)
	}
}

func TestResolvePathRoot(t *testing.T) {
	d := New(Options{RefreshToken: "token", RootID: "root-id"})
	got, err := d.ResolvePath(context.Background(), "/")
	if err != nil {
		t.Fatal(err)
	}
	if got != "root-id" {
		t.Fatalf("ResolvePath root = %q, want root-id", got)
	}
}

func TestInitValidatesConfiguredDriveAndRoot(t *testing.T) {
	var sawList bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			_ = json.NewEncoder(w).Encode(tokenResp{AccessToken: "access", RefreshToken: "next"})
		case "/v2/user/get":
			_ = json.NewEncoder(w).Encode(userResp{DefaultDriveID: "default-drive", UserID: "user"})
		case "/v2/file/list":
			if r.Header.Get("X-Device-Id") == "" {
				t.Fatal("missing X-Device-Id header")
			}
			if r.Header.Get("X-Signature") == "" {
				t.Fatal("missing X-Signature header")
			}
			if r.Header.Get("x-request-id") == "" {
				t.Fatal("missing x-request-id header")
			}
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode list body: %v", err)
			}
			if body["drive_id"] != "configured-drive" {
				t.Fatalf("drive_id = %v, want configured-drive", body["drive_id"])
			}
			if body["parent_file_id"] != "configured-root" {
				t.Fatalf("parent_file_id = %v, want configured-root", body["parent_file_id"])
			}
			sawList = true
			_ = json.NewEncoder(w).Encode(listResp{})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	d := New(Options{
		RefreshToken: "refresh",
		DriveID:      "configured-drive",
		RootID:       "configured-root",
		APIBaseURL:   server.URL,
		AuthURL:      server.URL + "/token",
	})
	if err := d.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !sawList {
		t.Fatal("expected Init to validate root with list request")
	}
	if d.driveID != "configured-drive" {
		t.Fatalf("driveID = %q, want configured-drive", d.driveID)
	}
}

func TestRefreshPersistsTokenState(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/token" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(tokenResp{AccessToken: "new-access", RefreshToken: "new-refresh"})
	}))
	defer server.Close()

	store := drive.NewFileStateStore(filepath.Join(t.TempDir(), "driver"))
	d := New(Options{RefreshToken: "old-refresh", AuthURL: server.URL + "/token"})
	d.InstallStateStore(store)
	if err := d.cl.refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	var state tokenState
	if err := store.LoadJSON("aliyundrive_token.json", &state); err != nil {
		t.Fatal(err)
	}
	if state.AccessToken != "new-access" || state.RefreshToken != "new-refresh" {
		t.Fatalf("unexpected state: %+v", state)
	}
}

func TestLoadTokenStateOverridesConfigToken(t *testing.T) {
	store := drive.NewFileStateStore(filepath.Join(t.TempDir(), "driver"))
	if err := store.SaveJSON("aliyundrive_token.json", tokenState{
		AccessToken:  "stored-access",
		RefreshToken: "stored-refresh",
		UpdatedAt:    time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	d := New(Options{RefreshToken: "config-refresh"})
	d.InstallStateStore(store)
	d.loadTokenState()
	access, refresh := d.cl.tokens()
	if access != "stored-access" || refresh != "stored-refresh" {
		t.Fatalf("tokens = access:%q refresh:%q", access, refresh)
	}
	if d.tokenSource != "state" {
		t.Fatalf("tokenSource = %q, want state", d.tokenSource)
	}
}

func TestRequestCreatesDeviceSessionOnSignatureInvalid(t *testing.T) {
	var listCalls int
	var sawCreateSession bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			_ = json.NewEncoder(w).Encode(tokenResp{AccessToken: "access", RefreshToken: "next"})
		case "/v2/user/get":
			_ = json.NewEncoder(w).Encode(userResp{DefaultDriveID: "default-drive", UserID: "user"})
		case "/users/v1/users/device/create_session":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode create_session body: %v", err)
			}
			if body["refreshToken"] != "next" {
				t.Fatalf("refreshToken = %v, want next", body["refreshToken"])
			}
			if body["pubKey"] == "" {
				t.Fatal("missing pubKey in create_session body")
			}
			if r.Header.Get("X-Device-Id") == "" || r.Header.Get("X-Signature") == "" {
				t.Fatal("missing signed create_session headers")
			}
			sawCreateSession = true
			_ = json.NewEncoder(w).Encode(map[string]any{})
		case "/v2/file/list":
			listCalls++
			if listCalls == 1 {
				_ = json.NewEncoder(w).Encode(apiError{Code: "DeviceSessionSignatureInvalid", Message: "invalid"})
				return
			}
			_ = json.NewEncoder(w).Encode(listResp{})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	d := New(Options{
		RefreshToken: "refresh",
		DriveID:      "drive",
		RootID:       "root",
		APIBaseURL:   server.URL,
		AuthURL:      server.URL + "/token",
	})
	if err := d.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !sawCreateSession {
		t.Fatal("expected DeviceSessionSignatureInvalid to create device session")
	}
	if listCalls != 2 {
		t.Fatalf("list calls = %d, want 2", listCalls)
	}
}

func TestReadCachesDownloadURL(t *testing.T) {
	var downloadURLCalls int
	var downloadCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/file/get_download_url":
			downloadURLCalls++
			_ = json.NewEncoder(w).Encode(downloadURLResp{URL: "http://" + r.Host + "/download"})
		case "/download":
			downloadCalls++
			_, _ = w.Write([]byte("data"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	d := New(Options{RefreshToken: "refresh", DriveID: "drive", RootID: "root", APIBaseURL: server.URL})
	entry := drive.Entry{ID: "file-id", Name: "file.txt", Size: 4}
	for i := 0; i < 2; i++ {
		rc, err := d.Read(context.Background(), entry, 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		got, err := io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != "data" {
			t.Fatalf("read body = %q, want data", got)
		}
	}
	if downloadURLCalls != 1 {
		t.Fatalf("download url calls = %d, want 1", downloadURLCalls)
	}
	if downloadCalls != 2 {
		t.Fatalf("download calls = %d, want 2", downloadCalls)
	}
}

func TestReadRefreshesDownloadURLOnForbidden(t *testing.T) {
	var downloadURLCalls int
	var oldURLCalls int
	var freshURLCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/file/get_download_url":
			downloadURLCalls++
			urlPath := "/old-download"
			if downloadURLCalls > 1 {
				urlPath = "/fresh-download"
			}
			_ = json.NewEncoder(w).Encode(downloadURLResp{URL: "http://" + r.Host + urlPath})
		case "/old-download":
			oldURLCalls++
			http.Error(w, "expired", http.StatusForbidden)
		case "/fresh-download":
			freshURLCalls++
			_, _ = w.Write([]byte("fresh"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	d := New(Options{RefreshToken: "refresh", DriveID: "drive", RootID: "root", APIBaseURL: server.URL})
	rc, err := d.Read(context.Background(), drive.Entry{ID: "file-id", Name: "file.txt", Size: 5}, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(rc)
	_ = rc.Close()
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "fresh" {
		t.Fatalf("read body = %q, want fresh", got)
	}
	if downloadURLCalls != 2 {
		t.Fatalf("download url calls = %d, want 2", downloadURLCalls)
	}
	if oldURLCalls != 1 || freshURLCalls != 1 {
		t.Fatalf("download calls old=%d fresh=%d, want 1/1", oldURLCalls, freshURLCalls)
	}
}

func TestPutFileUsesRapidUploadProofAfterPreHashMatched(t *testing.T) {
	tmp, err := os.CreateTemp("", "aliyundrive-rapid-*")
	if err != nil {
		t.Fatal(err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.WriteString("abcdefghijklmnopqrstuvwxyz"); err != nil {
		t.Fatal(err)
	}
	if err := tmp.Close(); err != nil {
		t.Fatal(err)
	}

	var createCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/adrive/v2/file/createWithFolders" {
			http.NotFound(w, r)
			return
		}
		createCalls++
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode create body: %v", err)
		}
		switch createCalls {
		case 1:
			if body["pre_hash"] == "" {
				t.Fatal("missing pre_hash on first create")
			}
			_ = json.NewEncoder(w).Encode(apiError{Code: "PreHashMatched", Message: "matched"})
		case 2:
			if body["pre_hash"] != nil {
				t.Fatalf("pre_hash should be removed on proof create: %v", body["pre_hash"])
			}
			if body["content_hash_name"] != "sha1" {
				t.Fatalf("content_hash_name = %v, want sha1", body["content_hash_name"])
			}
			if body["content_hash"] == "" {
				t.Fatal("missing content_hash")
			}
			if body["proof_code"] == "" {
				t.Fatal("missing proof_code")
			}
			_ = json.NewEncoder(w).Encode(createResp{FileID: "rapid-file", Name: "rapid.txt", RapidUpload: true})
		default:
			t.Fatalf("unexpected create call %d", createCalls)
		}
	}))
	defer server.Close()

	d := New(Options{RefreshToken: "refresh", DriveID: "drive", RootID: "root", APIBaseURL: server.URL})
	d.cl.mu.Lock()
	d.cl.accessToken = "access-token"
	d.cl.mu.Unlock()
	entry, err := d.PutFile(context.Background(), "parent", "rapid.txt", 26, tmpPath)
	if err != nil {
		t.Fatal(err)
	}
	if entry.ID != "rapid-file" || entry.ParentID != "parent" || entry.Name != "rapid.txt" || entry.Size != 26 {
		t.Fatalf("unexpected entry: %+v", entry)
	}
	if entry.ModTime.IsZero() {
		t.Fatal("rapid upload entry modtime is zero")
	}
	if createCalls != 2 {
		t.Fatalf("create calls = %d, want 2", createCalls)
	}
}

func TestBatchReportsChildResponseError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v3/batch" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(batchResp{
			Responses: []batchItemResp{{
				ID:     "file",
				Status: 404,
				Body:   json.RawMessage(`{"code":"NotFound","message":"missing"}`),
			}},
		})
	}))
	defer server.Close()

	d := New(Options{RefreshToken: "refresh", DriveID: "drive", RootID: "root", APIBaseURL: server.URL})
	err := d.batch(context.Background(), "file", "dst", "/file/move")
	if err == nil || !strings.Contains(err.Error(), "status=404") || !strings.Contains(err.Error(), "NotFound") {
		t.Fatalf("unexpected batch error: %v", err)
	}
}
