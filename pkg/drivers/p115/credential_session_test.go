package p115

import (
	"strings"
	"testing"

	driver115 "github.com/SheltonZhu/115driver/pkg/driver"
	"github.com/yinzhenyu/qrypt/pkg/drivers/internal/driverutil/credentialtest"
)

func TestSharedCredentialSessionPropagatesCookie(t *testing.T) {
	session := credentialtest.New("main")
	first := New(Options{Cookie: "UID=account; SEID=one"})
	first.InstallCredentialSession(session)
	first.subscribeCredentialSession()
	second := New(Options{Cookie: "UID=account; SEID=one"})
	second.InstallCredentialSession(session)
	second.subscribeCredentialSession()

	first.saveUpdatedCookie("SEID=two")

	if !strings.Contains(second.cookies, "SEID=two") {
		t.Fatalf("sibling cookie = %q, want the rotated value", second.cookies)
	}
	if second.cookieSource != "credential" {
		t.Fatalf("sibling source = %q, want credential", second.cookieSource)
	}
	if snapshot := session.Snapshot(); snapshot.Version != 1 || snapshot.Account != "account" {
		t.Fatalf("session snapshot = %+v, want version 1 with account", snapshot)
	}
}

func TestSharedCredentialSessionIsAdoptedAtInit(t *testing.T) {
	session := credentialtest.New("main")
	if _, err := session.Update(0, []byte(`{"cookie":"UID=account; SEID=state"}`), ""); err != nil {
		t.Fatal(err)
	}
	driver := New(Options{Cookie: "UID=account; SEID=config"})
	driver.InstallCredentialSession(session)
	driver.loadCredentialSession()

	if !strings.Contains(driver.cookies, "SEID=state") {
		t.Fatalf("cookie = %q, want the shared session value", driver.cookies)
	}
	if driver.cookieSource != "state" {
		t.Fatalf("source = %q, want state", driver.cookieSource)
	}
}

func TestAuthRetryAdoptsSiblingRotation(t *testing.T) {
	session := credentialtest.New("main")
	if _, err := session.Update(0, encodeCredentialCookie("UID=account; SEID=state"), "account"); err != nil {
		t.Fatal(err)
	}
	driver := New(Options{Cookie: "UID=account; SEID=old"})
	driver.InstallCredentialSession(session)

	calls := 0
	err := driver.withAuthRetry(func() error {
		calls++
		if calls == 1 {
			if strings.Contains(driver.cookies, "SEID=state") {
				t.Fatal("cookie was adopted before the auth failure")
			}
			return driver115.ErrNotLogin
		}
		if !strings.Contains(driver.cookies, "SEID=state") {
			t.Fatalf("cookie = %q, want the adopted session", driver.cookies)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("withAuthRetry: %v", err)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want one retry after adopting the session", calls)
	}
}
