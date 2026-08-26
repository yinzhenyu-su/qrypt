package mobile

import (
	"encoding/json"
	"github.com/yinzhenyu/qrypt/pkg/util"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
root_path = `+util.TOMLPath(remote)+`
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
root_path = `+util.TOMLPath(remote)+`

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
root_path = `+util.TOMLPath(remote)+`
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
