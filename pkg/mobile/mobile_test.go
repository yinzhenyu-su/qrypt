package mobile

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

func testRuntimeJSON(tmp string) string {
	return fmt.Sprintf(`{
		"config_dir": %q,
		"storage": {
				"read_cache_dir": %q,
				"thumbnail_cache_dir": %q,
				"writeback_dir": %q,
			"state_dir": %q,
			"log_dir": %q,
			"tmp_dir": %q
		}
	}`,
		filepath.Join(tmp, "files", "qrypt", "config"),
		filepath.Join(tmp, "cache", "qrypt", "read"),
		filepath.Join(tmp, "cache", "qrypt", "thumbnail"),
		filepath.Join(tmp, "files", "qrypt", "writeback"),
		filepath.Join(tmp, "files", "qrypt", "state"),
		filepath.Join(tmp, "files", "qrypt", "logs"),
		filepath.Join(tmp, "cache", "qrypt", "tmp"),
	)
}

func TestMobileListAndReadAt(t *testing.T) {
	tmp := t.TempDir()
	remote := filepath.Join(tmp, "remote")
	if err := os.MkdirAll(filepath.Join(remote, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(remote, "docs", "file.txt"), []byte("hello mobile core"), 0o644); err != nil {
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

	coreID, err := Open(configPath, testRuntimeJSON(tmp))
	if err != nil {
		t.Fatal(err)
	}
	defer Close(coreID)

	raw, err := List(coreID, "/quark")
	if err != nil {
		t.Fatal(err)
	}
	var entries []entry
	if err := json.Unmarshal([]byte(raw), &entries); err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name != "docs" || !entries[0].IsDir {
		t.Fatalf("entries = %+v, want docs directory", entries)
	}
	if entries[0].Path != "/quark/docs" {
		t.Fatalf("entry path = %q, want /quark/docs", entries[0].Path)
	}

	handleID, err := OpenFile(coreID, "/quark/docs/file.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer CloseFile(handleID)
	data, err := ReadAt(handleID, 6, 6)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "mobile" {
		t.Fatalf("ReadAt = %q, want mobile", string(data))
	}
	buf := []byte("......")
	n, err := ReadAtInto(handleID, 6, buf)
	if err != nil {
		t.Fatal(err)
	}
	if n != 6 || string(buf) != "mobile" {
		t.Fatalf("ReadAtInto n=%d data=%q, want 6 mobile", n, string(buf))
	}
}

func TestFromDriveEntryIncludesCreateAndUpdateTimes(t *testing.T) {
	createdAt := time.Date(2026, 7, 26, 10, 0, 0, 0, time.UTC)
	updatedAt := time.Date(2026, 7, 27, 10, 0, 0, 0, time.UTC)
	item := fromDriveEntry(drive.Entry{
		Name:      "file.txt",
		ModTime:   updatedAt,
		CreatedAt: createdAt,
		UpdatedAt: updatedAt,
	}, "/file.txt")
	if item.CreatedAt != createdAt.Format(time.RFC3339) || item.UpdatedAt != updatedAt.Format(time.RFC3339) {
		t.Fatalf("entry times = created %q updated %q", item.CreatedAt, item.UpdatedAt)
	}
}

func TestMobileCreateMoveTaskJSON(t *testing.T) {
	tmp := t.TempDir()
	remote := filepath.Join(tmp, "remote")
	if err := os.MkdirAll(remote, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(remote, "old.txt"), []byte("move"), 0o644); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(tmp, "qrypt.toml")
	if err := os.WriteFile(configPath, []byte(`
[[mounts]]
name = "local"
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
	if !opened.OK {
		t.Fatalf("OpenJSON = %+v, want ok", opened)
	}
	defer Close(opened.Data)

	raw := CreateMoveTaskJSON(opened.Data, `{"source_path":"/local/old.txt","dest_path":"/local/new.txt"}`, 0)
	var moved struct {
		OK   bool `json:"ok"`
		Data struct {
			ID     string         `json:"id"`
			Type   string         `json:"type"`
			State  string         `json:"state"`
			Path   string         `json:"path"`
			Detail map[string]any `json:"detail"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(raw), &moved); err != nil {
		t.Fatal(err)
	}
	if !moved.OK || moved.Data.Type != "move_remote" || moved.Data.State != "succeeded" || moved.Data.Path != "/local/old.txt" {
		t.Fatalf("CreateMoveTaskJSON = %s", raw)
	}
	if _, err := os.Stat(filepath.Join(remote, "new.txt")); err != nil {
		t.Fatalf("new file missing after move: %v", err)
	}
	listRaw := ListTasksJSON(opened.Data, `{"types":["move_remote"]}`)
	if !strings.Contains(listRaw, moved.Data.ID) {
		t.Fatalf("ListTasksJSON = %s, want move task %s", listRaw, moved.Data.ID)
	}
}

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
	defer Close(opened.Data)

	var listed struct {
		OK   bool    `json:"ok"`
		Data []entry `json:"data"`
	}
	if err := json.Unmarshal([]byte(ListJSON(opened.Data, "/quark")), &listed); err != nil {
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
	coreID, err := Open(configPath, testRuntimeJSON(tmp))
	if err != nil {
		t.Fatal(err)
	}
	defer Close(coreID)

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

	coreID, err := Open(configPath, testRuntimeJSON(tmp))
	if err != nil {
		t.Fatal(err)
	}
	defer Close(coreID)

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
writeback_dir = "/desktop/writeback"
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
	defer Close(opened.Data)

	var info struct {
		OK   bool `json:"ok"`
		Data struct {
			ID      string `json:"id"`
			Size    int64  `json:"size"`
			ModTime string `json:"mod_time"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(FileInfoJSON(opened.Data, "/quark/file.txt")), &info); err != nil {
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
	raw := ValidateResumeJSON(opened.Data, "/quark/file.txt", info.Data.ID, info.Data.Size, info.Data.ModTime)
	if err := json.Unmarshal([]byte(raw), &check); err != nil {
		t.Fatal(err)
	}
	if !check.OK || !check.Data.OK {
		t.Fatalf("ValidateResumeJSON = %s, want ok resume", raw)
	}
}

func TestMobileStreamingUploadWritesStaging(t *testing.T) {
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

	coreID, err := Open(configPath, testRuntimeJSON(tmp))
	if err != nil {
		t.Fatal(err)
	}
	defer Close(coreID)

	uploadID, err := OpenStreamingUpload(coreID, "/quark/upload.bin", 0)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := WriteStreamingUpload(uploadID, []byte("hello "), 0); err != nil || n != len("hello ") {
		t.Fatalf("WriteStreamingUpload first n=%d err=%v", n, err)
	}
	if n, err := WriteStreamingUpload(uploadID, []byte("streaming upload"), 0); err != nil || n != len("streaming upload") {
		t.Fatalf("WriteStreamingUpload second n=%d err=%v", n, err)
	}
	raw, err := FinishStreamingUpload(uploadID, 0)
	if err != nil {
		t.Fatal(err)
	}
	var result uploadFinishResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatal(err)
	}
	if result.Entry.Path != "/quark/upload.bin" || result.Entry.Size != int64(len("hello streaming upload")) {
		t.Fatalf("FinishStreamingUpload entry = %+v", result.Entry)
	}
	if result.Task.ID == "" || result.Task.Type != "upload_remote" || result.Task.Path != "/quark/upload.bin" {
		t.Fatalf("FinishStreamingUpload task = %+v", result.Task)
	}
	taskRaw := GetTaskJSON(coreID, result.Task.ID)
	var taskCheck struct {
		OK   bool `json:"ok"`
		Data struct {
			ID   string `json:"id"`
			Path string `json:"path"`
			Type string `json:"type"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(taskRaw), &taskCheck); err != nil {
		t.Fatal(err)
	}
	if !taskCheck.OK || taskCheck.Data.ID != result.Task.ID || taskCheck.Data.Path != "/quark/upload.bin" {
		t.Fatalf("GetTaskJSON = %s", taskRaw)
	}
	listRaw := ListTasksJSON(coreID, `{"types":["upload_remote"],"path":"/quark/upload.bin"}`)
	var listCheck struct {
		OK   bool `json:"ok"`
		Data []struct {
			ID   string `json:"id"`
			Path string `json:"path"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(listRaw), &listCheck); err != nil {
		t.Fatal(err)
	}
	if !listCheck.OK || len(listCheck.Data) != 1 || listCheck.Data[0].ID != result.Task.ID {
		t.Fatalf("ListTasksJSON = %s", listRaw)
	}

	handleID, err := OpenFile(coreID, "/quark/upload.bin")
	if err != nil {
		t.Fatal(err)
	}
	defer CloseFile(handleID)
	buf := make([]byte, len("hello streaming upload"))
	n, err := ReadAtInto(handleID, 0, buf)
	if err != nil {
		t.Fatal(err)
	}
	if n != len(buf) || string(buf) != "hello streaming upload" {
		t.Fatalf("ReadAtInto n=%d data=%q", n, string(buf[:n]))
	}
	if _, err := WriteStreamingUpload(uploadID, []byte("again"), 0); err == nil {
		t.Fatal("WriteStreamingUpload after finish succeeded, want error")
	}
}

func TestMobileStreamingUploadCancelRemovesStaging(t *testing.T) {
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

	coreID, err := Open(configPath, testRuntimeJSON(tmp))
	if err != nil {
		t.Fatal(err)
	}
	defer Close(coreID)

	uploadID, err := OpenStreamingUpload(coreID, "/quark/cancel.bin", 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := WriteStreamingUpload(uploadID, []byte("discard"), 0); err != nil {
		t.Fatal(err)
	}
	if err := CancelStreamingUpload(uploadID, 0); err != nil {
		t.Fatal(err)
	}
	if raw := StatJSON(coreID, "/quark/cancel.bin"); !strings.Contains(raw, `"ok":false`) {
		t.Fatalf("StatJSON after cancel = %s, want error", raw)
	}
}

func TestMobileReadAtRepeatedSeek(t *testing.T) {
	tmp := t.TempDir()
	remote := filepath.Join(tmp, "remote")
	if err := os.MkdirAll(remote, 0o755); err != nil {
		t.Fatal(err)
	}
	content := strings.Repeat("0123456789abcdef", 128*1024)
	if err := os.WriteFile(filepath.Join(remote, "large.bin"), []byte(content), 0o644); err != nil {
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

	coreID, err := Open(configPath, testRuntimeJSON(tmp))
	if err != nil {
		t.Fatal(err)
	}
	defer Close(coreID)

	handleID, err := OpenFile(coreID, "/quark/large.bin")
	if err != nil {
		t.Fatal(err)
	}
	defer CloseFile(handleID)

	reads := []struct {
		offset int64
		length int
		want   string
	}{
		{offset: 0, length: 16, want: "0123456789abcdef"},
		{offset: 1024 * 1024, length: 8, want: content[1024*1024 : 1024*1024+8]},
		{offset: 17, length: 6, want: content[17:23]},
		{offset: int64(len(content) - 10), length: 32, want: content[len(content)-10:]},
	}
	for _, read := range reads {
		data, err := ReadAt(handleID, read.offset, read.length)
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != read.want {
			t.Fatalf("ReadAt(%d,%d) = %q, want %q", read.offset, read.length, string(data), read.want)
		}
	}
}

func TestMobileVirtualFileReadAtInto(t *testing.T) {
	tmp := t.TempDir()
	remote := filepath.Join(tmp, "remote")
	if err := os.MkdirAll(remote, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(remote, "video.bin"), []byte("virtual video data"), 0o644); err != nil {
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

	coreID, err := Open(configPath, testRuntimeJSON(tmp))
	if err != nil {
		t.Fatal(err)
	}
	defer Close(coreID)

	raw, err := OpenVirtualFile(coreID, "/quark/video.bin", "passthrough", 0)
	if err != nil {
		t.Fatal(err)
	}
	var opened virtualOpenResult
	if err := json.Unmarshal([]byte(raw), &opened); err != nil {
		t.Fatal(err)
	}
	handleID := opened.Handle
	defer CloseVirtualFile(handleID)

	buf := []byte(".....")
	n, err := ReadVirtualFileAtInto(handleID, 8, buf, 0)
	if err != nil {
		t.Fatal(err)
	}
	if n != 5 || string(buf) != "video" {
		t.Fatalf("ReadVirtualFileAtInto n=%d data=%q, want 5 video", n, string(buf))
	}
}

func TestMobileErrorsAreClassified(t *testing.T) {
	if _, err := List("missing", "/"); err == nil || !strings.HasPrefix(err.Error(), "unknown: ") {
		t.Fatalf("List missing core error = %v, want classified unknown error", err)
	}
	raw, err := ClassifyErrorMessage("quark: 401 unauthorized")
	if err != nil {
		t.Fatal(err)
	}
	var info struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal([]byte(raw), &info); err != nil {
		t.Fatal(err)
	}
	if info.Code != "auth_expired" {
		t.Fatalf("code = %q, want auth_expired", info.Code)
	}
}
