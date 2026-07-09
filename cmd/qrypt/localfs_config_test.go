package main

import (
	"strings"
	"testing"

	"github.com/yinzhenyu/qrypt/internal/config"
)

func TestLocalFSRejectsLegacyRootParameter(t *testing.T) {
	cfg := &config.Config{Mounts: []config.MountConfig{{
		Name:   "local",
		Type:   "localfs",
		Params: config.ParamMap{"root": t.TempDir()},
	}}}
	err := validateConfig(cfg)
	if err == nil || !strings.Contains(err.Error(), "root_path") {
		t.Fatalf("expected root_path validation error, got %v", err)
	}
}
