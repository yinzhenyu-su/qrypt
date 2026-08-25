package sftp

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sftpserver "github.com/pkg/sftp"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

type mockSFTPServer struct {
	listener       net.Listener
	root           string
	user           string
	keyPath        string
	knownHostsPath string
	config         *ssh.ServerConfig
}

func newMockSFTPServer(t *testing.T) *mockSFTPServer {
	t.Helper()
	root := t.TempDir()
	keyDir := t.TempDir()
	_, userKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	userSigner, err := ssh.NewSignerFromKey(userKey)
	if err != nil {
		t.Fatal(err)
	}
	_, hostKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	hostSigner, err := ssh.NewSignerFromKey(hostKey)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(keyDir, "id_ed25519")
	keyPEM, err := ssh.MarshalPrivateKey(userKey, "mock")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(keyPEM), 0o600); err != nil {
		t.Fatal(err)
	}
	config := &ssh.ServerConfig{
		PublicKeyCallback: func(meta ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if meta.User() != "qrypt" || !bytes.Equal(key.Marshal(), userSigner.PublicKey().Marshal()) {
				return nil, fmt.Errorf("mock sftp: public key rejected")
			}
			return nil, nil
		},
	}
	config.AddHostKey(hostSigner)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	knownHostsPath := filepath.Join(keyDir, "known_hosts")
	knownHostsLine := knownhosts.Line([]string{listener.Addr().String()}, hostSigner.PublicKey()) + "\n"
	if err := os.WriteFile(knownHostsPath, []byte(knownHostsLine), 0o600); err != nil {
		t.Fatal(err)
	}
	server := &mockSFTPServer{listener: listener, root: root, user: "qrypt", keyPath: keyPath, knownHostsPath: knownHostsPath, config: config}
	t.Cleanup(func() { listener.Close() })
	go server.serve(t)
	return server
}

func (s *mockSFTPServer) serve(t *testing.T) {
	t.Helper()
	for {
		connection, err := s.listener.Accept()
		if err != nil {
			return
		}
		go s.serveConnection(t, connection)
	}
}

func (s *mockSFTPServer) serveConnection(t *testing.T, connection net.Conn) {
	_, channels, requests, err := ssh.NewServerConn(connection, s.config)
	if err != nil {
		connection.Close()
		return
	}
	go ssh.DiscardRequests(requests)
	for newChannel := range channels {
		if newChannel.ChannelType() != "session" {
			if err := newChannel.Reject(ssh.UnknownChannelType, "mock sftp only accepts sessions"); err != nil {
				t.Logf("reject unsupported SSH channel: %v", err)
			}
			continue
		}
		channel, channelRequests, err := newChannel.Accept()
		if err != nil {
			continue
		}
		go s.serveSession(t, channel, channelRequests)
	}
}

func (s *mockSFTPServer) serveSession(_ *testing.T, channel ssh.Channel, requests <-chan *ssh.Request) {
	for request := range requests {
		ok := request.Type == "subsystem" && len(request.Payload) >= 4 && string(request.Payload[4:]) == "sftp"
		if err := request.Reply(ok, nil); err != nil {
			return
		}
		if !ok {
			continue
		}
		server, err := sftpserver.NewServer(channel, sftpserver.WithServerWorkingDirectory(s.root))
		if err != nil {
			return
		}
		if err := server.Serve(); err != nil && err != io.EOF {
			return
		}
		server.Close()
		return
	}
}

func TestMockSFTPCRUD(t *testing.T) {
	server := newMockSFTPServer(t)
	driver := New(Options{Address: server.listener.Addr().String(), Username: server.user, PrivateKey: server.keyPath, KnownHosts: server.knownHostsPath, RootPath: server.root})
	ctx := context.Background()
	if err := driver.Init(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := driver.Drop(ctx); err != nil {
			t.Errorf("driver.Drop() = %v", err)
		}
	})
	space, err := driver.Space(ctx)
	if err != nil {
		t.Fatalf("Space() = %v", err)
	}
	if space.Total <= 0 || space.Free < 0 || space.Free > space.Total {
		t.Fatalf("unexpected space: %+v", space)
	}

	directory, err := driver.Mkdir(ctx, "/", "qa-dir")
	if err != nil {
		t.Fatal(err)
	}
	entry, err := driver.PutSource(ctx, drive.UploadRequest{ParentID: directory.ID, Name: "名字 #?.txt", Source: drive.NewBytesReadOnlyFileSource([]byte("sftp payload"))})
	if err != nil {
		t.Fatal(err)
	}
	entries, err := driver.List(ctx, directory.ID)
	if err != nil || len(entries) != 1 || entries[0].Name != "名字 #?.txt" {
		t.Fatalf("List = %+v, %v", entries, err)
	}
	reader, err := driver.Read(ctx, entry, 5, 7)
	if err != nil {
		t.Fatal(err)
	}
	data := make([]byte, len("payload"))
	readN, err := reader.Read(data)
	closeErr := reader.Close()
	if err != nil || closeErr != nil || readN != len(data) || string(data) != "payload" {
		t.Fatalf("Read = %q, n=%d, err=%v, closeErr=%v", data, readN, err, closeErr)
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

func TestMockSFTPReconnectsAfterConnectionLoss(t *testing.T) {
	server := newMockSFTPServer(t)
	driver := New(Options{Address: server.listener.Addr().String(), Username: server.user, PrivateKey: server.keyPath, KnownHosts: server.knownHostsPath, RootPath: server.root})
	ctx := context.Background()
	if err := driver.Init(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := driver.Drop(ctx); err != nil {
			t.Errorf("driver.Drop() = %v", err)
		}
	})

	driver.mu.RLock()
	client, connection := driver.client, driver.connection
	driver.mu.RUnlock()
	if client == nil || connection == nil {
		t.Fatal("driver initialized without an active connection")
	}
	if err := client.Close(); err != nil {
		t.Logf("close mock SFTP client: %v", err)
	}
	if err := connection.Close(); err != nil {
		t.Logf("close mock SSH connection: %v", err)
	}

	if _, err := driver.List(ctx, "/"); err != nil {
		t.Fatalf("List() after connection loss = %v, want automatic reconnect", err)
	}
}

func TestMockSFTPListRetriesReadDirAfterConnectionLoss(t *testing.T) {
	server := newMockSFTPServer(t)
	driver := New(Options{Address: server.listener.Addr().String(), Username: server.user, PrivateKey: server.keyPath, KnownHosts: server.knownHostsPath, RootPath: server.root})
	ctx := context.Background()
	if err := driver.Init(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := driver.Drop(ctx); err != nil {
			t.Errorf("driver.Drop() = %v", err)
		}
	})

	readDirCalls := 0
	defaultReadDir := driver.readDir
	driver.readDir = func(ctx context.Context, client *sftpserver.Client, parent string) ([]os.FileInfo, error) {
		readDirCalls++
		if readDirCalls == 1 {
			return nil, errors.New("connection lost")
		}
		return defaultReadDir(ctx, client, parent)
	}

	entries, err := driver.List(ctx, "/")
	if err != nil {
		t.Fatalf("List() after ReadDir connection loss = %v, want retry success", err)
	}
	if readDirCalls != 2 {
		t.Fatalf("ReadDir calls = %d, want 2", readDirCalls)
	}
	if entries == nil {
		t.Fatal("List() returned nil entries after retry")
	}
}

func TestMockSFTPBandwidthLimiter(t *testing.T) {
	server := newMockSFTPServer(t)
	driver := New(Options{Address: server.listener.Addr().String(), Username: server.user, PrivateKey: server.keyPath, KnownHosts: server.knownHostsPath, RootPath: server.root})
	handled := driver.InstallBandwidthLimiter(drive.NewBandwidthLimiter(drive.BandwidthLimits{DownloadBytesPerSecond: 1, UploadBytesPerSecond: 1}))
	if handled != drive.BandwidthLimitDownload|drive.BandwidthLimitUpload {
		t.Fatalf("handled directions = %v, want download|upload", handled)
	}
	ctx := context.Background()
	if err := driver.Init(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := driver.Drop(ctx); err != nil {
			t.Errorf("driver.Drop() = %v", err)
		}
	})
}

func TestMockSFTPMetricsCaptureOperationsAndBandwidth(t *testing.T) {
	server := newMockSFTPServer(t)
	driver := New(Options{Address: server.listener.Addr().String(), Username: server.user, PrivateKey: server.keyPath, KnownHosts: server.knownHostsPath, RootPath: server.root})
	ctx := drive.WithDebugOperation(context.Background(), drive.DebugOperation{OpID: "mock-op", Step: "sftp", Name: "metrics"})
	if err := driver.Init(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := driver.Drop(ctx); err != nil {
			t.Errorf("driver.Drop() = %v", err)
		}
	})
	if _, err := driver.Space(ctx); err != nil {
		t.Fatalf("Space() = %v", err)
	}

	entry, err := driver.PutSource(ctx, drive.UploadRequest{ParentID: "/", Name: "metrics.bin", Source: drive.NewBytesReadOnlyFileSource([]byte("metrics"))})
	if err != nil {
		t.Fatal(err)
	}
	reader, err := driver.Read(ctx, entry, 0, int64(len("metrics")))
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, len("metrics"))
	n, readErr := reader.Read(buf)
	if readErr != nil || n != len(buf) || string(buf) != "metrics" {
		t.Fatalf("read metrics payload = %q, n = %d, err = %v", buf[:n], n, readErr)
	}
	if err := reader.Close(); err != nil {
		t.Fatalf("close metrics reader: %v", err)
	}

	metrics, err := driver.Metrics(ctx, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]drive.MetricEvent{}
	for _, metric := range metrics {
		seen[metric.Operation] = metric
		if metric.Driver != "sftp" || metric.Layer != "driver.sftp" || metric.OpID != "mock-op" {
			t.Fatalf("unexpected normalized metric: %+v", metric)
		}
	}
	for _, operation := range []string{"upload", "upload_part", "upload_commit", "read", "space"} {
		metric, ok := seen[operation]
		if !ok {
			t.Fatalf("missing %q metric in %+v", operation, seen)
		}
		if !metric.OK || metric.Duration == "" || metric.FinishedAt.IsZero() {
			t.Fatalf("incomplete %q metric: %+v", operation, metric)
		}
	}
	if got := seen["upload_part"].Bytes; got != int64(len("metrics")) {
		t.Fatalf("upload_part bytes = %d, want %d", got, len("metrics"))
	}
	if got := seen["read"].Bytes; got != int64(len("metrics")) {
		t.Fatalf("read bytes = %d, want %d", got, len("metrics"))
	}
}

func TestMockSFTPRejectsPathsOutsideRoot(t *testing.T) {
	server := newMockSFTPServer(t)
	driver := New(Options{Address: server.listener.Addr().String(), Username: server.user, PrivateKey: server.keyPath, KnownHosts: server.knownHostsPath, RootPath: server.root})
	ctx := context.Background()
	if err := driver.Init(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := driver.Drop(ctx); err != nil {
			t.Errorf("driver.Drop() = %v", err)
		}
	})

	if _, err := driver.Mkdir(ctx, "/tmp", "escape"); !errors.Is(err, drive.ErrInvalidInput) {
		t.Fatalf("Mkdir outside root error = %v, want drive.ErrInvalidInput", err)
	}
	root := drive.Entry{ID: server.root, Name: filepath.Base(server.root), IsDir: true}
	if err := driver.Remove(ctx, root); !errors.Is(err, drive.ErrInvalidInput) {
		t.Fatalf("Remove root error = %v, want drive.ErrInvalidInput", err)
	}
	if err := driver.Rename(ctx, root, "renamed"); !errors.Is(err, drive.ErrInvalidInput) {
		t.Fatalf("Rename root error = %v, want drive.ErrInvalidInput", err)
	}
}

type failingUploadSource struct{}

func (failingUploadSource) Size() int64 { return int64(len("partial")) }

func (failingUploadSource) Open(context.Context) (drive.ReadOnlyFile, error) {
	return &failingUploadFile{Reader: bytes.NewReader([]byte("partial"))}, nil
}

type failingUploadFile struct {
	*bytes.Reader
	failed bool
}

func (f *failingUploadFile) Read(p []byte) (int, error) {
	if f.failed {
		return 0, errors.New("mock upload: injected read failure")
	}
	f.failed = true
	if len(p) > 2 {
		p = p[:2]
	}
	n, _ := f.Reader.Read(p)
	return n, errors.New("mock upload: injected read failure")
}

func (f *failingUploadFile) Close() error { return nil }

func TestMockSFTPFailedUploadLeavesNoPartialFile(t *testing.T) {
	server := newMockSFTPServer(t)
	driver := New(Options{Address: server.listener.Addr().String(), Username: server.user, PrivateKey: server.keyPath, KnownHosts: server.knownHostsPath, RootPath: server.root})
	ctx := context.Background()
	if err := driver.Init(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := driver.Drop(ctx); err != nil {
			t.Errorf("driver.Drop() = %v", err)
		}
	})

	if _, err := driver.PutSource(ctx, drive.UploadRequest{ParentID: "/", Name: "partial.bin", Source: failingUploadSource{}}); err == nil {
		t.Fatal("PutSource returned nil error for failed upload")
	}
	entries, err := driver.List(ctx, "/")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name == "partial.bin" || strings.HasPrefix(entry.Name, ".qrypt-upload-") {
			t.Fatalf("failed upload left remote entry: %+v", entry)
		}
	}
}

func TestMockSFTPResumesCompletedPartsFromState(t *testing.T) {
	server := newMockSFTPServer(t)
	stateStore := drive.NewFileStateStore(t.TempDir())
	driver := New(Options{Address: server.listener.Addr().String(), Username: server.user, PrivateKey: server.keyPath, KnownHosts: server.knownHostsPath, RootPath: server.root})
	driver.InstallStateStore(stateStore)
	ctx := context.Background()
	if err := driver.Init(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := driver.Drop(ctx); err != nil {
			t.Errorf("driver.Drop() = %v", err)
		}
	})

	data := append(bytes.Repeat([]byte("a"), sftpUploadPartSize), []byte("tail")...)
	source := drive.NewBytesReadOnlyFileSource(data)
	sha256Hex, err := sourceSHA256Hex(ctx, source, int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	key := driver.uploadSessionKey(server.root, "resume.bin", int64(len(data)), sha256Hex)
	remotePath := path.Join(server.root, ".qrypt-sftp-upload-"+key)
	if err := os.WriteFile(remotePath, data[:sftpUploadPartSize], 0o600); err != nil {
		t.Fatal(err)
	}
	driver.saveUploadSession(sftpUploadSession{
		Key:            key,
		ParentID:       server.root,
		Name:           "resume.bin",
		RemotePath:     remotePath,
		Size:           int64(len(data)),
		SHA256:         sha256Hex,
		PartSize:       sftpUploadPartSize,
		CompletedParts: map[int]bool{0: true},
	})

	entry, err := driver.PutSource(ctx, drive.UploadRequest{ParentID: "/", Name: "resume.bin", Source: source})
	if err != nil {
		t.Fatal(err)
	}
	if entry.Size != int64(len(data)) {
		t.Fatalf("resumed entry size = %d, want %d", entry.Size, len(data))
	}
	got, err := os.ReadFile(filepath.Join(server.root, "resume.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("resumed file content mismatch: got %d bytes, want %d", len(got), len(data))
	}
	if _, ok := driver.loadUploadSession(key); ok {
		t.Fatal("completed upload session was not deleted")
	}
}
