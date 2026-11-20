package core

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	cliruntime "github.com/yinzhenyu/qrypt/internal/cli/runtime"
	"github.com/yinzhenyu/qrypt/pkg/buildinfo"
	"github.com/yinzhenyu/qrypt/pkg/config"
	"github.com/yinzhenyu/qrypt/pkg/control"
	"github.com/yinzhenyu/qrypt/pkg/core/ownerlock"
)

func TestEnsureCoreServeArgsOmitsKeep(t *testing.T) {
	args := ensureCoreServeArgs(cliruntime.ConfigState{Path: "/tmp/qrypt.toml"})
	if slices.Contains(args, "--keep") {
		t.Fatalf("auto-started Core must be allowed to idle-exit, got %v", args)
	}
	if !slices.Contains(args, "--config") || !slices.Contains(args, "/tmp/qrypt.toml") {
		t.Fatalf("config path missing from %v", args)
	}

	args = ensureCoreServeArgs(cliruntime.ConfigState{})
	if slices.Contains(args, "--config") {
		t.Fatalf("config flag included without a config path: %v", args)
	}
}

func TestVerifyCoreStatus(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "qrypt.toml")
	if err := os.WriteFile(configPath, []byte("mount_point = \"\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fingerprint, err := ownerlock.FingerprintFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	wantVersion := buildinfo.Current().Version
	state := cliruntime.ConfigState{Path: configPath, Cfg: &config.Config{}}
	ok := control.RuntimeStatus{
		ProtocolVersion: control.ProtocolVersion,
		Version:         wantVersion,
		ConfigPath:      configPath,
		Fingerprint:     fingerprint,
	}
	if err := verifyCoreStatus(ok, state); err != nil {
		t.Fatalf("matching Core rejected: %v", err)
	}

	cases := []struct {
		name    string
		mutate  func(*control.RuntimeStatus)
		wantSub string
	}{
		{"protocol", func(s *control.RuntimeStatus) { s.ProtocolVersion++ }, "protocol"},
		{"version", func(s *control.RuntimeStatus) { s.Version = "0.0.0-other" }, "runs qrypt"},
		{"config path", func(s *control.RuntimeStatus) { s.ConfigPath = filepath.Join(dir, "other.toml") }, "uses config"},
		{"fingerprint", func(s *control.RuntimeStatus) { s.Fingerprint = "sha256:other" }, "different configuration"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status := ok
			tc.mutate(&status)
			err := verifyCoreStatus(status, state)
			if err == nil || !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("verifyCoreStatus = %v, want error containing %q", err, tc.wantSub)
			}
		})

	}
}
