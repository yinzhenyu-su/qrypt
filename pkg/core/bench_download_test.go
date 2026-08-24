package core

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/config"
	_ "github.com/yinzhenyu/qrypt/pkg/drivers/all"
)

// End-to-end download read pattern comparison: downloadOne currently issues
// one VFS.Read of the whole file and copies the returned reader to disk;
// the windowed variant reuses a fixed buffer through ReadAtInto. Both back
// onto the same localfs mount.

const benchDownloadSize = 32 << 20 // 32 MiB

func newBenchmarkCore(b *testing.B) (*Core, string) {
	tmp := b.TempDir()
	remote := filepath.Join(tmp, "remote")
	if err := os.MkdirAll(remote, 0o755); err != nil {
		b.Fatal(err)
	}
	content := make([]byte, benchDownloadSize)
	for i := range content {
		content[i] = 'a'
	}
	if err := os.WriteFile(filepath.Join(remote, "data.bin"), content, 0o644); err != nil {
		b.Fatal(err)
	}
	cfg := &config.Config{
		Mounts: []config.MountConfig{{
			Name:   "quark",
			Type:   "localfs",
			Params: config.ParamMap{"root_path": remote},
		}},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fs, cleanup, err := BuildFileSystem(ctx, cfg, Options{Runtime: testRuntimeLayout(tmp)})
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(cleanup)
	b.Cleanup(func() { stopTestVFS(b, fs) })
	fs.Start(ctx)
	return &Core{fs: fs, cleanup: cleanup}, "/quark/data.bin"
}

// downloadTaskBufferSize mirrors the legacy whole-file downloadOne copy buffer
// (512 KiB). It is kept only for the comparison baseline benchmark.
const downloadTaskBufferSize = 512 << 10

// Legacy downloadOne pattern: whole-file Read + io.CopyBuffer to the sink.
// Kept as a comparison baseline; the current downloadOne uses windowed reads.
func BenchmarkDownloadWholeFileRead(b *testing.B) {
	c, path := newBenchmarkCore(b)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b.ReportAllocs()

	for b.Loop() {
		rc, err := c.Read(ctx, path, 0, benchDownloadSize)
		if err != nil {
			b.Fatal(err)
		}
		if _, err := io.CopyBuffer(io.Discard, rc, make([]byte, downloadTaskBufferSize)); err != nil {
			b.Fatal(err)
		}
		rc.Close()
	}
}

// Current downloadOne pattern: reusable window buffer through ReadAtInto.
func BenchmarkDownloadWindowedRead(b *testing.B) {
	c, path := newBenchmarkCore(b)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	buf := make([]byte, downloadReadBufferSize)
	b.ReportAllocs()

	for b.Loop() {
		var off int64
		for off < benchDownloadSize {
			n, err := c.ReadAtInto(ctx, path, off, buf, 0)
			if err != nil {
				b.Fatal(err)
			}
			if n == 0 {
				break
			}
			off += int64(n)
		}
	}
}
