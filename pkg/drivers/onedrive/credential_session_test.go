package onedrive

import (
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/drivers/internal/driverutil/credentialtest"
)

func TestSharedCredentialSessionIsAdopted(t *testing.T) {
	session := credentialtest.New("main")
	payload, _, err := encodeCredentialPayload(credentialPayload{
		AccessToken:  "access-a",
		RefreshToken: "refresh-a",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := session.Update(0, payload, ""); err != nil {
		t.Fatal(err)
	}
	driver, _ := newTestDriver(t)
	driver.InstallCredentialSession(session)
	driver.loadCredentialSession()

	if got := driver.currentAccessToken(); got != "access-a" {
		t.Fatalf("access token = %q, want the shared session value", got)
	}
	if got := driver.currentRefreshToken(); got != "refresh-a" {
		t.Fatalf("refresh token = %q, want the shared session value", got)
	}
}

func TestAccountIDUsesConfiguredEmail(t *testing.T) {
	driver := &Driver{email: "user@example.com"}
	if got := driver.accountID(); got != "user@example.com" {
		t.Fatalf("accountID = %q, want the configured email", got)
	}
}
