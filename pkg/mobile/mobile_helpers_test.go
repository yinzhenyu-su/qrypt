package mobile

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// testLogRoot is a process-wide directory for session log files, deliberately
// OUTSIDE any t.TempDir: core opens a lumberjack writer on <log_dir>/qrypt.log
// that is only released when another core replaces it, so a TempDir living
// under t.TempDir would be locked by that handle on Windows cleanup.
const testLogRoot = "qrypt-mobile-test-logs"

func testRuntimeJSON(tmp string) string {
	return fmt.Sprintf(`{
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
		filepath.Join(tmp, "files", "qrypt", "state"),
		filepath.Join(os.TempDir(), testLogRoot),
		filepath.Join(tmp, "cache", "qrypt", "tmp"),
	)
}

// tomlPath renders a filesystem path as a TOML basic-string literal. On
// Windows paths contain backslashes, which TOML parses as escape sequences
// ("\U" is a unicode escape), so they must be escaped before embedding.
// macOS/Linux temp paths are unchanged by this.
func tomlPath(p string) string {
	p = strings.ReplaceAll(p, `\`, `\\`)
	p = strings.ReplaceAll(p, `"`, `\"`)
	return `"` + p + `"`
}
