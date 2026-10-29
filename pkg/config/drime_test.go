package config

import (
	"strings"
	"testing"

	_ "github.com/yinzhenyu/qrypt/pkg/drivers/drime"
)

func TestValidateDrimeMount(t *testing.T) {
	valid := MountConfig{
		Name: "drime",
		Type: "drime",
		Params: ParamMap{
			"access_token": "token",
			"root_path":    "/",
		},
	}
	if err := ValidateMount(&Config{}, 0, valid); err != nil {
		t.Fatalf("valid mount: %v", err)
	}

	missing := valid
	missing.Params = ParamMap{"root_path": "/"}
	if err := ValidateMount(&Config{}, 0, missing); err == nil {
		t.Fatal("missing access_token accepted")
	}

	badChunk := valid
	badChunk.Params = ParamMap{"access_token": "token", "chunk_size": "4"}
	if err := ValidateMount(&Config{}, 0, badChunk); err == nil || !strings.Contains(err.Error(), "chunk_size") {
		t.Fatalf("bad chunk_size error = %v", err)
	}

	badWorkspace := valid
	badWorkspace.Params = ParamMap{"access_token": "token", "workspace_id": "nope"}
	if err := ValidateMount(&Config{}, 0, badWorkspace); err == nil {
		t.Fatal("bad workspace_id accepted")
	}
}

func TestValidateDrimeSharedCredential(t *testing.T) {
	cfg := &Config{
		Credentials: []CredentialConfig{{
			Name:   "drime-account",
			Type:   "drime",
			Params: ParamMap{"access_token": "token"},
		}},
	}
	mount := MountConfig{
		Name:       "drime",
		Type:       "drime",
		Credential: "drime-account",
		Params:     ParamMap{"root_path": "/docs"},
	}
	if err := ValidateMount(cfg, 0, mount); err != nil {
		t.Fatalf("shared credential mount: %v", err)
	}
	params, err := cfg.ResolveMountParams(mount)
	if err != nil {
		t.Fatalf("ResolveMountParams: %v", err)
	}
	if params["access_token"] != "token" || params["root_path"] != "/docs" {
		t.Fatalf("params = %v", params)
	}

	inline := mount
	inline.Params = ParamMap{"root_path": "/docs", "access_token": "inline"}
	if err := ValidateMount(cfg, 0, inline); err == nil {
		t.Fatal("credential-scope access_token was accepted on a mount")
	}
}
