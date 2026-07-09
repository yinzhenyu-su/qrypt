package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

func testCommand() *cobra.Command {
	return &cobra.Command{Use: "test"}
}

func TestBuildFileSystemCreatesNamespaceFromMountConfig(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	remoteA := filepath.Join(tmp, "remote-a")
	remoteB := filepath.Join(tmp, "remote-b")
	if err := os.MkdirAll(remoteA, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(remoteB, 0o755); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(tmp, "qrypt.toml")
	err := os.WriteFile(configPath, []byte(`
mount_point = "`+filepath.Join(tmp, "mnt")+`"
cache_dir = "`+filepath.Join(tmp, "cache")+`"

[defaults.cache]
upload_delay = "10ms"

[[mounts]]
name = "quark"
type = "localfs"
[mounts.params]
root_path = "`+remoteA+`"

[[mounts]]
name = "quark2"
type = "localfs"
[mounts.params]
root_path = "`+remoteB+`"
`), 0o644)
	if err != nil {
		t.Fatal(err)
	}
	fs, cleanup, err := buildFileSystem(ctx, configPath)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	fs.Start(ctx)

	entries, err := fs.List(ctx, "/")
	if err != nil {

		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].Name != "quark" || entries[1].Name != "quark2" {
		t.Fatalf("unexpected namespace entries: %+v", entries)
	}

	if _, err := fs.WriteAt(ctx, "/quark2/test.txt", []byte("two"), 0); err != nil {
		t.Fatal(err)
	}
	if err := fs.Flush(ctx, "/quark2/test.txt"); err != nil {
		t.Fatal(err)
	}
	waitPendingEmpty(t, fs)
	data, err := os.ReadFile(filepath.Join(remoteB, "test.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "two" {
		t.Fatalf("unexpected remote data: %q", data)
	}
}

func TestFSCommandsCreateMoveAndRemoveLocalFS(t *testing.T) {
	tmp := t.TempDir()
	remote := filepath.Join(tmp, "remote")
	if err := os.MkdirAll(remote, 0o755); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(tmp, "qrypt.toml")
	if err := os.WriteFile(configPath, []byte(`
mount_point = "`+filepath.Join(tmp, "mnt")+`"
cache_dir = "`+filepath.Join(tmp, "cache")+`"

[defaults.cache]
delete_delay = "10ms"

[[mounts]]
name = "local"
type = "localfs"
[mounts.params]
root_path = "`+remote+`"
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

func TestBuildNamespaceUsesPerMountEncryption(t *testing.T) {
	ctx := context.Background()
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
	err := os.WriteFile(configPath, []byte(`
mount_point = "`+filepath.Join(tmp, "mnt")+`"
cache_dir = "`+filepath.Join(tmp, "cache")+`"

[defaults.cache]
upload_delay = "10ms"

[[mounts]]
name = "plain"
type = "localfs"
[mounts.params]
root_path = "`+plainRemote+`"
[mounts.encryption]
password = "plain-pass"
salt = "plain-salt"
filename_encryption = "off"

[[mounts]]
name = "encrypted"
type = "localfs"
[mounts.params]
root_path = "`+encryptedRemote+`"
[mounts.encryption]
password = "encrypted-pass"
salt = "encrypted-salt"
filename_encryption = "standard"
filename_encoding = "base32"
`), 0o644)
	if err != nil {
		t.Fatal(err)
	}

	fs, cleanup, err := buildFileSystem(ctx, configPath)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	fs.Start(ctx)

	if _, err := fs.WriteAt(ctx, "/plain/same.txt", []byte("plain mount"), 0); err != nil {
		t.Fatal(err)
	}
	if err := fs.Flush(ctx, "/plain/same.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.WriteAt(ctx, "/encrypted/same.txt", []byte("encrypted mount"), 0); err != nil {
		t.Fatal(err)
	}
	if err := fs.Flush(ctx, "/encrypted/same.txt"); err != nil {
		t.Fatal(err)
	}
	waitPendingEmpty(t, fs)

	if _, err := os.Stat(filepath.Join(plainRemote, "same.txt")); err != nil {
		t.Fatalf("plain mount should keep plaintext filename: %v", err)
	}
	encryptedNames, err := os.ReadDir(encryptedRemote)
	if err != nil {
		t.Fatal(err)
	}
	if len(encryptedNames) != 1 {
		t.Fatalf("expected one encrypted file, got %d", len(encryptedNames))
	}
	if encryptedNames[0].Name() == "same.txt" || strings.Contains(encryptedNames[0].Name(), "same") {
		t.Fatalf("encrypted mount used plaintext filename: %q", encryptedNames[0].Name())
	}
}

func TestBuildNamespaceAppliesGlobalEncryptionToMountWithoutEncryption(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	globalRemote := filepath.Join(tmp, "global")
	encryptedRemote := filepath.Join(tmp, "encrypted")
	if err := os.MkdirAll(globalRemote, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(encryptedRemote, 0o755); err != nil {
		t.Fatal(err)
	}

	configPath := filepath.Join(tmp, "qrypt.toml")
	err := os.WriteFile(configPath, []byte(`
mount_point = "`+filepath.Join(tmp, "mnt")+`"
cache_dir = "`+filepath.Join(tmp, "cache")+`"

[encryption]
password = "global-pass"
salt = "global-salt"
filename_encryption = "standard"
filename_encoding = "base32"

[defaults.cache]
upload_delay = "10ms"

[[mounts]]
name = "global"
type = "localfs"
[mounts.params]
root_path = "`+globalRemote+`"

[[mounts]]
name = "encrypted"
type = "localfs"
[mounts.params]
root_path = "`+encryptedRemote+`"
[mounts.encryption]
password = "encrypted-pass"
salt = "encrypted-salt"
filename_encryption = "standard"
filename_encoding = "base32"
`), 0o644)
	if err != nil {
		t.Fatal(err)
	}

	fs, cleanup, err := buildFileSystem(ctx, configPath)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	fs.Start(ctx)

	if _, err := fs.WriteAt(ctx, "/global/global.txt", []byte("global content"), 0); err != nil {
		t.Fatal(err)
	}
	if err := fs.Flush(ctx, "/global/global.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.WriteAt(ctx, "/encrypted/secret.txt", []byte("secret content"), 0); err != nil {
		t.Fatal(err)
	}
	if err := fs.Flush(ctx, "/encrypted/secret.txt"); err != nil {
		t.Fatal(err)
	}
	waitPendingEmpty(t, fs)

	if _, err := os.Stat(filepath.Join(globalRemote, "global.txt")); err == nil {
		t.Fatal("global-encrypted mount wrote plaintext filename")
	}
	if _, err := os.Stat(filepath.Join(encryptedRemote, "secret.txt")); err == nil {
		t.Fatal("encrypted mount wrote plaintext filename")
	}
}

func TestBuildNamespaceReadsPlainFilesWhenNoEncryptionConfigured(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	plainRemote := filepath.Join(tmp, "plain")
	if err := os.MkdirAll(plainRemote, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(plainRemote, "plain.txt"), []byte("plain content"), 0o644); err != nil {
		t.Fatal(err)
	}

	configPath := filepath.Join(tmp, "qrypt.toml")
	err := os.WriteFile(configPath, []byte(`
mount_point = "`+filepath.Join(tmp, "mnt")+`"
cache_dir = "`+filepath.Join(tmp, "cache")+`"

[defaults.cache]
upload_delay = "10ms"

[[mounts]]
name = "plain"
type = "localfs"
[mounts.params]
root_path = "`+plainRemote+`"
`), 0o644)
	if err != nil {
		t.Fatal(err)
	}

	fs, cleanup, err := buildFileSystem(ctx, configPath)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	fs.Start(ctx)

	reader, err := fs.Read(ctx, "/plain/plain.txt", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(reader)
	closeErr := reader.Close()
	if err != nil {
		t.Fatal(err)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	if string(data) != "plain content" {
		t.Fatalf("unexpected plain mount read: %q", data)
	}
}

func TestBuildNamespaceAppliesBandwidthToEncryptedMount(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tmp := t.TempDir()
	remote := filepath.Join(tmp, "remote")
	if err := os.MkdirAll(remote, 0o755); err != nil {
		t.Fatal(err)
	}

	configPath := filepath.Join(tmp, "qrypt.toml")
	err := os.WriteFile(configPath, []byte(`
cache_dir = "`+filepath.Join(tmp, "cache")+`"

[bandwidth]
upload = "1"

[defaults.cache]
upload_delay = "10ms"

[[mounts]]
name = "encrypted"
type = "localfs"
[mounts.params]
root_path = "`+remote+`"
[mounts.encryption]
password = "encrypted-pass"
salt = "encrypted-salt"
filename_encryption = "standard"
filename_encoding = "base32"
`), 0o644)
	if err != nil {
		t.Fatal(err)
	}

	fs, cleanup, err := buildFileSystem(ctx, configPath)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	fs.Start(ctx)

	if _, err := fs.WriteAt(ctx, "/encrypted/slow.txt", []byte("slow upload"), 0); err != nil {
		t.Fatal(err)
	}
	if err := fs.Flush(ctx, "/encrypted/slow.txt"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if len(fs.Pending()) == 0 {
		t.Fatal("expected encrypted upload to remain pending under 1 B/s upload limit")
	}
}

func TestBuildNamespaceUsesTopLevelCacheDir(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	remoteA := filepath.Join(tmp, "remote-a")
	remoteB := filepath.Join(tmp, "remote-b")
	cacheDir := filepath.Join(tmp, "configured-cache")
	if err := os.MkdirAll(remoteA, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(remoteB, 0o755); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(tmp, "qrypt.toml")
	err := os.WriteFile(configPath, []byte(`
mount_point = "`+filepath.Join(tmp, "mnt")+`"
cache_dir = "`+cacheDir+`"

[defaults.cache]
upload_delay = "10ms"

[[mounts]]
name = "one"
type = "localfs"
[mounts.params]
root_path = "`+remoteA+`"

[[mounts]]
name = "two"
type = "localfs"
[mounts.params]
root_path = "`+remoteB+`"
`), 0o644)
	if err != nil {
		t.Fatal(err)
	}

	fs, cleanup, err := buildFileSystem(ctx, configPath)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	fs.Start(ctx)

	if _, err := fs.WriteAt(ctx, "/two/cache.txt", []byte("uses configured cache"), 0); err != nil {
		t.Fatal(err)
	}
	if err := fs.Flush(ctx, "/two/cache.txt"); err != nil {
		t.Fatal(err)
	}
	waitPendingEmpty(t, fs)

	if _, err := os.Stat(filepath.Join(cacheDir, "one", "staging")); err != nil {
		t.Fatalf("expected cache dir for mount one: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cacheDir, "two", "staging")); err != nil {
		t.Fatalf("expected cache dir for mount two: %v", err)
	}
}

func TestLoggingFromConfig(t *testing.T) {
	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "qrypt.toml")
	err := os.WriteFile(configPath, []byte(`
[logging]
log_level = "debug"
log_file = "`+filepath.Join(tmp, "qrypt.log")+`"
`), 0o644)
	if err != nil {
		t.Fatal(err)
	}

	logging, err := loggingFromConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if logging.LogLevel != "debug" {
		t.Fatalf("unexpected log_level: %q", logging.LogLevel)
	}
	if logging.LogFile != filepath.Join(tmp, "qrypt.log") {
		t.Fatalf("unexpected log_file: %q", logging.LogFile)
	}
}

func TestBuildNamespaceRejectsInvalidDeleteDelay(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	remote := filepath.Join(tmp, "remote")
	if err := os.MkdirAll(remote, 0o755); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(tmp, "qrypt.toml")
	err := os.WriteFile(configPath, []byte(`
cache_dir = "`+filepath.Join(tmp, "cache")+`"

[[mounts]]
name = "local"
type = "localfs"
[mounts.params]
root_path = "`+remote+`"
[mounts.cache]
delete_delay = "soon"
`), 0o644)
	if err != nil {
		t.Fatal(err)
	}

	_, cleanup, err := buildFileSystem(ctx, configPath)
	if cleanup != nil {
		defer cleanup()
	}
	if err == nil || !strings.Contains(err.Error(), "cache.delete_delay") {
		t.Fatalf("expected cache.delete_delay error, got %v", err)
	}
}

func TestBuildFileSystemRejectsInvalidBandwidth(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	remote := filepath.Join(tmp, "remote")
	if err := os.MkdirAll(remote, 0o755); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(tmp, "qrypt.toml")
	err := os.WriteFile(configPath, []byte(`
[bandwidth]
download = "fast"
`), 0o644)
	if err != nil {
		t.Fatal(err)
	}

	_, cleanup, err := buildFileSystem(ctx, configPath)
	if cleanup != nil {
		defer cleanup()
	}
	if err == nil || !strings.Contains(err.Error(), "bandwidth.download") {
		t.Fatalf("expected bandwidth.download error, got %v", err)
	}
}

func TestMountConfigFromConfig(t *testing.T) {
	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "qrypt.toml")
	err := os.WriteFile(configPath, []byte(`
volume_name = "Qrypt Dev"
read_only = true
allow_other = true
default_permissions = true
no_apple_double = false
no_apple_xattr = true
attr_timeout = "1500ms"
entry_timeout = "2s"
negative_timeout = "250ms"
total_space = "2T"
free_space = "1.5T"

[logging]
log_level = "debug"
log_file = "`+filepath.Join(tmp, "qrypt.log")+`"
`), 0o644)
	if err != nil {
		t.Fatal(err)
	}

	mountConfig, err := mountConfigFromConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if mountConfig.VolumeName != "Qrypt Dev" {
		t.Fatalf("unexpected volume name: %q", mountConfig.VolumeName)
	}
	if !mountConfig.ReadOnly {
		t.Fatal("expected read_only to be enabled")
	}
	if !mountConfig.AllowOther {
		t.Fatal("expected allow_other to be enabled")
	}
	if !mountConfig.DefaultPermissions {
		t.Fatal("expected default_permissions to be enabled")
	}
	if mountConfig.NoAppleDouble {
		t.Fatal("expected no_apple_double to be disabled")
	}
	if !mountConfig.NoAppleXattr {
		t.Fatal("expected no_apple_xattr to be enabled")
	}
	if mountConfig.AttrTimeout != 1500*time.Millisecond {
		t.Fatalf("unexpected attr timeout: %s", mountConfig.AttrTimeout)
	}
	if mountConfig.EntryTimeout != 2*time.Second {
		t.Fatalf("unexpected entry timeout: %s", mountConfig.EntryTimeout)
	}
	if mountConfig.NegativeTimeout != 250*time.Millisecond {
		t.Fatalf("unexpected negative timeout: %s", mountConfig.NegativeTimeout)
	}
	if mountConfig.TotalSpace != 2<<40 {
		t.Fatalf("unexpected total space: %d", mountConfig.TotalSpace)
	}
	if mountConfig.FreeSpace != 1536<<30 {
		t.Fatalf("unexpected free space: %d", mountConfig.FreeSpace)
	}
	if mountConfig.Logging.LogLevel != "debug" {
		t.Fatalf("unexpected log_level: %q", mountConfig.Logging.LogLevel)
	}
	if mountConfig.Logging.LogFile != filepath.Join(tmp, "qrypt.log") {
		t.Fatalf("unexpected log_file: %q", mountConfig.Logging.LogFile)
	}
}

func TestMountConfigFromConfigDefaults(t *testing.T) {
	mountConfig, err := mountConfigFromConfig("")
	if err != nil {
		t.Fatal(err)
	}
	if mountConfig.VolumeName != "Qrypt" {
		t.Fatalf("unexpected default volume name: %q", mountConfig.VolumeName)
	}
	if !mountConfig.NoAppleDouble {
		t.Fatal("expected no_apple_double to default to true")
	}
	if mountConfig.NoAppleXattr {
		t.Fatal("expected no_apple_xattr to default to false")
	}
	if mountConfig.ReadOnly {
		t.Fatal("expected read_only to default to false")
	}
	if mountConfig.AllowOther {
		t.Fatal("expected allow_other to default to false")
	}
	if mountConfig.DefaultPermissions {
		t.Fatal("expected default_permissions to default to false")
	}
	if mountConfig.AttrTimeout != time.Second {
		t.Fatalf("unexpected default attr timeout: %s", mountConfig.AttrTimeout)
	}
	if mountConfig.EntryTimeout != time.Second {
		t.Fatalf("unexpected default entry timeout: %s", mountConfig.EntryTimeout)
	}
	if mountConfig.NegativeTimeout != 0 {
		t.Fatalf("unexpected default negative timeout: %s", mountConfig.NegativeTimeout)
	}
}

func TestMountConfigRejectsInvalidMetadataTimeout(t *testing.T) {
	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "qrypt.toml")
	if err := os.WriteFile(configPath, []byte(`attr_timeout = "-1s"`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := mountConfigFromConfig(configPath)
	if err == nil || !strings.Contains(err.Error(), "attr_timeout") {
		t.Fatalf("expected attr_timeout error, got %v", err)
	}
}

func TestMountConfigAllowsDisablingMetadataTimeouts(t *testing.T) {
	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "qrypt.toml")
	if err := os.WriteFile(configPath, []byte(`
attr_timeout = "0s"
entry_timeout = "0s"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	mountConfig, err := mountConfigFromConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if mountConfig.AttrTimeout != 0 || !mountConfig.AttrTimeoutSet {
		t.Fatalf("unexpected attr timeout: %s set=%t", mountConfig.AttrTimeout, mountConfig.AttrTimeoutSet)
	}
	if mountConfig.EntryTimeout != 0 || !mountConfig.EntryTimeoutSet {
		t.Fatalf("unexpected entry timeout: %s set=%t", mountConfig.EntryTimeout, mountConfig.EntryTimeoutSet)
	}
}

func TestInspectJournalCacheReportsPendingProblems(t *testing.T) {
	tmp := t.TempDir()
	cacheDir := filepath.Join(tmp, "cache")
	stagingDir := filepath.Join(cacheDir, "staging")
	if err := os.MkdirAll(stagingDir, 0o755); err != nil {
		t.Fatal(err)
	}
	localPath := filepath.Join(stagingDir, "file.staging")
	if err := os.WriteFile(localPath, []byte("abc"), 0o644); err != nil {
		t.Fatal(err)
	}
	orphanPath := filepath.Join(stagingDir, "orphan.staging")
	if err := os.WriteFile(orphanPath, []byte("orphan"), 0o644); err != nil {
		t.Fatal(err)
	}
	journalPath := filepath.Join(cacheDir, "pending.jsonl")
	dirty, err := json.Marshal(struct {
		Op string `json:"op"`
		vfs.PendingFile
	}{
		Op: "dirty",
		PendingFile: vfs.PendingFile{
			Path:       "/file.txt",
			FID:        "file",
			Name:       "file.txt",
			LocalPath:  localPath,
			Size:       4,
			RetryCount: 2,
			LastError:  "upload failed",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	clean, err := json.Marshal(struct {
		Op string `json:"op"`
		vfs.PendingFile
	}{
		Op:          "clean",
		PendingFile: vfs.PendingFile{Path: "/old.txt"},
	})
	if err != nil {
		t.Fatal(err)
	}
	content := string(dirty) + "\n" + string(clean) + "\n" + "{bad json\n"
	if err := os.WriteFile(journalPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	report := inspectJournalCache(debugCacheTarget{Name: "test", Dir: cacheDir})
	if report.Entries != 2 || report.DirtyEntries != 1 || report.CleanEntries != 1 {
		t.Fatalf("unexpected journal counts: %+v", report)
	}
	if len(report.InvalidEntries) != 1 {
		t.Fatalf("expected one invalid entry, got %+v", report.InvalidEntries)
	}
	if len(report.Pending) != 1 {
		t.Fatalf("expected one pending entry, got %+v", report.Pending)
	}
	if !report.Pending[0].StagingExists || report.Pending[0].StagingSize != 3 {
		t.Fatalf("expected staging size mismatch details, got %+v", report.Pending[0])
	}
	if len(report.OrphanStaging) != 1 || report.OrphanStaging[0] != orphanPath {
		t.Fatalf("unexpected orphan staging files: %+v", report.OrphanStaging)
	}
}

func TestPrintPendingVerboseIncludesDebugState(t *testing.T) {
	tmp := t.TempDir()
	localPath := filepath.Join(tmp, "file.staging")
	if err := os.WriteFile(localPath, []byte("abc"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	printPendingVerbose(&out, []vfs.PendingFile{{
		Path:       "/file.txt",
		Size:       4,
		LocalPath:  localPath,
		RetryCount: 1,
		LastError:  "boom",
	}})
	text := out.String()
	for _, want := range []string{"/file.txt", "size-mismatch(3)", "boom", "RETRY"} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected verbose pending output to contain %q, got:\n%s", want, text)
		}
	}
}

func waitPendingEmpty(t *testing.T, fs vfs.FileSystem) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if len(fs.Pending()) == 0 && activeUploadCount(fs) == 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("pending uploads did not drain: %+v", fs.Pending())
}

func activeUploadCount(fs vfs.FileSystem) int {
	snapshotter, ok := fs.(interface {
		DebugSnapshot() vfs.DebugSnapshot
	})
	if !ok {
		return 0
	}
	count := 0
	for _, mount := range snapshotter.DebugSnapshot().Mounts {
		count += len(mount.Uploads)
	}
	return count
}

func waitPathMissing(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		_, lastErr = os.Stat(path)
		if os.IsNotExist(lastErr) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("path still exists: %s err=%v", path, lastErr)
}
