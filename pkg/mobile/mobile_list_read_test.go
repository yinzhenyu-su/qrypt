package mobile

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

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
root_path = `+tomlPath(remote)+`
`), 0o644); err != nil {
		t.Fatal(err)
	}

	coreID, err := openCore(configPath, testRuntimeJSON(tmp))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = closeCore(coreID) }()

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
root_path = `+tomlPath(remote)+`
`), 0o644); err != nil {
		t.Fatal(err)
	}

	coreID, err := openCore(configPath, testRuntimeJSON(tmp))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = closeCore(coreID) }()

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
root_path = `+tomlPath(remote)+`
`), 0o644); err != nil {
		t.Fatal(err)
	}

	coreID, err := openCore(configPath, testRuntimeJSON(tmp))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = closeCore(coreID) }()

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

func TestMobileListPageJSON(t *testing.T) {
	tmp := t.TempDir()
	remote := filepath.Join(tmp, "remote")
	if err := os.MkdirAll(remote, 0o755); err != nil {
		t.Fatal(err)
	}
	// Create files out of alphabetical order to prove sorting.
	names := []string{"zeta.txt", "alpha.txt", "mid.txt", "beta.txt", "gamma.txt"}
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(remote, name), []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	configPath := filepath.Join(tmp, "qrypt.toml")
	if err := os.WriteFile(configPath, []byte(`
[[mounts]]
name = "quark"
type = "localfs"
[mounts.params]
root_path = `+tomlPath(remote)+`
`), 0o644); err != nil {
		t.Fatal(err)
	}
	coreID, err := openCore(configPath, testRuntimeJSON(tmp))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = closeCore(coreID) }()

	var page1 struct {
		OK   bool `json:"ok"`
		Data struct {
			Entries    []entry `json:"entries"`
			NextCursor string  `json:"next_cursor"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(ListPageJSON(coreID, "/quark", "", 2, 0)), &page1); err != nil {
		t.Fatal(err)
	}
	if !page1.OK {
		t.Fatalf("ListPageJSON first page = %+v", page1)
	}
	if len(page1.Data.Entries) != 2 || page1.Data.Entries[0].Name != "alpha.txt" || page1.Data.Entries[1].Name != "beta.txt" {
		t.Fatalf("page1 entries = %+v, want sorted alpha, beta", page1.Data.Entries)
	}
	if page1.Data.NextCursor == "" {
		t.Fatalf("page1 next_cursor empty, want opaque cursor")
	}

	var page2 struct {
		OK   bool `json:"ok"`
		Data struct {
			Entries    []entry `json:"entries"`
			NextCursor string  `json:"next_cursor"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(ListPageJSON(coreID, "/quark", page1.Data.NextCursor, 2, 0)), &page2); err != nil {
		t.Fatal(err)
	}
	if !page2.OK {
		t.Fatalf("ListPageJSON second page = %+v", page2)
	}
	if len(page2.Data.Entries) != 2 || page2.Data.Entries[0].Name != "gamma.txt" || page2.Data.Entries[1].Name != "mid.txt" {
		t.Fatalf("page2 entries = %+v, want sorted gamma, mid", page2.Data.Entries)
	}
	if page2.Data.NextCursor == "" {
		t.Fatalf("page2 next_cursor empty, want opaque cursor")
	}

	var page3 struct {
		OK   bool `json:"ok"`
		Data struct {
			Entries    []entry `json:"entries"`
			NextCursor string  `json:"next_cursor"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(ListPageJSON(coreID, "/quark", page2.Data.NextCursor, 2, 0)), &page3); err != nil {
		t.Fatal(err)
	}
	if !page3.OK || len(page3.Data.Entries) != 1 || page3.Data.Entries[0].Name != "zeta.txt" || page3.Data.NextCursor != "" {
		t.Fatalf("page3 = %+v, want only zeta and no cursor", page3.Data)
	}
}

func TestMobileCancelFileReadKeepsHandleUsable(t *testing.T) {
	tmp := t.TempDir()
	remote := filepath.Join(tmp, "remote")
	if err := os.MkdirAll(remote, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(remote, "file.txt"), []byte("cancel me"), 0o644); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(tmp, "qrypt.toml")
	if err := os.WriteFile(configPath, []byte(`
[[mounts]]
name = "quark"
type = "localfs"
[mounts.params]
root_path = `+tomlPath(remote)+`
`), 0o644); err != nil {
		t.Fatal(err)
	}
	coreID, err := openCore(configPath, testRuntimeJSON(tmp))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = closeCore(coreID) }()

	var opened struct {
		OK   bool   `json:"ok"`
		Data string `json:"data"`
	}
	if err := json.Unmarshal([]byte(OpenFileJSON(coreID, "/quark/file.txt", "")), &opened); err != nil {
		t.Fatal(err)
	}
	if !opened.OK || opened.Data == "" {
		t.Fatalf("OpenFileJSON = %+v, want handle", opened)
	}
	handleID := opened.Data
	defer CloseFileJSON(handleID)

	var cancelled struct {
		OK bool `json:"ok"`
	}
	if err := json.Unmarshal([]byte(CancelFileReadJSON(handleID)), &cancelled); err != nil {
		t.Fatal(err)
	}
	if !cancelled.OK {
		t.Fatalf("CancelFileReadJSON = %+v", cancelled)
	}
	// The handle must remain usable after a cancel.
	buf := make([]byte, 4)
	n, err := ReadAtInto(handleID, 0, buf, 0)
	if err != nil || n != 4 || string(buf) != "canc" {
		t.Fatalf("ReadAtInto after cancel n=%d data=%q err=%v, want usable handle", n, buf, err)
	}
	if raw := CancelFileReadJSON("missing-handle"); !strings.Contains(raw, `"ok":false`) {
		t.Fatalf("CancelFileReadJSON unknown handle = %s, want error", raw)
	}
}
