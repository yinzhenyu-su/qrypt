package config

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestCredentialsRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "qrypt.toml")
	want := &Config{
		Version: "1",
		Credentials: []CredentialConfig{
			{Name: "main", Type: "quark", Params: ParamMap{"cookie": "k=v"}},
		},
		Mounts: []MountConfig{
			{Name: "docs", Type: "quark", Credential: "main", Params: ParamMap{"root_path": "/Docs"}},
		},
	}
	if err := Save(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Credentials) != 1 || got.Credentials[0].Name != "main" || got.Credentials[0].Params["cookie"] != "k=v" {
		t.Fatalf("credentials = %+v, want the saved entry", got.Credentials)
	}
	if len(got.Mounts) != 1 || got.Mounts[0].Credential != "main" {
		t.Fatalf("mounts = %+v, want the credential reference", got.Mounts)
	}
}

func TestResolveMountParamsMergesCredential(t *testing.T) {
	cfg := &Config{
		Credentials: []CredentialConfig{
			{Name: "main", Type: "quark", Params: ParamMap{"cookie": "k=v", "base_url": "https://cred"}},
		},
	}
	params, err := cfg.ResolveMountParams(MountConfig{
		Name:       "docs",
		Type:       "quark",
		Credential: "main",
		Params:     ParamMap{"root_path": "/Docs", "base_url": "https://mount"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if params["cookie"] != "k=v" || params["root_path"] != "/Docs" {
		t.Fatalf("params = %+v, want credential and mount values merged", params)
	}
	if params["base_url"] != "https://mount" {
		t.Fatalf("base_url = %q, want the mount override", params["base_url"])
	}

	legacy, err := cfg.ResolveMountParams(MountConfig{
		Name:   "legacy",
		Type:   "quark",
		Params: ParamMap{"cookie": "inline"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if legacy["cookie"] != "inline" {
		t.Fatalf("legacy params = %+v, want inline cookie unchanged", legacy)
	}
}

func TestValidateCredentials(t *testing.T) {
	quarkCredential := func(params ParamMap) []CredentialConfig {
		return []CredentialConfig{{Name: "main", Type: "quark", Params: params}}
	}
	tests := []struct {
		name    string
		cfg     *Config
		wantErr string
	}{
		{
			name: "several mounts share one credential",
			cfg: &Config{
				Credentials: quarkCredential(ParamMap{"cookie": "k=v"}),
				Mounts: []MountConfig{
					{Name: "a", Type: "quark", Credential: "main", Params: ParamMap{"root_path": "/A"}},
					{Name: "b", Type: "quark", Credential: "main", Params: ParamMap{"root_path": "/B"}},
				},
			},
		},
		{
			name: "legacy inline mount stays valid",
			cfg: &Config{
				Mounts: []MountConfig{
					{Name: "a", Type: "quark", Params: ParamMap{"cookie": "k=v", "root_path": "/A"}},
				},
			},
		},
		{
			name: "unknown credential reference",
			cfg: &Config{
				Mounts: []MountConfig{
					{Name: "a", Type: "quark", Credential: "missing"},
				},
			},
			wantErr: "unknown credential",
		},
		{
			name: "credential type mismatch",
			cfg: &Config{
				Credentials: quarkCredential(ParamMap{"cookie": "k=v"}),
				Mounts: []MountConfig{
					{Name: "a", Type: "115", Credential: "main", Params: ParamMap{"root_path": "/A"}},
				},
			},
			wantErr: "does not match credential",
		},
		{
			name: "duplicate credential name",
			cfg: &Config{
				Credentials: []CredentialConfig{
					{Name: "main", Type: "quark", Params: ParamMap{"cookie": "a"}},
					{Name: "main", Type: "quark", Params: ParamMap{"cookie": "b"}},
				},
				Mounts: []MountConfig{{Name: "a", Type: "quark", Credential: "main"}},
			},
			wantErr: "duplicate credential name",
		},
		{
			name: "credential name is a single path component",
			cfg: &Config{
				Credentials: []CredentialConfig{{Name: "../main", Type: "quark", Params: ParamMap{"cookie": "a"}}},
				Mounts:      []MountConfig{{Name: "a", Type: "quark", Credential: "../main"}},
			},
			wantErr: "single path component",
		},
		{
			name: "credential cannot carry a mount parameter",
			cfg: &Config{
				Credentials: quarkCredential(ParamMap{"cookie": "k=v", "root_path": "/wrong"}),
				Mounts:      []MountConfig{{Name: "a", Type: "quark", Credential: "main"}},
			},
			wantErr: `parameter "root_path" has scope "mount"`,
		},
		{
			name: "mount cannot repeat a credential parameter",
			cfg: &Config{
				Credentials: quarkCredential(ParamMap{"cookie": "k=v"}),
				Mounts: []MountConfig{
					{Name: "a", Type: "quark", Credential: "main", Params: ParamMap{"cookie": "inline", "root_path": "/A"}},
				},
			},
			wantErr: `parameter "cookie" has scope "credential"`,
		},
		{
			name: "credential missing its required parameter",
			cfg: &Config{
				Credentials: quarkCredential(ParamMap{"base_url": "https://x"}),
				Mounts:      []MountConfig{{Name: "a", Type: "quark", Credential: "main"}},
			},
			wantErr: `missing required parameter "cookie"`,
		},
		{
			name: "credential satisfies an alternative group",
			cfg: &Config{
				Credentials: []CredentialConfig{{Name: "main", Type: "189", Params: ParamMap{"cookie": "k=v"}}},
				Mounts:      []MountConfig{{Name: "a", Type: "189", Credential: "main"}},
			},
		},
		{
			name: "credential misses every alternative group",
			cfg: &Config{
				Credentials: []CredentialConfig{{Name: "main", Type: "189", Params: ParamMap{}}},
				Mounts:      []MountConfig{{Name: "a", Type: "189", Credential: "main"}},
			},
			wantErr: "alternative parameter groups",
		},
		{
			name: "mount keeps a required mount parameter",
			cfg: &Config{
				Credentials: []CredentialConfig{{Name: "main", Type: "localfs", Params: ParamMap{}}},
				Mounts:      []MountConfig{{Name: "a", Type: "localfs", Credential: "main"}},
			},
			wantErr: `missing required parameter "root_path"`,
		},
		{
			name: "unknown driver parameter is rejected",
			cfg: &Config{
				Credentials: quarkCredential(ParamMap{"cookie": "k=v", "nonsense": "x"}),
				Mounts:      []MountConfig{{Name: "a", Type: "quark", Credential: "main"}},
			},
			wantErr: `unknown parameter "nonsense"`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Validate(tt.cfg)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate() error = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Validate() error = %v, want substring %q", err, tt.wantErr)
			}
		})
	}
}
