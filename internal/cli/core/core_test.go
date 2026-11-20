package core

import (
	"slices"
	"testing"

	cliruntime "github.com/yinzhenyu/qrypt/internal/cli/runtime"
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
