package p115

import (
	"context"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	driver115 "github.com/SheltonZhu/115driver/pkg/driver"
	"github.com/yinzhenyu/qrypt/pkg/drive"
)

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
