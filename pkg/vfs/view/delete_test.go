package view

import (
	"strings"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

func TestDeleteSchedulerSchedulesAndClearsFailure(t *testing.T) {
	v, overlay, deleteState := newTestDomain(t)
	vis := NewVisibility(overlay, deleteState, v, nil)
	deleteState.SetFailure("/file.txt", "old failure")

	fired := make(chan struct{}, 1)
	vis.ScheduleDelete("/file.txt", time.Hour, func() { fired <- struct{}{} })
	if !deleteState.Scheduled("/file.txt") {
		t.Fatal("path should be scheduled")
	}
	if _, ok := deleteState.Failure("/file.txt"); ok {
		t.Fatal("failure should be cleared by schedule")
	}
	select {
	case <-fired:
		t.Fatal("delete timer fired too early")
	default:
	}
	deleteState.StopAll()
}

func TestDeleteSchedulerCancelsChildDeletes(t *testing.T) {
	v, overlay, deleteState := newTestDomain(t)
	vis := NewVisibility(overlay, deleteState, v, nil)
	vis.ScheduleDelete("/dir/a.txt", time.Hour, func() {})
	vis.ScheduleDelete("/other.txt", time.Hour, func() {})
	vis.MarkDeleted("/dir/a.txt", drive.Entry{ID: "a"})
	vis.MarkDeleted("/other.txt", drive.Entry{ID: "other"})
	deleteState.SetFailure("/dir/a.txt", "failed")

	vis.CancelChildren("/dir")
	if deleteState.Scheduled("/dir/a.txt") || vis.IsDeleted("/dir/a.txt") {
		t.Fatal("child delete should be cancelled")
	}
	if _, ok := deleteState.Failure("/dir/a.txt"); ok {
		t.Fatal("child failure should be cleared")
	}
	if !deleteState.Scheduled("/other.txt") || !vis.IsDeleted("/other.txt") {
		t.Fatal("sibling delete should be preserved")
	}
	deleteState.StopAll()
}

func TestDeleteSchedulerStopsAllTimers(t *testing.T) {
	v, overlay, deleteState := newTestDomain(t)
	vis := NewVisibility(overlay, deleteState, v, nil)
	vis.ScheduleDelete("/a.txt", time.Hour, func() {})
	vis.ScheduleDelete("/b.txt", time.Hour, func() {})
	deleteState.StopAll()
	if deleteState.Scheduled("/a.txt") || deleteState.Scheduled("/b.txt") {
		t.Fatal("timers should be stopped")
	}
}

func TestDeleteTaskRecordsProjectStateFlags(t *testing.T) {
	v, overlay, deleteState := newTestDomain(t)
	vis := NewVisibility(overlay, deleteState, v, nil)
	vis.MarkDeleted("/scheduled.txt", drive.Entry{ID: "scheduled", Name: "scheduled.txt"})
	vis.MarkDeleted("/running.txt", drive.Entry{ID: "running", Name: "running.txt"})
	vis.MarkDeleted("/failed.txt", drive.Entry{ID: "failed", Name: "failed.txt"})
	deleteState.Schedule("/scheduled.txt", time.Hour, func() {})
	deleteState.SetActive("/running.txt", drive.Entry{ID: "running", Name: "running.txt"})
	deleteState.SetFailure("/failed.txt", "remote failed")
	defer deleteState.StopAll()

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
