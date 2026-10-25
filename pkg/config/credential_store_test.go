package config

import (
	"strings"
	"testing"
)

func TestCredentialUsedBy(t *testing.T) {
	cfg := &Config{
		Mounts: []MountConfig{
			{Name: "docs", Type: "quark", Credential: "main"},
			{Name: "photos", Type: "quark", Credential: "main"},
			{Name: "local", Type: "localfs"},
		},
	}
	got := cfg.CredentialUsedBy("main")
	if len(got) != 2 || got[0] != "docs" || got[1] != "photos" {
		t.Fatalf("CredentialUsedBy(main) = %v, want [docs photos]", got)
	}
	if got := cfg.CredentialUsedBy("missing"); len(got) != 0 {
		t.Fatalf("CredentialUsedBy(missing) = %v, want empty", got)
	}
	if got := cfg.CredentialUsedBy(""); got != nil {
		t.Fatalf("CredentialUsedBy(\"\") = %v, want nil", got)
	}
	var nilCfg *Config
	if got := nilCfg.CredentialUsedBy("main"); got != nil {
		t.Fatalf("nil config CredentialUsedBy = %v, want nil", got)
	}
}

func TestUpsertCredential(t *testing.T) {
	var nilCfg *Config
	if err := nilCfg.UpsertCredential(CredentialConfig{Name: "main"}); err == nil {
		t.Fatal("nil config accepted a credential")
	}

	cfg := &Config{}
	if err := cfg.UpsertCredential(CredentialConfig{Type: "quark"}); err == nil || !strings.Contains(err.Error(), "name is required") {
		t.Fatalf("error = %v, want a missing-name error", err)
	}
	if err := cfg.UpsertCredential(CredentialConfig{Name: "main", Type: "quark", Params: ParamMap{"cookie": "one"}}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := cfg.UpsertCredential(CredentialConfig{Name: "other", Type: "quark", Params: ParamMap{"cookie": "two"}}); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := cfg.UpsertCredential(CredentialConfig{Name: "main", Type: "quark", Params: ParamMap{"cookie": "updated"}}); err != nil {
		t.Fatalf("replace: %v", err)
	}
	if len(cfg.Credentials) != 2 {
		t.Fatalf("credentials = %+v, want two entries", cfg.Credentials)
	}
	if cfg.Credentials[0].Name != "main" || cfg.Credentials[0].Params["cookie"] != "updated" {
		t.Fatalf("main credential = %+v, want the replaced entry", cfg.Credentials[0])
	}
}

func TestAdoptMountCredentialMovesCredentialParams(t *testing.T) {
	cfg := &Config{
		Mounts: []MountConfig{{
			Name: "docs",
			Type: "quark",
			Params: ParamMap{
				"cookie":    "k=v",
				"base_url":  "https://mount.example",
				"root_path": "/Docs",
			},
		}},
	}
	if err := cfg.AdoptMountCredential("docs", "main"); err != nil {
		t.Fatalf("AdoptMountCredential() error = %v", err)
	}
	if len(cfg.Credentials) != 1 {
		t.Fatalf("credentials = %+v, want one adopted entry", cfg.Credentials)
	}
	cred := cfg.Credentials[0]
	if cred.Name != "main" || cred.Type != "quark" || cred.Params["cookie"] != "k=v" {
		t.Fatalf("credential = %+v, want the moved cookie", cred)
	}
	if _, ok := cred.Params["base_url"]; ok {
		t.Fatalf("credential params = %+v, want both-scope base_url left on the mount", cred.Params)
	}
	mount, ok := cfg.MountByName("docs")
	if !ok {
		t.Fatal("mount disappeared")
	}
	if mount.Credential != "main" {
		t.Fatalf("mount credential = %q, want main", mount.Credential)
	}
	if mount.Params["base_url"] != "https://mount.example" || mount.Params["root_path"] != "/Docs" {
		t.Fatalf("mount params = %+v, want both- and mount-scope params kept", mount.Params)
	}
	if _, ok := mount.Params["cookie"]; ok {
		t.Fatalf("mount params = %+v, want the credential-scope cookie moved", mount.Params)
	}
}

func TestAdoptMountCredentialErrors(t *testing.T) {
	quarkMount := func(params ParamMap) []MountConfig {
		return []MountConfig{{Name: "docs", Type: "quark", Params: params}}
	}
	tests := []struct {
		name    string
		cfg     *Config
		mount   string
		cred    string
		wantErr string
	}{
		{
			name:    "nil config",
			mount:   "docs",
			cred:    "main",
			wantErr: "configuration is empty",
		},
		{
			name:    "missing mount name",
			cfg:     &Config{},
			cred:    "main",
			wantErr: "mount name is required",
		},
		{
			name:    "missing credential name",
			cfg:     &Config{},
			mount:   "docs",
			wantErr: "credential name is required",
		},
		{
			name: "credential already exists",
			cfg: &Config{
				Credentials: []CredentialConfig{{Name: "main", Type: "quark", Params: ParamMap{"cookie": "k=v"}}},
				Mounts:      quarkMount(ParamMap{"cookie": "k=v"}),
			},
			mount:   "docs",
			cred:    "main",
			wantErr: "already exists",
		},
		{
			name:    "mount not found",
			cfg:     &Config{},
			mount:   "docs",
			cred:    "main",
			wantErr: "not found",
		},
		{
			name: "mount already references a credential",
			cfg: &Config{
				Mounts: []MountConfig{{Name: "docs", Type: "quark", Credential: "other", Params: ParamMap{"cookie": "k=v"}}},
			},
			mount:   "docs",
			cred:    "main",
			wantErr: "already references credential",
		},
		{
			name:    "no credential-scope parameters",
			cfg:     &Config{Mounts: quarkMount(ParamMap{"base_url": "https://x", "root_path": "/Docs"})},
			mount:   "docs",
			cred:    "main",
			wantErr: "no credential-scope parameters",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.AdoptMountCredential(tt.mount, tt.cred)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("AdoptMountCredential() error = %v, want substring %q", err, tt.wantErr)
			}
		})
	}
}

func TestRemoveCredential(t *testing.T) {
	var nilCfg *Config
	if err := nilCfg.RemoveCredential("main"); err == nil {
		t.Fatal("nil config accepted a removal")
	}

	cfg := &Config{
		Credentials: []CredentialConfig{
			{Name: "main", Type: "quark", Params: ParamMap{"cookie": "k=v"}},
			{Name: "spare", Type: "quark", Params: ParamMap{"cookie": "k=v"}},
		},
		Mounts: []MountConfig{{Name: "docs", Type: "quark", Credential: "main"}},
	}
	if err := cfg.RemoveCredential(""); err == nil || !strings.Contains(err.Error(), "name is required") {
		t.Fatalf("error = %v, want a missing-name error", err)
	}
	if err := cfg.RemoveCredential("main"); err == nil || !strings.Contains(err.Error(), "still referenced") {
		t.Fatalf("error = %v, want a reference error", err)
	}
	if err := cfg.RemoveCredential("missing"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("error = %v, want a not-found error", err)
	}
	if err := cfg.RemoveCredential("spare"); err != nil {
		t.Fatalf("RemoveCredential(spare) error = %v", err)
	}
	if len(cfg.Credentials) != 1 || cfg.Credentials[0].Name != "main" {
		t.Fatalf("credentials = %+v, want only main", cfg.Credentials)
	}
}

func TestMountByName(t *testing.T) {
	cfg := &Config{Mounts: []MountConfig{{Name: "docs", Type: "quark"}}}
	if mount, ok := cfg.MountByName("docs"); !ok || mount.Type != "quark" {
		t.Fatalf("MountByName(docs) = %+v, %v, want the mount", mount, ok)
	}
	if _, ok := cfg.MountByName("missing"); ok {
		t.Fatal("MountByName(missing) reported a mount")
	}
	if _, ok := cfg.MountByName(""); ok {
		t.Fatal("MountByName(\"\") reported a mount")
	}
	var nilCfg *Config
	if _, ok := nilCfg.MountByName("docs"); ok {
		t.Fatal("nil config reported a mount")
	}
}

func TestUpsertMount(t *testing.T) {
	var nilCfg *Config
	if err := nilCfg.UpsertMount(MountConfig{Name: "docs"}); err == nil {
		t.Fatal("nil config accepted a mount")
	}

	cfg := &Config{}
	if err := cfg.UpsertMount(MountConfig{Type: "quark"}); err == nil || !strings.Contains(err.Error(), "name is required") {
		t.Fatalf("error = %v, want a missing-name error", err)
	}
	if err := cfg.UpsertMount(MountConfig{Name: "docs", Type: "quark", Params: ParamMap{"root_path": "/A"}}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := cfg.UpsertMount(MountConfig{Name: "photos", Type: "quark", Params: ParamMap{"root_path": "/B"}}); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := cfg.UpsertMount(MountConfig{Name: "docs", Type: "quark", Params: ParamMap{"root_path": "/C"}}); err != nil {
		t.Fatalf("replace: %v", err)
	}
	if len(cfg.Mounts) != 2 {
		t.Fatalf("mounts = %+v, want two entries", cfg.Mounts)
	}
	if cfg.Mounts[0].Name != "docs" || cfg.Mounts[0].Params["root_path"] != "/C" {
		t.Fatalf("docs mount = %+v, want the replaced entry", cfg.Mounts[0])
	}
}

func TestRemoveMount(t *testing.T) {
	var nilCfg *Config
	if err := nilCfg.RemoveMount("docs"); err == nil {
		t.Fatal("nil config accepted a removal")
	}

	cfg := &Config{
		Upload: UploadConfig{DefaultMount: "docs"},
		Mounts: []MountConfig{
			{Name: "docs", Type: "quark"},
			{Name: "photos", Type: "quark"},
		},
	}
	if err := cfg.RemoveMount(""); err == nil || !strings.Contains(err.Error(), "name is required") {
		t.Fatalf("error = %v, want a missing-name error", err)
	}
	if err := cfg.RemoveMount("docs"); err == nil || !strings.Contains(err.Error(), "upload.default_mount") {
		t.Fatalf("error = %v, want a default-mount error", err)
	}
	if err := cfg.RemoveMount("missing"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("error = %v, want a not-found error", err)
	}
	if err := cfg.RemoveMount("photos"); err != nil {
		t.Fatalf("RemoveMount(photos) error = %v", err)
	}
	if len(cfg.Mounts) != 1 || cfg.Mounts[0].Name != "docs" {
		t.Fatalf("mounts = %+v, want only docs", cfg.Mounts)
	}

	last := &Config{Mounts: []MountConfig{{Name: "docs", Type: "quark"}}}
	if err := last.RemoveMount("docs"); err == nil || !strings.Contains(err.Error(), "last mount") {
		t.Fatalf("error = %v, want a last-mount error", err)
	}
}
