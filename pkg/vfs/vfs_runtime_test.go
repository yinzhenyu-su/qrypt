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

type noSpaceDriver struct {
	drive.UnsupportedOperations
}

func (noSpaceDriver) Init(context.Context) error { return nil }
func (noSpaceDriver) Drop(context.Context) error { return nil }
func (noSpaceDriver) List(context.Context, string) ([]drive.Entry, error) {
	return nil, drive.ErrUnsupported
}
func (noSpaceDriver) Read(context.Context, drive.Entry, int64, int64) (io.ReadCloser, error) {
	return nil, drive.ErrUnsupported
}
func (noSpaceDriver) Space(context.Context) (drive.Space, error) {
	return drive.Space{}, drive.ErrSpaceUnsupported
}
func (noSpaceDriver) Capabilities() []drive.Capability { return nil }
func (noSpaceDriver) DebugSnapshot(context.Context) (drive.DebugSnapshot, error) {
	return drive.DebugSnapshot{}, nil
}
func (noSpaceDriver) Metrics(context.Context, time.Time) ([]drive.MetricEvent, error) {
	return nil, nil
}

func TestDefaultVFSRuntimeFindsPendingUpload(t *testing.T) {
	fs, err := New(localfs.New(t.TempDir()), Options{StorageDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if err := fs.uploads.SaveUpload(PendingUpload{Path: "/pending.txt", FID: "pending", Name: "pending.txt"}); err != nil {
		t.Fatal(err)
	}

	pending, err := fs.pendingUpload("/pending.txt")
	if err != nil {
		t.Fatal(err)
	}
	if pending.FID != "pending" {
		t.Fatalf("pending = %+v", pending)
	}
	if _, err := fs.pendingUpload("/missing.txt"); err == nil {
		t.Fatal("expected missing pending upload error")
	}
}

func TestDefaultVFSRuntimeRejectsUnsupportedSpace(t *testing.T) {
	fs, err := New(noSpaceDriver{}, Options{StorageDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	_, err = fs.Space(context.Background())
	if err == nil || !strings.Contains(err.Error(), "does not support space") {
		t.Fatalf("space err = %v", err)
	}
}
