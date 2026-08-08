package delete

import (
	"context"
	"errors"
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

type fakeDeps struct {
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

func (d *fakeDeps) BeginDelete(string, string) bool {
	d.began++
	return d.begin
}

func (d *fakeDeps) MarkDeleteActive(string, drive.Entry) {
}

func (d *fakeDeps) MarkDeleteFailed(_ string, err error) {
	d.failed++
	if err != nil {
		d.failedErr = err.Error()
	}
}

func (d *fakeDeps) MarkDeleteComplete(_ string, entry drive.Entry) {
	d.completed++
	d.completedID = entry.ID
}

func (d *fakeDeps) CancelDelete(string) {
}

func (d *fakeDeps) Remove(context.Context, drive.Entry) error {
	d.removed++
	return d.removeErr
}

func (d *fakeDeps) RecordResult(string, error) {
	d.health++
}

func (d *fakeDeps) RemoveUploadState(string) {
	d.cleanup++
}

func TestExecutorSkipsStaleDelete(t *testing.T) {
	deps := &fakeDeps{}
	NewExecutor(ExecutorDeps{Driver: deps, Overlay: deps, Health: deps, Upload: deps}).Execute(context.Background(), "/file.txt", drive.Entry{ID: "file"})
	if deps.began != 1 || deps.removed != 0 || deps.health != 0 || deps.completed != 0 || deps.cleanup != 0 {
		t.Fatalf("deps = %+v, want begin-only skip", deps)
	}
}

func TestExecutorRecordsFailure(t *testing.T) {
	deps := &fakeDeps{begin: true, removeErr: errors.New("remote unavailable")}
	NewExecutor(ExecutorDeps{Driver: deps, Overlay: deps, Health: deps, Upload: deps}).Execute(context.Background(), "/file.txt", drive.Entry{ID: "file"})
	if deps.removed != 1 || deps.health != 1 || deps.failed != 1 || deps.failedErr != "remote unavailable" || deps.completed != 0 || deps.cleanup != 0 {
		t.Fatalf("deps = %+v, want failure recorded", deps)
	}
}

func TestExecutorCompletesAndCleansUploadState(t *testing.T) {
	deps := &fakeDeps{begin: true}
	NewExecutor(ExecutorDeps{Driver: deps, Overlay: deps, Health: deps, Upload: deps}).Execute(context.Background(), "/file.txt", drive.Entry{ID: "file"})
	if deps.removed != 1 || deps.health != 1 || deps.completed != 1 || deps.completedID != "file" || deps.cleanup != 1 || deps.failed != 0 {
		t.Fatalf("deps = %+v, want success cleanup", deps)
	}
}
