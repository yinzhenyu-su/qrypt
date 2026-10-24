package yun139

import (
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/drivers/internal/driverutil/credentialtest"
)

func TestSharedCredentialSessionPropagatesAuthorization(t *testing.T) {
	session := credentialtest.New("main")
	first := New("auth-one", "", "")
	first.InstallCredentialSession(session)
	first.subscribeCredentialSession()
	second := New("auth-one", "", "")
	second.InstallCredentialSession(session)
	second.subscribeCredentialSession()

	first.cl.account = "account-a"
	first.saveUpdatedAuthorization("auth-two")

	if got := second.cl.getAuthorization(); got != "auth-two" {
		t.Fatalf("sibling authorization = %q, want the refreshed value", got)
	}
	if second.authSource != "credential" {
		t.Fatalf("sibling source = %q, want credential", second.authSource)
	}
	if snapshot := session.Snapshot(); snapshot.Account != "account-a" {
		t.Fatalf("session account = %q, want account-a", snapshot.Account)
	}
}

func TestSharedCredentialSessionIsAdopted(t *testing.T) {
	session := credentialtest.New("main")
	if _, err := session.Update(0, []byte(`{"authorization":"auth-state"}`), ""); err != nil {
		t.Fatal(err)
	}
	driver := New("auth-config", "", "")
	driver.InstallCredentialSession(session)
	driver.loadCredentialSession()

	if got := driver.cl.getAuthorization(); got != "auth-state" {
		t.Fatalf("authorization = %q, want the shared session value", got)
	}
	if driver.authSource != "state" {
		t.Fatalf("source = %q, want state", driver.authSource)
	}
}

func TestAuthRecoveryAdoptsSiblingAuthorization(t *testing.T) {
	session := credentialtest.New("main")
	payload, err := encodeCredentialAuthorization("auth-state")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := session.Update(0, payload, ""); err != nil {
		t.Fatal(err)
	}
	driver := New("auth-old", "", "")
	driver.InstallCredentialSession(session)
	driver.cl.onAuthRecovery = driver.recoverCredential

	if !driver.cl.onAuthRecovery() {
		t.Fatal("recovery reported no newer session")
	}
	if got := driver.cl.getAuthorization(); got != "auth-state" {
		t.Fatalf("authorization = %q, want the adopted session", got)
	}
}

func TestRecoverStaticAuthorization(t *testing.T) {
	driver := New("auth-config", "", "")
	driver.cl.setAuthorization("auth-stale")
	if !driver.recoverStaticAuthorization() {
		t.Fatal("static authorization fallback should report a retry")
	}
	if got := driver.cl.getAuthorization(); got != "auth-config" {
		t.Fatalf("authorization = %q, want the configured value", got)
	}
	if driver.authSource != "config" {
		t.Fatalf("source = %q, want config", driver.authSource)
	}
}
