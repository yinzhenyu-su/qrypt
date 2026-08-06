package vfs

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/drivers/localfs"
)

type fakeUploadWriteRemote struct {
	entry drive.Entry
	data  string
	reads int
}

func (r *fakeUploadWriteRemote) Parent(context.Context, string) (drive.Entry, string, error) {
	return drive.Entry{ID: "parent", Name: "parent", IsDir: true}, "file.txt", nil
}

func (r *fakeUploadWriteRemote) Resolve(context.Context, string) (drive.Entry, error) {
	return r.entry, nil
}

func (r *fakeUploadWriteRemote) Read(context.Context, drive.Entry) (io.ReadCloser, error) {
	r.reads++
	return io.NopCloser(strings.NewReader(r.data)), nil
}

func (r *fakeUploadWriteRemote) InvalidateReadCache(drive.Entry) {}

func TestVFSStageExistingUsesUploadWriteRemote(t *testing.T) {
	fs, err := New(localfs.New(t.TempDir()), Options{StorageDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	store := newUploadStoreWriteAdapter(fs.upload.store)
	modTime := time.Now().Add(-time.Hour).Truncate(time.Second)
	remote := &fakeUploadWriteRemote{
		entry: drive.Entry{ID: "remote", Name: "file.txt", Size: 5, ModTime: modTime},
		data:  "hello",
	}

	if err := fs.stageExistingWithDeps(context.Background(), "/file.txt", store, remote); err != nil {
		t.Fatal(err)
	}
	if remote.reads != 1 {
		t.Fatalf("remote reads = %d, want 1", remote.reads)
	}
	pending, ok := fs.upload.store.UploadByPath("/file.txt")
	if !ok {
		t.Fatal("pending upload not recorded")
	}
	if pending.ParentID != "parent" || pending.Name != "file.txt" || pending.Size != 5 {
		t.Fatalf("pending = %+v", pending)
	}
	if got := uploadModTime(pending); !got.Equal(modTime) {
		t.Fatalf("pending mod time = %v, want %v", got, modTime)
	}
}
