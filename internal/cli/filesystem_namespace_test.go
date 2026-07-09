package cli

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
