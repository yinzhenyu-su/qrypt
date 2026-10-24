package core

import (
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/task"
)

// shareOutcomeResult folds a provider save outcome into the canonical task
// shape: a task state plus one tracking item per selected entry. It contains no
// provider branches: a synchronous batch, an asynchronous task and a
// monitor-handle copy all arrive as the same per-entry outcome.
func shareOutcomeResult(outcome drive.ShareSaveOutcome) (task.State, task.Tracking) {
	items := make([]task.ItemTracking, 0, len(outcome.Items))
	saved, failed := 0, 0
	for _, item := range outcome.Items {
		tracked := task.ItemTracking{
			ItemID:           item.SourceID,
			Path:             item.SourceName,
			SourceBytesTotal: item.SourceSize,
			State:            task.ItemStateSucceeded,
		}
		if item.Saved != nil {
			tracked.RemoteID = item.Saved.ID
			if tracked.Path == "" {
				tracked.Path = item.Saved.Name
			}
			tracked.SourceBytesDone = item.Saved.Size
		}
		if item.Error != nil {
			tracked.State = task.ItemStateFailed
			tracked.Error = &task.Error{
				Code:      string(item.Error.Code),
				Message:   item.Error.Message,
				Retryable: item.Error.Retryable,
			}
			failed++
		} else {
			saved++
		}
		items = append(items, tracked)
	}

	tracking := task.Tracking{Items: items}
	if !outcome.Complete() {
		return task.StateRunning, tracking
	}
	switch {
	case saved > 0 && failed == 0:
		return task.StateSucceeded, tracking
	case saved > 0 && failed > 0:
		return task.StatePartialFailed, tracking
	default:
		// Nothing was saved: the provider reported a terminal failure, or it
		// returned items that all failed.
		return task.StateFailed, tracking
	}
}

// reconcileShareOutcome makes a provider outcome cover every selected entry.
// Some providers (Quark's asynchronous save task) report only the destination
// FIDs they created, so entries missing from the response are marked failed
// instead of disappearing. Provider items are matched by source id, source
// name, then the reported destination name.
func reconcileShareOutcome(outcome drive.ShareSaveOutcome, entries []drive.ShareEntry) drive.ShareSaveOutcome {
	if !outcome.Complete() || len(entries) == 0 {
		return outcome
	}
	used := make([]bool, len(outcome.Items))
	items := make([]drive.ShareSaveItem, 0, len(entries))
	failed := 0
	for _, entry := range entries {
		index := -1
		for i, item := range outcome.Items {
			if used[i] {
				continue
			}
			if item.SourceID == entry.ID || item.SourceName == entry.Name ||
				(item.Saved != nil && item.Saved.Name == entry.Name) {
				index = i
				break
			}
		}
		if index < 0 {
			items = append(items, drive.ShareSaveItem{
				SourceID:   entry.ID,
				SourceName: entry.Name,
				SourceSize: entry.Size,
				Error:      drive.NewShareError(drive.ShareErrProvider, "", "provider did not report this entry"),
			})
			failed++
			continue
		}
		used[index] = true
		item := outcome.Items[index]
		item.SourceID = entry.ID
		item.SourceName = entry.Name
		if item.SourceSize == 0 {
			item.SourceSize = entry.Size
		}
		if item.Error != nil {
			failed++
		}
		items = append(items, item)
	}
	outcome.Items = items
	switch {
	case failed == 0:
		outcome.State = drive.SaveStateSucceeded
	case failed == len(items):
		outcome.State = drive.SaveStateFailed
	default:
		outcome.State = drive.SaveStatePartial
	}
	return outcome
}
