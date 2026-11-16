package sftp

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

func TestReadBodyUsesCallerLifetime(t *testing.T) {
	server := newMockSFTPServer(t)
	driver := New(Options{Address: server.listener.Addr().String(), Username: server.user, PrivateKey: server.keyPath, KnownHosts: server.knownHostsPath, RootPath: server.root})
	if err := driver.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = driver.Drop(context.Background()) })
	content := bytes.Repeat([]byte("0123456789abcdef"), 4096)
	name := filepath.Join(server.root, "read-lifetime.bin")
	if err := os.WriteFile(name, content, 0o600); err != nil {
		t.Fatal(err)
	}
	entry := drive.Entry{ID: name, Size: int64(len(content))}
	for _, deadline := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancel", true: "deadline"}[deadline], func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			if deadline {
				cancel()
				ctx, cancel = context.WithTimeout(context.Background(), time.Minute)
			}
			defer cancel()
			reader, err := driver.Read(ctx, entry, 137, 32768)
			if err != nil {
				t.Fatal(err)
			}
			defer reader.Close()
			// Inspect the actual body context without waiting 15 wall-clock seconds.
			// The opening deadline must not survive into the returned stream.
			body := reader.(*metricReadCloser).ReadCloser.(*limitedReadCloser).closer.(*contextReadCloser)
			bodyCtx := body.Reader.(contextReader).ctx
			wantDeadline, wantOK := ctx.Deadline()
			gotDeadline, gotOK := bodyCtx.Deadline()
			if gotOK != wantOK || !gotDeadline.Equal(wantDeadline) {
				t.Fatalf("body deadline = %v, %v; caller = %v, %v", gotDeadline, gotOK, wantDeadline, wantOK)
			}
			buf := make([]byte, 4096)
			if _, err := io.ReadFull(reader, buf); err != nil || !bytes.Equal(buf, content[137:137+len(buf)]) {
				t.Fatalf("range bytes differ or read failed: %v", err)
			}
			// Open a sibling before cancellation: reconnecting afterwards would
			// hide collateral damage to the shared connection.
			siblingCtx, cancelSibling := context.WithCancel(context.Background())
			defer cancelSibling()
			next, err := driver.Read(siblingCtx, entry, 4097, 1024)
			if err != nil {
				t.Fatal(err)
			}
			defer next.Close()
			cancel()
			select {
			case <-body.done:
			case <-time.After(5 * time.Second):
				t.Fatal("cancellation did not close body")
			}
			if _, err := reader.Read(buf); !errors.Is(err, context.Canceled) {
				t.Fatalf("read after caller cancellation = %v", err)
			}
			got, err := io.ReadAll(next)
			if err != nil || !bytes.Equal(got, content[4097:4097+1024]) {
				t.Fatalf("other range after cancellation: bytes=%d err=%v", len(got), err)
			}
			if err := next.Close(); err != nil {
				t.Fatal(err)
			}
			if err := siblingCtx.Err(); err != nil {
				t.Fatalf("closing body canceled its caller: %v", err)
			}
		})
	}
}
