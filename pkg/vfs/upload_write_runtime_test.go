package vfs

import (
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/drivers/localfs"
)

func TestVFSUploadWriteRuntimeOwnsWriteAdapters(t *testing.T) {
	fs, err := New(localfs.New(t.TempDir()), Options{StorageDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	runtime := newVFSUploadWriteRuntime(fs)

	if runtime.Store() == nil {
		t.Fatal("expected upload write store")
	}
	if _, ok := any(runtime.HashTracker()).(vfsUploadWriteHashTracker); !ok {
		t.Fatal("expected upload write hash tracker")
	}
	if runtime.Remote() == nil {
		t.Fatal("expected upload write remote")
	}
}
