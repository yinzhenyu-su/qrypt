package sftp

import (
	"context"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

func TestReadAllLimitedTerminates(t *testing.T) {
	server := newMockSFTPServer(t)
	driver := New(Options{Address: server.listener.Addr().String(), Username: server.user, PrivateKey: server.keyPath, KnownHosts: server.knownHostsPath, RootPath: server.root})
	ctx := context.Background()
	if err := driver.Init(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = driver.Drop(ctx)
	})

	entry, err := driver.PutSource(ctx, drive.UploadRequest{ParentID: "/", Name: "._.DS_Store", Source: drive.NewBytesReadOnlyFileSource([]byte("abc"))})
	if err != nil {
		t.Fatal(err)
	}

	rc, err := driver.Read(ctx, entry, 0, 4096)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()

	done := make(chan error, 1)
	go func() {
		data, readErr := io.ReadAll(rc)
		if readErr == nil && string(data) != "abc" {
			readErr = fmt.Errorf("unexpected data %q", data)
		}
		done <- readErr
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("ReadAll = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("ReadAll on a small sftp file must terminate; it busy-loops when EOF is swallowed as (0, nil)")
	}
}

func TestReadAtOrPastEOFReadAllTerminates(t *testing.T) {
	server := newMockSFTPServer(t)
	driver := New(Options{Address: server.listener.Addr().String(), Username: server.user, PrivateKey: server.keyPath, KnownHosts: server.knownHostsPath, RootPath: server.root})
	ctx := context.Background()
	if err := driver.Init(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = driver.Drop(ctx)
	})

	entry, err := driver.PutSource(ctx, drive.UploadRequest{ParentID: "/", Name: "small.bin", Source: drive.NewBytesReadOnlyFileSource([]byte("abcdef"))})
	if err != nil {
		t.Fatal(err)
	}

	for _, offset := range []int64{6, 10} {
		rc, err := driver.Read(ctx, entry, offset, 4096)
		if err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func(offset int64) {
			data, readErr := io.ReadAll(rc)
			_ = rc.Close()
			if readErr == nil && len(data) != 0 {
				readErr = fmt.Errorf("offset %d: expected empty read, got %q", offset, data)
			}
			done <- readErr
		}(offset)
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("offset %d: ReadAll = %v", offset, err)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("offset %d: ReadAll must terminate", offset)
		}
	}
}
