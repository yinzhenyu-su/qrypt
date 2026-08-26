package aliyundrive

import (
	"bytes"
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
	"github.com/yinzhenyu/qrypt/pkg/drive/session"
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
	createdAt := time.Date(2026, 6, 27, 12, 0, 0, 0, time.UTC)
	modTime := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	item := file{
		FileID:       "file-id",
		ParentFileID: "remote-parent",
		Type:         "file",
		Name:         "report.pdf",
		Size:         123,
		CreatedAt:    &createdAt,
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
	if !entry.CreatedAt.Equal(createdAt) || !entry.UpdatedAt.Equal(modTime) {
		t.Fatalf("entry times = created %s updated %s, want %s %s", entry.CreatedAt, entry.UpdatedAt, createdAt, modTime)
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
	if err == nil || !strings.Contains(err.Error(), "batch /file/move") || !strings.Contains(err.Error(), "404") || !strings.Contains(err.Error(), "NotFound") {
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
	uploadedParts := map[int]bool{} // mock 服务端的已确认分片
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
			uploadedParts[partNum] = true
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
	// 优雅退出：Drop 触发 Flush，节流期的确认位图落盘。
	if err := first.Drop(context.Background()); err != nil {
		t.Fatal(err)
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
	// 恢复基于本地确认位图 + 复用 create 下发的预签名 URL，part 1 跳过。
	if partAttempts["1"] != 1 || partAttempts["2"] != 2 || partAttempts["3"] != 1 {
		t.Fatalf("part attempts after resume = %+v, want part 1 skipped on resume", partAttempts)
	}
	// commit 成功后绑定清理：盘面无残留。
	reloaded := session.NewIndex(store, aliyunSessionFile, session.IndexOptions{})
	if bindings := reloaded.List(); len(bindings) != 0 {
		t.Fatalf("binding should be deleted after complete, got %d", len(bindings))
	}
}

func serverURL(r *http.Request) string {
	return "http://" + r.Host
}

func TestLoadTokenStateStateWinsAfterRotation(t *testing.T) {
	store := drive.NewFileStateStore(filepath.Join(t.TempDir(), "driver"))
	if err := store.SaveJSON("aliyundrive_token.json", tokenState{
		AccessToken:        "rotated-access",
		RefreshToken:       "rotated-refresh",
		ConfigRefreshToken: "original-refresh",
	}); err != nil {
		t.Fatal(err)
	}
	driver := New(Options{RefreshToken: "original-refresh"})
	driver.InstallStateStore(store)

	driver.loadTokenState()

	access, refresh := driver.cl.tokens()
	if access != "rotated-access" || refresh != "rotated-refresh" {
		t.Fatalf("tokens = %q/%q, want rotated state tokens", access, refresh)
	}
	if driver.tokenSource != "state" {
		t.Fatalf("tokenSource = %q, want state", driver.tokenSource)
	}
}

func TestLoadTokenStateConfigWinsOnAccountSwitch(t *testing.T) {
	store := drive.NewFileStateStore(filepath.Join(t.TempDir(), "driver"))
	if err := store.SaveJSON("aliyundrive_token.json", tokenState{
		AccessToken:        "old-access",
		RefreshToken:       "old-refresh",
		ConfigRefreshToken: "old-config-refresh",
	}); err != nil {
		t.Fatal(err)
	}
	driver := New(Options{RefreshToken: "new-config-refresh"})
	driver.InstallStateStore(store)

	driver.loadTokenState()

	access, refresh := driver.cl.tokens()
	if refresh != "new-config-refresh" {
		t.Fatalf("refresh = %q, want config token on account switch", refresh)
	}
	if access != "" {
		t.Fatalf("access = %q, want empty (config token not applied)", access)
	}
	if driver.tokenSource != "config" {
		t.Fatalf("tokenSource = %q, want config", driver.tokenSource)
	}
}

func TestLoadTokenStateStateWinsForLegacyState(t *testing.T) {
	store := drive.NewFileStateStore(filepath.Join(t.TempDir(), "driver"))
	if err := store.SaveJSON("aliyundrive_token.json", tokenState{
		AccessToken:  "legacy-access",
		RefreshToken: "legacy-refresh",
	}); err != nil {
		t.Fatal(err)
	}
	driver := New(Options{RefreshToken: "cfg-refresh"})
	driver.InstallStateStore(store)

	driver.loadTokenState()

	access, refresh := driver.cl.tokens()
	if access != "legacy-access" || refresh != "legacy-refresh" {
		t.Fatalf("tokens = %q/%q, want legacy state tokens", access, refresh)
	}
}

func TestDriverRemoteHashFromListMetadata(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/file/list" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(listResp{Items: []file{
			{
				DriveID:         "drive",
				FileID:          "f1",
				ParentFileID:    "root",
				Type:            "file",
				Name:            "secret.txt",
				Size:            100,
				ContentHash:     "0123456789abcdef0123456789abcdef01234567",
				ContentHashName: "sha1",
			},
		}})
	}))
	defer server.Close()

	d := New(Options{RefreshToken: "refresh", DriveID: "drive", RootID: "root", APIBaseURL: server.URL})
	d.cl.setTokens("access", "refresh")
	entries, err := d.List(context.Background(), "root")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(entries))
	}
	algorithm, hash, err := d.RemoteHash(context.Background(), entries[0])
	if err != nil {
		t.Fatal(err)
	}
	if algorithm != drive.HashSHA1 || hash != "0123456789abcdef0123456789abcdef01234567" {
		t.Fatalf("RemoteHash = (%s, %s), want (sha1, content_hash)", algorithm, hash)
	}
}

func TestServerSideCopyUsesBatchFileCopy(t *testing.T) {
	var sawURL string
	var sawBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v3/batch" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		sawURL = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&sawBody)
		_ = json.NewEncoder(w).Encode(batchResp{Responses: []batchItemResp{{
			ID:     "src-fid",
			Status: 200,
			Body:   json.RawMessage(`{"file_id":"new-fid"}`),
		}}})
	}))
	defer server.Close()

	d := New(Options{RefreshToken: "refresh", DriveID: "drive", RootID: "root", APIBaseURL: server.URL})
	dst, err := d.Copy(context.Background(), drive.Entry{ID: "src-fid", Name: "src.txt", Size: 4}, "dst-dir", "copied.txt")
	if err != nil {
		t.Fatal(err)
	}
	if sawURL != "/v3/batch" {
		t.Fatalf("batch path = %q", sawURL)
	}
	requests, ok := sawBody["requests"].([]any)
	if !ok || len(requests) != 1 {
		t.Fatalf("batch requests = %#v", sawBody["requests"])
	}
	req := requests[0].(map[string]any)
	if req["url"] != "/file/copy" {
		t.Fatalf("batch url = %#v, want /file/copy", req["url"])
	}
	if dst.ID != "new-fid" || dst.Name != "copied.txt" {
		t.Fatalf("copy entry = %+v", dst)
	}
}

func TestServerSideCopyRejectsDirectory(t *testing.T) {
	d := New(Options{RefreshToken: "refresh", DriveID: "drive", RootID: "root"})
	if _, err := d.Copy(context.Background(), drive.Entry{ID: "d", IsDir: true}, "0", "x"); err == nil {
		t.Fatal("directory copy should fail")
	}
}

func TestDropHandlelessBindings(t *testing.T) {
	store := drive.NewFileStateStore(filepath.Join(t.TempDir(), "driver"))
	d := New(Options{RefreshToken: "refresh", DriveID: "drive", RootID: "root"})
	d.InstallStateStore(store)

	handleless, _ := json.Marshal(aliyunToken{})
	if err := d.sessions.Create("key-empty", handleless); err != nil {
		t.Fatal(err)
	}
	withHandle, _ := json.Marshal(aliyunToken{FileID: "f", UploadID: "u", PartSize: 10 << 20})
	if err := d.sessions.Create("key-handle", withHandle); err != nil {
		t.Fatal(err)
	}

	d.dropHandlelessBindings()

	if _, ok := d.sessions.Get("key-empty"); ok {
		t.Fatal("handleless binding must be dropped eagerly")
	}
	if _, ok := d.sessions.Get("key-handle"); !ok {
		t.Fatal("binding with a provider handle must survive the sweep")
	}
}

func TestInstallStateStoreReclaimsExpiredBindings(t *testing.T) {
	store := drive.NewFileStateStore(filepath.Join(t.TempDir(), "driver"))
	stale, _ := json.Marshal(aliyunToken{FileID: "f", UploadID: "u", PartSize: 10 << 20})
	live, _ := json.Marshal(aliyunToken{FileID: "g", UploadID: "v", PartSize: 10 << 20})
	old := time.Now().Add(-30 * time.Hour)
	if err := store.SaveJSON(aliyunSessionFile, map[string]session.Session{
		"stale": {Key: "stale", Token: stale, CreatedAt: old, UpdatedAt: old},
		"live":  {Key: "live", Token: live, CreatedAt: time.Now(), UpdatedAt: time.Now()},
	}); err != nil {
		t.Fatal(err)
	}

	d := New(Options{RefreshToken: "refresh", DriveID: "drive", RootID: "root"})
	d.InstallStateStore(store)

	if _, ok := d.sessions.Get("stale"); ok {
		t.Fatal("stale binding must be reclaimed on install")
	}
	if _, ok := d.sessions.Get("live"); !ok {
		t.Fatal("live binding must survive the expiry pass")
	}
	// 回收需要落盘：新实例重载后 stale 也不在，而不是仅内存清理。
	reloaded := session.NewIndex(store, aliyunSessionFile, session.IndexOptions{})
	bindings := reloaded.List()
	if len(bindings) != 1 || bindings[0].Key != "live" {
		t.Fatalf("reloaded bindings = %+v, want only live", bindings)
	}
}

// completionServer mocks a multipart upload whose create returns three parts
// of partSize each and whose complete responds per completeStatus.
func completionServer(t *testing.T, source drive.ReadOnlyFileSource, partSize int, completeStatus func() int) (*httptest.Server, *int) {
	t.Helper()
	completeCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/adrive/v2/file/createWithFolders":
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
			_, _ = io.Copy(io.Discard, r.Body)
			w.WriteHeader(http.StatusOK)
		case r.URL.Path == "/v2/file/complete":
			completeCalls++
			if status := completeStatus(); status != http.StatusOK {
				http.Error(w, "complete failed", status)
				return
			}
			_ = json.NewEncoder(w).Encode(completeResp{FileID: "file-1", Name: "resume.bin", Size: source.Size()})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server, &completeCalls
}

func newAliyunUploadDriver(t *testing.T, serverURL string, store drive.StateStore) *Driver {
	t.Helper()
	d := New(Options{RefreshToken: "refresh", DriveID: "drive", RootID: "root", APIBaseURL: serverURL})
	d.partSize = 3
	d.cl.mu.Lock()
	d.cl.accessToken = "access-token"
	d.cl.mu.Unlock()
	d.InstallStateStore(store)
	return d
}

func TestPutSourceCompleteInvalidErrorDeletesBinding(t *testing.T) {
	source := drive.NewBytesReadOnlyFileSource([]byte("abcdefgh"))
	store := drive.NewFileStateStore(filepath.Join(t.TempDir(), "driver"))
	server, completeCalls := completionServer(t, source, 3, func() int { return http.StatusNotFound })
	defer server.Close()

	d := newAliyunUploadDriver(t, server.URL, store)
	_, err := d.PutSource(context.Background(), drive.UploadRequest{
		ParentID: "parent",
		Name:     "resume.bin",
		Source:   source,
	})
	if err == nil || !strings.Contains(err.Error(), "upload complete") {
		t.Fatalf("upload error = %v, want complete failure", err)
	}
	if *completeCalls != 1 {
		t.Fatalf("complete calls = %d, want 1", *completeCalls)
	}
	// 404 判定为会话失效：绑定被回收，避免下次盲目续传。
	if bindings := d.sessions.List(); len(bindings) != 0 {
		t.Fatalf("binding must be deleted after invalid complete error, got %d", len(bindings))
	}
}

// noHashSource 是不提供任何内容指纹的 source：驱动无法寻址，不应创建上传绑定。
type noHashSource struct{ data []byte }

func (s noHashSource) Size() int64 { return int64(len(s.data)) }

func (s noHashSource) Open(context.Context) (drive.ReadOnlyFile, error) {
	return &noHashFile{bytes.NewReader(s.data)}, nil
}

type noHashFile struct{ bytes *bytes.Reader }

func (f *noHashFile) Read(p []byte) (int, error)  { return f.bytes.Read(p) }
func (f *noHashFile) ReadAt(p []byte, off int64) (int, error) {
	return f.bytes.ReadAt(p, off)
}
func (f *noHashFile) Seek(off int64, whence int) (int64, error) {
	return f.bytes.Seek(off, whence)
}
func (f *noHashFile) Close() error { return nil }

func TestPutSourceWithoutFingerprintCreatesNoBinding(t *testing.T) {
	source := noHashSource{data: []byte("abcdefgh")}
	store := drive.NewFileStateStore(filepath.Join(t.TempDir(), "driver"))
	server, _ := completionServer(t, source, 3, func() int { return http.StatusOK })
	defer server.Close()

	d := newAliyunUploadDriver(t, server.URL, store)
	entry, err := d.PutSource(context.Background(), drive.UploadRequest{
		ParentID: "parent",
		Name:     "resume.bin",
		Source:   source,
	})
	if err != nil {
		t.Fatal(err)
	}
	if entry.ID != "file-1" {
		t.Fatalf("entry id = %q, want file-1", entry.ID)
	}
	// 无内容指纹 ⇒ 无 sessionKey ⇒ 全程不写绑定（成功清理由 Delete 之外，
	// 这里直接从未创建）。
	if bindings := d.sessions.List(); len(bindings) != 0 {
		t.Fatalf("no binding should ever be created without fingerprint, got %d", len(bindings))
	}
}

func TestAliyunTokenJSONRoundTrip(t *testing.T) {
	want := aliyunToken{
		FileID:   "file-1",
		UploadID: "upload-1",
		PartSize: 3,
		PartURLs: []uploadPartInfo{
			{PartNumber: 1, UploadURL: "https://oss.example/upload/1"},
			{PartNumber: 2, UploadURL: "https://oss.example/upload/2"},
		},
		Confirmed: []byte{0b00000111}, // base64 "Bw=="
	}
	raw, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	var got aliyunToken
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.FileID != want.FileID || got.UploadID != want.UploadID || got.PartSize != want.PartSize {
		t.Fatalf("round trip lost identity fields: %+v", got)
	}
	if len(got.PartURLs) != 2 || got.PartURLs[0].PartNumber != 1 || got.PartURLs[1].UploadURL != "https://oss.example/upload/2" {
		t.Fatalf("round trip lost part urls: %+v", got.PartURLs)
	}
	if !bytes.Equal(got.Confirmed, want.Confirmed) {
		t.Fatalf("round trip bitmap = %v, want %v", got.Confirmed, want.Confirmed)
	}
	// 位图经 JSON 以 base64 保存，字节语义不变（此前线上问题即来自字段语义漂移）。
	var state map[string]any
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatal(err)
	}
	if _, ok := state["confirmed"].(string); !ok {
		t.Fatalf("confirmed field type = %T, want base64 string", state["confirmed"])
	}
}

func TestPutSourceCompleteTransientKeepsBindingAndResumes(t *testing.T) {
	source := drive.NewBytesReadOnlyFileSource([]byte("abcdefgh"))
	store := drive.NewFileStateStore(filepath.Join(t.TempDir(), "driver"))
	createCalls := 0
	completeCalls := 0
	completeFails := true
	oldWait := aliyunRetryWait
	aliyunRetryWait = func(context.Context, int) error { return nil } // 重试等待归零，测试不睡
	t.Cleanup(func() { aliyunRetryWait = oldWait })
	completeServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/adrive/v2/file/createWithFolders":
			createCalls++
			if createCalls > 1 {
				t.Fatal("fresh create must not be repeated on transient complete failure")
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
			_, _ = io.Copy(io.Discard, r.Body)
			w.WriteHeader(http.StatusOK)
		case r.URL.Path == "/v2/file/complete":
			completeCalls++
			if completeFails {
				// 始终 500：client 内部重试耗尽后仍报错，模拟持续临时故障。
				http.Error(w, "temporary failure", http.StatusInternalServerError)
				return
			}
			_ = json.NewEncoder(w).Encode(completeResp{FileID: "file-1", Name: "resume.bin", Size: source.Size()})
		default:
			http.NotFound(w, r)
		}
	}))
	defer completeServer.Close()

	d := newAliyunUploadDriver(t, completeServer.URL, store)
	_, err := d.PutSource(context.Background(), drive.UploadRequest{
		ParentID: "parent",
		Name:     "resume.bin",
		Source:   source,
	})
	if err == nil || !strings.Contains(err.Error(), "upload complete") {
		t.Fatalf("first upload error = %v, want complete failure", err)
	}
	// 500 是临时错误：绑定保留，且本地确认位图更新了全部 3 片。
	bindings := d.sessions.List()
	if len(bindings) != 1 {
		t.Fatalf("binding must survive transient complete error, got %d", len(bindings))
	}
	var tok aliyunToken
	if err := json.Unmarshal(bindings[0].Token, &tok); err != nil {
		t.Fatal(err)
	}
	if tok.FileID != "file-1" || tok.UploadID != "upload-1" || len(tok.PartURLs) != 3 {
		t.Fatalf("unexpected retained token: %+v", tok)
	}
	if tok.Confirmed[0] != 0b00000111 {
		t.Fatalf("confirmed bitmap = %08b, want parts 1-3 confirmed", tok.Confirmed[0])
	}

	// 故障恢复后重试走恢复路径：跳过全部 3 片，直接再次 complete，不重复 create。
	completeFails = false
	entry, err := d.PutSource(context.Background(), drive.UploadRequest{
		ParentID: "parent",
		Name:     "resume.bin",
		Source:   source,
	})
	if err != nil {
		t.Fatal(err)
	}
	if entry.ID != "file-1" {
		t.Fatalf("resumed entry id = %q", entry.ID)
	}
	if createCalls != 1 {
		t.Fatalf("create calls = %d, want 1", createCalls)
	}
	if bindings := d.sessions.List(); len(bindings) != 0 {
		t.Fatalf("binding should be deleted after successful retry, got %d", len(bindings))
	}
}
