package p189

import (
	"strings"
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/drivers/internal/driverutil/credentialtest"
)

func newCredentialTestDriver(cookie string) *Driver {
	return &Driver{cl: newClient(cookie, "", ""), cookieSource: "config"}
}

func TestSharedCredentialSessionPropagatesCookie(t *testing.T) {
	session := credentialtest.New("main")
	first := newCredentialTestDriver("UID=account; SEID=one")
	first.InstallCredentialSession(session)
	first.subscribeCredentialSession()
	second := newCredentialTestDriver("UID=account; SEID=one")
	second.InstallCredentialSession(session)
	second.subscribeCredentialSession()

	first.saveUpdatedCookie("UID=account; SEID=two")

	if got := second.cl.cookieValue(); !strings.Contains(got, "SEID=two") {
		t.Fatalf("sibling cookie = %q, want the rotated value", got)
	}
	if second.cookieSource != "credential" {
		t.Fatalf("sibling source = %q, want credential", second.cookieSource)
	}
}

func TestSharedCredentialSessionCarriesReloginState(t *testing.T) {
	session := credentialtest.New("main")
	first := newCredentialTestDriver("UID=account; SEID=one")
	first.InstallCredentialSession(session)
	first.subscribeCredentialSession()
	second := newCredentialTestDriver("UID=account; SEID=one")
	second.InstallCredentialSession(session)
	second.subscribeCredentialSession()

	first.savePasswordReloginState(first.cl.passwordReloginFailedAt, "bad password")

	var payload credentialPayload
	raw := session.Snapshot().Payload
	if err := decodeInto(t, raw, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.PasswordReloginError != "bad password" {
		t.Fatalf("payload error = %q, want bad password", payload.PasswordReloginError)
	}
	if got := second.cl.cookieValue(); !strings.Contains(got, "SEID=one") {
		t.Fatalf("sibling cookie = %q", got)
	}
}

func decodeInto(t *testing.T, raw []byte, out *credentialPayload) error {
	t.Helper()
	decoded, err := decodeCredentialPayload(raw)
	if err != nil {
		return err
	}
	*out = decoded
	return nil
}
