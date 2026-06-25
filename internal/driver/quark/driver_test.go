package quark

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

func TestDriverInitListAndResolveRootPath(t *testing.T) {
	var seenCookie string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenCookie = r.Header.Get("Cookie")
		if r.URL.Path != "/file/sort" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		parent := r.URL.Query().Get("pdir_fid")
		switch parent {
		case "0":
			writeJSON(t, w, map[string]any{
				"status": 200,
				"code":   0,
				"data": map[string]any{
					"list": []map[string]any{
						{"fid": "root-docs", "file_name": "Docs", "file": false, "size": 0},
					},
				},
				"metadata": map[string]any{"_total": 1},
			})
		case "root-docs":
			writeJSON(t, w, map[string]any{
				"status": 200,
				"code":   0,
				"data": map[string]any{
					"list": []map[string]any{
						{"fid": "file-1", "file_name": "a.txt", "file": true, "file_size": 12, "updated_at": 1700000000000},
					},
				},
				"metadata": map[string]any{"_total": 1},
			})
		default:
			t.Fatalf("unexpected parent: %s", parent)
		}
	}))
	defer server.Close()

	driver := New("k=v", Options{RootPath: "/Docs", BaseURL: server.URL, V2URL: server.URL})
	if err := driver.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	if seenCookie != "k=v" {
		t.Fatalf("cookie header = %q, want k=v", seenCookie)
	}

	entries, err := driver.List(context.Background(), "0")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("entry count = %d, want 1", len(entries))
	}
	entry := entries[0]
	if entry.ID != "file-1" || entry.ParentID != "root-docs" || entry.Name != "a.txt" || entry.IsDir || entry.Size != 12 {
		t.Fatalf("unexpected entry: %+v", entry)
	}
}

func TestRegisterQuarkDriver(t *testing.T) {
	driver, err := drive.New("quark", drive.Params{
		"cookie":   "k=v",
		"base_url": "http://127.0.0.1",
		"v2_url":   "http://127.0.0.1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := driver.(*Driver); !ok {
		t.Fatalf("driver type = %T, want *quark.Driver", driver)
	}
}

func TestDriverPutInstantUploadFinishes(t *testing.T) {
	var finishCalled bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/file/upload/pre":
			writeJSON(t, w, map[string]any{
				"status": 200,
				"code":   0,
				"data": map[string]any{
					"task_id": "task-1",
					"obj_key": "obj-1",
					"fid":     "pre-fid",
					"finish":  true,
				},
			})
		case "/file/upload/finish":
			finishCalled = true
			writeJSON(t, w, map[string]any{
				"status": 200,
				"code":   0,
				"data":   map[string]any{"fid": "final-fid"},
			})
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	driver := New("k=v", Options{BaseURL: server.URL, V2URL: server.URL})
	entry, err := driver.Put(context.Background(), "parent", "same.bin", 6, strings.NewReader("unused"))
	if err != nil {
		t.Fatal(err)
	}
	if !finishCalled {
		t.Fatal("finish was not called")
	}
	if entry.ID != "final-fid" || entry.ParentID != "parent" || entry.Name != "same.bin" || entry.Size != 6 {
		t.Fatalf("unexpected entry: %+v", entry)
	}
}

func TestDriverPutMultipartUpload(t *testing.T) {
	var partsMu sync.Mutex
	parts := map[string][]byte{}
	var completed bool
	oss := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/obj-1" {
			t.Fatalf("unexpected oss path: %s", r.URL.Path)
		}
		switch r.Method {
		case http.MethodPut:
			data, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatal(err)
			}
			partsMu.Lock()
			parts[r.URL.Query().Get("partNumber")] = data
			partsMu.Unlock()
			w.Header().Set("Etag", "etag-"+r.URL.Query().Get("partNumber"))
			w.WriteHeader(http.StatusOK)
		case http.MethodPost:
			data, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{"etag-1", "etag-2", "etag-3"} {
				if !bytes.Contains(data, []byte(want)) {
					t.Fatalf("complete body missing %s: %s", want, data)
				}
			}
			completed = true
			w.WriteHeader(http.StatusOK)
		default:
			t.Fatalf("unexpected oss method: %s", r.Method)
		}
	}))
	defer oss.Close()

	var authMu sync.Mutex
	var authCalls int
	var hashCalled bool
	var finishCalled bool
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/file/upload/pre":
			writeJSON(t, w, map[string]any{
				"status": 200,
				"code":   0,
				"data": map[string]any{
					"task_id":    "task-1",
					"upload_id":  "upload-1",
					"obj_key":    "obj-1",
					"upload_url": strings.TrimPrefix(oss.URL, "https://"),
					"fid":        "pre-fid",
					"bucket":     "bucket",
					"callback":   json.RawMessage(`{}`),
					"auth_info":  "auth-info",
				},
				"metadata": map[string]any{"part_size": 3},
			})
		case "/file/upload/auth":
			authMu.Lock()
			authCalls++
			authMu.Unlock()
			writeJSON(t, w, map[string]any{
				"status": 200,
				"code":   0,
				"data":   map[string]any{"auth_key": "auth-key"},
			})
		case "/file/update/hash":
			hashCalled = true
			writeJSON(t, w, map[string]any{
				"status": 200,
				"code":   0,
				"data":   map[string]any{"finish": false},
			})
		case "/file/upload/finish":
			finishCalled = true
			writeJSON(t, w, map[string]any{
				"status": 200,
				"code":   0,
				"data":   map[string]any{"fid": "final-fid"},
			})
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer api.Close()

	driver := New("k=v", Options{BaseURL: api.URL, V2URL: api.URL})
	routeOSSToTestServer(driver.cl.httpClient, oss)

	entry, err := driver.Put(context.Background(), "parent", "data.bin", 8, strings.NewReader("abcdefgh"))
	if err != nil {
		t.Fatal(err)
	}
	if entry.ID != "final-fid" || entry.ParentID != "parent" || entry.Name != "data.bin" || entry.Size != 8 {
		t.Fatalf("unexpected entry: %+v", entry)
	}
	if got := len(parts); got != 3 {
		t.Fatalf("part count = %d, want 3", got)
	}
	if got := string(parts["1"]) + string(parts["2"]) + string(parts["3"]); got != "abcdefgh" {
		t.Fatalf("uploaded data = %q, want abcdefgh", got)
	}
	authMu.Lock()
	gotAuthCalls := authCalls
	authMu.Unlock()
	if gotAuthCalls != 4 {
		t.Fatalf("auth calls = %d, want 4", gotAuthCalls)
	}
	if !hashCalled {
		t.Fatal("hash update was not called")
	}
	if !completed {
		t.Fatal("oss complete was not called")
	}
	if !finishCalled {
		t.Fatal("finish was not called")
	}
}

func TestDriverPutRespectsServerPartSize(t *testing.T) {
	var partsMu sync.Mutex
	var partSizes []int
	oss := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPut:
			data, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatal(err)
			}
			partsMu.Lock()
			partSizes = append(partSizes, len(data))
			partsMu.Unlock()
			w.Header().Set("Etag", "etag-"+r.URL.Query().Get("partNumber"))
			w.WriteHeader(http.StatusOK)
		case http.MethodPost:
			w.WriteHeader(http.StatusOK)
		default:
			t.Fatalf("unexpected oss method: %s", r.Method)
		}
	}))
	defer oss.Close()

	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/file/upload/pre":
			writeJSON(t, w, map[string]any{
				"status": 200,
				"code":   0,
				"data": map[string]any{
					"task_id":    "task-1",
					"upload_id":  "upload-1",
					"obj_key":    "obj-1",
					"upload_url": strings.TrimPrefix(oss.URL, "https://"),
					"fid":        "pre-fid",
					"bucket":     "bucket",
					"callback":   json.RawMessage(`{}`),
					"auth_info":  "auth-info",
				},
				"metadata": map[string]any{"part_size": 4 * 1024 * 1024},
			})
		case "/file/upload/auth":
			writeJSON(t, w, map[string]any{
				"status": 200,
				"code":   0,
				"data":   map[string]any{"auth_key": "auth-key"},
			})
		case "/file/update/hash":
			writeJSON(t, w, map[string]any{
				"status": 200,
				"code":   0,
				"data":   map[string]any{"finish": false},
			})
		case "/file/upload/finish":
			writeJSON(t, w, map[string]any{
				"status": 200,
				"code":   0,
				"data":   map[string]any{"fid": "final-fid"},
			})
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer api.Close()

	driver := New("k=v", Options{BaseURL: api.URL, V2URL: api.URL})
	routeOSSToTestServer(driver.cl.httpClient, oss)

	if _, err := driver.Put(context.Background(), "parent", "data.bin", 12*1024*1024, strings.NewReader(strings.Repeat("a", 12*1024*1024))); err != nil {
		t.Fatal(err)
	}
	partsMu.Lock()
	defer partsMu.Unlock()
	if len(partSizes) != 1 {
		t.Fatalf("part count = %d, want 1 (bumped to 16MB); sizes=%v", len(partSizes), partSizes)
	}
	if partSizes[0] != 12*1024*1024 {
		t.Fatalf("part size = %d, want %d (12MB as single part)", partSizes[0], 12*1024*1024)
	}
}

func TestDriverPutDoesNotSendUserAgent(t *testing.T) {
	var headers http.Header
	oss := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			headers = r.Header.Clone()
			w.Header().Set("Etag", "etag-1")
			w.WriteHeader(http.StatusOK)
		} else {
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer oss.Close()

	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/file/upload/pre":
			writeJSON(t, w, map[string]any{
				"status": 200, "code": 0,
				"data": map[string]any{
					"task_id": "t", "upload_id": "u", "obj_key": "obj",
					"upload_url": strings.TrimPrefix(oss.URL, "https://"),
					"fid": "f", "bucket": "bucket", "callback": json.RawMessage(`{}`),
					"auth_info": "a",
				},
				"metadata": map[string]any{"part_size": 1},
			})
		case "/file/upload/auth":
			writeJSON(t, w, map[string]any{"status": 200, "code": 0, "data": map[string]any{"auth_key": "k"}})
		case "/file/update/hash":
			writeJSON(t, w, map[string]any{"status": 200, "code": 0, "data": map[string]any{"finish": true, "fid": "f"}})
		case "/file/upload/finish":
			writeJSON(t, w, map[string]any{"status": 200, "code": 0, "data": map[string]any{"fid": "f"}})
		default:
			t.Fatalf("unexpected api path: %s", r.URL.Path)
		}
	}))
	defer api.Close()

	driver := New("k=v", Options{BaseURL: api.URL, V2URL: api.URL})
	routeOSSToTestServer(driver.cl.httpClient, oss)
	if _, err := driver.Put(context.Background(), "parent", "f.bin", 3, strings.NewReader("abc")); err != nil {
		t.Fatal(err)
	}
	if headers == nil {
		t.Fatal("OSS PUT request was not received")
	}
	if v := headers.Get("User-Agent"); v == "Go-http-client/1.1" || v == "Go-http-client/2.0" {
	} else if v != "" {
		t.Fatalf("OSS PUT request must not include custom User-Agent header, got: %q", v)
	}
	for _, h := range []string{"Authorization", "Content-Type", "x-oss-date", "x-oss-user-agent", "Referer"} {
		if headers.Get(h) == "" {
			t.Fatalf("OSS PUT request is missing required header: %s", h)
		}
	}
}

func TestOssURLWithBucketPrefixesHost(t *testing.T) {
	pre := &upPreResp{}
	pre.Data.Bucket = "ul-sz"
	pre.Data.UploadURL = "pds.quark.cn"
	pre.Data.ObjKey = "path/to/file.bin"
	got := ossURL(pre)
	want := "https://ul-sz.pds.quark.cn/path/to/file.bin"
	if got != want {
		t.Fatalf("ossURL = %q, want %q", got, want)
	}
}

func TestOssURLWithoutBucket(t *testing.T) {
	pre := &upPreResp{}
	pre.Data.Bucket = ""
	pre.Data.UploadURL = "endpoint.quark.cn"
	pre.Data.ObjKey = "obj"
	got := ossURL(pre)
	want := "https://endpoint.quark.cn/obj"
	if got != want {
		t.Fatalf("ossURL = %q, want %q", got, want)
	}
}

func TestOssURLStripsProtocol(t *testing.T) {
	pre := &upPreResp{}
	pre.Data.Bucket = "ul-sz"
	pre.Data.UploadURL = "http://pds.quark.cn"
	pre.Data.ObjKey = "obj"
	got := ossURL(pre)
	want := "https://ul-sz.pds.quark.cn/obj"
	if got != want {
		t.Fatalf("ossURL = %q, want %q", got, want)
	}
}

func TestOssURLStripsPathFromUploadURL(t *testing.T) {
	pre := &upPreResp{}
	pre.Data.Bucket = "ul-sz"
	pre.Data.UploadURL = "https://pds.quark.cn/some/path"
	pre.Data.ObjKey = "obj"
	got := ossURL(pre)
	want := "https://ul-sz.pds.quark.cn/obj"
	if got != want {
		t.Fatalf("ossURL = %q, want %q", got, want)
	}
}

func TestOssURLWithRealWorldExample(t *testing.T) {
	pre := &upPreResp{}
	pre.Data.Bucket = "ul-sz"
	pre.Data.UploadURL = "http://pds.quark.cn"
	pre.Data.ObjKey = "j0uOasD0/5936150331/e410edbb116847b08f047d6474aa52396a3cefab/6a3cefab53760f59fd5b42be8a069dd70859b8b9"
	got := ossURL(pre)
	want := "https://ul-sz.pds.quark.cn/j0uOasD0/5936150331/e410edbb116847b08f047d6474aa52396a3cefab/6a3cefab53760f59fd5b42be8a069dd70859b8b9"
	if got != want {
		t.Fatalf("ossURL = %q, want %q", got, want)
	}
}

func routeOSSToTestServer(c *http.Client, server *httptest.Server) {
	targetAddr := strings.TrimPrefix(server.URL, "https://")
	baseTransport := c.Transport.(*http.Transport)
	baseTransport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	baseDial := baseTransport.DialContext
	baseTransport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		if strings.HasSuffix(addr, targetAddr) {
			addr = targetAddr
		}
		return baseDial(ctx, network, addr)
	}
}

func writeJSON(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Fatal(err)
	}
}
