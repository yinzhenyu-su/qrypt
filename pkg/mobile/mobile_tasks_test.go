package mobile

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

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
	defer func() { _ = closeCore(opened.Data) }()

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
	defer func() { _ = closeCore(opened.Data) }()

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
	defer func() { _ = closeCore(opened.Data) }()

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
	defer func() { _ = closeCore(opened.Data) }()

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
		if listedTask.Type == "upload_remote" {
			t.Fatalf("ListTasksJSON default = %+v, want sync upload internals hidden", listed.Data)
		}
	}
	foundUserUpload := false
	for _, listedTask := range listed.Data {
		if listedTask.ID == created.Data.ID {
			foundUserUpload = true
			break
		}
	}
	if !foundUserUpload {
		t.Fatalf("ListTasksJSON default = %+v, want user upload task %s visible", listed.Data, created.Data.ID)
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
	defer func() { _ = closeCore(opened.Data) }()

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
	defer func() { _ = closeCore(opened.Data) }()

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
	defer func() { _ = closeCore(opened.Data) }()

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
	defer func() { _ = closeCore(opened.Data) }()

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
	defer func() { _ = closeCore(coreID) }()

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

func TestMobileCreateDirectUploadTaskJSON(t *testing.T) {
	tmp := t.TempDir()
	remote := filepath.Join(tmp, "remote")
	if err := os.MkdirAll(remote, 0o755); err != nil {
		t.Fatal(err)
	}
	local := filepath.Join(tmp, "direct-source.bin")
	content := []byte("mobile direct upload")
	if err := os.WriteFile(local, content, 0o644); err != nil {
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
	defer func() { _ = closeCore(coreID) }()

	raw := CreateDirectUploadTaskJSON(coreID, `{"items":[{"item_id":"direct","source_path":`+fmt.Sprintf("%q", local)+`,"dest_path":"/quark/direct.bin","size":`+fmt.Sprintf("%d", len(content))+`}]}`, 0)
	var created struct {
		OK   bool       `json:"ok"`
		Data mobileTask `json:"data"`
	}
	if err := json.Unmarshal([]byte(raw), &created); err != nil {
		t.Fatal(err)
	}
	if !created.OK || created.Data.ID == "" {
		t.Fatalf("CreateDirectUploadTaskJSON = %s, want task", raw)
	}
	if created.Data.Type != "upload_stream_direct" {
		t.Fatalf("task type = %q, want upload_stream_direct", created.Data.Type)
	}
	item := waitMobileTaskState(t, coreID, created.Data.ID, "succeeded")
	if item.Progress.StagingBytesDone != 0 || item.Progress.StagingBytesTotal != 0 {
		t.Fatalf("task progress = %+v, want no user-visible staging bytes", item.Progress)
	}
	if len(item.Result.Items) != 1 || item.Result.Items[0].Phase != "direct" {
		t.Fatalf("task result = %+v, want direct result", item.Result.Items)
	}
	if data, err := os.ReadFile(filepath.Join(remote, "direct.bin")); err != nil || string(data) != string(content) {
		t.Fatalf("remote data = %q err=%v, want %q", data, err, content)
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

func TestMobileUploadLocalFileJSONCreatesUserTask(t *testing.T) {
	tmp := t.TempDir()
	remote := filepath.Join(tmp, "remote")
	if err := os.MkdirAll(remote, 0o755); err != nil {
		t.Fatal(err)
	}
	local := filepath.Join(tmp, "local.bin")
	content := []byte("local file upload via stream task")
	if err := os.WriteFile(local, content, 0o644); err != nil {
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
	defer func() { _ = closeCore(coreID) }()

	raw := UploadLocalFileJSON(coreID, local, "/quark/uploaded.bin", 5000)
	var result struct {
		OK   bool `json:"ok"`
		Data struct {
			Entry struct {
				Name string `json:"name"`
			} `json:"entry"`
			Task mobileTask `json:"task"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatal(err)
	}
	if !result.OK || result.Data.Task.ID == "" {
		t.Fatalf("UploadLocalFileJSON = %s, want task", raw)
	}
	if result.Data.Task.Type != "upload_stream_batch" {
		t.Fatalf("task type = %q, want upload_stream_batch", result.Data.Task.Type)
	}
	if result.Data.Entry.Name != "uploaded.bin" {
		t.Fatalf("entry = %+v, want uploaded.bin", result.Data.Entry)
	}
	item := waitMobileTaskState(t, coreID, result.Data.Task.ID, "succeeded")
	if item.Progress.StagingBytesDone != int64(len(content)) {
		t.Fatalf("task progress = %+v, want staging bytes %d", item.Progress, len(content))
	}
	listRaw := ListTasksJSON(coreID, `{}`)
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
	found := false
	for _, task := range listed.Data {
		if task.ID == result.Data.Task.ID {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("ListTasksJSON default = %+v, want UploadLocalFileJSON task visible", listed.Data)
	}
	if data, err := os.ReadFile(filepath.Join(remote, "uploaded.bin")); err != nil || string(data) != string(content) {
		t.Fatalf("remote upload data = %q err=%v", data, err)
	}
}
