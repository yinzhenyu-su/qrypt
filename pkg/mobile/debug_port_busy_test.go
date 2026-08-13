package mobile

import (
	"os"
	"path/filepath"
	"testing"
)

// 模拟旧进程 core 占着 debug 端口,新 core 打开应降级而非失败
func TestMobileOpenSecondCoreWithPortBusy(t *testing.T) {
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

	// 第二个 core 用同一 runtime(debug listen 127.0.0.1:19090 相同)
	second, err := openCore(configPath, testRuntimeJSON(t.TempDir()))
	if err != nil {
		t.Fatalf("second core open failed while first holds debug port: %v", err)
	}
	_ = closeCore(second)
}
