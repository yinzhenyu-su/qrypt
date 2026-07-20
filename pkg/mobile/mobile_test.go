package mobile

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
root_path = "`+remote+`"
`), 0o644); err != nil {
		t.Fatal(err)
	}

	coreID, err := Open(configPath, filepath.Join(tmp, "work"))
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
	if err := json.Unmarshal([]byte(OpenJSON(configPath, filepath.Join(tmp, "work"))), &opened); err != nil {
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

	coreID, err := Open(configPath, filepath.Join(tmp, "work"))
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
