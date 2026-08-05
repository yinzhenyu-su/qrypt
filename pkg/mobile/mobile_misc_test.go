package mobile

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestMobileJSONEnvelopeAndDiagnostics(t *testing.T) {
	tmp := t.TempDir()
	remote := filepath.Join(tmp, "remote")
	if err := os.MkdirAll(remote, 0o755); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(tmp, "qrypt.toml")
	if err := os.WriteFile(configPath, []byte(`
[[mounts]]
name = "quark"
type = "localfs"
[mounts.params]
root_path = "`+remote+`"
`), 0o644); err != nil {
		t.Fatal(err)
	}

	var opened struct {
		OK   bool   `json:"ok"`
		Data string `json:"data"`
	}
	if err := json.Unmarshal([]byte(OpenJSON(configPath, testRuntimeJSON(tmp))), &opened); err != nil {
		t.Fatal(err)
	}
	if !opened.OK || opened.Data == "" {
		t.Fatalf("OpenJSON = %+v, want ok core id", opened)
	}
	defer closeCore(opened.Data)

	var listed struct {
		OK   bool    `json:"ok"`
		Data []entry `json:"data"`
	}
	if err := json.Unmarshal([]byte(ListJSON(opened.Data, "/quark", 0)), &listed); err != nil {
		t.Fatal(err)
	}
	if !listed.OK {
		t.Fatalf("ListJSON = %+v, want ok", listed)
	}

	var drivers struct {
		OK   bool     `json:"ok"`
		Data []string `json:"data"`
	}
	if err := json.Unmarshal([]byte(DriverNamesJSON()), &drivers); err != nil {
		t.Fatal(err)
	}
	if !drivers.OK || len(drivers.Data) == 0 {
		t.Fatalf("DriverNamesJSON = %+v, want drivers", drivers)
	}

	var schema struct {
		OK   bool `json:"ok"`
		Data []struct {
			Name string `json:"name"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(DriverSchemaJSON("localfs")), &schema); err != nil {
		t.Fatal(err)
	}
	if !schema.OK || len(schema.Data) == 0 || schema.Data[0].Name != "root_path" {
		t.Fatalf("DriverSchemaJSON = %+v, want localfs root_path schema", schema)
	}

	var snapshot struct {
		OK   bool `json:"ok"`
		Data struct {
			Kind string `json:"kind"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(DebugSnapshotJSON(opened.Data)), &snapshot); err != nil {
		t.Fatal(err)
	}
	if !snapshot.OK || snapshot.Data.Kind == "" {
		t.Fatalf("DebugSnapshotJSON = %+v, want snapshot", snapshot)
	}
	if raw := FlushReadCacheJSON(opened.Data); !strings.Contains(raw, `"ok":true`) {
		t.Fatalf("FlushReadCacheJSON = %s, want ok", raw)
	}
	readingFile := filepath.Join(tmp, "cache", "qrypt", "read", "quark", "mobile.batch")
	if err := os.WriteFile(readingFile, []byte("mobile-cache"), 0o644); err != nil {
		t.Fatal(err)
	}
	var storage struct {
		OK   bool `json:"ok"`
		Data struct {
			ReadCacheBytes int64 `json:"read_cache_bytes"`
			StagingBytes   int64 `json:"staging_bytes"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(StorageUsageJSON(opened.Data)), &storage); err != nil {
		t.Fatal(err)
	}
	if !storage.OK || storage.Data.ReadCacheBytes != int64(len("mobile-cache")) {
		t.Fatalf("StorageUsageJSON = %+v, want read cache bytes", storage)
	}
	if raw := ClearReadCacheJSON(opened.Data, 0); !strings.Contains(raw, `"ok":true`) {
		t.Fatalf("ClearReadCacheJSON = %s, want ok", raw)
	}
	if _, err := os.Stat(readingFile); !os.IsNotExist(err) {
		t.Fatalf("reading file still exists after ClearReadCacheJSON, err=%v", err)
	}

	var logs struct {
		OK   bool `json:"ok"`
		Data []struct {
			Name string `json:"name"`
			Size int64  `json:"size"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(LogFilesJSON(opened.Data)), &logs); err != nil {
		t.Fatal(err)
	}
	if !logs.OK || len(logs.Data) == 0 || logs.Data[0].Name == "" {
		t.Fatalf("LogFilesJSON = %+v, want log files", logs)
	}
	if raw := ReadLogJSON(opened.Data, logs.Data[0].Name, 0, 64); !strings.Contains(raw, `"ok":true`) {
		t.Fatalf("ReadLogJSON = %s, want ok", raw)
	}
}

func TestMobileThumbnailCacheUsesFilePaths(t *testing.T) {
	tmp := t.TempDir()
	remote := filepath.Join(tmp, "remote")
	if err := os.MkdirAll(remote, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(remote, "photo.jpg"), []byte("source"), 0o644); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(tmp, "qrypt.toml")
	if err := os.WriteFile(configPath, []byte(`
	[[mounts]]
	name = "quark"
	type = "localfs"
	[mounts.params]
	root_path = "`+remote+`"
	`), 0o644); err != nil {
		t.Fatal(err)
	}
	coreID, err := openCore(configPath, testRuntimeJSON(tmp))
	if err != nil {
		t.Fatal(err)
	}
	defer closeCore(coreID)

	localThumb := filepath.Join(tmp, "thumb.jpg")
	if err := os.WriteFile(localThumb, []byte("thumb"), 0o644); err != nil {
		t.Fatal(err)
	}
	var put struct {
		OK   bool `json:"ok"`
		Data struct {
			Hit  bool   `json:"hit"`
			Path string `json:"path"`
			Mime string `json:"mime"`
			Size int64  `json:"size"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(PutThumbnailFileJSON(coreID, "/quark/photo.jpg", "grid-128", "image/jpeg", localThumb, 0)), &put); err != nil {
		t.Fatal(err)
	}
	if !put.OK || !put.Data.Hit || put.Data.Path == "" || put.Data.Mime != "image/jpeg" || put.Data.Size != int64(len("thumb")) {
		t.Fatalf("PutThumbnailFileJSON = %+v", put)
	}
	if strings.Contains(put.Data.Path, localThumb) {
		t.Fatalf("thumbnail path = %q, want qrypt-managed cache path", put.Data.Path)
	}

	var got struct {
		OK   bool `json:"ok"`
		Data struct {
			Hit  bool   `json:"hit"`
			Path string `json:"path"`
			Mime string `json:"mime"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(GetThumbnailFileJSON(coreID, "/quark/photo.jpg", "grid-128", 0)), &got); err != nil {
		t.Fatal(err)
	}
	if !got.OK || !got.Data.Hit || got.Data.Path != put.Data.Path || got.Data.Mime != "image/jpeg" {
		t.Fatalf("GetThumbnailFileJSON = %+v", got)
	}
	if raw := ThumbnailCacheUsageJSON(coreID, 0); !strings.Contains(raw, `"ok":true`) || !strings.Contains(raw, `"data":`) {
		t.Fatalf("ThumbnailCacheUsageJSON = %s", raw)
	}
	if raw := ClearThumbnailCacheJSON(coreID, 0); !strings.Contains(raw, `"ok":true`) {
		t.Fatalf("ClearThumbnailCacheJSON = %s", raw)
	}
	if raw := GetThumbnailFileJSON(coreID, "/quark/photo.jpg", "grid-128", 0); !strings.Contains(raw, `"hit":false`) {
		t.Fatalf("GetThumbnailFileJSON after clear = %s", raw)
	}
}

func TestMobileMountsJSONReportsEncryptedPerMount(t *testing.T) {
	tmp := t.TempDir()
	plainRemote := filepath.Join(tmp, "plain-remote")
	encryptedRemote := filepath.Join(tmp, "encrypted-remote")
	if err := os.MkdirAll(plainRemote, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(encryptedRemote, 0o755); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(tmp, "qrypt.toml")
	if err := os.WriteFile(configPath, []byte(`
[[mounts]]
name = "plain"
type = "localfs"
[mounts.params]
root_path = "`+plainRemote+`"

[[mounts]]
name = "secret"
type = "localfs"
[mounts.params]
root_path = "`+encryptedRemote+`"
[mounts.encryption]
password = "test-password"
`), 0o644); err != nil {
		t.Fatal(err)
	}

	coreID, err := openCore(configPath, testRuntimeJSON(tmp))
	if err != nil {
		t.Fatal(err)
	}
	defer closeCore(coreID)

	var mounts struct {
		OK   bool `json:"ok"`
		Data []struct {
			Name      string `json:"name"`
			Path      string `json:"path"`
			Encrypted bool   `json:"encrypted"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(MountsJSON(coreID)), &mounts); err != nil {
		t.Fatal(err)
	}
	if !mounts.OK || len(mounts.Data) != 2 {
		t.Fatalf("MountsJSON = %+v, want two mounts", mounts)
	}
	got := map[string]bool{}
	for _, mount := range mounts.Data {
		got[mount.Path] = mount.Encrypted
	}
	if got["/plain"] {
		t.Fatalf("plain mount reported encrypted: %+v", mounts.Data)
	}
	if !got["/secret"] {
		t.Fatalf("secret mount did not report encrypted: %+v", mounts.Data)
	}
}

func TestMobileImportOpenAndResume(t *testing.T) {
	tmp := t.TempDir()
	remote := filepath.Join(tmp, "remote")
	if err := os.MkdirAll(remote, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(remote, "file.txt"), []byte("resume"), 0o644); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(tmp, "qrypt.toml")
	if err := os.WriteFile(configPath, []byte(`
[storage]
read_cache_dir = "/desktop/cache/read"
upload_dir = "/desktop/upload"
state_dir = "/desktop/state"

[[mounts]]
name = "quark"
type = "localfs"
[mounts.params]
root_path = "`+remote+`"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	runtimeRaw := testRuntimeJSON(tmp)
	if raw := ImportConfigJSON(configPath, runtimeRaw); !strings.Contains(raw, `"ok":true`) {
		t.Fatalf("ImportConfigJSON = %s, want ok", raw)
	}
	var opened struct {
		OK   bool   `json:"ok"`
		Data string `json:"data"`
	}
	if err := json.Unmarshal([]byte(OpenImportedJSON(runtimeRaw)), &opened); err != nil {
		t.Fatal(err)
	}
	if !opened.OK || opened.Data == "" {
		t.Fatalf("OpenImportedJSON = %+v, want core id", opened)
	}
	defer closeCore(opened.Data)

	var info struct {
		OK   bool `json:"ok"`
		Data struct {
			ID      string `json:"id"`
			Size    int64  `json:"size"`
			ModTime string `json:"mod_time"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(FileInfoJSON(opened.Data, "/quark/file.txt", 0)), &info); err != nil {
		t.Fatal(err)
	}
	if !info.OK || info.Data.Size != int64(len("resume")) {
		t.Fatalf("FileInfoJSON = %+v, want file info", info)
	}
	var check struct {
		OK   bool `json:"ok"`
		Data struct {
			OK bool `json:"ok"`
		} `json:"data"`
	}
	raw := ValidateResumeJSON(opened.Data, "/quark/file.txt", info.Data.ID, info.Data.Size, info.Data.ModTime, 0)
	if err := json.Unmarshal([]byte(raw), &check); err != nil {
		t.Fatal(err)
	}
	if !check.OK || !check.Data.OK {
		t.Fatalf("ValidateResumeJSON = %s, want ok resume", raw)
	}
}

func TestMobileWaitLocalFileStableJSON(t *testing.T) {
	tmp := t.TempDir()
	remote := filepath.Join(tmp, "remote")
	if err := os.MkdirAll(remote, 0o755); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(tmp, "qrypt.toml")
	if err := os.WriteFile(configPath, []byte(`
[[mounts]]
name = "quark"
type = "localfs"
[mounts.params]
root_path = "`+remote+`"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	coreID, err := openCore(configPath, testRuntimeJSON(tmp))
	if err != nil {
		t.Fatal(err)
	}
	defer closeCore(coreID)

	localPath := filepath.Join(tmp, "local.txt")
	if err := os.WriteFile(localPath, []byte("stable"), 0o644); err != nil {
		t.Fatal(err)
	}
	raw := WaitLocalFileStableJSON(coreID, localPath, `{"quiet_ms":5,"poll_ms":1}`, 1000)
	var got struct {
		OK   bool `json:"ok"`
		Data struct {
			Path   string `json:"path"`
			Stable bool   `json:"stable"`
			Size   int64  `json:"size"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatal(err)
	}
	if !got.OK || !got.Data.Stable || got.Data.Path != localPath || got.Data.Size != int64(len("stable")) {
		t.Fatalf("WaitLocalFileStableJSON = %s, want stable snapshot", raw)
	}
}

func TestMobileConfigManagement(t *testing.T) {
	tmp := t.TempDir()
	remote := filepath.Join(tmp, "remote")
	if err := os.MkdirAll(remote, 0o755); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(tmp, "qrypt.toml")
	configContent := `
[[mounts]]
name = "quark"
type = "localfs"
[mounts.params]
root_path = "` + remote + `"
`
	if err := os.WriteFile(configPath, []byte(configContent), 0o644); err != nil {
		t.Fatal(err)
	}
	coreID, err := openCore(configPath, testRuntimeJSON(tmp))
	if err != nil {
		t.Fatal(err)
	}
	defer closeCore(coreID)

	var summary struct {
		OK   bool `json:"ok"`
		Data struct {
			ConfigPath string `json:"config_path"`
			Mounts     []struct {
				Name      string `json:"name"`
				Type      string `json:"type"`
				Encrypted bool   `json:"encrypted"`
			} `json:"mounts"`
			ReadCache struct {
				MaxSize string `json:"max_size"`
			} `json:"read_cache"`
			Upload struct {
				DefaultMount string `json:"default_mount"`
			} `json:"upload"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(ConfigSummaryJSON(coreID)), &summary); err != nil {
		t.Fatal(err)
	}
	if !summary.OK || len(summary.Data.Mounts) != 1 || summary.Data.Mounts[0].Name != "quark" || summary.Data.Mounts[0].Type != "localfs" {
		t.Fatalf("ConfigSummaryJSON = %+v", summary)
	}

	raw := UpdateConfigJSON(coreID, `{"read_cache":{"max_size":"128M"},"upload":{"default_mount":"quark","default_path":"/inbox"},"mounts":[{"action":"add","name":"backup","type":"localfs","params":{"root_path":`+fmt.Sprintf("%q", filepath.Join(tmp, "backup-remote"))+`}}]}`, 0)
	var updated struct {
		OK   bool `json:"ok"`
		Data struct {
			Mounts []struct {
				Name string `json:"name"`
			} `json:"mounts"`
			ReadCache struct {
				MaxSize string `json:"max_size"`
			} `json:"read_cache"`
			Upload struct {
				DefaultMount string `json:"default_mount"`
				DefaultPath  string `json:"default_path"`
			} `json:"upload"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(raw), &updated); err != nil {
		t.Fatal(err)
	}
	if !updated.OK {
		t.Fatalf("UpdateConfigJSON = %s, want ok", raw)
	}
	if len(updated.Data.Mounts) != 2 || updated.Data.Mounts[1].Name != "backup" {
		t.Fatalf("updated mounts = %+v, want backup added", updated.Data.Mounts)
	}
	if updated.Data.ReadCache.MaxSize != "128M" || updated.Data.Upload.DefaultMount != "quark" || updated.Data.Upload.DefaultPath != "/inbox" {
		t.Fatalf("updated settings = %+v", updated.Data)
	}

	badRaw := UpdateConfigJSON(coreID, `{"mounts":[{"action":"add","name":"bad","type":"does-not-exist","params":{}}]}`, 0)
	var bad struct {
		OK bool `json:"ok"`
	}
	if err := json.Unmarshal([]byte(badRaw), &bad); err != nil {
		t.Fatal(err)
	}
	if bad.OK {
		t.Fatalf("UpdateConfigJSON with unknown driver = %s, want error", badRaw)
	}
	afterBad := ConfigSummaryJSON(coreID)
	if strings.Contains(afterBad, `"bad"`) {
		t.Fatalf("failed update must not persist: %s", afterBad)
	}

	if err := os.MkdirAll(filepath.Join(tmp, "backup-remote"), 0o755); err != nil {
		t.Fatal(err)
	}

	if raw := ReloadConfigJSON(coreID, 5000); !strings.Contains(raw, `"ok":true`) {
		t.Fatalf("ReloadConfigJSON = %s, want ok", raw)
	}
	if err := os.MkdirAll(filepath.Join(remote, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	listRaw := ListJSON(coreID, "/quark", 0)
	if !strings.Contains(listRaw, `"docs"`) {
		t.Fatalf("ListJSON after reload = %s, want docs visible", listRaw)
	}
	if data, err := os.ReadFile(configPath); err != nil || !strings.Contains(string(data), "128M") {
		t.Fatalf("config file after update = %q err=%v, want saved read_cache.max_size", data, err)
	}
}

func TestMobileDebugSnapshotHandleCounts(t *testing.T) {
	tmp := t.TempDir()
	remote := filepath.Join(tmp, "remote")
	if err := os.MkdirAll(remote, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(remote, "file.txt"), []byte("count"), 0o644); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(tmp, "qrypt.toml")
	if err := os.WriteFile(configPath, []byte(`
[[mounts]]
name = "quark"
type = "localfs"
[mounts.params]
root_path = "`+remote+`"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	coreID, err := openCore(configPath, testRuntimeJSON(tmp))
	if err != nil {
		t.Fatal(err)
	}
	defer closeCore(coreID)

	var opened struct {
		OK   bool   `json:"ok"`
		Data string `json:"data"`
	}
	if err := json.Unmarshal([]byte(OpenFileJSON(coreID, "/quark/file.txt", "")), &opened); err != nil {
		t.Fatal(err)
	}
	if !opened.OK {
		t.Fatalf("OpenFileJSON = %+v", opened)
	}
	handleID := opened.Data

	snapshotRaw := DebugSnapshotJSON(coreID)
	var snapshot struct {
		OK   bool `json:"ok"`
		Data struct {
			MobileHandles map[string]int `json:"mobile_handles"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(snapshotRaw), &snapshot); err != nil {
		t.Fatal(err)
	}
	if !snapshot.OK || snapshot.Data.MobileHandles["files"] != 1 {
		t.Fatalf("DebugSnapshotJSON handles = %+v, want 1 open file handle", snapshot.Data.MobileHandles)
	}
	CloseFileJSON(handleID)
	snapshotRaw = DebugSnapshotJSON(coreID)
	var after struct {
		OK   bool `json:"ok"`
		Data struct {
			MobileHandles map[string]int `json:"mobile_handles"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(snapshotRaw), &after); err != nil {
		t.Fatal(err)
	}
	if after.Data.MobileHandles["files"] != 0 {
		t.Fatalf("DebugSnapshotJSON handles after close = %+v, want 0", after.Data.MobileHandles)
	}
}

// TestMobileReloadConcurrentWithAPICalls exercises the session core lock:
// API calls read s.core while ReloadConfigJSON swaps it, and the old core is
// only closed after no reader can hold it. Run under -race.

func TestMobileReloadConcurrentWithAPICalls(t *testing.T) {
	tmp := t.TempDir()
	remote := filepath.Join(tmp, "remote")
	if err := os.MkdirAll(remote, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(remote, "a.txt"), []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(tmp, "qrypt.toml")
	if err := os.WriteFile(configPath, []byte("[[mounts]]\nname = \"loc\"\ntype = \"localfs\"\n[mounts.params]\nroot_path = \""+remote+"\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	coreID, err := openCore(configPath, testRuntimeJSON(tmp))
	if err != nil {
		t.Fatal(err)
	}
	defer closeCore(coreID)

	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 15; j++ {
				raw := ListJSON(coreID, "/loc", 1000)
				if !strings.Contains(raw, `"ok":true`) {
					t.Errorf("ListJSON failed concurrently: %s", raw)
					return
				}
			}
		}()
	}
	for i := 0; i < 4; i++ {
		raw := ReloadConfigJSON(coreID, 1000)
		if !strings.Contains(raw, `"ok":true`) {
			t.Errorf("ReloadConfigJSON failed: %s", raw)
			return
		}
	}
	wg.Wait()
}
