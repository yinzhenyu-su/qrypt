package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "github.com/yinzhenyu/qrypt/pkg/drivers/koofr"
	_ "github.com/yinzhenyu/qrypt/pkg/drivers/quark"
	_ "github.com/yinzhenyu/qrypt/pkg/drivers/webdav"
)

func koofrMount(streams, windowChunks int) MountConfig {
	return MountConfig{
		Name: "koofr",
		Type: "koofr",
		Params: ParamMap{
			"email":        "user@example.com",
			"app_password": "app-password",
		},
		ReadProfile: &ReadProfileConfig{Streams: streams, WindowChunks: windowChunks},
	}
}

func TestValidateReadProfileOverride(t *testing.T) {
	tests := []struct {
		name    string
		mount   MountConfig
		wantErr string
	}{
		{name: "valid parallel", mount: koofrMount(4, 16)},
		{name: "inherit", mount: koofrMount(0, 0)},
		{name: "single stream with window", mount: koofrMount(1, 16)},
		{
			name:    "streams too large",
			mount:   koofrMount(33, 0),
			wantErr: "between 1 and 32",
		},
		{
			name:    "window too large",
			mount:   koofrMount(4, 65),
			wantErr: "between 1 and 64",
		},
		{
			name: "driver without parallel range",
			mount: MountConfig{
				Name: "webdav",
				Type: "webdav",
				Params: ParamMap{
					"url":      "https://example.invalid/",
					"username": "user",
					"password": "password",
				},
				ReadProfile: &ReadProfileConfig{Streams: 4},
			},
			wantErr: "requires the parallel_range capability",
		},
		{
			name: "internal parallel driver",
			mount: MountConfig{
				Name:        "quark",
				Type:        "quark",
				Params:      ParamMap{"cookie": "k=v"},
				ReadProfile: &ReadProfileConfig{Streams: 4},
			},
			wantErr: "internal_parallel_read",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateMount(&Config{}, 0, tt.mount)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("ValidateMount: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("ValidateMount error = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestReadProfileFor(t *testing.T) {
	cfg := &Config{
		Mounts: []MountConfig{
			{Name: "a", Type: "koofr", ReadProfile: &ReadProfileConfig{Streams: 4, WindowChunks: 16}},
			{Name: "b", Type: "koofr"},
		},
	}
	got := cfg.ReadProfileFor("a")
	if got == nil || got.Streams != 4 || got.WindowChunks != 16 {
		t.Fatalf("ReadProfileFor(a) = %+v", got)
	}
	if cfg.ReadProfileFor("b") != nil {
		t.Fatal("ReadProfileFor(b) returned an override")
	}
	if cfg.ReadProfileFor("missing") != nil {
		t.Fatal("ReadProfileFor(missing) returned an override")
	}
	got.Streams = 9
	if cfg.Mounts[0].ReadProfile.Streams != 4 {
		t.Fatal("ReadProfileFor returned an alias to the config")
	}
}

func TestLoadReadProfileTable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "qrypt.toml")
	body := `
[[mounts]]
name = "local"
type = "localfs"

[mounts.read_profile]
streams = 4
window_chunks = 16
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	profile := cfg.ReadProfileFor("local")
	if profile == nil || profile.Streams != 4 || profile.WindowChunks != 16 {
		t.Fatalf("read profile = %+v, want 4/16", profile)
	}
}
