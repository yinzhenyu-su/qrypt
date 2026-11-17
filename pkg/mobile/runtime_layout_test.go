package mobile

import (
	"fmt"
	"path/filepath"
	"testing"
)

func TestParseRuntimeJSONRunDir(t *testing.T) {
	tmp := t.TempDir()
	stateDir := filepath.Join(tmp, "files", "qrypt", "state")
	base := fmt.Sprintf(`{
		"config_dir": %q,
		"storage": {
			"read_cache_dir": %q,
			"thumbnail_cache_dir": %q,
			"upload_dir": %q,
			"state_dir": %q,
			"log_dir": %q,
			"tmp_dir": %q
		}
	}`,
		filepath.Join(tmp, "files", "qrypt", "config"),
		filepath.Join(tmp, "cache", "qrypt", "read"),
		filepath.Join(tmp, "cache", "qrypt", "thumbnail"),
		filepath.Join(tmp, "files", "qrypt", "upload"),
		stateDir,
		filepath.Join(tmp, "files", "qrypt", "logs"),
		filepath.Join(tmp, "cache", "qrypt", "tmp"),
	)

	// Mobile has no writable home directory, so an omitted run_dir must stay
	// inside the app-owned state root instead of the desktop ~/.qrypt default.
	runtime, err := parseRuntimeJSON(base)
	if err != nil {
		t.Fatalf("parseRuntimeJSON() error = %v", err)
	}
	if want := filepath.Join(stateDir, "run"); runtime.RunDir != want {
		t.Fatalf("RunDir = %q, want %q", runtime.RunDir, want)
	}

	explicit := filepath.Join(tmp, "run")
	withRun := fmt.Sprintf(`{
		"config_dir": %q,
		"storage": {
			"run_dir": %q,
			"read_cache_dir": %q,
			"thumbnail_cache_dir": %q,
			"upload_dir": %q,
			"state_dir": %q,
			"log_dir": %q,
			"tmp_dir": %q
		}
	}`,
		filepath.Join(tmp, "files", "qrypt", "config"),
		explicit,
		filepath.Join(tmp, "cache", "qrypt", "read"),
		filepath.Join(tmp, "cache", "qrypt", "thumbnail"),
		filepath.Join(tmp, "files", "qrypt", "upload"),
		stateDir,
		filepath.Join(tmp, "files", "qrypt", "logs"),
		filepath.Join(tmp, "cache", "qrypt", "tmp"),
	)
	runtime, err = parseRuntimeJSON(withRun)
	if err != nil {
		t.Fatalf("parseRuntimeJSON() error = %v", err)
	}
	if runtime.RunDir != explicit {
		t.Fatalf("RunDir = %q, want explicit %q", runtime.RunDir, explicit)
	}
}
