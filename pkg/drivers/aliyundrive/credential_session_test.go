package aliyundrive

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/drivers/internal/driverutil/credentialtest"
)

func TestSharedCredentialSessionIsAdopted(t *testing.T) {
	session := credentialtest.New("main")
	if _, err := session.Update(0, []byte(`{"access_token":"a1","refresh_token":"r1"}`), ""); err != nil {
		t.Fatal(err)
	}
	driver := New(Options{RefreshToken: "config"})
	driver.InstallCredentialSession(session)
	driver.loadCredentialSession()

	access, refresh := driver.cl.tokens()
	if access != "a1" || refresh != "r1" {
		t.Fatalf("tokens = %q/%q, want the shared session pair", access, refresh)
	}
	if driver.tokenSource != "state" {
		t.Fatalf("source = %q, want state", driver.tokenSource)
	}
}

func TestSharedCredentialSessionCommitsRefresh(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{
			"access_token":  "a2",
			"refresh_token": "r2",
		})
	}))
	defer api.Close()

	session := credentialtest.New("main")
	driver := New(Options{RefreshToken: "config", AuthURL: api.URL})
	driver.InstallCredentialSession(session)
	driver.subscribeCredentialSession()

	if err := driver.refreshCredential(context.Background()); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	payload, err := decodeCredentialPayload(session.Snapshot().Payload)
	if err != nil {
		t.Fatal(err)
	}
	if payload.AccessToken != "a2" || payload.RefreshToken != "r2" {
		t.Fatalf("committed payload = %+v, want the rotated pair", payload)
	}
	if driver.tokenSource != "credential" {
		t.Fatalf("source = %q, want credential after the commit", driver.tokenSource)
	}
}

func TestExportRequiresConfigMarker(t *testing.T) {
	store := drive.NewFileStateStore(t.TempDir())
	if err := store.SaveJSON("aliyundrive_token.json", map[string]any{
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
