package sync

import (
	"path/filepath"
	"testing"
)

func useTestQryptHome(t *testing.T) string {
	t.Helper()
	home := filepath.Join(t.TempDir(), "qrypt-home")
	t.Setenv("QRYPT_HOME", home)
	return home
}
