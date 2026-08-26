package p115

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	driver115 "github.com/SheltonZhu/115driver/pkg/driver"
	"github.com/aliyun/aliyun-oss-go-sdk/oss"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/drive/session"
)

func TestResolvePathRootUsesConfiguredRootID(t *testing.T) {
	d := &Driver{rootID: "root-cid"}
	got, err := d.ResolvePath(context.Background(), "/")
	if err != nil {
		t.Fatal(err)
	}
	if got != "root-cid" {
		t.Fatalf("ResolvePath root = %q, want configured root id", got)
	}
}

func TestRequiredUploadHashesIncludesSHA1(t *testing.T) {
	d := &Driver{}
	got := d.RequiredUploadHashes()
	if len(got) != 1 || got[0] != drive.HashSHA1 {
		t.Fatalf("RequiredUploadHashes = %+v, want [%s]", got, drive.HashSHA1)
	}
}

func TestDebugSnapshotReportsInstantUploadCount(t *testing.T) {
	d := &Driver{}
	snapshot, err := d.DebugSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := snapshot.Extra[drive.DebugExtraInstantUploadCount]; got != int64(0) {
		t.Fatalf("instant upload count = %v, want 0", got)
	}

	d.instantUploads.Add(2)
	snapshot, err = d.DebugSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := snapshot.Extra[drive.DebugExtraInstantUploadCount]; got != int64(2) {
		t.Fatalf("instant upload count = %v, want 2", got)
	}
}

func TestLoginCheckWithRetryRetriesEOF(t *testing.T) {
	oldDelays := loginCheckRetryDelays
	loginCheckRetryDelays = []time.Duration{0}
	t.Cleanup(func() { loginCheckRetryDelays = oldDelays })

	driver := New(Options{})
	calls := 0
	err := driver.loginCheckWithRetry(context.Background(), func() error {
		calls++
		if calls == 1 {
			return io.EOF
		}
		return nil
	})

	if err != nil {
		t.Fatalf("loginCheckWithRetry error = %v", err)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
}

func TestLoginCheckWithRetryDoesNotRetryBusinessError(t *testing.T) {
	oldDelays := loginCheckRetryDelays
	loginCheckRetryDelays = []time.Duration{0}
	t.Cleanup(func() { loginCheckRetryDelays = oldDelays })

	driver := New(Options{})
	want := errors.New("bad cookie")
	calls := 0
	err := driver.loginCheckWithRetry(context.Background(), func() error {
		calls++
		return want
	})

	if !errors.Is(err, want) {
		t.Fatalf("loginCheckWithRetry error = %v, want %v", err, want)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}
}

func TestLoginCheckWithFallbackFallsBackToConfigCookie(t *testing.T) {
	store := drive.NewFileStateStore(filepath.Join(t.TempDir(), "driver"))
	if err := store.SaveJSON("115_cookie.json", cookieState{Cookie: "UID=state-uid; CID=state-cid; SEID=state-seid; KID=state-kid"}); err != nil {
		t.Fatal(err)
	}
	driver := New(Options{Cookie: "UID=cfg-uid; CID=cfg-cid; SEID=cfg-seid; KID=cfg-kid"})
	driver.InstallStateStore(store)
	driver.loadCookieState()
	driver.cl = driver115.New()

	want := errors.New("bad cookie")
	calls := 0
	err := driver.loginCheckWithFallback(context.Background(), "UID=cfg-uid; CID=cfg-cid; SEID=cfg-seid; KID=cfg-kid", func() error {
		calls++
		if calls == 1 {
			return want // state cookie fails
		}
		return nil // config cookie succeeds
	})

	if err != nil {
		t.Fatalf("loginCheckWithFallback error = %v, want nil after config fallback", err)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2 (state + config retry)", calls)
	}
	if driver.cookieSource != "config" {
		t.Fatalf("cookieSource = %q, want config after fallback", driver.cookieSource)
	}
	// The stale state must be replaced by the working config cookie.
	var state cookieState
	if err := store.LoadJSON("115_cookie.json", &state); err != nil {
		t.Fatal(err)
	}
	if state.Cookie == "" || strings.Contains(state.Cookie, "state-uid") {
		t.Fatalf("state cookie not refreshed: %q", state.Cookie)
	}
}

func TestLoginCheckWithFallbackNoFallbackWithoutConfigCookie(t *testing.T) {
	store := drive.NewFileStateStore(filepath.Join(t.TempDir(), "driver"))
	if err := store.SaveJSON("115_cookie.json", cookieState{Cookie: "UID=state-uid; CID=state-cid; SEID=state-seid; KID=state-kid"}); err != nil {
		t.Fatal(err)
	}
	driver := New(Options{}) // no config cookie
	driver.InstallStateStore(store)
	driver.loadCookieState()
	driver.cl = driver115.New()

	want := errors.New("bad cookie")
	calls := 0
	err := driver.loginCheckWithFallback(context.Background(), "", func() error {
		calls++
		return want
	})

	if !errors.Is(err, want) {
		t.Fatalf("loginCheckWithFallback error = %v, want %v", err, want)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1 (no config cookie to retry with)", calls)
	}
}

func TestLoginCheckWithFallbackNoFallbackWhenConfigMatchesState(t *testing.T) {
	cookie := "UID=uid; CID=cid; SEID=seid; KID=kid"
	store := drive.NewFileStateStore(filepath.Join(t.TempDir(), "driver"))
	if err := store.SaveJSON("115_cookie.json", cookieState{Cookie: cookie}); err != nil {
		t.Fatal(err)
	}
	driver := New(Options{Cookie: cookie})
	driver.InstallStateStore(store)
	driver.loadCookieState()
	driver.cl = driver115.New()

	want := errors.New("bad cookie")
	calls := 0
	err := driver.loginCheckWithFallback(context.Background(), cookie, func() error {
		calls++
		return want
	})

	if !errors.Is(err, want) {
		t.Fatalf("loginCheckWithFallback error = %v, want %v", err, want)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1 (config equals state, nothing to retry with)", calls)
	}
}

func TestFactoryAllowsCookieFromState(t *testing.T) {
	drv, err := drive.New("115", drive.Params{})
	if err != nil {
		t.Fatalf("drive.New returned error: %v", err)
	}
	err = drv.Init(context.Background())
	if err == nil || !strings.Contains(err.Error(), "missing cookie") {
		t.Fatalf("Init error = %v, want missing cookie", err)
	}
}

func TestLoadCookieStateMergesWithConfigCookie(t *testing.T) {
	store := drive.NewFileStateStore(filepath.Join(t.TempDir(), "driver"))
	if err := store.SaveJSON("115_cookie.json", cookieState{
		Cookie: "SEID=state; KID=state-kid",
	}); err != nil {
		t.Fatal(err)
	}
	driver := New(Options{Cookie: "UID=uid; CID=cid; SEID=config"})
	driver.InstallStateStore(store)

	driver.loadCookieState()

	if driver.cookies != "UID=uid; CID=cid; SEID=state; KID=state-kid" {
		t.Fatalf("cookie = %q, want merged config and state cookie", driver.cookies)
	}
	if driver.cookieSource != "state" {
		t.Fatalf("cookieSource = %q, want state", driver.cookieSource)
	}
}

func TestSaveUpdatedCookiePreservesExistingCookieKeys(t *testing.T) {
	store := drive.NewFileStateStore(filepath.Join(t.TempDir(), "driver"))
	driver := New(Options{Cookie: "UID=uid; CID=cid; SEID=old; KID=kid"})
	driver.InstallStateStore(store)

	driver.saveUpdatedCookie("SEID=new")

	var state cookieState
	if err := store.LoadJSON("115_cookie.json", &state); err != nil {
		t.Fatal(err)
	}
	if state.Cookie != "UID=uid; CID=cid; SEID=new; KID=kid" {
		t.Fatalf("cookie state = %q, want updated SEID with existing keys preserved", state.Cookie)
	}
	if driver.cookieSource != "response" {
		t.Fatalf("cookieSource = %q, want response", driver.cookieSource)
	}
	if state.UpdatedAt.IsZero() {
		t.Fatal("UpdatedAt is zero")
	}
}

func TestSaveCookieStatePersistsCurrentCookie(t *testing.T) {
	store := drive.NewFileStateStore(filepath.Join(t.TempDir(), "driver"))
	driver := New(Options{Cookie: "UID=uid; CID=cid; SEID=seid"})
	driver.InstallStateStore(store)

	driver.saveCookieState(driver.cookies, driver.cookieSource)

	var state cookieState
	if err := store.LoadJSON("115_cookie.json", &state); err != nil {
		t.Fatal(err)
	}
	if state.Cookie != "UID=uid; CID=cid; SEID=seid" {
		t.Fatalf("cookie state = %q, want current cookie", state.Cookie)
	}
	if state.UpdatedAt.IsZero() {
		t.Fatal("UpdatedAt is zero")
	}
	if driver.cookieSource != "config" {
		t.Fatalf("cookieSource = %q, want config", driver.cookieSource)
	}
}

func TestCurrentCookieHeaderMergesRestyJarCookies(t *testing.T) {
	driver := New(Options{Cookie: "UID=uid; CID=cid; SEID=old; KID=kid"})
	driver.cl = driver115.New()
	u, err := url.Parse("https://webapi.115.com/")
	if err != nil {
		t.Fatal(err)
	}
	driver.cl.Client.GetClient().Jar.SetCookies(u, []*http.Cookie{
		{Name: "SEID", Value: "new"},
		{Name: "OOFL", Value: "extra"},
	})

	got := driver.currentCookieHeader()

	for _, want := range []string{"UID=uid", "CID=cid", "SEID=new", "KID=kid", "OOFL=extra"} {
		if !strings.Contains(got, want) {
			t.Fatalf("cookie = %q, missing %q", got, want)
		}
	}
}

func TestUploadPartRanges(t *testing.T) {
	parts := p115UploadPartRanges(35, 16)
	want := []p115UploadPartRange{
		{Number: 1, Offset: 0, Size: 16},
		{Number: 2, Offset: 16, Size: 16},
		{Number: 3, Offset: 32, Size: 3},
	}
	if len(parts) != len(want) {
		t.Fatalf("parts len = %d, want %d", len(parts), len(want))
	}
	for i := range want {
		if parts[i] != want[i] {
			t.Fatalf("part[%d] = %+v, want %+v", i, parts[i], want[i])
		}
	}
}

func TestWrappedEntryExtraPreservesRawMetadata(t *testing.T) {
	raw := driver115.File{
		FileID:   "file-id",
		Name:     "encrypted-name",
		Size:     74,
		PickCode: "pick-code",
		Sha1:     "abc123",
	}
	entry := drive.Entry{
		ID:    "file-id",
		Name:  "plain.txt",
		Size:  26,
		Extra: drive.EntryExtraWrapper{RemoteName: raw.Name, Raw: raw},
	}

	if got := rawEntrySize(entry); got != raw.Size {
		t.Fatalf("rawEntrySize = %d, want %d", got, raw.Size)
	}
	if got := entrySHA1(entry); got != "ABC123" {
		t.Fatalf("entrySHA1 = %q, want ABC123", got)
	}
}

func TestUploadSessionBindingPersistsAcrossInstances(t *testing.T) {
	dir := t.TempDir()
	store := drive.NewFileStateStore(filepath.Join(dir, "driver"))
	driver := New(Options{Cookie: "UID=uid"})
	driver.InstallStateStore(store)

	key := session.Identity{ParentID: "0", Name: "video.bin", Size: 32 << 20, Fingerprint: "ABC"}.Key()
	token, err := json.Marshal(p115Token{Bucket: "bucket", Object: "object", UploadID: "upload-id", PartSize: p115MultipartPartSize})
	if err != nil {
		t.Fatal(err)
	}
	if err := driver.sessions.Create(key, token); err != nil {
		t.Fatal(err)
	}

	reloaded := session.NewIndex(drive.NewFileStateStore(filepath.Join(dir, "driver")), p115SessionFile, session.IndexOptions{})
	binding, ok := reloaded.Get(key)
	if !ok {
		t.Fatal("expected binding to survive a new index instance")
	}
	var tok p115Token
	if err := json.Unmarshal(binding.Token, &tok); err != nil {
		t.Fatal(err)
	}
	if tok.UploadID != "upload-id" || tok.Bucket != "bucket" || tok.Object != "object" {
		t.Fatalf("unexpected persisted token: %+v", tok)
	}
}

// newMockOSSBucket builds an *oss.Bucket whose HTTP calls hit handler. The
// handler inspects r.URL.Path and method to answer OSS ListParts
// (GET /bucket/object?uploadId=...), InitiateMultipartUpload
// (POST /bucket/object?uploads) and AbortMultipartUpload (DELETE ...).
func newMockOSSBucket(t *testing.T, handler http.HandlerFunc) *oss.Bucket {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := oss.New(server.URL, "access-key", "secret-key")
	if err != nil {
		t.Fatal(err)
	}
	bucket, err := client.Bucket("bucket")
	if err != nil {
		t.Fatal(err)
	}
	return bucket
}

func seedP115Binding(t *testing.T, d *Driver, key string, tok p115Token) {
	t.Helper()
	raw, err := json.Marshal(tok)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.sessions.Create(key, raw); err != nil {
		t.Fatal(err)
	}
}

func bindingP115Token(t *testing.T, d *Driver, key string) p115Token {
	t.Helper()
	binding, ok := d.sessions.Get(key)
	if !ok {
		t.Fatal("binding missing")
	}
	var tok p115Token
	if err := json.Unmarshal(binding.Token, &tok); err != nil {
		t.Fatal(err)
	}
	return tok
}

func TestBeginMultipartUploadListPartsTransientReusesHandle(t *testing.T) {
	store := drive.NewFileStateStore(filepath.Join(t.TempDir(), "driver"))
	d := New(Options{})
	d.InstallStateStore(store)
	key := session.Identity{ParentID: "0", Name: "a.bin", Size: 32 << 20, Fingerprint: "ABC"}.Key()
	seedP115Binding(t, d, key, p115Token{Bucket: "bucket", Object: "object", UploadID: "old-uid", PartSize: p115MultipartPartSize})

	getCalls := 0
	deleteCalls := 0
	postCalls := 0
	bucket := newMockOSSBucket(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/bucket/object":
			getCalls++
			// 网络/服务端临时故障：非 404 的非 2xx 都是 transient。
			http.Error(w, "service unavailable", http.StatusInternalServerError)
		case r.Method == http.MethodDelete:
			deleteCalls++
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost:
			postCalls++
			w.WriteHeader(http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	})

	params := &driver115.UploadOSSParams{Bucket: "bucket", Object: "object"}
	imur, completed, err := d.beginMultipartUpload(context.Background(), key, params, p115MultipartPartSize, bucket, &driver115.UploadOSSTokenResp{SecurityToken: "sts"})
	if err != nil {
		t.Fatal(err)
	}
	if imur.UploadID != "old-uid" {
		t.Fatalf("imur upload id = %q, want same handle on transient list failure", imur.UploadID)
	}
	if len(completed) != 0 {
		t.Fatalf("completed parts = %+v, want none (full re-upload)", completed)
	}
	if getCalls != 1 || deleteCalls != 0 || postCalls != 0 {
		t.Fatalf("calls get/delete/post = %d/%d/%d, want 1/0/0", getCalls, deleteCalls, postCalls)
	}
	if tok := bindingP115Token(t, d, key); tok.UploadID != "old-uid" {
		t.Fatalf("binding upload id = %q, want unchanged", tok.UploadID)
	}
}

func TestBeginMultipartUploadListPartsInvalidRecreatesSession(t *testing.T) {
	store := drive.NewFileStateStore(filepath.Join(t.TempDir(), "driver"))
	d := New(Options{})
	d.InstallStateStore(store)
	key := session.Identity{ParentID: "0", Name: "a.bin", Size: 32 << 20, Fingerprint: "ABC"}.Key()
	seedP115Binding(t, d, key, p115Token{Bucket: "bucket", Object: "object", UploadID: "old-uid", PartSize: p115MultipartPartSize})

	getCalls, deleteCalls, postCalls := 0, 0, 0
	bucket := newMockOSSBucket(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/bucket/object":
			getCalls++
			// 会话已失效：OSS 返回 404 NoSuchUpload。
			http.Error(w, "NoSuchUpload", http.StatusNotFound)
		case r.Method == http.MethodDelete:
			deleteCalls++
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && r.URL.Path == "/bucket/object":
			postCalls++
			_, _ = w.Write([]byte(`<InitiateMultipartUploadResult><Bucket>bucket</Bucket><Key>object</Key><UploadId>fresh-uid</UploadId></InitiateMultipartUploadResult>`))
		default:
			http.NotFound(w, r)
		}
	})

	params := &driver115.UploadOSSParams{Bucket: "bucket", Object: "object"}
	imur, completed, err := d.beginMultipartUpload(context.Background(), key, params, p115MultipartPartSize, bucket, &driver115.UploadOSSTokenResp{SecurityToken: "sts"})
	if err != nil {
		t.Fatal(err)
	}
	// 旧会话被幂等回收，新上传句柄绑定落地。
	if imur.UploadID != "fresh-uid" {
		t.Fatalf("imur upload id = %q, want fresh-uid", imur.UploadID)
	}
	if len(completed) != 0 {
		t.Fatalf("completed parts = %+v, want none", completed)
	}
	if getCalls != 1 || deleteCalls != 1 || postCalls != 1 {
		t.Fatalf("calls get/delete/post = %d/%d/%d, want 1/1/1", getCalls, deleteCalls, postCalls)
	}
	if tok := bindingP115Token(t, d, key); tok.UploadID != "fresh-uid" || tok.Object != "object" {
		t.Fatalf("binding = %+v, want fresh session recorded", tok)
	}
}

func TestBeginMultipartUploadListPartsResumesPagination(t *testing.T) {
	store := drive.NewFileStateStore(filepath.Join(t.TempDir(), "driver"))
	d := New(Options{})
	d.InstallStateStore(store)
	key := session.Identity{ParentID: "0", Name: "a.bin", Size: 32 << 20, Fingerprint: "ABC"}.Key()
	seedP115Binding(t, d, key, p115Token{Bucket: "bucket", Object: "object", UploadID: "old-uid", PartSize: p115MultipartPartSize})

	getCalls, postCalls := 0, 0
	bucket := newMockOSSBucket(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/bucket/object":
			getCalls++
			marker := r.URL.Query().Get("part-number-marker")
			switch marker {
			case "0":
				_, _ = w.Write([]byte(`<ListPartsResult><Bucket>bucket</Bucket><Key>object</Key><UploadId>old-uid</UploadId><PartNumberMarker>0</PartNumberMarker><NextPartNumberMarker>2</NextPartNumberMarker><MaxParts>1000</MaxParts><IsTruncated>true</IsTruncated><Part><PartNumber>1</PartNumber><LastModified>2026-01-01T00:00:00Z</LastModified><ETag>"etag-1"</ETag><Size>5242880</Size></Part></ListPartsResult>`))
			case "2":
				_, _ = w.Write([]byte(`<ListPartsResult><Bucket>bucket</Bucket><Key>object</Key><UploadId>old-uid</UploadId><PartNumberMarker>2</PartNumberMarker><MaxParts>1000</MaxParts><IsTruncated>false</IsTruncated><Part><PartNumber>3</PartNumber><LastModified>2026-01-01T00:00:01Z</LastModified><ETag>"etag-3"</ETag><Size>5242880</Size></Part></ListPartsResult>`))
			default:
				t.Fatalf("unexpected part-number-marker %q", marker)
			}
		case r.Method == http.MethodPost:
			postCalls++
			http.Error(w, "unexpected initiate", http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	})

	params := &driver115.UploadOSSParams{Bucket: "bucket", Object: "object"}
	imur, completed, err := d.beginMultipartUpload(context.Background(), key, params, p115MultipartPartSize, bucket, &driver115.UploadOSSTokenResp{SecurityToken: "sts"})
	if err != nil {
		t.Fatal(err)
	}
	if imur.UploadID != "old-uid" {
		t.Fatalf("imur upload id = %q, want old-uid", imur.UploadID)
	}
	if len(completed) != 2 || completed[0].PartNumber != 1 || completed[1].PartNumber != 3 {
		t.Fatalf("completed parts = %+v, want [1 3] across pages", completed)
	}
	if completed[0].ETag != `"etag-1"` || completed[1].ETag != `"etag-3"` {
		t.Fatalf("completed etags = %+v, want server etags", completed)
	}
	if getCalls != 2 || postCalls != 0 {
		t.Fatalf("calls get/post = %d/%d, want 2/0", getCalls, postCalls)
	}
}

func TestBeginMultipartUploadHandlelessBindingIsRecreated(t *testing.T) {
	store := drive.NewFileStateStore(filepath.Join(t.TempDir(), "driver"))
	d := New(Options{})
	d.InstallStateStore(store)
	key := session.Identity{ParentID: "0", Name: "a.bin", Size: 32 << 20, Fingerprint: "ABC"}.Key()
	// 预留后未完成创建的绑定（空上传 id）：不应被续传，直接作废重来。
	seedP115Binding(t, d, key, p115Token{Bucket: "bucket", Object: "object", UploadID: ""})

	getCalls, postCalls := 0, 0
	bucket := newMockOSSBucket(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet:
			getCalls++
			http.Error(w, "unexpected list", http.StatusInternalServerError)
		case r.Method == http.MethodPost && r.URL.Path == "/bucket/object":
			postCalls++
			_, _ = w.Write([]byte(`<InitiateMultipartUploadResult><Bucket>bucket</Bucket><Key>object</Key><UploadId>fresh-uid</UploadId></InitiateMultipartUploadResult>`))
		default:
			http.NotFound(w, r)
		}
	})

	params := &driver115.UploadOSSParams{Bucket: "bucket", Object: "object"}
	imur, _, err := d.beginMultipartUpload(context.Background(), key, params, p115MultipartPartSize, bucket, &driver115.UploadOSSTokenResp{SecurityToken: "sts"})
	if err != nil {
		t.Fatal(err)
	}
	if imur.UploadID != "fresh-uid" {
		t.Fatalf("imur upload id = %q, want fresh-uid", imur.UploadID)
	}
	if getCalls != 0 || postCalls != 1 {
		t.Fatalf("calls get/post = %d/%d, want 0/1", getCalls, postCalls)
	}
	if tok := bindingP115Token(t, d, key); tok.UploadID != "fresh-uid" {
		t.Fatalf("binding = %+v, want fresh session recorded", tok)
	}
}

func TestBeginMultipartUploadInitiateFailureDeletesReservation(t *testing.T) {
	store := drive.NewFileStateStore(filepath.Join(t.TempDir(), "driver"))
	d := New(Options{})
	d.InstallStateStore(store)
	key := session.Identity{ParentID: "0", Name: "a.bin", Size: 32 << 20, Fingerprint: "ABC"}.Key()

	postCalls := 0
	bucket := newMockOSSBucket(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/bucket/object" {
			postCalls++
			http.Error(w, "initiate failed", http.StatusInternalServerError)
			return
		}
		http.NotFound(w, r)
	})

	params := &driver115.UploadOSSParams{Bucket: "bucket", Object: "object"}
	_, _, err := d.beginMultipartUpload(context.Background(), key, params, p115MultipartPartSize, bucket, &driver115.UploadOSSTokenResp{SecurityToken: "sts"})
	if err == nil {
		t.Fatal("expected initiate failure")
	}
	// 预留绑定（空句柄）在失败后清理，不留无记录残留。
	if postCalls != 1 {
		t.Fatalf("initiate calls = %d, want 1", postCalls)
	}
	if _, ok := d.sessions.Get(key); ok {
		t.Fatal("reservation binding must be deleted after initiate failure")
	}
}

// TestDriverRemoteHashFromListMetadata verifies RemoteHash reads the sha1
// the 115 list API returned via the SDK file type.
func TestDriverRemoteHashFromListMetadata(t *testing.T) {
	d := New(Options{})
	entry := drive.Entry{
		ID:   "f1",
		Size: 100,
		Extra: driver115.File{
			FileID: "f1",
			Name:   "a.txt",
			Sha1:   "abcdef0123456789abcdef0123456789abcdef01",
		},
	}
	algorithm, hash, err := d.RemoteHash(context.Background(), entry)
	if err != nil {
		t.Fatal(err)
	}
	if algorithm != drive.HashSHA1 || hash != "ABCDEF0123456789ABCDEF0123456789ABCDEF01" {
		t.Fatalf("RemoteHash = (%s, %s), want (sha1, uppercase)", algorithm, hash)
	}
	// Pointer variant and missing sha1.
	entry2 := drive.Entry{Extra: &driver115.File{FileID: "f2", Sha1: "beef"}}
	if _, h, err := d.RemoteHash(context.Background(), entry2); err != nil || h != "BEEF" {
		t.Fatalf("pointer entry RemoteHash = (%s, %v), want BEEF", h, err)
	}
	entry3 := drive.Entry{Extra: driver115.File{FileID: "f3"}}
	if _, _, err := d.RemoteHash(context.Background(), entry3); err != drive.ErrUnsupported {
		t.Fatalf("RemoteHash without sha1 err = %v, want ErrUnsupported", err)
	}
}
