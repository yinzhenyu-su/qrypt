package baidunetdisk

import (
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/drivers/internal/driverutil/credentialtest"
)

func TestSharedCredentialSessionIsAdopted(t *testing.T) {
	session := credentialtest.New("main")
	expires := time.Now().Add(time.Hour).UTC()
	payload, _, err := encodeCredentialPayload(credentialPayload{
		AccessToken:  "access-a",
		RefreshToken: "refresh-a",
		ExpiresAt:    expires,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := session.Update(0, payload, ""); err != nil {
		t.Fatal(err)
	}
	driver := New(Options{RefreshToken: "config"})
	driver.InstallCredentialSession(session)
	driver.loadCredentialSession()

	if driver.accessToken != "access-a" || driver.refreshToken != "refresh-a" {
		t.Fatalf("tokens = %q/%q, want the shared session pair", driver.accessToken, driver.refreshToken)
	}
	if driver.tokenSource != "state" {
		t.Fatalf("source = %q, want state", driver.tokenSource)
	}
}

func TestExportRequiresConfigMarker(t *testing.T) {
	store := drive.NewFileStateStore(t.TempDir())
	if err := store.SaveJSON("baidu_netdisk_token.json", map[string]any{
		"access_token": "access", "refresh_token": "refresh",
	}); err != nil {
		t.Fatal(err)
	}
	driver := New(Options{RefreshToken: "config"})
	_, _, ok, err := driver.ExportCredentialSession(store, drive.Params{"refresh_token": "config"})
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("state without config_refresh_token must not be adopted")
	}
}

func TestExportReturnsRecordedAccount(t *testing.T) {
	store := drive.NewFileStateStore(t.TempDir())
	if err := store.SaveJSON("baidu_netdisk_token.json", map[string]any{
		"access_token":         "access",
		"refresh_token":        "refresh",
		"account":              "baidu-user",
		"config_refresh_token": "config",
	}); err != nil {
		t.Fatal(err)
	}
	driver := New(Options{RefreshToken: "config"})
	_, account, ok, err := driver.ExportCredentialSession(store, drive.Params{"refresh_token": "config"})
	if err != nil {
		t.Fatal(err)
	}
	if !ok || account != "baidu-user" {
		t.Fatalf("export = ok:%v account:%q, want baidu-user", ok, account)
	}
}
