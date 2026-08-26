package view

import (
	"strings"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

func TestDeleteSchedulerSchedulesAndClearsFailure(t *testing.T) {
	v, overlay, tasks := newTestDomain(t)
	vis := NewVisibility(overlay, tasks, v, nil)
	tasks.SetFailure("/file.txt", "old failure")

	fired := make(chan struct{}, 1)
	vis.ScheduleDelete("/file.txt", time.Hour, func() { fired <- struct{}{} })
	if !tasks.Scheduled("/file.txt") {
		t.Fatal("path should be scheduled")
	}
	if _, ok := tasks.Failure("/file.txt"); ok {
		t.Fatal("failure should be cleared by schedule")
	}
	select {
	case <-fired:
		t.Fatal("delete timer fired too early")
	default:
	}
	tasks.StopAll()
}

func TestDeleteSchedulerCancelsChildDeletes(t *testing.T) {
	v, overlay, tasks := newTestDomain(t)
	vis := NewVisibility(overlay, tasks, v, nil)
	vis.ScheduleDelete("/dir/a.txt", time.Hour, func() {})
	vis.ScheduleDelete("/other.txt", time.Hour, func() {})
	vis.MarkDeleted("/dir/a.txt", drive.Entry{ID: "a"})
	vis.MarkDeleted("/other.txt", drive.Entry{ID: "other"})
	tasks.SetFailure("/dir/a.txt", "failed")

	vis.CancelChildren("/dir")
	if tasks.Scheduled("/dir/a.txt") || vis.IsDeleted("/dir/a.txt") {
		t.Fatal("child delete should be cancelled")
	}
	if _, ok := tasks.Failure("/dir/a.txt"); ok {
		t.Fatal("child failure should be cleared")
	}
	if !tasks.Scheduled("/other.txt") || !vis.IsDeleted("/other.txt") {
		t.Fatal("sibling delete should be preserved")
	}
	tasks.StopAll()
}

func TestDeleteSchedulerStopsAllTimers(t *testing.T) {
	v, overlay, tasks := newTestDomain(t)
	vis := NewVisibility(overlay, tasks, v, nil)
	vis.ScheduleDelete("/a.txt", time.Hour, func() {})
	vis.ScheduleDelete("/b.txt", time.Hour, func() {})
	tasks.StopAll()
	if tasks.Scheduled("/a.txt") || tasks.Scheduled("/b.txt") {
		t.Fatal("timers should be stopped")
	}
}

func TestDeleteTaskRecordsProjectStateFlags(t *testing.T) {
	v, overlay, tasks := newTestDomain(t)
	vis := NewVisibility(overlay, tasks, v, nil)
	vis.MarkDeleted("/scheduled.txt", drive.Entry{ID: "scheduled", Name: "scheduled.txt"})
	vis.MarkDeleted("/running.txt", drive.Entry{ID: "running", Name: "running.txt"})
	vis.MarkDeleted("/failed.txt", drive.Entry{ID: "failed", Name: "failed.txt"})
	tasks.Schedule("/scheduled.txt", time.Hour, func() {})
	tasks.SetActive("/running.txt", drive.Entry{ID: "running", Name: "running.txt"})
	tasks.SetFailure("/failed.txt", "remote failed")
	defer tasks.StopAll()

	records := vis.DeleteTaskRecords()
	states := map[string]DeleteRecord{}
	for _, record := range records {
		states[record.Path] = record
	}

	running := states["/running.txt"]
	if !running.Running || running.Scheduled {
		t.Fatalf("running record = %+v", running)
	}
	scheduled := states["/scheduled.txt"]
	if scheduled.Running || !scheduled.Scheduled {
		t.Fatalf("scheduled record = %+v", scheduled)
	}
	failed := states["/failed.txt"]
	if failed.Running || failed.Scheduled || failed.ErrorText != "remote failed" {
		t.Fatalf("failed record = %+v", failed)
	}
	// IDs carry the delete-task prefix and the remote ID.
	if !strings.HasPrefix(running.ID, "delete:") || running.ID != "delete:running" {
		t.Fatalf("record ID = %q", running.ID)
	}
}
