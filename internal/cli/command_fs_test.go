package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/crypt"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/util"
)

func TestFSCommandsCreateMoveAndRemoveLocalFS(t *testing.T) {
	tmp := t.TempDir()
	remote := filepath.Join(tmp, "remote")
	if err := os.MkdirAll(remote, 0o755); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(tmp, "qrypt.toml")
	if err := os.WriteFile(configPath, []byte(`
mount_point = `+util.TOMLPath(filepath.Join(tmp, "mnt"))+`
[storage]
read_cache_dir = `+util.TOMLPath(filepath.Join(tmp, "cache", "read"))+`
upload_dir = `+util.TOMLPath(filepath.Join(tmp, "upload"))+`
state_dir = `+util.TOMLPath(filepath.Join(tmp, "state"))+`

[upload]
upload_delay = "10ms"
delete_delay = "10ms"

[[mounts]]
name = "local"
type = "localfs"
[mounts.params]
root_path = `+util.TOMLPath(remote)+`
`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(tmp)

	if err := runMkdir(testCommand(), []string{"/local/dir"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(remote, "dir")); err != nil {
		t.Fatalf("mkdir did not create remote dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(remote, "dir", "file.txt"), []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	downloadPath := filepath.Join(tmp, "download.txt")
	if err := runGet(testCommand(), []string{"/local/dir/file.txt", downloadPath}); err != nil {
		t.Fatal(err)
	}
	downloaded, err := os.ReadFile(downloadPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(downloaded) != "data" {
		t.Fatalf("unexpected downloaded content: %q", downloaded)
	}
	if err := runGet(testCommand(), []string{"/local/dir/file.txt", downloadPath}); err == nil {
		t.Fatal("expected get to reject an existing local destination")
	}
	forceGet := testCommand()
	forceGet.Flags().Bool("force", true, "")
	if err := runGet(forceGet, []string{"/local/dir/file.txt", downloadPath}); err != nil {
		t.Fatalf("forced get: %v", err)
	}
	stdinPut := testCommand()
	stdinPut.SetIn(strings.NewReader("from stdin"))
	if err := runPut(stdinPut, []string{"-", "/local/dir/stdin.txt"}); err != nil {
		t.Fatalf("stdin put: %v", err)
	}
	stdinData, err := os.ReadFile(filepath.Join(remote, "dir", "stdin.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(stdinData) != "from stdin" {
		t.Fatalf("unexpected stdin upload: %q", stdinData)
	}
	if err := runMv(testCommand(), []string{"/local/dir/file.txt", "/local/dir/renamed.txt"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(remote, "dir", "renamed.txt")); err != nil {
		t.Fatalf("mv did not rename remote file: %v", err)
	}
	if err := runRm(testCommand(), []string{"/local/dir/renamed.txt"}); err != nil {
		t.Fatal(err)
	}
	if err := runRm(testCommand(), []string{"/local/dir/stdin.txt"}); err != nil {
		t.Fatal(err)
	}
	waitPathMissing(t, filepath.Join(remote, "dir", "renamed.txt"))
	if err := runRm(testCommand(), []string{"/local/dir"}); err != nil {
		t.Fatal(err)
	}
	waitPathMissing(t, filepath.Join(remote, "dir"))
}

func TestFSListRemoteNamesForEncryptedMount(t *testing.T) {
	tmp := t.TempDir()
	remote := filepath.Join(tmp, "remote")
	if err := os.MkdirAll(remote, 0o755); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(tmp, "qrypt.toml")
	if err := os.WriteFile(configPath, []byte(`
mount_point = `+util.TOMLPath(filepath.Join(tmp, "mnt"))+`
[storage]
read_cache_dir = `+util.TOMLPath(filepath.Join(tmp, "cache", "read"))+`
upload_dir = `+util.TOMLPath(filepath.Join(tmp, "upload"))+`
state_dir = `+util.TOMLPath(filepath.Join(tmp, "state"))+`

[upload]
upload_delay = "10ms"
delete_delay = "10ms"

[[mounts]]
name = "encrypted"
type = "localfs"
[mounts.params]
root_path = `+util.TOMLPath(remote)+`
[mounts.encryption]
password = "encrypted-pass"
salt = "encrypted-salt"
filename_encryption = "standard"
filename_encoding = "base32"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	localPath := filepath.Join(tmp, "secret.txt")
	if err := os.WriteFile(localPath, []byte("secret data"), 0o644); err != nil {
		t.Fatal(err)
	}

	root := NewRootCommand()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"fs", "--config", configPath, "put", "--wait-timeout", "5s", localPath, "/encrypted/secret.txt"})
	if err := root.Execute(); err != nil {
		t.Fatalf("fs put: %v", err)
	}

	rawEntries, err := os.ReadDir(remote)
	if err != nil {
		t.Fatal(err)
	}
	if len(rawEntries) != 1 {
		t.Fatalf("remote entry count = %d, want 1", len(rawEntries))
	}
	rawName := rawEntries[0].Name()
	if rawName == "secret.txt" {
		t.Fatal("expected encrypted backend filename")
	}
	rawData, err := os.ReadFile(filepath.Join(remote, rawName))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(rawData, []byte(crypt.FileMagic)) {
		t.Fatal("expected encrypted backend file data")
	}

	var catOut bytes.Buffer
	root = NewRootCommand()
	root.SetOut(&catOut)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"fs", "--config", configPath, "cat", "/encrypted/secret.txt"})
	if err := root.Execute(); err != nil {
		t.Fatalf("fs cat: %v", err)
	}
	if catOut.String() != "secret data" {
		t.Fatalf("fs cat output = %q, want plaintext", catOut.String())
	}

	var rawCatOut bytes.Buffer
	root = NewRootCommand()
	root.SetOut(&rawCatOut)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"fs", "--config", configPath, "cat", "--raw", "/encrypted/secret.txt"})
	if err := root.Execute(); err != nil {
		t.Fatalf("fs cat --raw: %v", err)
	}
	if !bytes.Equal(rawCatOut.Bytes(), rawData) {
		t.Fatal("fs cat --raw output differs from backend ciphertext")
	}
	if bytes.Contains(rawCatOut.Bytes(), []byte("secret data")) {
		t.Fatal("fs cat --raw leaked plaintext")
	}

	var out bytes.Buffer
	root = NewRootCommand()
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"fs", "--config", configPath, "list", "--json", "--remote-names", "/encrypted"})
	if err := root.Execute(); err != nil {
		t.Fatalf("fs list: %v", err)
	}
	var entries []fsListEntry
	if err := json.Unmarshal(out.Bytes(), &entries); err != nil {
		t.Fatalf("unmarshal list output: %v\n%s", err, out.String())
	}
	if len(entries) != 1 {
		t.Fatalf("list entries = %d, want 1: %s", len(entries), out.String())
	}
	if entries[0].Name != "secret.txt" {
		t.Fatalf("name = %q, want secret.txt", entries[0].Name)
	}
	if entries[0].RemoteName != rawName {
		t.Fatalf("remote_name = %q, want %q", entries[0].RemoteName, rawName)
	}
	if entries[0].RemotePath != "/encrypted/"+rawName {
		t.Fatalf("remote_path = %q, want %q", entries[0].RemotePath, "/encrypted/"+rawName)
	}

	root = NewRootCommand()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"fs", "--config", configPath, "mkdir", "/encrypted/nested-dir"})
	if err := root.Execute(); err != nil {
		t.Fatalf("fs mkdir nested: %v", err)
	}
	rawEntries, err = os.ReadDir(remote)
	if err != nil {
		t.Fatal(err)
	}
	var rawDir string
	for _, entry := range rawEntries {
		if entry.IsDir() {
			rawDir = entry.Name()
			break
		}
	}
	if rawDir == "" {
		t.Fatal("expected encrypted backend directory")
	}

	root = NewRootCommand()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"fs", "--config", configPath, "put", "--wait-timeout", "5s", localPath, "/encrypted/nested-dir/child.txt"})
	if err := root.Execute(); err != nil {
		t.Fatalf("fs put nested: %v", err)
	}
	rawChildren, err := os.ReadDir(filepath.Join(remote, rawDir))
	if err != nil {
		t.Fatal(err)
	}
	if len(rawChildren) != 1 {
		t.Fatalf("raw child count = %d, want 1", len(rawChildren))
	}
	rawChild := rawChildren[0].Name()

	out.Reset()
	root = NewRootCommand()
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"fs", "--config", configPath, "list", "--json", "--remote-names", "/encrypted/nested-dir"})
	if err := root.Execute(); err != nil {
		t.Fatalf("fs list nested: %v", err)
	}
	entries = nil
	if err := json.Unmarshal(out.Bytes(), &entries); err != nil {
		t.Fatalf("unmarshal nested list output: %v\n%s", err, out.String())
	}
	if len(entries) != 1 {
		t.Fatalf("nested list entries = %d, want 1: %s", len(entries), out.String())
	}
	if entries[0].RemoteName != rawChild {
		t.Fatalf("nested remote_name = %q, want %q", entries[0].RemoteName, rawChild)
	}
	if entries[0].RemotePath != "/encrypted/"+rawDir+"/"+rawChild {
		t.Fatalf("nested remote_path = %q, want %q", entries[0].RemotePath, "/encrypted/"+rawDir+"/"+rawChild)
	}
}

func TestFSCatRawUsesMatchedMountEncryption(t *testing.T) {
	tmp := t.TempDir()
	plainRemote := filepath.Join(tmp, "plain")
	encryptedRemote := filepath.Join(tmp, "encrypted")
	if err := os.MkdirAll(plainRemote, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(encryptedRemote, 0o755); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(tmp, "qrypt.toml")
	if err := os.WriteFile(configPath, []byte(`
mount_point = `+util.TOMLPath(filepath.Join(tmp, "mnt"))+`
[storage]
read_cache_dir = `+util.TOMLPath(filepath.Join(tmp, "cache", "read"))+`
upload_dir = `+util.TOMLPath(filepath.Join(tmp, "upload"))+`
state_dir = `+util.TOMLPath(filepath.Join(tmp, "state"))+`

[upload]
upload_delay = "10ms"
delete_delay = "10ms"

[[mounts]]
name = "plain"
type = "localfs"
[mounts.params]
root_path = `+util.TOMLPath(plainRemote)+`

[[mounts]]
name = "encrypted"
type = "localfs"
[mounts.params]
root_path = `+util.TOMLPath(encryptedRemote)+`
[mounts.encryption]
password = "encrypted-pass"
salt = "encrypted-salt"
filename_encryption = "standard"
filename_encoding = "base32"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	plainLocal := filepath.Join(tmp, "plain.txt")
	secretLocal := filepath.Join(tmp, "secret.txt")
	if err := os.WriteFile(plainLocal, []byte("plain data"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secretLocal, []byte("secret data"), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, item := range []struct {
		local  string
		remote string
	}{
		{local: plainLocal, remote: "/plain/same.txt"},
		{local: secretLocal, remote: "/encrypted/same.txt"},
	} {
		root := NewRootCommand()
		root.SetOut(&bytes.Buffer{})
		root.SetErr(&bytes.Buffer{})
		root.SetArgs([]string{"fs", "--config", configPath, "put", "--wait-timeout", "5s", item.local, item.remote})
		if err := root.Execute(); err != nil {
			t.Fatalf("fs put %s: %v", item.remote, err)
		}
	}

	var plainRaw bytes.Buffer
	root := NewRootCommand()
	root.SetOut(&plainRaw)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"fs", "--config", configPath, "cat", "--raw", "/plain/same.txt"})
	if err := root.Execute(); err != nil {
		t.Fatalf("fs cat --raw plain: %v", err)
	}
	if plainRaw.String() != "plain data" {
		t.Fatalf("plain raw output = %q, want plaintext from plain mount", plainRaw.String())
	}

	encryptedEntries, err := os.ReadDir(encryptedRemote)
	if err != nil {
		t.Fatal(err)
	}
	if len(encryptedEntries) != 1 {
		t.Fatalf("encrypted backend entries = %d, want 1", len(encryptedEntries))
	}
	encryptedData, err := os.ReadFile(filepath.Join(encryptedRemote, encryptedEntries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(encryptedData, []byte(crypt.FileMagic)) {
		t.Fatal("encrypted backend did not store ciphertext")
	}
	var encryptedRaw bytes.Buffer
	root = NewRootCommand()
	root.SetOut(&encryptedRaw)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"fs", "--config", configPath, "cat", "--raw", "/encrypted/same.txt"})
	if err := root.Execute(); err != nil {
		t.Fatalf("fs cat --raw encrypted: %v", err)
	}
	if !bytes.Equal(encryptedRaw.Bytes(), encryptedData) {
		t.Fatal("encrypted raw output differs from matched mount backend bytes")
	}
	if bytes.Contains(encryptedRaw.Bytes(), []byte("secret data")) {
		t.Fatal("encrypted raw output used decrypted stream")
	}
}

func TestFSGetDirRecursive(t *testing.T) {
	tmp := t.TempDir()
	remote := filepath.Join(tmp, "remote")
	if err := os.MkdirAll(remote, 0o755); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(tmp, "qrypt.toml")
	if err := os.WriteFile(configPath, []byte(`
mount_point = `+util.TOMLPath(filepath.Join(tmp, "mnt"))+`
[storage]
read_cache_dir = `+util.TOMLPath(filepath.Join(tmp, "cache", "read"))+`
upload_dir = `+util.TOMLPath(filepath.Join(tmp, "upload"))+`
state_dir = `+util.TOMLPath(filepath.Join(tmp, "state"))+`

[upload]
delete_delay = "10ms"

[[mounts]]
name = "local"
type = "localfs"
[mounts.params]
root_path = `+util.TOMLPath(remote)+`
`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(tmp)

	// Create nested remote directory structure.
	if err := runMkdir(testCommand(), []string{"/local/parent"}); err != nil {
		t.Fatal(err)
	}
	if err := runMkdir(testCommand(), []string{"/local/parent/sub"}); err != nil {
		t.Fatal(err)
	}

	// Write files at each level directly to the remote backing store.
	if err := os.WriteFile(filepath.Join(remote, "parent", "a.txt"), []byte("file-a"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(remote, "parent", "sub", "b.txt"), []byte("file-b"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(remote, "parent", "sub", "c.txt"), []byte("file-c"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Download the whole directory recursively.
	// LOCAL is treated as the parent directory; the remote dir name is appended.
	downloadParent := filepath.Join(tmp, "dl")
	if err := runGet(testCommand(), []string{"/local/parent", downloadParent}); err != nil {
		t.Fatalf("get dir: %v", err)
	}

	checkFile := func(path, want string) {
		t.Helper()
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %q: %v", path, err)
		}
		if string(data) != want {
			t.Fatalf("%q: got %q, want %q", path, data, want)
		}
	}

	// Remote dir "parent" is placed inside LOCAL.
	base := filepath.Join(downloadParent, "parent")
	checkFile(filepath.Join(base, "a.txt"), "file-a")
	checkFile(filepath.Join(base, "sub", "b.txt"), "file-b")
	checkFile(filepath.Join(base, "sub", "c.txt"), "file-c")

	// Without --force, existing files are skipped (not overwritten, not error).
	// Modify a local file, then re-download into the same parent.
	unwantedContent := []byte("UNWANTED")
	if err := os.WriteFile(filepath.Join(base, "a.txt"), unwantedContent, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := runGet(testCommand(), []string{"/local/parent", downloadParent}); err != nil {
		t.Fatalf("get dir skip existing: %v", err)
	}
	// a.txt should still have the unwanted content (was not overwritten).
	data, err := os.ReadFile(filepath.Join(base, "a.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(unwantedContent) {
		t.Fatalf("expected skip: got %q, want %q", data, unwantedContent)
	}

	// With --force, existing files are overwritten.
	forceGet := testCommand()
	forceGet.Flags().Bool("force", true, "")
	if err := runGet(forceGet, []string{"/local/parent", downloadParent}); err != nil {
		t.Fatalf("get dir force: %v", err)
	}
	checkFile(filepath.Join(base, "a.txt"), "file-a")

	// Downloading a directory to an existing file path should error.
	filePath := filepath.Join(tmp, "blocker.txt")
	if err := os.WriteFile(filePath, []byte("block"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := runGet(testCommand(), []string{"/local/parent", filePath}); err == nil {
		t.Fatal("expected error when local exists as a file")
	}
}

func TestFSMountFlagInitializesOnlySelectedMount(t *testing.T) {
	tmp := t.TempDir()
	remote := filepath.Join(tmp, "remote")
	if err := os.MkdirAll(remote, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(remote, "ok.txt"), []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(tmp, "qrypt.toml")
	if err := os.WriteFile(configPath, []byte(`
mount_point = `+util.TOMLPath(filepath.Join(tmp, "mnt"))+`
[storage]
read_cache_dir = `+util.TOMLPath(filepath.Join(tmp, "cache", "read"))+`
upload_dir = `+util.TOMLPath(filepath.Join(tmp, "upload"))+`
state_dir = `+util.TOMLPath(filepath.Join(tmp, "state"))+`

[[mounts]]
name = "local"
type = "localfs"
[mounts.params]
root_path = `+util.TOMLPath(remote)+`

[[mounts]]
name = "broken"
type = "localfs"
[mounts.params]
root_path = `+util.TOMLPath(filepath.Join(tmp, "missing"))+`
`), 0o644); err != nil {
		t.Fatal(err)
	}

	root := NewRootCommand()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"fs", "--config", configPath, "--mount", "local", "list", "/local"})
	if err := root.Execute(); err != nil {
		t.Fatalf("fs --mount local list: %v", err)
	}
	if !strings.Contains(out.String(), "ok.txt") {
		t.Fatalf("expected selected mount listing, got %q", out.String())
	}

	root = NewRootCommand()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"fs", "--config", configPath, "list", "/"})
	if err := root.Execute(); err == nil {
		t.Fatal("expected full namespace build to fail on broken mount")
	}
}

func TestPrintEntryStat(t *testing.T) {
	var out bytes.Buffer
	printEntryStat(&out, drive.Entry{
		ID:       "id",
		ParentID: "parent",
		Name:     "file.txt",
		Size:     12,
		ModTime:  time.Unix(123, 0).UTC(),
	})
	text := out.String()
	for _, want := range []string{"type: file", "name: file.txt", "id: id", "parent_id: parent", "size: 12", "mod_time: 1970-01-01T00:02:03Z"} {
		if !strings.Contains(text, want) {
			t.Fatalf("stat output missing %q:\n%s", want, text)
		}
	}
}

func TestFsCryptEncodeDecodeRoundTrip(t *testing.T) {
	tmp := t.TempDir()
	remote := filepath.Join(tmp, "remote")
	if err := os.MkdirAll(remote, 0o755); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(tmp, "qrypt.toml")
	if err := os.WriteFile(configPath, []byte(`
[[mounts]]
name = "encrypted"
type = "localfs"
[mounts.params]
root_path = `+util.TOMLPath(remote)+`
[mounts.encryption]
password = "encrypted-pass"
salt = "encrypted-salt"
filename_encryption = "standard"
filename_encoding = "base32"
`), 0o644); err != nil {
		t.Fatal(err)
	}

	var encodeOut bytes.Buffer
	root := NewRootCommand()
	root.SetOut(&encodeOut)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"fs", "--config", configPath, "crypt-encode", "encrypted", "dir/hello.txt", "--json"})
	if err := root.Execute(); err != nil {
		t.Fatalf("crypt-encode failed: %v", err)
	}
	var encoded fsCryptResult
	if err := json.Unmarshal(encodeOut.Bytes(), &encoded); err != nil {
		t.Fatalf("encode JSON invalid: %v\n%s", err, encodeOut.String())
	}
	if encoded.Mount != "encrypted" || encoded.Input != "dir/hello.txt" {
		t.Fatalf("encode result = %+v", encoded)
	}
	if encoded.Output == encoded.Input || encoded.Output == "" {
		t.Fatalf("encode produced no transformation: %+v", encoded)
	}

	var decodeOut bytes.Buffer
	root = NewRootCommand()
	root.SetOut(&decodeOut)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"fs", "--config", configPath, "crypt-decode", "encrypted", encoded.Output, "--json"})
	if err := root.Execute(); err != nil {
		t.Fatalf("crypt-decode failed: %v", err)
	}
	var decoded fsCryptResult
	if err := json.Unmarshal(decodeOut.Bytes(), &decoded); err != nil {
		t.Fatalf("decode JSON invalid: %v\n%s", err, decodeOut.String())
	}
	if decoded.Output != "dir/hello.txt" {
		t.Fatalf("decode round trip = %q, want dir/hello.txt", decoded.Output)
	}
}

func TestFsCryptEncodeDefaultsToFirstEncryptedMount(t *testing.T) {
	tmp := t.TempDir()
	remote := filepath.Join(tmp, "remote")
	if err := os.MkdirAll(remote, 0o755); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(tmp, "qrypt.toml")
	if err := os.WriteFile(configPath, []byte(`
[[mounts]]
name = "plain"
type = "localfs"
[mounts.params]
root_path = `+util.TOMLPath(remote)+`

[[mounts]]
name = "encrypted"
type = "localfs"
[mounts.params]
root_path = `+util.TOMLPath(remote)+`
[mounts.encryption]
password = "p"
salt = "s"
filename_encryption = "standard"
filename_encoding = "base32"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	root := NewRootCommand()
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"fs", "--config", configPath, "crypt-encode", "hello.txt", "--json"})
	if err := root.Execute(); err != nil {
		t.Fatalf("crypt-encode without mount failed: %v", err)
	}
	var result fsCryptResult
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Mount != "encrypted" {
		t.Fatalf("default mount = %q, want encrypted", result.Mount)
	}
}

func TestFsCryptRejectsUnencryptedMount(t *testing.T) {
	tmp := t.TempDir()
	remote := filepath.Join(tmp, "remote")
	if err := os.MkdirAll(remote, 0o755); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(tmp, "qrypt.toml")
	if err := os.WriteFile(configPath, []byte(`
[[mounts]]
name = "plain"
type = "localfs"
[mounts.params]
root_path = `+util.TOMLPath(remote)+`
`), 0o644); err != nil {
		t.Fatal(err)
	}
	root := NewRootCommand()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"fs", "--config", configPath, "crypt-encode", "plain", "hello.txt"})
	if err := root.Execute(); err == nil {
		t.Fatal("crypt-encode on unencrypted mount must fail")
	}
}
