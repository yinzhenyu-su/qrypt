package quark

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	cryptpkg "github.com/yinzhenyu/qrypt/pkg/crypt"
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

func TestDriverDebugSnapshot(t *testing.T) {
	driver := New("k=v", Options{RootID: "root", RootPath: "/Docs"})
	snapshot, err := driver.DebugSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Driver != "quark" {
		t.Fatalf("driver = %q, want quark", snapshot.Driver)
	}
	if snapshot.Stats[drive.DebugStatRootID] != "root" {
		t.Fatalf("unexpected stats: %+v", snapshot.Stats)
	}
	if snapshot.Stats[drive.DebugStatRootPath] != "/Docs" {
		t.Fatalf("unexpected stats: %+v", snapshot.Stats)
	}
	if snapshot.Extra[drive.DebugExtraCredentialSource] == nil {
		t.Fatalf("expected credential_source extra, got %+v", snapshot.Extra)
	}
}

func TestCookieUpdatePersistsState(t *testing.T) {
	store := drive.NewFileStateStore(filepath.Join(t.TempDir(), "driver"))
	driver := New("k=v", Options{})
	driver.InstallStateStore(store)
	driver.cl.updateCookie("__puus", "new")
	var state cookieState
	if err := store.LoadJSON("quark_cookie.json", &state); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(state.Cookie, "__puus=new") {
		t.Fatalf("cookie state = %q, want __puus=new", state.Cookie)
	}
	if driver.cookieSource != "response" {
		t.Fatalf("cookieSource = %q, want response", driver.cookieSource)
	}
}

func TestLoadCookieStateOverridesConfigCookie(t *testing.T) {
	store := drive.NewFileStateStore(filepath.Join(t.TempDir(), "driver"))
	if err := store.SaveJSON("quark_cookie.json", cookieState{
		Cookie:    "stored=1; __puus=stored",
		UpdatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	driver := New("config=1", Options{})
	driver.InstallStateStore(store)
	driver.loadCookieState()
	if got := driver.cl.cookieValue(); got != "stored=1; __puus=stored" {
		t.Fatalf("cookie = %q, want stored cookie", got)
	}
	if driver.cookieSource != "state" {
		t.Fatalf("cookieSource = %q, want state", driver.cookieSource)
	}
}

func TestOSSClientHasNoWholeRequestTimeout(t *testing.T) {
	client := newOSSClient()
	if client.Timeout != 0 {
		t.Fatalf("oss client timeout = %s, want no whole-request timeout", client.Timeout)
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
	localPath := filepath.Join(t.TempDir(), "same.bin")
	if err := os.WriteFile(localPath, []byte("unused"), 0o600); err != nil {
		t.Fatal(err)
	}
	entry, err := driver.PutSource(context.Background(), drive.UploadRequest{
		ParentID: "parent",
		Name:     "same.bin",
		Source:   drive.NewLocalReadOnlyFileSource(localPath, 6),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !finishCalled {
		t.Fatal("finish was not called")
	}
	if entry.ID != "final-fid" || entry.ParentID != "parent" || entry.Name != "same.bin" || entry.Size != 6 {
		t.Fatalf("unexpected entry: %+v", entry)
	}
	if entry.ModTime.IsZero() {
		t.Fatal("instant upload entry modtime is zero")
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
	content := []byte("abcdefgh")
	contentMD5 := md5.Sum(content)
	contentSHA1 := sha1.Sum(content)
	wantMD5 := fmt.Sprintf("%X", contentMD5[:])
	wantSHA1 := fmt.Sprintf("%X", contentSHA1[:])
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
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["md5"] != wantMD5 || body["sha1"] != wantSHA1 {
				t.Fatalf("unexpected hash body: %+v, want md5=%s sha1=%s", body, wantMD5, wantSHA1)
			}
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
	routeOSSToTestServer(driver.cl.ossClient, oss)

	tmp := filepath.Join(t.TempDir(), "source.bin")
	if err := os.WriteFile(tmp, content, 0o600); err != nil {
		t.Fatal(err)
	}
	entry, err := driver.PutSource(context.Background(), drive.UploadRequest{
		ParentID: "parent",
		Name:     "data.bin",
		Source:   drive.NewLocalReadOnlyFileSource(tmp, int64(len(content))),
	})
	if err != nil {
		t.Fatal(err)
	}
	if entry.ID != "final-fid" || entry.ParentID != "parent" || entry.Name != "data.bin" || entry.Size != 8 {
		t.Fatalf("unexpected entry: %+v", entry)
	}
	if entry.ModTime.IsZero() {
		t.Fatal("multipart upload entry modtime is zero")
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

func TestDriverPutSourceUnderCryptHashesEncryptedSource(t *testing.T) {
	var uploadedCipher []byte
	var completed bool
	oss := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPut:
			data, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatal(err)
			}
			uploadedCipher = append(uploadedCipher[:0], data...)
			w.Header().Set("Etag", "etag-1")
			w.WriteHeader(http.StatusOK)
		case http.MethodPost:
			data, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Contains(data, []byte("etag-1")) {
				t.Fatalf("complete body missing etag-1: %s", data)
			}
			completed = true
			w.WriteHeader(http.StatusOK)
		default:
			t.Fatalf("unexpected oss method: %s", r.Method)
		}
	}))
	defer oss.Close()

	plain := []byte("plain payload")
	plainMD5 := md5.Sum(plain)
	plainSHA1 := sha1.Sum(plain)
	plainMD5Hex := fmt.Sprintf("%X", plainMD5[:])
	plainSHA1Hex := fmt.Sprintf("%X", plainSHA1[:])
	var hashCalled bool
	var finishCalled bool
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/file/upload/pre":
			writeJSON(t, w, map[string]any{
				"status": 200,
				"code":   0,
				"data": map[string]any{
					"task_id":    "task-crypt",
					"upload_id":  "upload-crypt",
					"obj_key":    "obj-crypt",
					"upload_url": strings.TrimPrefix(oss.URL, "https://"),
					"fid":        "pre-fid",
					"bucket":     "bucket",
					"callback":   json.RawMessage(`{}`),
					"auth_info":  "auth-info",
				},
				"metadata": map[string]any{"part_size": 64},
			})
		case "/file/upload/auth":
			writeJSON(t, w, map[string]any{
				"status": 200,
				"code":   0,
				"data":   map[string]any{"auth_key": "auth-key"},
			})
		case "/file/update/hash":
			hashCalled = true
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			cipherMD5 := md5.Sum(uploadedCipher)
			cipherSHA1 := sha1.Sum(uploadedCipher)
			wantMD5 := fmt.Sprintf("%X", cipherMD5[:])
			wantSHA1 := fmt.Sprintf("%X", cipherSHA1[:])
			if body["md5"] != wantMD5 || body["sha1"] != wantSHA1 {
				t.Fatalf("unexpected encrypted hash body: %+v, want md5=%s sha1=%s", body, wantMD5, wantSHA1)
			}
			if body["md5"] == plainMD5Hex || body["sha1"] == plainSHA1Hex {
				t.Fatalf("hash body used plaintext hash: %+v", body)
			}
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
	routeOSSToTestServer(driver.cl.ossClient, oss)
	cp, err := cryptpkg.NewRcloneCipher("password", "salt")
	if err != nil {
		t.Fatal(err)
	}
	cryptDriver := cryptpkg.NewDriver(driver, cp, cryptpkg.DriverOptions{})
	tmp := filepath.Join(t.TempDir(), "plain.bin")
	if err := os.WriteFile(tmp, plain, 0o600); err != nil {
		t.Fatal(err)
	}
	entry, err := cryptDriver.PutSource(context.Background(), drive.UploadRequest{
		ParentID: "parent",
		Name:     "secret.bin",
		Source:   drive.NewLocalReadOnlyFileSource(tmp, int64(len(plain))),
	})
	if err != nil {
		t.Fatal(err)
	}
	if entry.ID != "final-fid" || entry.ParentID != "parent" || entry.Name != "secret.bin" || entry.Size != int64(len(plain)) {
		t.Fatalf("unexpected entry: %+v", entry)
	}
	if bytes.Contains(uploadedCipher, plain) {
		t.Fatal("uploaded body contains plaintext")
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

func TestDriverPutSourceUnderContentDedupCryptInstantUploadsByEncryptedHash(t *testing.T) {
	ossCalled := false
	oss := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ossCalled = true
		t.Fatalf("oss should not be called for hash-finished upload: %s %s", r.Method, r.URL)
	}))
	defer oss.Close()

	plain := []byte("same plaintext should instant upload when encrypted content hash already exists")
	cp, err := cryptpkg.NewRcloneCipher("password", "salt")
	if err != nil {
		t.Fatal(err)
	}
	encrypted := encryptForTest(t, cp, plain)
	encryptedMD5 := md5.Sum(encrypted)
	encryptedSHA1 := sha1.Sum(encrypted)
	wantMD5 := fmt.Sprintf("%X", encryptedMD5[:])
	wantSHA1 := fmt.Sprintf("%X", encryptedSHA1[:])
	var hashCalled bool
	var finishCalled bool
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/file/upload/pre":
			writeJSON(t, w, map[string]any{
				"status": 200,
				"code":   0,
				"data": map[string]any{
					"task_id":    "task-dedup",
					"upload_id":  "upload-dedup",
					"obj_key":    "obj-dedup",
					"upload_url": strings.TrimPrefix(oss.URL, "https://"),
					"fid":        "pre-fid",
					"bucket":     "bucket",
					"callback":   json.RawMessage(`{}`),
					"auth_info":  "auth-info",
				},
				"metadata": map[string]any{"part_size": 64},
			})
		case "/file/update/hash":
			hashCalled = true
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["md5"] != wantMD5 || body["sha1"] != wantSHA1 {
				t.Fatalf("unexpected encrypted hash body: %+v, want md5=%s sha1=%s", body, wantMD5, wantSHA1)
			}
			writeJSON(t, w, map[string]any{
				"status": 200,
				"code":   0,
				"data":   map[string]any{"finish": true, "fid": "existing-fid"},
			})
		case "/file/upload/finish":
			finishCalled = true
			writeJSON(t, w, map[string]any{
				"status": 200,
				"code":   0,
				"data":   map[string]any{"fid": "final-existing-fid"},
			})
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer api.Close()

	driver := New("k=v", Options{BaseURL: api.URL, V2URL: api.URL})
	routeOSSToTestServer(driver.cl.ossClient, oss)
	cryptDriver := cryptpkg.NewDriver(driver, cp, cryptpkg.DriverOptions{ContentDedup: true})
	entry, err := cryptDriver.PutSource(context.Background(), drive.UploadRequest{
		ParentID: "parent",
		Name:     "secret.bin",
		Source:   drive.NewBytesReadOnlyFileSource(plain),
	})
	if err != nil {
		t.Fatal(err)
	}
	if entry.ID != "final-existing-fid" || entry.ParentID != "parent" || entry.Name != "secret.bin" || entry.Size != int64(len(plain)) {
		t.Fatalf("unexpected entry: %+v", entry)
	}
	if !hashCalled {
		t.Fatal("hash update was not called")
	}
	if !finishCalled {
		t.Fatal("finish was not called")
	}
	if ossCalled {
		t.Fatal("oss was called")
	}
}

func encryptForTest(t *testing.T, cp cryptpkg.Cipher, plain []byte) []byte {
	t.Helper()
	plainSHA256 := sha256.Sum256(plain)
	nonce, err := cp.ContentDedupNonce(plainSHA256, int64(len(plain)))
	if err != nil {
		t.Fatal(err)
	}
	encrypted := make([]byte, 0, cp.EncryptedSize(int64(len(plain))))
	encrypted = append(encrypted, []byte(cryptpkg.FileMagic)...)
	encrypted = append(encrypted, nonce[:]...)
	for blockIndex, offset := uint64(0), 0; offset < len(plain); blockIndex, offset = blockIndex+1, offset+cryptpkg.BlockDataSize {
		end := offset + cryptpkg.BlockDataSize
		if end > len(plain) {
			end = len(plain)
		}
		block, err := cp.EncryptBlock(plain[offset:end], blockIndex, nonce)
		if err != nil {
			t.Fatal(err)
		}
		encrypted = append(encrypted, block...)
	}
	return encrypted
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
	routeOSSToTestServer(driver.cl.ossClient, oss)

	localPath := filepath.Join(t.TempDir(), "data.bin")
	if err := os.WriteFile(localPath, []byte(strings.Repeat("a", 12*1024*1024)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := driver.PutSource(context.Background(), drive.UploadRequest{
		ParentID: "parent",
		Name:     "data.bin",
		Source:   drive.NewLocalReadOnlyFileSource(localPath, 12*1024*1024),
	}); err != nil {
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

func TestDriverUploadPartUsesNativeBandwidthLimiter(t *testing.T) {
	oss := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Fatalf("unexpected oss method: %s", r.Method)
		}
		_, _ = io.ReadAll(r.Body)
		w.Header().Set("Etag", "etag-1")
		w.WriteHeader(http.StatusOK)
	}))
	defer oss.Close()

	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/file/upload/auth" {
			t.Fatalf("unexpected api path: %s", r.URL.Path)
		}
		writeJSON(t, w, map[string]any{
			"status": 200,
			"code":   0,
			"data":   map[string]any{"auth_key": "auth-key"},
		})
	}))
	defer api.Close()

	driver := New("k=v", Options{BaseURL: api.URL, V2URL: api.URL})
	routeOSSToTestServer(driver.cl.ossClient, oss)
	driver.InstallBandwidthLimiter(drive.NewBandwidthLimiter(drive.BandwidthLimits{UploadBytesPerSecond: 1}))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	pre := &upPreResp{}
	pre.Data.TaskID = "task-1"
	pre.Data.UploadID = "upload-1"
	pre.Data.ObjKey = "obj"
	pre.Data.UploadURL = strings.TrimPrefix(oss.URL, "https://")
	pre.Data.Bucket = "bucket"
	pre.Data.AuthInfo = "auth-info"

	_, err := driver.uploadPart(ctx, pre, 1, []byte("slow"))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("uploadPart error = %v, want context deadline exceeded", err)
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
