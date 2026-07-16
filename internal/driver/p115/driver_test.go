package p115

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	driver115 "github.com/SheltonZhu/115driver/pkg/driver"
	"github.com/yinzhenyu/qrypt/pkg/drive"
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

func TestUploadSessionStoreRoundTrip(t *testing.T) {
	store := drive.NewFileStateStore(filepath.Join(t.TempDir(), "driver"))
	driver := New(Options{Cookie: "UID=uid"})
	driver.InstallStateStore(store)

	session := p115UploadSession{
		Key:      "session-key",
		ParentID: "0",
		Name:     "video.bin",
		Size:     32 << 20,
		SHA1:     "ABC",
		Bucket:   "bucket",
		Object:   "object",
		UploadID: "upload-id",
		PartSize: p115MultipartPartSize,
		Parts: []ossPart{
			{Number: 1, ETag: "etag-1"},
		},
		Callback:  "callback",
		CallbackV: "callback-var",
	}
	driver.saveUploadSession(session)

	loaded, ok := driver.loadUploadSession("session-key")
	if !ok {
		t.Fatal("expected session to load")
	}
	if loaded.UploadID != "upload-id" || len(loaded.Parts) != 1 || loaded.Parts[0].ETag != "etag-1" {
		t.Fatalf("unexpected loaded session: %+v", loaded)
	}
	if loaded.SavedAt.IsZero() {
		t.Fatal("SavedAt was not set")
	}

	var state p115UploadSessionState
	if err := store.LoadJSON(p115UploadSessionStateFile, &state); err != nil {
		t.Fatal(err)
	}
	if state.Version != 1 || len(state.Sessions) != 1 {
		t.Fatalf("unexpected persisted state: %+v", state)
	}
}

func TestUploadSessionStoreRejectsEmptyParts(t *testing.T) {
	store := drive.NewFileStateStore(filepath.Join(t.TempDir(), "driver"))
	driver := New(Options{Cookie: "UID=uid"})
	driver.InstallStateStore(store)

	driver.saveUploadSession(p115UploadSession{
		Key:      "session-key",
		Bucket:   "bucket",
		Object:   "object",
		UploadID: "upload-id",
		PartSize: p115MultipartPartSize,
	})
	if _, ok := driver.loadUploadSession("session-key"); ok {
		t.Fatal("expected empty-parts session to be rejected")
	}
}
