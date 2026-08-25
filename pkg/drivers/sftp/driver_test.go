package sftp

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"golang.org/x/crypto/ssh"
)

type blockingReadSource struct {
	closed chan struct{}
	once   sync.Once
}

func (s *blockingReadSource) Read([]byte) (int, error) {
	<-s.closed
	return 0, io.ErrClosedPipe
}

func (s *blockingReadSource) Close() error {
	s.once.Do(func() { close(s.closed) })
	return nil
}

func TestFactoryRequiresConnectionAndAuthentication(t *testing.T) {
	tests := []struct {
		name   string
		params drive.Params
	}{
		{name: "address", params: drive.Params{"username": "u", "password": "p"}},
		{name: "username", params: drive.Params{"address": "host:22", "password": "p"}},
		{name: "authentication", params: drive.Params{"address": "host:22", "username": "u"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := drive.New("sftp", test.params); err == nil {
				t.Fatal("drive.New returned nil error")
			}
		})
	}
}

func TestResolvePathUsesConfiguredRoot(t *testing.T) {
	driver := New(Options{RootPath: "/srv/qrypt"})
	for input, want := range map[string]string{"/": "/srv/qrypt", "/dir/file.txt": "/srv/qrypt/dir/file.txt", "dir/file.txt": "/srv/qrypt/dir/file.txt"} {
		got, err := driver.ResolvePath(context.Background(), input)
		if err != nil {
			t.Fatalf("ResolvePath(%q): %v", input, err)
		}
		if got != want {
			t.Errorf("ResolvePath(%q) = %q, want %q", input, got, want)
		}
	}
	if _, err := driver.ResolvePath(context.Background(), "/../../etc"); !errors.Is(err, drive.ErrInvalidInput) {
		t.Fatalf("ResolvePath error = %v, want drive.ErrInvalidInput", err)
	}
}

func TestReadRejectsNegativeRangeBeforeInitialization(t *testing.T) {
	driver := New(Options{})
	if _, err := driver.Read(context.Background(), drive.Entry{ID: "/file"}, -1, 1); !errors.Is(err, drive.ErrInvalidInput) {
		t.Fatalf("Read error = %v, want drive.ErrInvalidInput", err)
	}
}

func TestContextReadCloserClosesBlockedSourceOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	source := &blockingReadSource{closed: make(chan struct{})}
	reader := newContextReadCloser(ctx, source, source, nil)
	t.Cleanup(func() { _ = reader.Close() })

	readDone := make(chan error, 1)
	go func() {
		_, err := reader.Read(make([]byte, 1))
		readDone <- err
	}()
	cancel()

	select {
	case err := <-readDone:
		if err == nil {
			t.Fatal("blocked read returned nil error")
		}
	case <-time.After(time.Second):
		t.Fatal("blocked read did not stop after context cancellation")
	}
}

func TestCapabilitiesDeclareImplementedOperations(t *testing.T) {
	driver := New(Options{})
	for _, capability := range []drive.Capability{drive.CapabilityWriter, drive.CapabilitySourceUploader, drive.CapabilityResumableUploader, drive.CapabilitySpace, drive.CapabilityPathResolver, drive.CapabilityMtime} {
		if !drive.HasCapability(driver, capability) {
			t.Errorf("missing capability %q", capability)
		}
	}
}

func TestDebugSnapshotDoesNotExposeCredentials(t *testing.T) {
	driver := New(Options{Address: "host:22", Username: "u", Password: "secret", PrivateKey: "private"})
	snapshot, err := driver.DebugSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Stats["password"] != nil || snapshot.Stats["private_key"] != nil {
		t.Fatalf("snapshot exposes credentials: %+v", snapshot)
	}
}

func TestAuthMethodLoadsPrivateKeyFromPath(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := ssh.MarshalPrivateKey(privateKey, "")
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(t.TempDir(), "id_ed25519")
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(encoded), 0o600); err != nil {
		t.Fatal(err)
	}
	driver := New(Options{PrivateKey: keyPath})
	if _, err := driver.authMethod(); err != nil {
		t.Fatalf("authMethod() = %v, want private key path to load", err)
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	homeKeyPath := filepath.Join(home, ".ssh", "id_ed25519")
	if err := os.MkdirAll(filepath.Dir(homeKeyPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(homeKeyPath, pem.EncodeToMemory(encoded), 0o600); err != nil {
		t.Fatal(err)
	}
	keyBytes, err := loadPrivateKey("~/.ssh/id_ed25519")
	if err != nil || !bytes.Equal(keyBytes, pem.EncodeToMemory(encoded)) {
		t.Fatalf("loadPrivateKey(~/.ssh/id_ed25519) = %v, want expanded key file", err)
	}
}

func TestLiveSFTPCRUD(t *testing.T) {
	address := os.Getenv("QRYPT_SFTP_TEST_ADDR")
	keyPath := os.Getenv("QRYPT_SFTP_TEST_KEY")
	rootPath := os.Getenv("QRYPT_SFTP_TEST_ROOT")
	if address == "" || keyPath == "" || rootPath == "" {
		t.Skip("QRYPT_SFTP_TEST_ADDR, QRYPT_SFTP_TEST_KEY, and QRYPT_SFTP_TEST_ROOT are required")
	}
	driver := New(Options{Address: address, Username: os.Getenv("QRYPT_SFTP_TEST_USER"), PrivateKey: keyPath, RootPath: rootPath})
	ctx := context.Background()
	if err := driver.Init(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := driver.Drop(ctx); err != nil {
			t.Errorf("driver.Drop() = %v", err)
		}
	}()

	directory, err := driver.Mkdir(ctx, "/", "qa-dir")
	if err != nil {
		t.Fatal(err)
	}
	name := "名字 #?.txt"
	modTime := time.Date(2024, time.January, 2, 3, 4, 5, 0, time.UTC)
	entry, err := driver.PutSource(ctx, drive.UploadRequest{ParentID: directory.ID, Name: name, Source: drive.NewBytesReadOnlyFileSource([]byte("sftp payload")), ModTime: modTime})
	if err != nil {
		t.Fatal(err)
	}
	entries, err := driver.List(ctx, directory.ID)
	if err != nil || len(entries) != 1 || entries[0].Name != name {
		t.Fatalf("List = %+v, %v", entries, err)
	}
	reader, err := driver.Read(ctx, entry, 5, 0)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(reader)
	reader.Close()
	if err != nil || string(data) != "payload" {
		t.Fatalf("Read = %q, %v", data, err)
	}
	if err := driver.Rename(ctx, entry, "renamed.txt"); err != nil {
		t.Fatal(err)
	}
	entry.Name = "renamed.txt"
	entry.ID = filepath.Join(directory.ID, entry.Name)
	destination, err := driver.Mkdir(ctx, "/", "qa-destination")
	if err != nil {
		t.Fatal(err)
	}
	if err := driver.Move(ctx, entry, destination.ID); err != nil {
		t.Fatal(err)
	}
	entry.ParentID = destination.ID
	entry.ID = filepath.Join(destination.ID, entry.Name)
	if err := driver.Remove(ctx, entry); err != nil {
		t.Fatal(err)
	}
	if err := driver.Remove(ctx, directory); err != nil {
		t.Fatal(err)
	}
	if err := driver.Remove(ctx, destination); err != nil {
		t.Fatal(err)
	}
}
