package aliyundrive

import (
	"context"
	"crypto/sha1"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

type countingSHA1Source struct {
	sum   [sha1.Size]byte
	opens int
}

func (s *countingSHA1Source) Size() int64 {
	return 4
}

func (s *countingSHA1Source) Open(context.Context) (drive.ReadOnlyFile, error) {
	s.opens++
	return nil, io.ErrUnexpectedEOF
}

func (s *countingSHA1Source) Hash(algorithm drive.HashAlgorithm) ([]byte, bool) {
	if algorithm != drive.HashSHA1 {
		return nil, false
	}
	return s.sum[:], true
}

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

func TestFactoryCreatesDriver(t *testing.T) {
	raw, err := drive.New("aliyundrive", drive.Params{
		"refresh_token":   "token",
		"drive_id":        "drive-id",
		"root_path":       "/",
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
	if d.driveID != "drive-id" || d.rootID != "root" || d.rootPath != "/" || d.orderBy != "name" || d.orderDirection != "ASC" {
		t.Fatalf("unexpected driver config drive=%q root=%q order=%q/%q", d.driveID, d.rootID, d.orderBy, d.orderDirection)
	}
}

func TestFileSHA1UsesSourceMetadata(t *testing.T) {
	source := &countingSHA1Source{sum: sha1.Sum([]byte("data"))}
	got, err := fileSHA1(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	if source.opens != 0 {
		t.Fatalf("source opened %d times, want 0", source.opens)
	}
	if want := "a17c9aaa61e80a1bf71d0d850af4e5baa9800bbd"; got != want {
		t.Fatalf("sha1 = %s, want %s", got, want)
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

func TestRequestRetriesTemporaryStatus(t *testing.T) {
	withoutAliyunRetryWait(t)
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/file/list" {
			http.NotFound(w, r)
			return
		}
		calls++
		if calls == 1 {
			http.Error(w, "temporary unavailable", http.StatusServiceUnavailable)
			return
		}
		_ = json.NewEncoder(w).Encode(listResp{})
	}))
	defer server.Close()

	d := New(Options{RefreshToken: "refresh", DriveID: "drive", RootID: "root", APIBaseURL: server.URL})
	d.cl.setTokens("access", "refresh")
	if _, err := d.List(context.Background(), "root"); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("list calls = %d, want 2", calls)
	}
}

func withoutAliyunRetryWait(t *testing.T) {
	t.Helper()
	original := aliyunRetryWait
	aliyunRetryWait = func(context.Context, int) error { return nil }
	t.Cleanup(func() { aliyunRetryWait = original })
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

func TestPutSourceUsesInstantUploadProofAfterPreHashMatched(t *testing.T) {
	tmp, err := os.CreateTemp("", "aliyundrive-instant-*")
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
			_ = json.NewEncoder(w).Encode(createResp{FileID: "instant-file", Name: "instant.txt", InstantUpload: true})
		default:
			t.Fatalf("unexpected create call %d", createCalls)
		}
	}))
	defer server.Close()

	d := New(Options{RefreshToken: "refresh", DriveID: "drive", RootID: "root", APIBaseURL: server.URL})
	d.cl.mu.Lock()
	d.cl.accessToken = "access-token"
	d.cl.mu.Unlock()
	entry, err := d.PutSource(context.Background(), drive.UploadRequest{
		ParentID: "parent",
		Name:     "instant.txt",
		Source:   drive.NewLocalReadOnlyFileSource(tmpPath, 26),
	})
	if err != nil {
		t.Fatal(err)
	}
	if entry.ID != "instant-file" || entry.ParentID != "parent" || entry.Name != "instant.txt" || entry.Size != 26 {
		t.Fatalf("unexpected entry: %+v", entry)
	}
	if entry.ModTime.IsZero() {
		t.Fatal("instant upload entry modtime is zero")
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

func TestAliyunDebugSnapshot(t *testing.T) {
	d := New(Options{
		RefreshToken: "token",
		DriveID:      "drive-id",
		RootPath:     "/",
	})
	snapshot, err := d.DebugSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Driver != "aliyundrive" {
		t.Fatalf("driver = %q, want aliyundrive", snapshot.Driver)
	}
	if snapshot.Health != "ok" {
		t.Fatalf("health = %q, want ok", snapshot.Health)
	}
	if snapshot.Stats[drive.DebugStatRootPath] != "/" {
		t.Fatalf("unexpected stats: %+v", snapshot.Stats)
	}
	if snapshot.Extra[drive.DebugExtraCredentialSource] == nil {
		t.Fatalf("expected credential_source in extra, got %+v", snapshot.Extra)
	}
	if _, ok := snapshot.Extra[drive.DebugExtraLastError]; !ok {
		t.Fatalf("expected last_error in extra")
	}
}

func TestPutSourceWithPrecomputedSHA1SkipsPreHash(t *testing.T) {
	var createCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/adrive/v2/file/createWithFolders" {
			http.NotFound(w, r)
			return
		}
		createCalls++
		if createCalls > 1 {
			t.Fatalf("unexpected second create call (sha1 fast path should send only one)")
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode create body: %v", err)
		}
		if body["pre_hash"] != nil {
			t.Fatalf("pre_hash should not be set when source provides sha1, got: %v", body["pre_hash"])
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
		if body["proof_version"] != "v1" {
			t.Fatalf("proof_version = %v, want v1", body["proof_version"])
		}
		_ = json.NewEncoder(w).Encode(createResp{FileID: "instant-file", Name: "test.bin", InstantUpload: true})
	}))
	defer server.Close()

	d := New(Options{RefreshToken: "refresh", DriveID: "drive", RootID: "root", APIBaseURL: server.URL})
	d.cl.mu.Lock()
	d.cl.accessToken = "access-token"
	d.cl.mu.Unlock()

	// NewBytesReadOnlyFileSource auto-computes SHA1 and attaches it via HashProvider.
	source := drive.NewBytesReadOnlyFileSource([]byte("hello world this is test data for instant upload"))
	entry, err := d.PutSource(context.Background(), drive.UploadRequest{
		ParentID: "parent",
		Name:     "test.bin",
		Source:   source,
	})
	if err != nil {
		t.Fatal(err)
	}
	if entry.ID != "instant-file" || entry.ParentID != "parent" || entry.Name != "test.bin" || entry.Size != int64(len("hello world this is test data for instant upload")) {
		t.Fatalf("unexpected entry: %+v", entry)
	}
	if entry.ModTime.IsZero() {
		t.Fatal("instant upload entry modtime is zero")
	}
	if createCalls != 1 {
		t.Fatalf("create calls = %d, want 1 (sha1 fast path should not need retry)", createCalls)
	}
}

func TestPutSourceResumesPersistedUploadSession(t *testing.T) {
	source := drive.NewBytesReadOnlyFileSource([]byte("abcdefgh"))
	store := drive.NewFileStateStore(filepath.Join(t.TempDir(), "driver"))
	partAttempts := map[string]int{}
	createCalls := 0
	completeCalls := 0
	failPart2 := true

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/adrive/v2/file/createWithFolders":
			createCalls++
			if createCalls > 1 {
				t.Fatalf("unexpected create call during resume")
			}
			_ = json.NewEncoder(w).Encode(createResp{
				FileID:   "file-1",
				Name:     "resume.bin",
				Size:     source.Size(),
				UploadID: "upload-1",
				PartInfoList: []uploadPartInfo{
					{PartNumber: 1, UploadURL: serverURL(r) + "/upload/1"},
					{PartNumber: 2, UploadURL: serverURL(r) + "/upload/2"},
					{PartNumber: 3, UploadURL: serverURL(r) + "/upload/3"},
				},
			})
		case strings.HasPrefix(r.URL.Path, "/upload/"):
			part := strings.TrimPrefix(r.URL.Path, "/upload/")
			partAttempts[part]++
			data, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatalf("read part body: %v", err)
			}
			partNum, err := strconv.Atoi(part)
			if err != nil {
				t.Fatalf("bad part path: %s", part)
			}
			start := (partNum - 1) * 3
			end := start + 3
			if end > int(source.Size()) {
				end = int(source.Size())
			}
			if string(data) != string([]byte("abcdefgh")[start:end]) {
				t.Fatalf("part %s body = %q", part, data)
			}
			if part == "2" && failPart2 {
				failPart2 = false
				http.Error(w, "temporary failure", http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusOK)
		case r.URL.Path == "/v2/file/complete":
			completeCalls++
			_ = json.NewEncoder(w).Encode(completeResp{FileID: "file-1", Name: "resume.bin", Size: source.Size()})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	first := New(Options{RefreshToken: "refresh", DriveID: "drive", RootID: "root", APIBaseURL: server.URL})
	first.partSize = 3
	first.cl.mu.Lock()
	first.cl.accessToken = "access-token"
	first.cl.mu.Unlock()
	first.InstallStateStore(store)
	_, err := first.PutSource(context.Background(), drive.UploadRequest{
		ParentID: "parent",
		Name:     "resume.bin",
		Source:   source,
	})
	if err == nil || !strings.Contains(err.Error(), "upload part 2") {
		t.Fatalf("first upload error = %v, want part 2 failure", err)
	}
	if partAttempts["1"] != 1 || partAttempts["2"] != 1 || partAttempts["3"] != 0 {
		t.Fatalf("part attempts after first upload = %+v", partAttempts)
	}
	var state aliyunUploadSessionState
	if err := store.LoadJSON(aliyunUploadSessionStateFile, &state); err != nil {
		t.Fatal(err)
	}
	if len(state.Sessions) != 1 {
		t.Fatalf("session count after failed upload = %d, want 1", len(state.Sessions))
	}

	second := New(Options{RefreshToken: "refresh", DriveID: "drive", RootID: "root", APIBaseURL: server.URL})
	second.partSize = 3
	second.cl.mu.Lock()
	second.cl.accessToken = "access-token"
	second.cl.mu.Unlock()
	second.InstallStateStore(store)
	entry, err := second.PutSource(context.Background(), drive.UploadRequest{
		ParentID: "parent",
		Name:     "resume.bin",
		Source:   source,
	})
	if err != nil {
		t.Fatal(err)
	}
	if entry.ID != "file-1" || entry.Name != "resume.bin" || entry.Size != source.Size() {
		t.Fatalf("unexpected resumed entry: %+v", entry)
	}
	if createCalls != 1 {
		t.Fatalf("create calls = %d, want 1", createCalls)
	}
	if completeCalls != 1 {
		t.Fatalf("complete calls = %d, want 1", completeCalls)
	}
	if partAttempts["1"] != 1 || partAttempts["2"] != 2 || partAttempts["3"] != 1 {
		t.Fatalf("part attempts after resume = %+v, want part 1 skipped on resume", partAttempts)
	}
	state = aliyunUploadSessionState{}
	if err := store.LoadJSON(aliyunUploadSessionStateFile, &state); err != nil {
		t.Fatal(err)
	}
	if len(state.Sessions) != 0 {
		t.Fatalf("session should be deleted after complete, got %+v", state.Sessions)
	}
}

func serverURL(r *http.Request) string {
	return "http://" + r.Host
}
