package vfs

import (
	"context"
	"errors"
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/drivers/localfs"
)

type fakeDeleteRemoteRuntime struct {
	begin       bool
	removeErr   error
	began       int
	removed     int
	health      int
	failed      int
	completed   int
	cleanup     int
	failedErr   string
	completedID string
}

func (r *fakeDeleteRemoteRuntime) BeginRemoteDelete(string, drive.Entry) bool {
	r.began++
	return r.begin
}

func (r *fakeDeleteRemoteRuntime) RemoveRemote(context.Context, drive.Entry) error {
	r.removed++
	return r.removeErr
}

func (r *fakeDeleteRemoteRuntime) RecordDeleteHealth(error) {
	r.health++
}

func (r *fakeDeleteRemoteRuntime) MarkRemoteDeleteFailed(_ string, err error) {
	r.failed++
	if err != nil {
		r.failedErr = err.Error()
	}
}

func (r *fakeDeleteRemoteRuntime) MarkRemoteDeleteComplete(_ string, entry drive.Entry) {
	r.completed++
	r.completedID = entry.ID
}

func (r *fakeDeleteRemoteRuntime) CleanupUploadState(string) {
	r.cleanup++
}

func TestDeleteRemoteWithRuntimeSkipsStaleDelete(t *testing.T) {
	fs, err := New(localfs.New(t.TempDir()), Options{StorageDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	runtime := &fakeDeleteRemoteRuntime{}
	fs.deleteRemoteWithRuntime(context.Background(), "/file.txt", drive.Entry{ID: "file"}, runtime)
	if runtime.began != 1 || runtime.removed != 0 || runtime.health != 0 || runtime.completed != 0 || runtime.cleanup != 0 {
		t.Fatalf("runtime = %+v, want begin-only skip", runtime)
	}
}

func TestDeleteRemoteWithRuntimeRecordsFailure(t *testing.T) {
	fs, err := New(localfs.New(t.TempDir()), Options{StorageDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	runtime := &fakeDeleteRemoteRuntime{begin: true, removeErr: errors.New("remote unavailable")}
	fs.deleteRemoteWithRuntime(context.Background(), "/file.txt", drive.Entry{ID: "file"}, runtime)
	if runtime.removed != 1 || runtime.health != 1 || runtime.failed != 1 || runtime.failedErr != "remote unavailable" || runtime.completed != 0 || runtime.cleanup != 0 {
		t.Fatalf("runtime = %+v, want failure recorded", runtime)
	}
}

func TestDeleteRemoteWithRuntimeCompletesAndCleansUploadState(t *testing.T) {
	fs, err := New(localfs.New(t.TempDir()), Options{StorageDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	runtime := &fakeDeleteRemoteRuntime{begin: true}
	fs.deleteRemoteWithRuntime(context.Background(), "/file.txt", drive.Entry{ID: "file"}, runtime)
	if runtime.removed != 1 || runtime.health != 1 || runtime.completed != 1 || runtime.completedID != "file" || runtime.cleanup != 1 || runtime.failed != 0 {
		t.Fatalf("runtime = %+v, want success cleanup", runtime)
	}
}
