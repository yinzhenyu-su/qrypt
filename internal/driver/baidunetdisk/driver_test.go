package baidunetdisk

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

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

func TestPutFileUploadsAndCreatesFile(t *testing.T) {
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
	entry, err := d.PutFile(ctx, "", "upload.txt", int64(len("hello world")), tmp)
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

func TestPutFileRejectsEmptyFile(t *testing.T) {
	d := New(Options{RefreshToken: "refresh", UseOnlineAPI: true})
	_, err := d.PutFile(context.Background(), "", "empty.txt", 0, "missing")
	if err == nil || !strings.Contains(err.Error(), "empty files") {
		t.Fatalf("err = %v, want empty files error", err)
	}
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
