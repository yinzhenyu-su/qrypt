package mobile

import (
	"fmt"
	"path/filepath"
)

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
		filepath.Join(tmp, "files", "qrypt", "logs"),
		filepath.Join(tmp, "cache", "qrypt", "tmp"),
	)
}
