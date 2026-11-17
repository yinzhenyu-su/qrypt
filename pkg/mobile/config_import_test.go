package mobile

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/config"
	"github.com/yinzhenyu/qrypt/pkg/util"
)

// TestMobileImportConfigWithSharedCredentials covers the v0.6+ config shape:
// named [[credentials]] shared by mounts. The Android app imports the file
// through this path, so the credential block must survive the copy and the
// imported config must still open.
func TestMobileImportConfigWithSharedCredentials(t *testing.T) {
	tmp := t.TempDir()
	root := filepath.Join(tmp, "root")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(tmp, "qrypt.toml")
	raw := `
version = "1"

[[credentials]]
name = "quark"
type = "quark"
[credentials.params]
cookie = "k=v"

[[mounts]]
name = "local"
type = "localfs"
[mounts.params]
root_path = ` + util.TOMLPath(root) + `
`
	if err := os.WriteFile(src, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}

	runtimeRaw := testRuntimeJSON(tmp)
	var imported struct {
		OK   bool   `json:"ok"`
		Data string `json:"data"`
	}
	if err := json.Unmarshal([]byte(ImportConfigJSON(src, runtimeRaw)), &imported); err != nil {
		t.Fatal(err)
	}
	if !imported.OK || imported.Data == "" {
		t.Fatalf("ImportConfigJSON = %+v, want imported path", imported)
	}
	cfg, err := config.Load(imported.Data)
	if err != nil {
		t.Fatalf("load imported config: %v", err)
	}
	if len(cfg.Credentials) != 1 || cfg.Credentials[0].Name != "quark" {
		t.Fatalf("credentials = %+v, want the quark entry", cfg.Credentials)
	}

	var opened struct {
		OK    bool   `json:"ok"`
		Data  string `json:"data"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(OpenImportedJSON(runtimeRaw)), &opened); err != nil {
		t.Fatal(err)
	}
	if !opened.OK || opened.Data == "" {
		t.Fatalf("OpenImportedJSON = %+v, want ok core id", opened)
	}
	defer func() { _ = closeCore(opened.Data) }()

	var mounts struct {
		OK   bool `json:"ok"`
		Data []struct {
			Name  string `json:"name"`
			State string `json:"state"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(MountsJSON(opened.Data)), &mounts); err != nil {
		t.Fatal(err)
	}
	if !mounts.OK || len(mounts.Data) != 1 || mounts.Data[0].Name != "local" || mounts.Data[0].State == "failed" {
		t.Fatalf("MountsJSON = %+v, want the local mount initialized", mounts)
	}
}
