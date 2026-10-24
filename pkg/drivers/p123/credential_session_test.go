package p123

import (
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drivers/internal/driverutil/credentialtest"
)

func TestSharedCredentialSessionIsAdopted(t *testing.T) {
	session := credentialtest.New("main")
	expire := time.Now().Add(time.Hour).UTC()
	payload, err := encodeCredentialPayload(credentialPayload{Token: "token-a", Username: "user", Expire: expire})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := session.Update(0, payload, "user"); err != nil {
		t.Fatal(err)
	}
	driver := New(Options{Username: "user", Password: "pass"})
	driver.InstallCredentialSession(session)
	driver.loadCredentialSession()

	if got := driver.cl.tokenValue(); got != "token-a" {
		t.Fatalf("token = %q, want the shared session token", got)
	}
	if driver.tokenSource != "state" {
		t.Fatalf("source = %q, want state", driver.tokenSource)
	}
}

func TestSharedCredentialSessionSkipsExpiredToken(t *testing.T) {
	session := credentialtest.New("main")
	payload, err := encodeCredentialPayload(credentialPayload{
		Token:    "token-a",
		Username: "user",
		Expire:   time.Now().Add(-time.Minute).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := session.Update(0, payload, "user"); err != nil {
		t.Fatal(err)
	}
	driver := New(Options{Username: "user", Password: "pass"})
	driver.InstallCredentialSession(session)
	driver.loadCredentialSession()

	if got := driver.cl.tokenValue(); got != "" {
		t.Fatalf("token = %q, want an expired session to be ignored", got)
	}
}

func TestSharedCredentialSessionRejectsAnotherAccount(t *testing.T) {
	session := credentialtest.New("main")
	payload, err := encodeCredentialPayload(credentialPayload{
		Token:    "token-a",
		Username: "someone-else",
		Expire:   time.Now().Add(time.Hour).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := session.Update(0, payload, "someone-else"); err != nil {
		t.Fatal(err)
	}
	driver := New(Options{Username: "user", Password: "pass"})
	driver.InstallCredentialSession(session)
	driver.loadCredentialSession()

	if got := driver.cl.tokenValue(); got != "" {
		t.Fatalf("token = %q, want another account's session to be ignored", got)
	}
}
