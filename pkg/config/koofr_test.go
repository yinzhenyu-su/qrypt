package config

import (
	"strings"
	"testing"

	_ "github.com/yinzhenyu/qrypt/pkg/drivers/koofr"
)

func TestValidateKoofrMount(t *testing.T) {
	valid := MountConfig{
		Name: "koofr",
		Type: "koofr",
		Params: ParamMap{
			"email":        "user@example.com",
			"app_password": "app-password",
			"mount_id":     "mount-id",
			"root_path":    "/docs",
		},
	}
	if err := ValidateMount(&Config{}, 0, valid); err != nil {
		t.Fatalf("valid mount: %v", err)
	}

	missingEmail := valid
	missingEmail.Params = ParamMap{"app_password": "app-password"}
	if err := ValidateMount(&Config{}, 0, missingEmail); err == nil || !strings.Contains(err.Error(), "email") {
		t.Fatalf("missing email error = %v", err)
	}

	missingPassword := valid
	missingPassword.Params = ParamMap{"email": "user@example.com"}
	if err := ValidateMount(&Config{}, 0, missingPassword); err == nil || !strings.Contains(err.Error(), "app_password") {
		t.Fatalf("missing app_password error = %v", err)
	}
}

func TestValidateKoofrSharedCredential(t *testing.T) {
	cfg := &Config{
		Credentials: []CredentialConfig{{
			Name: "koofr-account",
			Type: "koofr",
			Params: ParamMap{
				"email":        "user@example.com",
				"app_password": "app-password",
			},
		}},
	}
	mount := MountConfig{
		Name:       "koofr",
		Type:       "koofr",
		Credential: "koofr-account",
		Params: ParamMap{
			"mount_id":  "mount-id",
			"root_path": "/docs",
		},
	}
	if err := ValidateMount(cfg, 0, mount); err != nil {
		t.Fatalf("shared credential mount: %v", err)
	}
	params, err := cfg.ResolveMountParams(mount)
	if err != nil {
		t.Fatalf("ResolveMountParams: %v", err)
	}
	if params["email"] != "user@example.com" || params["app_password"] != "app-password" || params["root_path"] != "/docs" {
		t.Fatalf("params = %v", params)
	}

	inline := mount
	inline.Params = ParamMap{"root_path": "/docs", "email": "inline@example.com"}
	if err := ValidateMount(cfg, 0, inline); err == nil {
		t.Fatal("credential-scope email was accepted on a mount")
	}

	badCredential := &Config{
		Credentials: []CredentialConfig{{
			Name: "koofr-account",
			Type: "koofr",
			Params: ParamMap{
				"email":        "user@example.com",
				"app_password": "app-password",
				"mount_id":     "mount-id",
			},
		}},
		Mounts: []MountConfig{{Name: "koofr", Type: "koofr", Credential: "koofr-account"}},
	}
	if err := Validate(badCredential); err == nil || !strings.Contains(err.Error(), `parameter "mount_id" has scope "mount"`) {
		t.Fatalf("credential with mount_id error = %v", err)
	}
}
