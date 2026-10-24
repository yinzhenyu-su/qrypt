package core

import (
	"context"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/config"
	"github.com/yinzhenyu/qrypt/pkg/core/internal/credentials"
	"github.com/yinzhenyu/qrypt/pkg/drive"
)

type resolverProvider struct {
	account string
	known   bool
}

func (p resolverProvider) Account(drive.Params) (string, bool) { return p.account, p.known }
func (p resolverProvider) Login(_ context.Context, _ drive.LoginUI) (drive.LoginResult, error) {
	return drive.LoginResult{}, nil
}

func credentialConfig(name string, params config.ParamMap) *config.Config {
	return &config.Config{
		Credentials: []config.CredentialConfig{{
			Name:   name,
			Type:   "quark",
			Params: params,
		}},
	}
}

func seedSession(t *testing.T, layout RuntimeLayout, cfg *config.Config, account string) {
	t.Helper()
	cred, ok := cfg.CredentialFor("main")
	if !ok {
		t.Fatal("credential missing from fixture")
	}
	manager := credentials.Open(layout.StateDir)
	session := manager.Session(credentials.Spec{
		Name:       cred.Name,
		ParamsSeed: credentials.ParamsSeed(cred.Type, cred.Params),
	})
	if _, err := session.Update(0, []byte("payload"), account); err != nil {
		t.Fatalf("seed session: %v", err)
	}
}

func TestResolveCredentialAccount(t *testing.T) {
	t.Run("static only", func(t *testing.T) {
		cfg := credentialConfig("main", config.ParamMap{"cookie": "__uid=static"})
		info := ResolveCredentialAccount(cfg, RuntimeLayout{StateDir: t.TempDir()}, "main", resolverProvider{account: "static", known: true})
		if info.Account != "static" || info.Source != CredentialAccountSourceConfig {
			t.Fatalf("info = %+v, want static/config", info)
		}
		if info.Version != 0 || !info.UpdatedAt.IsZero() {
			t.Fatalf("info = %+v, want no session metadata", info)
		}
	})

	t.Run("static account with session metadata", func(t *testing.T) {
		layout := RuntimeLayout{StateDir: t.TempDir()}
		cfg := credentialConfig("main", config.ParamMap{"cookie": "__uid=static"})
		seedSession(t, layout, cfg, "session")
		info := ResolveCredentialAccount(cfg, layout, "main", resolverProvider{account: "static", known: true})
		if info.Account != "static" {
			t.Fatalf("account = %q, want static", info.Account)
		}
		if info.Source != CredentialAccountSourceSession || info.Version != 1 || info.UpdatedAt.IsZero() {
			t.Fatalf("info = %+v, want session metadata", info)
		}
	})

	t.Run("session only", func(t *testing.T) {
		layout := RuntimeLayout{StateDir: t.TempDir()}
		cfg := credentialConfig("main", config.ParamMap{"refresh_token": "token"})
		seedSession(t, layout, cfg, "session")
		info := ResolveCredentialAccount(cfg, layout, "main", resolverProvider{known: false})
		if info.Account != "session" || info.Source != CredentialAccountSourceSession || info.Version != 1 {
			t.Fatalf("info = %+v, want session/session", info)
		}
	})

	t.Run("nil provider falls back to session", func(t *testing.T) {
		layout := RuntimeLayout{StateDir: t.TempDir()}
		cfg := credentialConfig("main", config.ParamMap{"refresh_token": "token"})
		seedSession(t, layout, cfg, "session")
		info := ResolveCredentialAccount(cfg, layout, "main", nil)
		if info.Account != "session" || info.Source != CredentialAccountSourceSession {
			t.Fatalf("info = %+v, want session fallback", info)
		}
	})

	t.Run("unknown everywhere", func(t *testing.T) {
		cfg := credentialConfig("main", config.ParamMap{"refresh_token": "token"})
		info := ResolveCredentialAccount(cfg, RuntimeLayout{StateDir: t.TempDir()}, "main", nil)
		if info != (CredentialAccountInfo{}) {
			t.Fatalf("info = %+v, want zero", info)
		}
	})

	t.Run("unknown name", func(t *testing.T) {
		info := ResolveCredentialAccount(credentialConfig("main", nil), RuntimeLayout{StateDir: t.TempDir()}, "other", nil)
		if info != (CredentialAccountInfo{}) {
			t.Fatalf("info = %+v, want zero", info)
		}
	})

	t.Run("session timestamp is reported", func(t *testing.T) {
		layout := RuntimeLayout{StateDir: t.TempDir()}
		cfg := credentialConfig("main", config.ParamMap{"refresh_token": "token"})
		seedSession(t, layout, cfg, "session")
		info := ResolveCredentialAccount(cfg, layout, "main", nil)
		if time.Since(info.UpdatedAt) > time.Minute {
			t.Fatalf("updated_at = %v, want recent", info.UpdatedAt)
		}
	})
}
