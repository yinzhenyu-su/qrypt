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

	first.saveUpdatedAuthorization("auth-two")

	if got := second.cl.getAuthorization(); got != "auth-two" {
		t.Fatalf("sibling authorization = %q, want the refreshed value", got)
	}
	if second.authSource != "credential" {
		t.Fatalf("sibling source = %q, want credential", second.authSource)
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
