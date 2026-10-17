package mobile

import (
	"os"
	"path/filepath"
	"testing"
)

// Multiple Mobile cores can open without implicitly binding a debug port.
func TestMobileOpenSecondCore(t *testing.T) {
	tmp := t.TempDir()
	remote := filepath.Join(tmp, "remote")
	if err := os.MkdirAll(remote, 0o755); err != nil {
		t.Fatal(err)
	}
	configPath := directUploadTestConfig(t, tmp, remote)

	first, err := openCore(configPath, testRuntimeJSON(tmp))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = closeCore(first) }()

	// The second core uses a separate runtime and does not auto-start debug.
	second, err := openCore(configPath, testRuntimeJSON(t.TempDir()))
	if err != nil {
		t.Fatalf("second core open failed while first holds debug port: %v", err)
	}
	_ = closeCore(second)
}
