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

	coreID, err := openCore(configPath, testRuntimeJSON(tmp))
	if err != nil {
		t.Fatal(err)
	}
	defer closeCore(coreID)

	var listed struct {
		OK   bool    `json:"ok"`
		Data []entry `json:"data"`
	}
	if err := json.Unmarshal([]byte(ListJSON(coreID, "/quark", 0)), &listed); err != nil {
		t.Fatal(err)
	}
	if !listed.OK {
		t.Fatalf("ListJSON = %+v, want ok", listed)
	}
	entries := listed.Data
	if len(entries) != 1 || entries[0].Name != "docs" || !entries[0].IsDir {
		t.Fatalf("entries = %+v, want docs directory", entries)
	}
	if entries[0].Path != "/quark/docs" {
		t.Fatalf("entry path = %q, want /quark/docs", entries[0].Path)
	}

	var openedFile struct {
		OK   bool   `json:"ok"`
		Data string `json:"data"`
	}
	if err := json.Unmarshal([]byte(OpenFileJSON(coreID, "/quark/docs/file.txt", "")), &openedFile); err != nil {
		t.Fatal(err)
	}
	if !openedFile.OK || openedFile.Data == "" {
		t.Fatalf("OpenFileJSON = %+v, want file handle", openedFile)
	}
	handleID := openedFile.Data
	defer CloseFileJSON(handleID)
	data := make([]byte, 6)
	n, err := ReadAtInto(handleID, 6, data, 0)
	if err != nil {
		t.Fatal(err)
	}
	if n != 6 || string(data) != "mobile" {
		t.Fatalf("ReadAtInto n=%d data=%q, want 6 mobile", n, string(data))
	}
	buf := []byte("......")
	n, err = ReadAtInto(handleID, 6, buf, 0)
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

func TestMobileCreateTaskJSONMoveRemote(t *testing.T) {
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
	defer closeCore(opened.Data)

	raw := CreateTaskJSON(opened.Data, `{"type":"move_remote","items":[{"source_path":"/local/old.txt","dest_path":"/local/new.txt"}]}`, 0)
	var created struct {
		OK   bool `json:"ok"`
		Data struct {
			ID    string `json:"id"`
			Type  string `json:"type"`
			State string `json:"state"`
			Path  string `json:"path"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(raw), &created); err != nil {
		t.Fatal(err)
	}
	if !created.OK || created.Data.Type != "move_remote" || created.Data.Path != "/local/old.txt" {
		t.Fatalf("CreateTaskJSON = %s", raw)
	}
	waitMobileTaskState(t, opened.Data, created.Data.ID, "succeeded")
	if _, err := os.Stat(filepath.Join(remote, "new.txt")); err != nil {
		t.Fatalf("new file missing after move: %v", err)
	}
}

func TestMobileCreateTaskJSONDeleteBatchPartialFailed(t *testing.T) {
	tmp := t.TempDir()
	remote := filepath.Join(tmp, "remote")
	if err := os.MkdirAll(remote, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(remote, "ok.txt"), []byte("delete"), 0o644); err != nil {
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
	defer closeCore(opened.Data)

	raw := CreateTaskJSON(opened.Data, `{"type":"delete_batch","items":[{"path":"/local/ok.txt"},{"path":"/local/missing.txt"}]}`, 0)
	var created struct {
		OK   bool `json:"ok"`
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(raw), &created); err != nil {
		t.Fatal(err)
	}
	if !created.OK || created.Data.ID == "" {
		t.Fatalf("CreateTaskJSON = %s", raw)
	}
	got := waitMobileTaskState(t, opened.Data, created.Data.ID, "partial_failed")
	if got.Progress.ItemsDone != 2 || got.Progress.ItemsFailed != 1 || got.Error == nil {
		t.Fatalf("task = %+v, want partial delete progress", got)
	}
	if len(got.Result.Items) != 2 || got.Result.Items[1].Path != "/local/missing.txt" || got.Result.Items[1].State != "failed" || got.Result.Items[1].Error == nil {
		t.Fatalf("result items = %+v, want missing path failure", got.Result.Items)
	}
	removedRaw := DismissTaskJSON(opened.Data, created.Data.ID, 0)
	var removed struct {
		OK bool `json:"ok"`
	}
	if err := json.Unmarshal([]byte(removedRaw), &removed); err != nil {
		t.Fatal(err)
	}
	if !removed.OK {
		t.Fatalf("DismissTaskJSON = %s", removedRaw)
	}
	getRaw := GetTaskJSON(opened.Data, created.Data.ID)
	var missing struct {
		OK bool `json:"ok"`
	}
	if err := json.Unmarshal([]byte(getRaw), &missing); err != nil {
		t.Fatal(err)
	}
	if missing.OK {
		t.Fatalf("GetTaskJSON after remove = %s, want error", getRaw)
	}
}

func TestMobileCreateTaskJSONDeleteBatchRecursiveDirectory(t *testing.T) {
	tmp := t.TempDir()
	remote := filepath.Join(tmp, "remote")
	if err := os.MkdirAll(filepath.Join(remote, "dir", "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(remote, "dir", "nested", "file.txt"), []byte("delete recursive"), 0o644); err != nil {
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
	defer closeCore(opened.Data)

	raw := CreateTaskJSON(opened.Data, `{"type":"delete_batch","items":[{"path":"/local/dir"}],"options":{"recursive":true}}`, 0)
	var created struct {
		OK   bool `json:"ok"`
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(raw), &created); err != nil {
		t.Fatal(err)
	}
	if !created.OK || created.Data.ID == "" {
		t.Fatalf("CreateTaskJSON = %s", raw)
	}
	got := waitMobileTaskState(t, opened.Data, created.Data.ID, "succeeded")
	if got.Type != "delete_batch" || got.Progress.ItemsDone != 3 || got.Progress.ItemsTotal != 3 {
		t.Fatalf("task = %+v, want recursive delete progress", got)
	}
	statRaw := StatJSON(opened.Data, "/local/dir", 0)
	var stat struct {
		OK bool `json:"ok"`
	}
	if err := json.Unmarshal([]byte(statRaw), &stat); err != nil {
		t.Fatal(err)
	}
	if stat.OK {
		t.Fatalf("StatJSON after recursive delete = %s, want error", statRaw)
	}
	clearRaw := DismissFinishedTasksJSON(opened.Data, `{"types":["delete_batch"]}`, 0)
	var cleared struct {
		OK   bool `json:"ok"`
		Data struct {
			Removed int `json:"removed"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(clearRaw), &cleared); err != nil {
		t.Fatal(err)
	}
	if !cleared.OK || cleared.Data.Removed != 1 {
		t.Fatalf("DismissFinishedTasksJSON = %s", clearRaw)
	}
	getRaw := GetTaskJSON(opened.Data, created.Data.ID)
	var missing struct {
		OK bool `json:"ok"`
	}
	if err := json.Unmarshal([]byte(getRaw), &missing); err != nil {
		t.Fatal(err)
	}
	if missing.OK {
		t.Fatalf("GetTaskJSON after clear = %s, want error", getRaw)
	}
}

func TestMobileCreateTaskJSONUploadBatch(t *testing.T) {
	tmp := t.TempDir()
	remote := filepath.Join(tmp, "remote")
	if err := os.MkdirAll(remote, 0o755); err != nil {
		t.Fatal(err)
	}
	local := filepath.Join(tmp, "local.txt")
	if err := os.WriteFile(local, []byte("mobile upload batch"), 0o644); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(tmp, "qrypt.toml")
	if err := os.WriteFile(configPath, []byte(`
[[mounts]]
name = "local"
type = "localfs"
[mounts.params]
root_path = "`+remote+`"

[upload]
upload_delay = "10ms"
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
	defer closeCore(opened.Data)

	raw := CreateTaskJSON(opened.Data, `{"type":"upload_batch","items":[{"source_path":`+fmt.Sprintf("%q", local)+`,"dest_path":"/local/uploaded.txt"}]}`, 0)
	var created struct {
		OK   bool `json:"ok"`
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(raw), &created); err != nil {
		t.Fatal(err)
	}
	if !created.OK || created.Data.ID == "" {
		t.Fatalf("CreateTaskJSON = %s", raw)
	}
	item := waitMobileTaskState(t, opened.Data, created.Data.ID, "succeeded")
	if item.Type != "upload_batch" || item.Progress.ItemsDone != 1 {
		t.Fatalf("task = %+v, want succeeded upload_batch", item)
	}
	listRaw := ListTasksJSON(opened.Data, `{}`)
	var listed struct {
		OK   bool `json:"ok"`
		Data []struct {
			ID   string `json:"id"`
			Type string `json:"type"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(listRaw), &listed); err != nil {
		t.Fatal(err)
	}
	if !listed.OK {
		t.Fatalf("ListTasksJSON default = %s", listRaw)
	}
	for _, listedTask := range listed.Data {
		if listedTask.ID == created.Data.ID || listedTask.Type == "upload_batch" || listedTask.Type == "upload_remote" {
			t.Fatalf("ListTasksJSON default = %+v, want upload internals hidden", listed.Data)
		}
	}
	explicitRaw := ListTasksJSON(opened.Data, `{"types":["upload_batch"]}`)
	var explicit struct {
		OK   bool `json:"ok"`
		Data []struct {
			ID   string `json:"id"`
			Type string `json:"type"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(explicitRaw), &explicit); err != nil {
		t.Fatal(err)
	}
	if !explicit.OK || len(explicit.Data) != 1 || explicit.Data[0].ID != created.Data.ID {
		t.Fatalf("ListTasksJSON explicit = %s, want upload_batch visible by explicit filter", explicitRaw)
	}
	if data, err := os.ReadFile(filepath.Join(remote, "uploaded.txt")); err != nil || string(data) != "mobile upload batch" {
		t.Fatalf("uploaded data = %q err=%v", data, err)
	}
}

func TestMobileUploadTaskEventsIncludeItemProgress(t *testing.T) {
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

[upload]
upload_delay = "10ms"
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
	defer closeCore(opened.Data)

	createRaw := CreateUploadTaskJSON(opened.Data, `{"items":[{"item_id":"stream","dest_path":"/quark/stream.txt","size":11}]}`, 0)
	var created struct {
		OK   bool `json:"ok"`
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(createRaw), &created); err != nil {
		t.Fatal(err)
	}
	if !created.OK || created.Data.ID == "" {
		t.Fatalf("CreateUploadTaskJSON = %s", createRaw)
	}

	openEventsRaw := OpenTaskEventsJSON(opened.Data, fmt.Sprintf(`{"id":%q}`, created.Data.ID), 0)
	var openedEvents struct {
		OK   bool   `json:"ok"`
		Data string `json:"data"`
	}
	if err := json.Unmarshal([]byte(openEventsRaw), &openedEvents); err != nil {
		t.Fatal(err)
	}
	if !openedEvents.OK || openedEvents.Data == "" {
		t.Fatalf("OpenTaskEventsJSON = %s", openEventsRaw)
	}
	defer CloseTaskEventsJSON(openedEvents.Data)
	_ = ReadTaskEventsJSON(openedEvents.Data, 0)

	openItemRaw := OpenUploadItemJSON(opened.Data, created.Data.ID, "stream", 0)
	var openedItem struct {
		OK   bool   `json:"ok"`
		Data string `json:"data"`
	}
	if err := json.Unmarshal([]byte(openItemRaw), &openedItem); err != nil {
		t.Fatal(err)
	}
	if !openedItem.OK || openedItem.Data == "" {
		t.Fatalf("OpenUploadItemJSON = %s", openItemRaw)
	}
	if n, err := WriteUploadItem(openedItem.Data, []byte("hello world"), 0); err != nil || n != len("hello world") {
		t.Fatalf("WriteUploadItem n=%d err=%v", n, err)
	}
	if !readUploadEventItem(t, openedEvents.Data, created.Data.ID, func(item mobileTaskItem) bool {
		return item.ItemID == "stream" &&
			item.StagingBytesDone == int64(len("hello world")) &&
			item.StagingBytesTotal == 11
	}) {
		t.Fatal("upload event did not include staging progress")
	}
	commitRaw := CommitUploadItemJSON(openedItem.Data, 0)
	if !strings.Contains(commitRaw, `"ok":true`) {
		t.Fatalf("CommitUploadItemJSON = %s", commitRaw)
	}
	if !readUploadEventItem(t, openedEvents.Data, created.Data.ID, func(item mobileTaskItem) bool {
		return item.ItemID == "stream" &&
			item.State == "succeeded" &&
			item.CloudBytesDone == int64(len("hello world")) &&
			item.CloudBytesTotal == int64(len("hello world"))
	}) {
		t.Fatal("upload event did not include cloud progress")
	}
	task := waitMobileTaskState(t, opened.Data, created.Data.ID, "succeeded")
	if task.Progress.CloudBytesDone != int64(len("hello world")) ||
		task.Progress.CloudBytesTotal != int64(len("hello world")) {
		t.Fatalf("task progress = %+v, want cloud progress", task.Progress)
	}
}

func TestMobileCreateTaskJSONDownload(t *testing.T) {
	tmp := t.TempDir()
	remote := filepath.Join(tmp, "remote")
	if err := os.MkdirAll(remote, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(remote, "remote.txt"), []byte("mobile download"), 0o644); err != nil {
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
	defer closeCore(opened.Data)

	dest := filepath.Join(tmp, "downloads", "remote.txt")
	raw := CreateTaskJSON(opened.Data, `{"type":"download","items":[{"source_path":"/local/remote.txt","dest_path":`+fmt.Sprintf("%q", dest)+`}]}`, 0)
	var created struct {
		OK   bool `json:"ok"`
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(raw), &created); err != nil {
		t.Fatal(err)
	}
	if !created.OK || created.Data.ID == "" {
		t.Fatalf("CreateTaskJSON = %s", raw)
	}
	item := waitMobileTaskState(t, opened.Data, created.Data.ID, "succeeded")
	if item.Type != "download" || item.Progress.ItemsDone != 1 || item.Progress.OutputBytesDone != int64(len("mobile download")) {
		t.Fatalf("task = %+v, want succeeded download", item)
	}
	if data, err := os.ReadFile(dest); err != nil || string(data) != "mobile download" {
		t.Fatalf("download data = %q err=%v", data, err)
	}
}

func TestMobileCreateTaskJSONDownloadRecursiveDirectory(t *testing.T) {
	tmp := t.TempDir()
	remote := filepath.Join(tmp, "remote")
	if err := os.MkdirAll(filepath.Join(remote, "dir", "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(remote, "dir", "nested", "file.txt"), []byte("recursive mobile download"), 0o644); err != nil {
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
	defer closeCore(opened.Data)

	dest := filepath.Join(tmp, "downloads", "dir")
	raw := CreateTaskJSON(opened.Data, `{"type":"download","items":[{"source_path":"/local/dir","dest_path":`+fmt.Sprintf("%q", dest)+`}],"options":{"recursive":true}}`, 0)
	var created struct {
		OK   bool `json:"ok"`
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(raw), &created); err != nil {
		t.Fatal(err)
	}
	if !created.OK || created.Data.ID == "" {
		t.Fatalf("CreateTaskJSON = %s", raw)
	}
	item := waitMobileTaskState(t, opened.Data, created.Data.ID, "succeeded")
	if item.Type != "download" || item.Progress.ItemsDone != 1 || item.Progress.ItemsTotal != 1 {
		t.Fatalf("task = %+v, want succeeded recursive download", item)
	}
	if data, err := os.ReadFile(filepath.Join(dest, "nested", "file.txt")); err != nil || string(data) != "recursive mobile download" {
		t.Fatalf("download data = %q err=%v", data, err)
	}
}

func TestMobileDownloadStreamBatch(t *testing.T) {
	tmp := t.TempDir()
	remote := filepath.Join(tmp, "remote")
	if err := os.MkdirAll(remote, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(remote, "stream.txt"), []byte("mobile stream"), 0o644); err != nil {
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
	defer closeCore(opened.Data)

	raw := CreateDownloadTaskJSON(opened.Data, `{"items":[{"item_id":"one","source_path":"/local/stream.txt"}]}`, 0)
	var created struct {
		OK   bool `json:"ok"`
		Data struct {
			ID     string `json:"id"`
			Result struct {
				Items []struct {
					ItemID           string `json:"item_id"`
					OutputBytesTotal int64  `json:"output_bytes_total"`
				} `json:"items"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(raw), &created); err != nil {
		t.Fatal(err)
	}
	if !created.OK || created.Data.ID == "" || len(created.Data.Result.Items) != 1 || created.Data.Result.Items[0].ItemID != "one" {
		t.Fatalf("CreateTaskJSON = %s", raw)
	}
	openRaw := OpenDownloadItemJSON(opened.Data, created.Data.ID, "one", 0)
	var openedItem struct {
		OK   bool   `json:"ok"`
		Data string `json:"data"`
	}
	if err := json.Unmarshal([]byte(openRaw), &openedItem); err != nil {
		t.Fatal(err)
	}
	if !openedItem.OK || openedItem.Data == "" {
		t.Fatalf("OpenDownloadItemJSON = %s", openRaw)
	}
	buf := make([]byte, 64)
	n, err := ReadDownloadItemInto(openedItem.Data, buf, 0)
	if err != nil {
		t.Fatal(err)
	}
	if string(buf[:n]) != "mobile stream" {
		t.Fatalf("stream data = %q", string(buf[:n]))
	}
	ackRaw := AckDownloadItemJSON(openedItem.Data, int64(n))
	var acked struct {
		OK bool `json:"ok"`
	}
	if err := json.Unmarshal([]byte(ackRaw), &acked); err != nil {
		t.Fatal(err)
	}
	if !acked.OK {
		t.Fatalf("AckDownloadItemJSON = %s", ackRaw)
	}
	commitRaw := CommitDownloadItemJSON(openedItem.Data, 0)
	var committed struct {
		OK bool `json:"ok"`
	}
	if err := json.Unmarshal([]byte(commitRaw), &committed); err != nil {
		t.Fatal(err)
	}
	if !committed.OK {
		t.Fatalf("CommitDownloadItemJSON = %s", commitRaw)
	}
	item := waitMobileTaskState(t, opened.Data, created.Data.ID, "succeeded")
	if item.Type != "download_stream_batch" ||
		item.Progress.OutputBytesDone != int64(len("mobile stream")) {
		t.Fatalf("task = %+v, want stream download success", item)
	}
}

func TestMobileUploadStreamBatch(t *testing.T) {
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

[upload]
upload_delay = "10ms"
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
	defer closeCore(opened.Data)

	raw := CreateUploadTaskJSON(opened.Data, `{"items":[{"item_id":"one","dest_path":"/quark/upload.txt","size":13}]}`, 0)
	var created struct {
		OK   bool `json:"ok"`
		Data struct {
			ID     string `json:"id"`
			Result struct {
				Items []struct {
					ItemID string `json:"item_id"`
				} `json:"items"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(raw), &created); err != nil {
		t.Fatal(err)
	}
	if !created.OK || created.Data.ID == "" || len(created.Data.Result.Items) != 1 || created.Data.Result.Items[0].ItemID != "one" {
		t.Fatalf("CreateTaskJSON = %s", raw)
	}
	listItemsRaw := ListTaskItemsJSON(opened.Data, created.Data.ID, `{"states":["waiting_input"]}`)
	var listedItems struct {
		OK   bool `json:"ok"`
		Data []struct {
			ItemID       string `json:"item_id"`
			State        string `json:"state"`
			Capabilities struct {
				OpenInput  bool `json:"open_input"`
				Cancelable bool `json:"cancelable"`
			} `json:"capabilities"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(listItemsRaw), &listedItems); err != nil {
		t.Fatal(err)
	}
	if !listedItems.OK || len(listedItems.Data) != 1 || listedItems.Data[0].ItemID != "one" {
		t.Fatalf("ListTaskItemsJSON = %s", listItemsRaw)
	}
	if !listedItems.Data[0].Capabilities.OpenInput || !listedItems.Data[0].Capabilities.Cancelable {
		t.Fatalf("item capabilities = %+v, want open input and cancelable", listedItems.Data[0].Capabilities)
	}
	openRaw := OpenUploadItemJSON(opened.Data, created.Data.ID, "one", 0)
	var openedItem struct {
		OK   bool   `json:"ok"`
		Data string `json:"data"`
	}
	if err := json.Unmarshal([]byte(openRaw), &openedItem); err != nil {
		t.Fatal(err)
	}
	if !openedItem.OK || openedItem.Data == "" {
		t.Fatalf("OpenUploadItemJSON = %s", openRaw)
	}
	if n, err := WriteUploadItem(openedItem.Data, []byte("mobile "), 0); err != nil || n != len("mobile ") {
		t.Fatalf("WriteUploadItem first n=%d err=%v", n, err)
	}
	if n, err := WriteUploadItem(openedItem.Data, []byte("stream"), 0); err != nil || n != len("stream") {
		t.Fatalf("WriteUploadItem second n=%d err=%v", n, err)
	}
	commitRaw := CommitUploadItemJSON(openedItem.Data, 0)
	var committed struct {
		OK bool `json:"ok"`
	}
	if err := json.Unmarshal([]byte(commitRaw), &committed); err != nil {
		t.Fatal(err)
	}
	if !committed.OK {
		t.Fatalf("CommitUploadItemJSON = %s", commitRaw)
	}
	item := waitMobileTaskState(t, opened.Data, created.Data.ID, "succeeded")
	itemRaw := GetTaskItemJSON(opened.Data, created.Data.ID, "one")
	var gotItem struct {
		OK   bool `json:"ok"`
		Data struct {
			ItemID           string `json:"item_id"`
			State            string `json:"state"`
			StagingBytesDone int64  `json:"staging_bytes_done"`
			ResumeOffset     int64  `json:"resume_offset"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(itemRaw), &gotItem); err != nil {
		t.Fatal(err)
	}
	if !gotItem.OK ||
		gotItem.Data.ItemID != "one" ||
		gotItem.Data.State != "succeeded" ||
		gotItem.Data.StagingBytesDone != int64(len("mobile stream")) ||
		gotItem.Data.ResumeOffset != int64(len("mobile stream")) {
		t.Fatalf("GetTaskItemJSON = %s", itemRaw)
	}
	if item.Type != "upload_stream_batch" ||
		item.Progress.StagingBytesDone != int64(len("mobile stream")) {
		t.Fatalf("task = %+v, want stream upload success", item)
	}
	internalRaw := ListTasksJSON(opened.Data, `{"types":["upload_remote"],"path":"/quark/upload.txt"}`)
	var internal struct {
		OK   bool `json:"ok"`
		Data []struct {
			ID   string `json:"id"`
			Type string `json:"type"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(internalRaw), &internal); err != nil {
		t.Fatal(err)
	}
	if !internal.OK || len(internal.Data) != 0 {
		t.Fatalf("ListTasksJSON internal upload_remote = %s, want dismissed after stream upload", internalRaw)
	}
	if data, err := os.ReadFile(filepath.Join(remote, "upload.txt")); err != nil || string(data) != "mobile stream" {
		t.Fatalf("remote data = %q err=%v", data, err)
	}
}

func TestMobileCancelUploadStreamItem(t *testing.T) {
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
	if !opened.OK {
		t.Fatalf("OpenJSON = %+v, want ok", opened)
	}
	defer closeCore(opened.Data)

	raw := CreateUploadTaskJSON(opened.Data, `{"items":[{"item_id":"one","dest_path":"/quark/cancel.txt","size":7}]}`, 0)
	var created struct {
		OK   bool `json:"ok"`
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(raw), &created); err != nil {
		t.Fatal(err)
	}
	if !created.OK || created.Data.ID == "" {
		t.Fatalf("CreateTaskJSON = %s", raw)
	}
	openRaw := OpenUploadItemJSON(opened.Data, created.Data.ID, "one", 0)
	var openedItem struct {
		OK   bool   `json:"ok"`
		Data string `json:"data"`
	}
	if err := json.Unmarshal([]byte(openRaw), &openedItem); err != nil {
		t.Fatal(err)
	}
	if !openedItem.OK || openedItem.Data == "" {
		t.Fatalf("OpenUploadItemJSON = %s", openRaw)
	}
	if n, err := WriteUploadItem(openedItem.Data, []byte("partial"), 0); err != nil || n != len("partial") {
		t.Fatalf("WriteUploadItem n=%d err=%v", n, err)
	}
	cancelRaw := CancelTaskItemJSON(opened.Data, created.Data.ID, "one", 0)
	var canceled struct {
		OK bool `json:"ok"`
	}
	if err := json.Unmarshal([]byte(cancelRaw), &canceled); err != nil {
		t.Fatal(err)
	}
	if !canceled.OK {
		t.Fatalf("CancelTaskItemJSON = %s", cancelRaw)
	}
	item := waitMobileTaskState(t, opened.Data, created.Data.ID, "failed")
	if item.Result.Items[0].State != "canceled" {
		t.Fatalf("task = %+v, want canceled item", item)
	}
	if raw := StatJSON(opened.Data, "/quark/cancel.txt", 0); !strings.Contains(raw, `"ok":false`) {
		t.Fatalf("StatJSON after item cancel = %s, want error", raw)
	}
}

func TestMobileTaskEvents(t *testing.T) {
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
	if !opened.OK {
		t.Fatalf("OpenJSON = %+v, want ok", opened)
	}
	defer closeCore(opened.Data)

	openRaw := OpenTaskEventsJSON(opened.Data, `{"types":["upload_stream_batch"]}`, 0)
	var openedEvents struct {
		OK   bool   `json:"ok"`
		Data string `json:"data"`
	}
	if err := json.Unmarshal([]byte(openRaw), &openedEvents); err != nil {
		t.Fatal(err)
	}
	if !openedEvents.OK || openedEvents.Data == "" {
		t.Fatalf("OpenTaskEventsJSON = %s", openRaw)
	}

	emptyRaw := ReadTaskEventsJSON(openedEvents.Data, 0)
	var empty struct {
		OK   bool `json:"ok"`
		Data []struct {
			Seq uint64 `json:"seq"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(emptyRaw), &empty); err != nil {
		t.Fatal(err)
	}
	if !empty.OK || len(empty.Data) != 0 {
		t.Fatalf("ReadTaskEventsJSON immediate = %s, want empty", emptyRaw)
	}

	createRaw := CreateUploadTaskJSON(opened.Data, `{"items":[{"item_id":"one","dest_path":"/quark/event.txt","size":5}]}`, 0)
	var created struct {
		OK   bool `json:"ok"`
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(createRaw), &created); err != nil {
		t.Fatal(err)
	}
	if !created.OK || created.Data.ID == "" {
		t.Fatalf("CreateTaskJSON = %s", createRaw)
	}
	eventRaw := ReadTaskEventsJSON(openedEvents.Data, 3000)
	var events struct {
		OK   bool `json:"ok"`
		Data []struct {
			Type   string `json:"type"`
			TaskID string `json:"task_id"`
			Task   struct {
				ID   string `json:"id"`
				Type string `json:"type"`
			} `json:"task"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(eventRaw), &events); err != nil {
		t.Fatal(err)
	}
	if !events.OK || len(events.Data) == 0 {
		t.Fatalf("ReadTaskEventsJSON = %s, want events", eventRaw)
	}
	found := false
	for _, event := range events.Data {
		if event.Type == "task_updated" && event.TaskID == created.Data.ID && event.Task.Type == "upload_stream_batch" {
			found = true
		}
	}
	if !found {
		t.Fatalf("events = %+v, want update for created task", events.Data)
	}
	if raw := CloseTaskEventsJSON(openedEvents.Data); !strings.Contains(raw, `"ok":true`) {
		t.Fatalf("CloseTaskEventsJSON = %s, want ok", raw)
	}
}

func waitMobileTaskState(t *testing.T, coreID, taskID, want string) mobileTask {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		raw := GetTaskJSON(coreID, taskID)
		var got struct {
			OK   bool       `json:"ok"`
			Data mobileTask `json:"data"`
		}
		if err := json.Unmarshal([]byte(raw), &got); err != nil {
			t.Fatal(err)
		}
		if !got.OK {
			t.Fatalf("GetTaskJSON = %s", raw)
		}
		if got.Data.State == want {
			return got.Data
		}
		if got.Data.State == "failed" || got.Data.State == "canceled" {
			t.Fatalf("task %s state=%s, want %s", taskID, got.Data.State, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("task %s did not reach state %s", taskID, want)
	return mobileTask{}
}

func readUploadEventItem(t *testing.T, handleID, taskID string, match func(mobileTaskItem) bool) bool {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		raw := ReadTaskEventsJSON(handleID, 500)
		var got struct {
			OK   bool `json:"ok"`
			Data []struct {
				Type   string     `json:"type"`
				TaskID string     `json:"task_id"`
				Task   mobileTask `json:"task"`
			} `json:"data"`
		}
		if err := json.Unmarshal([]byte(raw), &got); err != nil {
			t.Fatal(err)
		}
		if !got.OK {
			t.Fatalf("ReadTaskEventsJSON = %s", raw)
		}
		for _, event := range got.Data {
			if event.Type != "task_updated" || event.TaskID != taskID {
				continue
			}
			for _, item := range event.Task.Result.Items {
				if match(item) {
					return true
				}
			}
		}
	}
	return false
}

type mobileTask struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	State    string `json:"state"`
	Progress struct {
		CloudBytesDone     int64 `json:"cloud_bytes_done"`
		CloudBytesTotal    int64 `json:"cloud_bytes_total"`
		StagingBytesDone   int64 `json:"staging_bytes_done"`
		StagingBytesTotal  int64 `json:"staging_bytes_total"`
		OutputBytesDone    int64 `json:"output_bytes_done"`
		OutputBytesTotal   int64 `json:"output_bytes_total"`
		TransferBytesDone  int64 `json:"transfer_bytes_done"`
		TransferBytesTotal int64 `json:"transfer_bytes_total"`
		ItemsDone          int64 `json:"items_done"`
		ItemsTotal         int64 `json:"items_total"`
		ItemsFailed        int64 `json:"items_failed"`
	} `json:"progress"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
	Result struct {
		Items []mobileTaskItem `json:"items"`
	} `json:"result"`
}

type mobileTaskItem struct {
	Path              string `json:"path"`
	ItemID            string `json:"item_id"`
	State             string `json:"state"`
	Phase             string `json:"phase"`
	StagingBytesDone  int64  `json:"staging_bytes_done"`
	StagingBytesTotal int64  `json:"staging_bytes_total"`
	CloudBytesDone    int64  `json:"cloud_bytes_done"`
	CloudBytesTotal   int64  `json:"cloud_bytes_total"`
	OutputBytesDone   int64  `json:"output_bytes_done"`
	Error             *struct {
		Message string `json:"message"`
	} `json:"error"`
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

	coreID, err := openCore(configPath, testRuntimeJSON(tmp))
	if err != nil {
		t.Fatal(err)
	}
	defer closeCore(coreID)

	var openedFile struct {
		OK   bool   `json:"ok"`
		Data string `json:"data"`
	}
	if err := json.Unmarshal([]byte(OpenFileJSON(coreID, "/quark/large.bin", `{"priority":"high","deadline_ms":1000}`)), &openedFile); err != nil {
		t.Fatal(err)
	}
	if !openedFile.OK || openedFile.Data == "" {
		t.Fatalf("OpenFileJSON = %+v, want file handle", openedFile)
	}
	handleID := openedFile.Data
	defer CloseFileJSON(handleID)

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
		data := make([]byte, read.length)
		n, err := ReadAtInto(handleID, read.offset, data, 0)
		if err != nil {
			t.Fatal(err)
		}
		if string(data[:n]) != read.want {
			t.Fatalf("ReadAtInto(%d,%d) = %q, want %q", read.offset, read.length, string(data[:n]), read.want)
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

	coreID, err := openCore(configPath, testRuntimeJSON(tmp))
	if err != nil {
		t.Fatal(err)
	}
	defer closeCore(coreID)

	raw := OpenVirtualFileJSON(coreID, "/quark/video.bin", "passthrough", 0)
	var opened struct {
		OK   bool              `json:"ok"`
		Data virtualOpenResult `json:"data"`
	}
	if err := json.Unmarshal([]byte(raw), &opened); err != nil {
		t.Fatal(err)
	}
	if !opened.OK || opened.Data.Handle == "" {
		t.Fatalf("OpenVirtualFileJSON = %s, want handle", raw)
	}
	handleID := opened.Data.Handle
	defer CloseVirtualFileJSON(handleID)

	buf := []byte(".....")
	n, err := ReadVirtualFileAtInto(handleID, 8, buf, 0)
	if err != nil {
		t.Fatal(err)
	}
	if n != 5 || string(buf) != "video" {
		t.Fatalf("ReadVirtualFileAtInto n=%d data=%q, want 5 video", n, string(buf))
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

func TestMobileCreateLocalUploadTaskJSON(t *testing.T) {
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

[upload]
upload_delay = "10ms"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	coreID, err := openCore(configPath, testRuntimeJSON(tmp))
	if err != nil {
		t.Fatal(err)
	}
	defer closeCore(coreID)

	localPath := filepath.Join(tmp, "local.txt")
	if err := os.WriteFile(localPath, []byte("local upload"), 0o644); err != nil {
		t.Fatal(err)
	}
	raw := CreateLocalUploadTaskJSON(coreID, `{"wait_stable":true,"stability":{"quiet_ms":5,"poll_ms":1},"items":[{"local_path":`+fmt.Sprintf("%q", localPath)+`,"dest_path":"/quark/uploaded.txt"}]}`, 1000)
	var created struct {
		OK   bool       `json:"ok"`
		Data mobileTask `json:"data"`
	}
	if err := json.Unmarshal([]byte(raw), &created); err != nil {
		t.Fatal(err)
	}
	if !created.OK || created.Data.ID == "" {
		t.Fatalf("CreateLocalUploadTaskJSON = %s, want task", raw)
	}
	item := waitMobileTaskState(t, coreID, created.Data.ID, "succeeded")
	if item.State != "succeeded" {
		t.Fatalf("task = %+v, want upload success", item)
	}
	if data, err := os.ReadFile(filepath.Join(remote, "uploaded.txt")); err != nil || string(data) != "local upload" {
		t.Fatalf("remote upload data = %q err=%v", data, err)
	}
}

func TestMobileErrorsAreClassified(t *testing.T) {
	raw := ListJSON("missing", "/", 0)
	if !strings.Contains(raw, `"ok":false`) || !strings.Contains(raw, `"code":"unknown"`) {
		t.Fatalf("ListJSON missing core error = %s, want classified unknown error", raw)
	}
	raw = ClassifyErrorMessageJSON("quark: 401 unauthorized")
	var info struct {
		OK   bool `json:"ok"`
		Data struct {
			Code string `json:"code"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(raw), &info); err != nil {
		t.Fatal(err)
	}
	if !info.OK || info.Data.Code != "auth_expired" {
		t.Fatalf("ClassifyErrorMessageJSON = %s, want auth_expired", raw)
	}
}
