package vfs

import (
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/drivers/localfs"
	"github.com/yinzhenyu/qrypt/pkg/task"
)

func TestVFSDeleteTaskRuntimeBuildsRecordsFromDeleteState(t *testing.T) {
	fs, err := New(localfs.New(t.TempDir()), Options{StorageDir: t.TempDir(), DeleteDelay: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	runtime := newVFSDeleteTaskRuntime(fs)
	fs.view.overlay.mu.Lock()
	fs.view.overlay.deleted["/scheduled.txt"] = drive.Entry{ID: "scheduled", Name: "scheduled.txt"}
	fs.view.overlay.deleted["/running.txt"] = drive.Entry{ID: "running", Name: "running.txt"}
	fs.view.overlay.deleted["/failed.txt"] = drive.Entry{ID: "failed", Name: "failed.txt"}
	fs.deletes.tasks.timers["/scheduled.txt"] = time.AfterFunc(time.Hour, func() {})
	fs.deletes.tasks.active["/running.txt"] = drive.Entry{ID: "running", Name: "running.txt"}
	fs.deletes.tasks.failures["/failed.txt"] = "remote failed"
	fs.view.overlay.mu.Unlock()
	defer newVFSDeleteScheduler(fs).StopAll()

	records := runtime.Records()
	states := map[string]task.State{}
	errors := map[string]string{}
	for _, record := range records {
		states[record.path] = record.state
		errors[record.path] = record.errorText
	}
	if states["/scheduled.txt"] != task.StateScheduled {
		t.Fatalf("scheduled state = %s", states["/scheduled.txt"])
	}
	if states["/running.txt"] != task.StateRunning {
		t.Fatalf("running state = %s", states["/running.txt"])
	}
	if states["/failed.txt"] != task.StateFailed || errors["/failed.txt"] != "remote failed" {
		t.Fatalf("failed state=%s err=%q", states["/failed.txt"], errors["/failed.txt"])
	}
}

func TestVFSDeleteTaskRuntimeRetryClearsFailureAndSchedulesDelete(t *testing.T) {
	fs, err := New(localfs.New(t.TempDir()), Options{StorageDir: t.TempDir(), DeleteDelay: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	runtime := newVFSDeleteTaskRuntime(fs)
	record := deleteTaskRecord{
		path:  "/failed.txt",
		entry: drive.Entry{ID: "failed", Name: "failed.txt"},
	}
	fs.view.overlay.mu.Lock()
	fs.deletes.tasks.failures[record.path] = "remote failed"
	fs.view.overlay.mu.Unlock()
	defer newVFSDeleteScheduler(fs).StopAll()

	runtime.Retry(record)
	fs.view.overlay.mu.Lock()
	_, failure := fs.deletes.tasks.failures[record.path]
	_, timer := fs.deletes.tasks.timers[record.path]
	fs.view.overlay.mu.Unlock()
	if failure {
		t.Fatal("failure should be cleared")
	}
	if !timer {
		t.Fatal("retry should schedule delete")
	}
}
