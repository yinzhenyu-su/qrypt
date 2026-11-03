package fs

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/internal/cli/i18n"
	"github.com/yinzhenyu/qrypt/pkg/drive"
)

type fakeGetReader struct {
	mu      sync.Mutex
	active  int
	maxSeen int
	entries map[string][]drive.Entry
	data    map[string][]byte
}

func (f *fakeGetReader) Stat(_ context.Context, path string) (drive.Entry, error) {
	if data, ok := f.data[path]; ok {
		return drive.Entry{Name: filepath.Base(path), Size: int64(len(data))}, nil
	}
	if _, ok := f.entries[path]; ok {
		return drive.Entry{Name: filepath.Base(path), IsDir: true}, nil
	}
	return drive.Entry{}, drive.ErrNotFound
}

func (f *fakeGetReader) List(_ context.Context, path string) ([]drive.Entry, error) {
	entries, ok := f.entries[path]
	if !ok {
		return nil, drive.ErrNotFound
	}
	return entries, nil
}

func (f *fakeGetReader) Read(_ context.Context, path string, offset, size int64) (io.ReadCloser, error) {
	f.mu.Lock()
	f.active++
	if f.active > f.maxSeen {
		f.maxSeen = f.active
	}
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		f.active--
		f.mu.Unlock()
	}()
	time.Sleep(5 * time.Millisecond)

	data := f.data[path]
	if offset >= int64(len(data)) {
		return io.NopCloser(bytes.NewReader(nil)), nil
	}
	end := int64(len(data))
	if size > 0 && offset+size < end {
		end = offset + size
	}
	return io.NopCloser(bytes.NewReader(data[offset:end])), nil
}

func TestGetDirDownloadsWithBoundedWorkers(t *testing.T) {
	files := make(map[string][]byte)
	entries := make(map[string][]drive.Entry)
	root := make([]drive.Entry, 0, 7)
	for i := 0; i < 6; i++ {
		name := "f" + string(rune('0'+i)) + ".txt"
		files["/dir/"+name] = []byte("content-" + name)
		root = append(root, drive.Entry{Name: name, Size: int64(len(files["/dir/"+name]))})
	}
	files["/dir/sub/g.txt"] = []byte("content-g")
	root = append(root, drive.Entry{Name: "sub", IsDir: true})
	entries["/dir"] = root
	entries["/dir/sub"] = []drive.Entry{{Name: "g.txt", Size: int64(len(files["/dir/sub/g.txt"]))}}
	entries["/dir/sub/g.txt"] = nil

	reader := &fakeGetReader{entries: entries, data: files}
	local := filepath.Join(t.TempDir(), "out")
	bar := newProgressBar(7, i18n.New(i18n.LangEN), io.Discard)

	if err := getDir(context.Background(), reader, "/dir", local, false, bar); err != nil {
		t.Fatal(err)
	}
	for path, want := range files {
		localPath := filepath.Join(local, filepath.FromSlash(path[len("/dir/"):]))
		got, err := os.ReadFile(localPath)
		if err != nil {
			t.Fatalf("read %s: %v", localPath, err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("%s = %q, want %q", localPath, got, want)
		}
	}
	bar.mu.Lock()
	completed, skipped := bar.completed, bar.skipped
	bar.mu.Unlock()
	if completed != 7 || skipped != 0 {
		t.Fatalf("progress completed=%d skipped=%d, want 7/0", completed, skipped)
	}
	reader.mu.Lock()
	maxSeen := reader.maxSeen
	reader.mu.Unlock()
	if maxSeen > downloadWorkers {
		t.Fatalf("max concurrent downloads = %d, want at most %d", maxSeen, downloadWorkers)
	}
}
