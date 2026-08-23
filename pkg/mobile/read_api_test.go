package mobile

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func openTestLocalFile(t *testing.T, content string) (coreID, path string) {
	t.Helper()
	tmp := t.TempDir()
	remote := filepath.Join(tmp, "remote")
	if err := os.MkdirAll(remote, 0o755); err != nil {
		t.Fatal(err)
	}
	path = "/local/file.bin"
	if err := os.WriteFile(filepath.Join(remote, "file.bin"), []byte(content), 0o644); err != nil {
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
	var err error
	coreID, err = openCore(configPath, testRuntimeJSON(tmp))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = closeCore(coreID) })
	return coreID, path
}

func mobileHandleID(t *testing.T, raw string) string {
	t.Helper()
	var result struct {
		OK   bool   `json:"ok"`
		Data string `json:"data"`
	}
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatal(err)
	}
	if !result.OK || result.Data == "" {
		t.Fatalf("open result = %s", raw)
	}
	return result.Data
}

func TestMobileBatchReadAtInto(t *testing.T) {
	coreID, path := openTestLocalFile(t, "0123456789abcdef")
	handleID := mobileHandleID(t, OpenFileJSON(coreID, path, ""))
	defer CloseFileJSON(handleID)

	dst := make([]byte, 12)
	raw := ReadAtBatchIntoJSON(handleID, `[0,8]`, dst, 6, 0)
	var result struct {
		OK   bool  `json:"ok"`
		Data []int `json:"data"`
	}
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatal(err)
	}
	if !result.OK || len(result.Data) != 2 || result.Data[0] != 6 || result.Data[1] != 6 || string(dst) != "01234589abcd" {
		t.Fatalf("batch result=%s data=%q", raw, dst)
	}
}

func TestMobileSequentialFileStream(t *testing.T) {
	coreID, path := openTestLocalFile(t, "0123456789abcdef")
	handleID := mobileHandleID(t, OpenFileStreamJSON(coreID, path, ""))
	defer CloseFileJSON(handleID)

	first := make([]byte, 5)
	n, err := ReadFileStreamInto(handleID, first, 1000)
	if err != nil || n != 5 || string(first) != "01234" {
		t.Fatalf("first stream read n=%d data=%q err=%v", n, first, err)
	}
	second := make([]byte, 5)
	n, err = ReadFileStreamInto(handleID, second, 1000)
	if err != nil || n != 5 || string(second) != "56789" {
		t.Fatalf("second stream read n=%d data=%q err=%v", n, second, err)
	}
	if n, err := ReadAtInto(handleID, 0, make([]byte, 1), 0); err == nil || n != 0 {
		t.Fatalf("random read on stream n=%d err=%v, want rejection", n, err)
	}
}

func TestReadCancelsSupportsConcurrentAndSingleCancel(t *testing.T) {
	var reads readCancels
	firstCtx, firstDone, concurrent, _, firstID, err := reads.begin(0)
	if err != nil || concurrent || firstID == 0 {
		t.Fatalf("first begin concurrent=%t id=%d err=%v", concurrent, firstID, err)
	}
	secondCtx, secondDone, concurrent, _, secondID, err := reads.begin(0)
	if err != nil || !concurrent || secondID == firstID {
		t.Fatalf("second begin concurrent=%t id=%d err=%v", concurrent, secondID, err)
	}
	if !reads.cancel(firstID) {
		t.Fatal("cancel(first) = false")
	}
	select {
	case <-firstCtx.Done():
	default:
		t.Fatal("first context was not canceled")
	}
	select {
	case <-secondCtx.Done():
		t.Fatal("single cancel canceled sibling request")
	default:
	}
	secondDone()
	firstDone()
	if reads.cancel(secondID) {
		t.Fatal("completed request remained cancelable")
	}
}
