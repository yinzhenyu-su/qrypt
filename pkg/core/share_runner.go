package core

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/task"
)

const (
	defaultSharePollInterval   = 500 * time.Millisecond
	defaultShareVerifyInterval = 300 * time.Millisecond
	maxShareListPages          = 50
	defaultShareVerifyAttempts = 10
)

// shareRun is one execution of a share task. It rebuilds itself from the
// persisted detail, so a retry or a restart resumes provider work instead of
// depending on in-process state.
type shareRun struct {
	core     *Core
	taskID   string
	detail   shareDetail
	driver   drive.Driver
	receiver drive.ShareReceiver
	saver    drive.ShareSaver

	pollInterval   time.Duration
	verifyInterval time.Duration
	verifyAttempts int
}

func (c *Core) newShareRun(ctx context.Context, taskID string) (*shareRun, error) {
	current, err := c.GetTask(ctx, taskID)
	if err != nil {
		return nil, err
	}
	detail, ok := decodeShareDetail(current.Detail)
	if !ok {
		return nil, fmt.Errorf("core: share task %q has no share detail", taskID)
	}
	driver, ok := c.offlineDriver(detail.Mount)
	if !ok {
		return nil, fmt.Errorf("%w: mount %q", ErrShareMountNotFound, detail.Mount)
	}
	receiver, ok := driver.(drive.ShareReceiver)
	if !ok {
		return nil, fmt.Errorf("%w: mount %q", ErrShareUnsupported, detail.Mount)
	}
	// A direct fetch never saves provider-side, so it must not require
	// ShareSave. Only a task that asked for a cloud destination carries a saver.
	var saver drive.ShareSaver
	if detail.ToRequested {
		saver, ok = driver.(drive.ShareSaver)
		if !ok {
			return nil, fmt.Errorf("%w: mount %q", ErrShareUnsupported, detail.Mount)
		}
	}
	run := &shareRun{
		core:           c,
		taskID:         taskID,
		detail:         detail,
		driver:         driver,
		receiver:       receiver,
		saver:          saver,
		pollInterval:   defaultSharePollInterval,
		verifyInterval: defaultShareVerifyInterval,
		verifyAttempts: defaultShareVerifyAttempts,
	}
	if c.sharePollInterval > 0 {
		run.pollInterval = c.sharePollInterval
	}
	if c.shareVerifyInterval > 0 {
		run.verifyInterval = c.shareVerifyInterval
	}
	return run, nil
}

// runShareTask is the manager run function for both share task types.
func (c *Core) runShareTask(ctx context.Context, update task.UpdateFunc, taskID string, fetch bool) error {
	if !c.beginOfflineRun(taskID) {
		return fmt.Errorf("core: share task %q already has an active runner", taskID)
	}
	defer c.endOfflineRun(taskID)
	run, err := c.newShareRun(ctx, taskID)
	if err != nil {
		return err
	}
	if fetch {
		return run.runFetch(ctx, update)
	}
	return run.runSave(ctx, update)
}

// runSave opens the link, selects entries, saves them provider-side, verifies
// the destination, and folds the per-entry outcome into the task.
func (r *shareRun) runSave(ctx context.Context, update task.UpdateFunc) error {
	if r.saver == nil {
		return r.fail(update, fmt.Errorf("%w: mount %q", ErrShareUnsupported, r.detail.Mount))
	}
	if r.detail.Phase == "" {
		r.detail.Phase = sharePhaseOpen
	}
	// A retry or restart after the provider save already finished must not save
	// again: only the destination verification is outstanding.
	if r.canResumeVerification() {
		if r.verifySavedEntries(ctx) {
			return r.finishFromDetail(update)
		}
		r.markVerified(false)
		r.persist(update)
		return r.fail(update, drive.NewShareError(drive.ShareErrDestinationUnverified, "",
			"saved entries did not appear in the destination"))
	}

	if r.detail.SaveHandle != "" {
		// Resume a submitted provider save first. The persisted selection lets
		// polling continue even when the link has expired or its listing
		// changed; only a task from before the selection was persisted falls
		// back to reopening the link.
		selected, err := r.resumeSelection(ctx, update)
		if err != nil {
			return r.fail(update, err)
		}
		update(func(item *task.Task) {
			item.Progress.ItemsTotal = int64(len(selected))
		})
		r.detail.Phase = sharePhaseSave
		r.persist(update)
		outcome, resumed, err := r.resumeSave(ctx, update, selected)
		if err != nil {
			return r.fail(update, err)
		}
		if resumed {
			outcome = reconcileShareOutcome(outcome, selected)
			return r.finishSave(ctx, update, outcome)
		}
		// The provider explicitly no longer knows the handle: submit the same
		// selection again. The provider's own de-duplication keeps this safe.
		session, err := r.openSession(ctx, update)
		if err != nil {
			return r.fail(update, err)
		}
		outcome, err = r.saveSelected(ctx, update, session, selected)
		if err != nil {
			return r.fail(update, err)
		}
		return r.finishSave(ctx, update, outcome)
	}

	session, err := r.openSession(ctx, update)
	if err != nil {
		return r.fail(update, err)
	}
	r.detail.Phase = sharePhaseSave
	r.persist(update)
	selected, err := r.listSelected(ctx, session)
	if err != nil {
		return r.fail(update, err)
	}
	update(func(item *task.Task) {
		item.Progress.ItemsTotal = int64(len(selected))
	})
	outcome, err := r.saveSelected(ctx, update, session, selected)
	if err != nil {
		return r.fail(update, err)
	}
	return r.finishSave(ctx, update, outcome)
}

// resumeSave re-attaches to a provider save persisted by an earlier run. When
// the provider no longer knows the handle, the caller starts a fresh save; the
// provider's own de-duplication keeps that from creating a second copy.
func (r *shareRun) resumeSave(ctx context.Context, update task.UpdateFunc, entries []drive.ShareEntry) (drive.ShareSaveOutcome, bool, error) {
	outcome, err := r.saver.GetShareSave(ctx, r.detail.SaveHandle, entries)
	if err != nil {
		if drive.IsShareErrorCode(err, drive.ShareErrNotFound) {
			// Only an explicitly missing handle may fall back to a new submit.
			// A timeout, throttle, or transport failure leaves a provider task
			// that may still be running, so keep the handle and let the caller
			// surface a retryable failure.
			r.detail.SaveHandle = ""
			r.persist(update)
			return drive.ShareSaveOutcome{}, false, nil
		}
		return drive.ShareSaveOutcome{}, false, err
	}
	if !outcome.Complete() {
		outcome, err = r.pollSave(ctx, update, r.detail.SaveHandle, entries)
		if err != nil {
			return drive.ShareSaveOutcome{}, false, err
		}
	}
	return outcome, true, nil
}

// resumeSelection returns the selection a persisted save was submitted with.
// It reads durable detail first; only a task that predates that detail needs to
// reopen and list the share.
func (r *shareRun) resumeSelection(ctx context.Context, update task.UpdateFunc) ([]drive.ShareEntry, error) {
	if entries := shareSelectedDriveEntries(r.detail.SelectedEntries); len(entries) > 0 {
		return entries, nil
	}
	session, err := r.openSession(ctx, update)
	if err != nil {
		return nil, err
	}
	entries, err := r.listSelected(ctx, session)
	if err != nil {
		return nil, err
	}
	r.detail.SelectedEntries = shareSelectedEntries(entries)
	r.persist(update)
	return entries, nil
}

func (r *shareRun) pollSave(ctx context.Context, update task.UpdateFunc, handle string, entries []drive.ShareEntry) (drive.ShareSaveOutcome, error) {
	for {
		select {
		case <-ctx.Done():
			return drive.ShareSaveOutcome{}, ctx.Err()
		case <-time.After(r.pollInterval):
		}
		outcome, err := r.saver.GetShareSave(ctx, handle, entries)
		if err != nil {
			return drive.ShareSaveOutcome{}, err
		}
		r.recordOutcome(outcome)
		r.persist(update)
		if outcome.Complete() {
			return outcome, nil
		}
	}
}

// finishSave verifies the destination and folds the provider outcome into the
// task state and items.
func (r *shareRun) finishSave(ctx context.Context, update task.UpdateFunc, outcome drive.ShareSaveOutcome) error {
	state, _ := shareOutcomeResult(outcome)
	r.recordOutcome(outcome)
	if state == task.StateFailed {
		r.detail.Phase = sharePhaseComplete
		r.persist(update)
		return r.fail(update, shareOutcomeError(outcome))
	}

	r.detail.Phase = sharePhaseVerify
	r.persist(update)
	verified := r.verifyDestination(ctx, outcome)
	r.markVerified(verified)

	state, tracking := shareOutcomeResult(outcome)
	r.detail.Phase = sharePhaseComplete
	update(func(item *task.Task) {
		r.detail.apply(item)
		item.Progress.Phase = task.PhaseComplete
		item.Progress.ItemsDone = int64(savedEntryCount(outcome))
		item.Tracking = tracking
		if state == task.StatePartialFailed {
			item.State = task.StatePartialFailed
		}
	})
	if !verified {
		return r.fail(update, drive.NewShareError(drive.ShareErrDestinationUnverified, "",
			"saved entries did not appear in the destination"))
	}
	return nil
}

// canResumeVerification reports whether the previous attempt already saved
// entries and only verification is outstanding.
func (r *shareRun) canResumeVerification() bool {
	if r.detail.Phase != sharePhaseVerify && r.detail.Phase != sharePhaseComplete {
		return false
	}
	for _, saved := range r.detail.Saved {
		if saved.ErrorCode == "" {
			return true
		}
	}
	return false
}

// shareVerifyTargets are the identities a destination listing may match: the
// name a provider reported and the remote id it assigned. At least one of them
// is required to verify a saved entry; an entry with neither cannot be proven
// to exist.
type shareVerifyTargets struct {
	names map[string]struct{}
	ids   map[string]struct{}
}

func newShareVerifyTargets() shareVerifyTargets {
	return shareVerifyTargets{
		names: map[string]struct{}{},
		ids:   map[string]struct{}{},
	}
}

// add records one saved entry's identity. It reports false when the entry has
// no name and no id, because such an entry can never be verified.
func (t shareVerifyTargets) add(name, id string) bool {
	name = strings.TrimSpace(name)
	id = strings.TrimSpace(id)
	if name != "" {
		t.names[name] = struct{}{}
	}
	if id != "" {
		t.ids[id] = struct{}{}
	}
	return name != "" || id != ""
}

func (t shareVerifyTargets) empty() bool {
	return len(t.names) == 0 && len(t.ids) == 0
}

func (t shareVerifyTargets) match(entry drive.Entry) {
	if entry.Name != "" {
		delete(t.names, entry.Name)
	}
	if entry.ID != "" {
		delete(t.ids, entry.ID)
	}
}

// verifySavedEntries re-verifies the entries the previous attempt saved.
func (r *shareRun) verifySavedEntries(ctx context.Context) bool {
	targets := newShareVerifyTargets()
	for _, saved := range r.detail.Saved {
		if saved.ErrorCode != "" {
			continue
		}
		if !targets.add(saved.Name, saved.RemoteID) {
			return false
		}
	}
	if targets.empty() {
		return false
	}
	return r.waitForTargets(ctx, targets)
}

// verifyDestination confirms that the entries the provider reported as saved
// are actually listed in the destination. Provider listings are eventually
// consistent, so it retries a bounded number of times. It never treats "the
// provider returned no verifiable identity" as success.
func (r *shareRun) verifyDestination(ctx context.Context, outcome drive.ShareSaveOutcome) bool {
	targets := newShareVerifyTargets()
	saved := 0
	for _, item := range outcome.Items {
		if item.Error != nil || item.Saved == nil {
			continue
		}
		saved++
		if !targets.add(item.Saved.Name, item.Saved.ID) {
			return false
		}
	}
	if saved == 0 || targets.empty() {
		return false
	}
	return r.waitForTargets(ctx, targets)
}

func (r *shareRun) waitForTargets(ctx context.Context, targets shareVerifyTargets) bool {
	for attempt := 0; attempt < r.verifyAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return false
			case <-time.After(r.verifyInterval):
			}
		}
		entries, err := r.driver.List(ctx, r.detail.ToEntry.ID)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			targets.match(entry)
		}
		if targets.empty() {
			return true
		}
	}
	return false
}

// finishFromDetail completes a task whose provider save already happened,
// rebuilding the item projection from the persisted per-entry outcome.
func (r *shareRun) finishFromDetail(update task.UpdateFunc) error {
	items := make([]task.ItemTracking, 0, len(r.detail.Saved))
	saved, failed := 0, 0
	for _, entry := range r.detail.Saved {
		item := task.ItemTracking{
			ItemID:           entry.SourceID,
			Path:             entry.Name,
			RemoteID:         entry.RemoteID,
			SourceBytesTotal: entry.Size,
			State:            task.ItemStateSucceeded,
		}
		if entry.ErrorCode != "" {
			item.State = task.ItemStateFailed
			item.Error = &task.Error{Code: entry.ErrorCode, Message: entry.ErrorMessage, Retryable: entry.Retryable}
			failed++
		} else {
			saved++
		}
		items = append(items, item)
	}
	state := task.StateSucceeded
	switch {
	case saved == 0 && failed > 0:
		state = task.StateFailed
	case saved > 0 && failed > 0:
		state = task.StatePartialFailed
	}
	r.detail.Phase = sharePhaseComplete
	r.markVerified(true)
	update(func(item *task.Task) {
		r.detail.apply(item)
		item.Error = nil
		item.Progress.Phase = task.PhaseComplete
		item.Progress.ItemsTotal = int64(len(items))
		item.Progress.ItemsDone = int64(saved)
		item.Tracking = task.Tracking{Items: items}
		item.State = state
	})
	if state == task.StateFailed {
		return r.fail(update, shareDetailError(r.detail.Saved))
	}
	return nil
}

func shareDetailError(entries []shareSavedEntry) error {
	for _, entry := range entries {
		if entry.ErrorCode != "" {
			return drive.NewShareError(drive.ShareErrorCode(entry.ErrorCode), "", entry.ErrorMessage)
		}
	}
	return drive.NewShareError(drive.ShareErrProvider, "", "provider reported a failed save without a reason")
}

func (r *shareRun) markVerified(verified bool) {
	if r.detail.Observation == nil {
		r.detail.Observation = &shareObservation{}
	}
	r.detail.Observation.DestinationVerified = verified
	r.detail.Observation.UpdatedAt = time.Now().UTC()
}

func (r *shareRun) listAll(ctx context.Context, session drive.ShareSession) ([]drive.ShareEntry, error) {
	var entries []drive.ShareEntry
	dir := drive.SharePath{ID: "0"}
	cursor := ""
	for page := 0; page < maxShareListPages; page++ {
		result, err := r.receiver.ListShareEntries(ctx, session, dir, cursor)
		if err != nil {
			return nil, err
		}
		entries = append(entries, result.Entries...)
		if result.Next == "" {
			return entries, nil
		}
		cursor = result.Next
	}
	return entries, nil
}

// selected applies the neutral selection to the share's top-level entries.
func (r *shareRun) selected(entries []drive.ShareEntry) []drive.ShareEntry {
	files := make([]drive.OfflineFile, len(entries))
	for i, entry := range entries {
		files[i] = drive.OfflineFile{Name: entry.Name, Size: entry.Size}
	}
	flags := offlineSelectIndexes(files, r.detail.selectionTask())
	out := make([]drive.ShareEntry, 0, len(entries))
	for i, ok := range flags {
		if ok {
			out = append(out, entries[i])
		}
	}
	return out
}

func (r *shareRun) recordOutcome(outcome drive.ShareSaveOutcome) {
	r.detail.SaveState = string(outcome.State)
	r.detail.Saved = shareSavedEntries(outcome)
	if r.detail.Observation == nil {
		r.detail.Observation = &shareObservation{}
	}
	observation := r.detail.Observation
	observation.State = string(outcome.State)
	observation.Progress = outcome.Progress
	observation.UpdatedAt = time.Now().UTC()
	for _, item := range outcome.Items {
		if item.Error != nil {
			observation.ErrorCode = string(item.Error.Code)
			observation.ProviderError = item.Error.Message
			break
		}
	}
}

func (r *shareRun) persist(update task.UpdateFunc) {
	update(func(item *task.Task) {
		r.detail.apply(item)
		item.Progress.Phase = r.detail.Phase.display()
	})
}

func (r *shareRun) fail(update task.UpdateFunc, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	code, message, retryable := shareFailureFields(err)
	update(func(item *task.Task) {
		item.Error = &task.Error{Code: code, Message: message, Retryable: retryable}
		item.Capabilities.Retryable = true
	})
	return err
}

func shareFailureFields(err error) (string, string, bool) {
	if share, ok := drive.AsShareError(err); ok {
		return string(share.Code), share.Message, share.Retryable
	}
	switch {
	case errors.Is(err, ErrShareLinkInvalid):
		return string(drive.ShareErrLinkInvalid), err.Error(), false
	case errors.Is(err, ErrShareLinkProviderMismatch):
		return string(drive.ShareErrLinkProviderMismatch), err.Error(), false
	case errors.Is(err, ErrShareCrossProviderUnsupported):
		return string(drive.ShareErrCrossProviderUnsupported), err.Error(), false
	case errors.Is(err, ErrShareUnsupported):
		return string(drive.ShareErrUnsupported), err.Error(), false
	case errors.Is(err, ErrShareMountNotFound):
		return string(drive.ShareErrUnsupported), err.Error(), false
	case errors.Is(err, ErrShareDestinationMissing):
		return string(drive.ShareErrUnsupported), err.Error(), false
	default:
		return string(drive.ShareErrProvider), err.Error(), false
	}
}

func shareOutcomeError(outcome drive.ShareSaveOutcome) error {
	for _, item := range outcome.Items {
		if item.Error != nil {
			return item.Error
		}
	}
	return drive.NewShareError(drive.ShareErrProvider, "", "provider reported a failed save without a reason")
}

func shareSavedEntries(outcome drive.ShareSaveOutcome) []shareSavedEntry {
	out := make([]shareSavedEntry, 0, len(outcome.Items))
	for _, item := range outcome.Items {
		saved := shareSavedEntry{SourceID: item.SourceID, Name: item.SourceName, Size: item.SourceSize}
		if item.Saved != nil {
			saved.RemoteID = item.Saved.ID
			if item.Saved.Name != "" {
				saved.Name = item.Saved.Name
			}
			if item.Saved.Size > 0 {
				saved.Size = item.Saved.Size
			}
		}
		if item.Error != nil {
			saved.ErrorCode = string(item.Error.Code)
			saved.ErrorMessage = item.Error.Message
			saved.Retryable = item.Error.Retryable
		}
		out = append(out, saved)
	}
	return out
}

func savedEntryCount(outcome drive.ShareSaveOutcome) int {
	count := 0
	for _, item := range outcome.Items {
		if item.Error == nil {
			count++
		}
	}
	return count
}

// recoverShareSaveTasks resumes an interrupted share save from its persisted
// detail. The runner reopens the link and re-enters the provider save, so no
// in-process state is required.
func (c *Core) recoverShareSaveTasks(ctx context.Context, manager *task.Manager) {
	c.recoverShareTasks(ctx, manager, task.TypeShareSave)
}

func (c *Core) recoverShareFetchTasks(ctx context.Context, manager *task.Manager) {
	c.recoverShareTasks(ctx, manager, task.TypeShareFetch)
}

func (c *Core) recoverShareTasks(ctx context.Context, manager *task.Manager, typ task.Type) {
	if c == nil || manager == nil || c.fs == nil {
		return
	}
	items, err := manager.ListTasks(ctx, task.Filter{Types: []task.Type{typ}})
	if err != nil {
		return
	}
	fetch := typ == task.TypeShareFetch
	for _, item := range items {
		if !offlineRecoverable(item.State, item.Error) {
			continue
		}
		if c.offlineRunActive(item.ID) {
			continue
		}
		id := item.ID
		_, ok, err := manager.RecoverTask(ctx, id, func(runCtx context.Context, update task.UpdateFunc) error {
			return c.runShareTask(runCtx, update, id, fetch)
		})
		if err != nil || !ok {
			c.endOfflineRun(id)
		}
	}
}
