package baidunetdisk

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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

func TestResolvePathUsesConfiguredRootPath(t *testing.T) {
	d := New(Options{RootPath: "/A/B/C"})
	root, err := d.ResolvePath(context.Background(), "/")
	if err != nil {
		t.Fatal(err)
	}
	if root != "/A/B/C" {
		t.Fatalf("ResolvePath root = %q, want configured root path", root)
	}
	nested, err := d.ResolvePath(context.Background(), "/x/y.txt")
	if err != nil {
		t.Fatal(err)
	}
	if nested != "/A/B/C/x/y.txt" {
		t.Fatalf("ResolvePath nested = %q, want path under configured root", nested)
	}
}

func TestListUsesRootPathAndPaginates(t *testing.T) {
	ctx := context.Background()
	var listed []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			writeJSON(t, w, tokenResp{AccessToken: "access", RefreshToken: "refresh", ExpiresIn: 3600})
		case "/rest/2.0/xpan/file":
			if r.URL.Query().Get("method") != "list" {
				t.Fatalf("unexpected method %q", r.URL.Query().Get("method"))
			}
			dir := r.URL.Query().Get("dir")
			listed = append(listed, dir)
			start := r.URL.Query().Get("start")
			resp := listResp{}
			if dir == "/Qrypt" && start == "0" {
				resp.List = []file{{FsID: 11, Path: "/Qrypt/a.txt", ServerFilename: "a.txt", Size: 3}}
			}
			writeJSON(t, w, resp)
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	d := New(Options{
		RefreshToken: "refresh",
		ClientID:     "client",
		ClientSecret: "secret",
		RootPath:     "/Qrypt",
		OAuthURL:     srv.URL + "/token",
		APIBaseURL:   srv.URL + "/rest/2.0",
		UseOnlineAPI: false,
	})
	entries, err := d.List(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("len(entries) = %d, want 1", len(entries))
	}
	if entries[0].ID != "/Qrypt/a.txt" || entries[0].ParentID != "/Qrypt" || entries[0].Name != "a.txt" {
		t.Fatalf("unexpected entry: %+v", entries[0])
	}
	if got := strings.Join(listed, ","); got != "/Qrypt" {
		t.Fatalf("listed dirs = %q, want /Qrypt", got)
	}
}

func TestReadFromOffsetToEOFUsesOpenEndedRange(t *testing.T) {
	ctx := context.Background()
	var gotRange string
	var gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			writeJSON(t, w, tokenResp{AccessToken: "access", RefreshToken: "refresh", ExpiresIn: 3600})
		case "/rest/2.0/xpan/multimedia":
			writeJSON(t, w, downloadResp{List: []struct {
				Dlink string `json:"dlink"`
			}{{Dlink: srvURL(r) + "/dlink"}}})
		case "/dlink":
			w.Header().Set("Location", srvURL(r)+"/download")
			w.WriteHeader(http.StatusFound)
		case "/download":
			gotRange = r.Header.Get("Range")
			gotUA = r.Header.Get("User-Agent")
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write([]byte("world"))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	d := New(Options{
		RefreshToken: "refresh",
		ClientID:     "client",
		ClientSecret: "secret",
		OAuthURL:     srv.URL + "/token",
		APIBaseURL:   srv.URL + "/rest/2.0",
		UseOnlineAPI: false,
	})
	entry := driveEntry("/Qrypt/a.txt", 42, 11)
	rc, err := d.Read(ctx, entry, 6, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "world" {
		t.Fatalf("read = %q, want world", string(data))
	}
	if gotRange != "bytes=6-" {
		t.Fatalf("Range = %q, want bytes=6-", gotRange)
	}
	if gotUA != defaultDownloadUA {
		t.Fatalf("User-Agent = %q, want %q", gotUA, defaultDownloadUA)
	}
}

func TestMkdirReturnsCreatedFsID(t *testing.T) {
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			writeJSON(t, w, tokenResp{AccessToken: "access", RefreshToken: "refresh", ExpiresIn: 3600})
		case "/rest/2.0/xpan/file":
			if err := r.ParseForm(); err != nil {
				t.Fatal(err)
			}
			if got := r.Form.Get("path"); got != "/Qrypt/new-dir" {
				t.Fatalf("path = %q, want /Qrypt/new-dir", got)
			}
			writeJSON(t, w, createResp{FsID: 99, Path: "/Qrypt/new-dir"})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	d := New(Options{
		RefreshToken: "refresh",
		ClientID:     "client",
		ClientSecret: "secret",
		RootPath:     "/Qrypt",
		OAuthURL:     srv.URL + "/token",
		APIBaseURL:   srv.URL + "/rest/2.0",
		UseOnlineAPI: false,
	})
	entry, err := d.Mkdir(ctx, "", "new-dir")
	if err != nil {
		t.Fatal(err)
	}
	if entry.ID != "/Qrypt/new-dir" || !entry.IsDir {
		t.Fatalf("unexpected entry: %+v", entry)
	}
	if got := entryFSID(entry); got != "99" {
		t.Fatalf("fs_id = %q, want 99", got)
	}
}

func TestPutSourceUploadsAndCreatesFile(t *testing.T) {
	ctx := context.Background()
	tmp := filepath.Join(t.TempDir(), "upload.txt")
	if err := os.WriteFile(tmp, []byte("hello world"), 0o600); err != nil {
		t.Fatal(err)
	}
	var sawUpload bool
	var sawCreate bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			writeJSON(t, w, tokenResp{AccessToken: "access", RefreshToken: "refresh", ExpiresIn: 3600})
		case "/rest/2.0/xpan/file":
			method := r.URL.Query().Get("method")
			if err := r.ParseForm(); err != nil {
				t.Fatal(err)
			}
			switch method {
			case "precreate":
				if got := r.Form.Get("path"); got != "/Qrypt/upload.txt" {
					t.Fatalf("precreate path = %q", got)
				}
				if got := r.Form.Get("block_list"); !strings.Contains(got, "5eb63bbbe01eeed093cb22bb8f5acdc3") {
					t.Fatalf("block_list = %q", got)
				}
				writeJSON(t, w, precreateResp{ReturnType: 1, UploadID: "upload-id", BlockList: []int{0}})
			case "create":
				sawCreate = true
				if got := r.Form.Get("uploadid"); got != "upload-id" {
					t.Fatalf("uploadid = %q", got)
				}
				writeJSON(t, w, createResp{FsID: 123, Path: "/Qrypt/upload.txt"})
			default:
				t.Fatalf("unexpected xpan method %q", method)
			}
		case "/rest/2.0/pcs/superfile2":
			sawUpload = true
			if got := r.URL.Query().Get("method"); got != "upload" {
				t.Fatalf("upload method = %q", got)
			}
			if got := r.URL.Query().Get("partseq"); got != "0" {
				t.Fatalf("partseq = %q", got)
			}
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				t.Fatal(err)
			}
			file, _, err := r.FormFile("file")
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			data, err := io.ReadAll(file)
			if err != nil {
				t.Fatal(err)
			}
			if string(data) != "hello world" {
				t.Fatalf("uploaded data = %q", string(data))
			}
			writeJSON(t, w, uploadSliceResp{})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	d := New(Options{
		RefreshToken: "refresh",
		ClientID:     "client",
		ClientSecret: "secret",
		RootPath:     "/Qrypt",
		OAuthURL:     srv.URL + "/token",
		APIBaseURL:   srv.URL + "/rest/2.0",
		UploadAPI:    srv.URL,
		UseOnlineAPI: false,
	})
	entry, err := d.PutSource(ctx, drive.UploadRequest{
		ParentID: "",
		Name:     "upload.txt",
		Source:   drive.NewLocalReadOnlyFileSource(tmp, int64(len("hello world"))),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !sawUpload || !sawCreate {
		t.Fatalf("sawUpload=%v sawCreate=%v", sawUpload, sawCreate)
	}
	if entry.ID != "/Qrypt/upload.txt" || entryFSID(entry) != "123" {
		t.Fatalf("unexpected entry: %+v", entry)
	}
}

func TestPutSourceResumesPersistedUploadSession(t *testing.T) {
	ctx := context.Background()
	data := bytes.Repeat([]byte("x"), defaultUploadPart*2+7)
	source := drive.NewBytesReadOnlyFileSource(data)
	store := drive.NewFileStateStore(filepath.Join(t.TempDir(), "driver"))
	precreateCalls := 0
	createCalls := 0
	partAttempts := map[string]int{}
	failPart1 := true

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			writeJSON(t, w, tokenResp{AccessToken: "access", RefreshToken: "refresh", ExpiresIn: 3600})
		case "/rest/2.0/xpan/file":
			method := r.URL.Query().Get("method")
			if err := r.ParseForm(); err != nil {
				t.Fatal(err)
			}
			switch method {
			case "precreate":
				precreateCalls++
				if precreateCalls > 1 {
					t.Fatalf("unexpected precreate call during resume")
				}
				writeJSON(t, w, precreateResp{ReturnType: 1, UploadID: "upload-id", BlockList: []int{0, 1, 2}})
			case "create":
				createCalls++
				if got := r.Form.Get("uploadid"); got != "upload-id" {
					t.Fatalf("uploadid = %q, want upload-id", got)
				}
				writeJSON(t, w, createResp{FsID: 123, Path: "/Qrypt/resume.bin"})
			default:
				t.Fatalf("unexpected xpan method %q", method)
			}
		case "/rest/2.0/pcs/superfile2":
			partSeq := r.URL.Query().Get("partseq")
			partAttempts[partSeq]++
			if err := r.ParseMultipartForm(16 << 20); err != nil {
				t.Fatal(err)
			}
			file, _, err := r.FormFile("file")
			if err != nil {
				t.Fatal(err)
			}
			body, err := io.ReadAll(file)
			_ = file.Close()
			if err != nil {
				t.Fatal(err)
			}
			seq, err := strconv.Atoi(partSeq)
			if err != nil {
				t.Fatalf("partseq = %q", partSeq)
			}
			offset := seq * defaultUploadPart
			end := offset + defaultUploadPart
			if end > len(data) {
				end = len(data)
			}
			if string(body) != string(data[offset:end]) {
				t.Fatalf("part %d body mismatch: got %d bytes", seq, len(body))
			}
			if partSeq == "1" && failPart1 {
				failPart1 = false
				http.Error(w, "temporary failure", http.StatusInternalServerError)
				return
			}
			writeJSON(t, w, uploadSliceResp{})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	first := newTestBaiduDriver(srv.URL, store)
	_, err := first.PutSource(ctx, drive.UploadRequest{
		ParentID: "",
		Name:     "resume.bin",
		Source:   source,
	})
	if err == nil || !strings.Contains(err.Error(), "upload part 1") {
		t.Fatalf("first upload error = %v, want part 1 failure", err)
	}
	if partAttempts["0"] != 1 || partAttempts["1"] != 1 || partAttempts["2"] != 0 {
		t.Fatalf("part attempts after first upload = %+v", partAttempts)
	}
	var state baiduUploadSessionState
	if err := store.LoadJSON(baiduUploadSessionStateFile, &state); err != nil {
		t.Fatal(err)
	}
	if len(state.Sessions) != 1 {
		t.Fatalf("session count after failed upload = %d, want 1", len(state.Sessions))
	}

	second := newTestBaiduDriver(srv.URL, store)
	entry, err := second.PutSource(ctx, drive.UploadRequest{
		ParentID: "",
		Name:     "resume.bin",
		Source:   source,
	})
	if err != nil {
		t.Fatal(err)
	}
	if entry.ID != "/Qrypt/resume.bin" || entryFSID(entry) != "123" {
		t.Fatalf("unexpected entry: %+v", entry)
	}
	if precreateCalls != 1 {
		t.Fatalf("precreate calls = %d, want 1", precreateCalls)
	}
	if createCalls != 1 {
		t.Fatalf("create calls = %d, want 1", createCalls)
	}
	if partAttempts["0"] != 1 || partAttempts["1"] != 2 || partAttempts["2"] != 1 {
		t.Fatalf("part attempts after resume = %+v, want part 0 skipped", partAttempts)
	}
	state = baiduUploadSessionState{}
	if err := store.LoadJSON(baiduUploadSessionStateFile, &state); err != nil {
		t.Fatal(err)
	}
	if len(state.Sessions) != 0 {
		t.Fatalf("session should be deleted after complete, got %+v", state.Sessions)
	}
}

func TestPutSourceRejectsEmptyFile(t *testing.T) {
	d := New(Options{RefreshToken: "refresh", UseOnlineAPI: true})
	_, err := d.PutSource(context.Background(), drive.UploadRequest{
		ParentID: "",
		Name:     "empty.txt",
		Source:   drive.NewLocalReadOnlyFileSource("missing", 0),
	})
	if err == nil || !strings.Contains(err.Error(), "empty files") {
		t.Fatalf("err = %v, want empty files error", err)
	}
	if !drive.IsNonRetryable(err) {
		t.Fatalf("err = %v, want non-retryable", err)
	}
}

func TestPutSourceInstantUploadIncrementsDebugCounter(t *testing.T) {
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			writeJSON(t, w, tokenResp{AccessToken: "access", RefreshToken: "refresh", ExpiresIn: 3600})
		case "/rest/2.0/xpan/file":
			if r.URL.Query().Get("method") != "precreate" {
				t.Fatalf("unexpected method %q", r.URL.Query().Get("method"))
			}
			writeJSON(t, w, precreateResp{
				ReturnType: 2,
				File: file{
					FsID:           456,
					Path:           "/Qrypt/instant.txt",
					ServerFilename: "instant.txt",
					Size:           4,
				},
			})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	d := New(Options{
		RefreshToken: "refresh",
		ClientID:     "client",
		ClientSecret: "secret",
		RootPath:     "/Qrypt",
		OAuthURL:     srv.URL + "/token",
		APIBaseURL:   srv.URL + "/rest/2.0",
		UseOnlineAPI: false,
	})
	entry, err := d.PutSource(ctx, drive.UploadRequest{
		ParentID: "",
		Name:     "instant.txt",
		Source:   drive.NewBytesReadOnlyFileSource([]byte("same")),
	})
	if err != nil {
		t.Fatal(err)
	}
	if entry.ID != "/Qrypt/instant.txt" {
		t.Fatalf("entry = %+v", entry)
	}
	snapshot, err := d.DebugSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Extra[drive.DebugExtraInstantUploadCount] != int64(1) {
		t.Fatalf("%s = %v, want 1", drive.DebugExtraInstantUploadCount, snapshot.Extra[drive.DebugExtraInstantUploadCount])
	}
}

func TestInitPersistsRotatedTokenState(t *testing.T) {
	ctx := context.Background()
	var gotRefresh string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			gotRefresh = r.URL.Query().Get("refresh_token")
			writeJSON(t, w, tokenResp{AccessToken: "new-access", RefreshToken: "new-refresh", ExpiresIn: 3600})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	store := drive.NewFileStateStore(filepath.Join(t.TempDir(), "driver"))
	d := New(Options{
		RefreshToken: "old-refresh",
		ClientID:     "client",
		ClientSecret: "secret",
		OAuthURL:     srv.URL + "/token",
		UseOnlineAPI: false,
	})
	d.InstallStateStore(store)
	if err := d.Init(ctx); err != nil {
		t.Fatal(err)
	}
	if gotRefresh != "old-refresh" {
		t.Fatalf("refresh_token = %q, want old-refresh", gotRefresh)
	}
	var state tokenState
	if err := store.LoadJSON("baidu_netdisk_token.json", &state); err != nil {
		t.Fatal(err)
	}
	if state.RefreshToken != "new-refresh" || state.AccessToken != "new-access" {
		t.Fatalf("unexpected state: %+v", state)
	}
}

func TestInitUsesStoredTokenStateBeforeConfigToken(t *testing.T) {
	ctx := context.Background()
	store := drive.NewFileStateStore(filepath.Join(t.TempDir(), "driver"))
	if err := store.SaveJSON("baidu_netdisk_token.json", tokenState{
		AccessToken:  "stored-access",
		RefreshToken: "stored-refresh",
		ExpiresAt:    time.Now().Add(time.Hour),
		UpdatedAt:    time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("token endpoint should not be called when stored access token is still valid")
	}))
	defer srv.Close()

	d := New(Options{
		RefreshToken: "old-refresh",
		ClientID:     "client",
		ClientSecret: "secret",
		OAuthURL:     srv.URL + "/token",
		UseOnlineAPI: false,
	})
	d.InstallStateStore(store)
	if err := d.Init(ctx); err != nil {
		t.Fatal(err)
	}
	if d.refreshToken != "stored-refresh" || d.accessToken != "stored-access" {
		t.Fatalf("driver tokens = refresh:%q access:%q", d.refreshToken, d.accessToken)
	}
	if d.tokenSource != "state" {
		t.Fatalf("tokenSource = %q, want state", d.tokenSource)
	}
}

func newTestBaiduDriver(serverURL string, store drive.StateStore) *Driver {
	d := New(Options{
		RefreshToken: "refresh",
		ClientID:     "client",
		ClientSecret: "secret",
		RootPath:     "/Qrypt",
		OAuthURL:     serverURL + "/token",
		APIBaseURL:   serverURL + "/rest/2.0",
		UploadAPI:    serverURL,
		UseOnlineAPI: false,
	})
	d.InstallStateStore(store)
	return d
}

func writeJSON(t *testing.T, w http.ResponseWriter, v any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Fatal(err)
	}
}

func srvURL(r *http.Request) string {
	return fmt.Sprintf("http://%s", r.Host)
}

func driveEntry(id string, size, fsID int64) drive.Entry {
	return drive.Entry{
		ID:   id,
		Size: size,
		Extra: map[string]any{
			"fs_id": fmt.Sprintf("%d", fsID),
		},
	}
}

func TestBaiduDebugSnapshot(t *testing.T) {
	d := New(Options{
		RefreshToken: "token",
		RootPath:     "/",
	})
	snapshot, err := d.DebugSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Driver != "baidu_netdisk" {
		t.Fatalf("driver = %q, want baidu_netdisk", snapshot.Driver)
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
