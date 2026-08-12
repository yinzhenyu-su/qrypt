package mobile

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// 复现真机问题:content_dedup=true + 直传 → 读回全零
func TestMobileDirectUploadDedupReadBack(t *testing.T) {
	tmp := t.TempDir()
	remote := filepath.Join(tmp, "remote")
	if err := os.MkdirAll(remote, 0o755); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(tmp, "qrypt.toml")
	config := fmt.Sprintf(`
[[mounts]]
name = "quark"
type = "localfs"
[mounts.params]
root_path = %q

[mounts.encryption]
password = "test-password"
filename_encryption = "standard"
filename_encoding = "base32"
content_dedup = true

[upload]
upload_delay = "10ms"
`, remote)
	if err := os.WriteFile(configPath, []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}

	opener := newFakeOpener()
	content := []byte("dedup direct upload round trip: " + string(make([]byte, 300*1024)))
	opener.add("content://fake/photo.jpg", content)

	coreID, err := openCore(configPath, testRuntimeJSON(tmp))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = closeCore(coreID) }()
	if raw := SetUploadSourceOpenerJSON(opener); !jsonOK(raw) {
		t.Fatalf("SetUploadSourceOpenerJSON = %s", raw)
	}
	defer ClearUploadSourceOpenerJSON()

	raw := CreateDirectUploadTaskJSON(coreID, `{"items":[{"item_id":"d1","source_path":"content://fake/photo.jpg","dest_path":"/quark/photo.jpg","size":`+fmt.Sprintf("%d", len(content))+`}]}`, 0)
	var created struct {
		OK   bool       `json:"ok"`
		Data mobileTask `json:"data"`
	}
	if err := json.Unmarshal([]byte(raw), &created); err != nil {
		t.Fatal(err)
	}
	if !created.OK {
		t.Fatalf("CreateDirectUploadTaskJSON = %s", raw)
	}
	waitMobileTaskState(t, coreID, created.Data.ID, "succeeded")

	openRaw := OpenFileJSON(coreID, "/quark/photo.jpg", "")
	var file struct {
		OK   bool   `json:"ok"`
		Data string `json:"data"`
	}
	if err := json.Unmarshal([]byte(openRaw), &file); err != nil {
		t.Fatal(err)
	}
	if !file.OK {
		t.Fatalf("OpenFileJSON = %s", openRaw)
	}
	buf := make([]byte, len(content))
	read, err := ReadAtInto(file.Data, 0, buf, 5000)
	if err != nil {
		t.Fatal(err)
	}
	_ = CloseFileJSON(file.Data)
	if string(buf[:read]) != string(content) {
		zeros := 0
		for _, b := range buf[:read] {
			if b == 0 {
				zeros++
			}
		}
		t.Fatalf("read back mismatch: read=%d zeroBytes=%d (content=%d)", read, zeros, len(content))
	}
}
