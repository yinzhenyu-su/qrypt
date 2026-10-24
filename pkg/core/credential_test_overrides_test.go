package core

import (
	"strings"
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/config"
)

func TestValidateCredentialTestOverrides(t *testing.T) {
	quark := config.CredentialConfig{Name: "q", Type: "quark", Params: config.ParamMap{"cookie": "k=v"}}
	if err := validateCredentialTestOverrides(quark, map[string]string{"base_url": "https://x"}); err != nil {
		t.Fatalf("both-scope non-secret override should be allowed: %v", err)
	}
	if err := validateCredentialTestOverrides(quark, map[string]string{"cookie": "x"}); err == nil || !strings.Contains(err.Error(), "credential-scope") {
		t.Fatalf("credential-scope override error = %v", err)
	}
	if err := validateCredentialTestOverrides(quark, map[string]string{"nonsense": "x"}); err == nil {
		t.Fatal("unknown override should be rejected")
	}
	onedrive := config.CredentialConfig{
		Name:   "o",
		Type:   "onedrive",
		Params: config.ParamMap{"refresh_token": "r", "client_secret": "stored"},
	}
	if err := validateCredentialTestOverrides(onedrive, map[string]string{"client_secret": "other"}); err == nil || !strings.Contains(err.Error(), "stored secret") {
		t.Fatalf("stored both-scope secret override error = %v", err)
	}
}
