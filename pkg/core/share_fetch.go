package core

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/task"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
	"github.com/yinzhenyu/qrypt/pkg/vfs/vfstypes"
)

// shareFetchTarget is one file to download: either a direct share target or an
// entry the save phase already created in the account's drive.
type shareFetchTarget struct {
	name     string
	size     int64
	remoteID string
	url      *drive.DownloadTarget
}

// runFetch performs the optional save phase, then downloads the selected
// content, then removes the cloud copy this fetch saved (unless kept).
func (r *shareRun) runFetch(ctx context.Context, update task.UpdateFunc) error {
	if r.detail.Phase == "" {
		r.detail.Phase = sharePhaseOpen
	}
	fromSave := r.detail.ToRequested
	if fromSave && !r.savePhaseDone() {
		if err := r.fetchSavePhase(ctx, update); err != nil {
			return err
		}
	}
	if r.detail.Phase == sharePhaseOpen || r.detail.Phase == sharePhaseSave || r.detail.Phase == sharePhaseVerify {
		r.detail.Phase = sharePhaseDownload
		r.persist(update)
	}
	if r.detail.Phase == sharePhaseDownload {
		if err := r.downloadPhase(ctx, update, fromSave); err != nil {
			return r.fail(update, err)
		}
		r.detail.Phase = sharePhaseCleanup
		r.persist(update)
	}
	if r.detail.Phase == sharePhaseCleanup {
		if err := r.cleanupFetch(ctx, update, fromSave); err != nil {
			return r.fail(update, err)
		}
	}
	r.detail.Phase = sharePhaseComplete
	return r.finishFetch(update)
}

// savePhaseDone reports whether the persisted detail already carries a
// completed provider save. A still-running provider task is never "done", even
// if it has reported some entries so far; a failure-only save is done only when
// the provider reached a terminal state, and otherwise resumes its handle.
func (r *shareRun) savePhaseDone() bool {
	switch drive.SaveState(r.detail.SaveState) {
	case drive.SaveStateQueued, drive.SaveStateRunning:
		return false
	}
	for _, saved := range r.detail.Saved {
		if saved.ErrorCode == "" {
			return true
		}
	}
	return false
}

func (r *shareRun) fetchSavePhase(ctx context.Context, update task.UpdateFunc) error {
	var outcome drive.ShareSaveOutcome
	if r.detail.SaveHandle != "" {
		// Resume the submitted save before touching the link. The persisted
		// selection is sufficient for polling and verification; reopening an
		// expired or changed share must not block recovery.
		entries, err := r.resumeSelection(ctx, update)
		if err != nil {
			return r.fail(update, err)
		}
		resumedOutcome, resumed, err := r.resumeSave(ctx, update, entries)
		if err != nil {
			return r.fail(update, err)
		}
		if resumed {
			outcome = reconcileShareOutcome(resumedOutcome, entries)
		} else {
			session, err := r.openSession(ctx, update)
			if err != nil {
				return r.fail(update, err)
			}
			outcome, err = r.saveSelected(ctx, update, session, entries)
			if err != nil {
				return r.fail(update, err)
			}
		}
	} else {
		session, err := r.openSession(ctx, update)
		if err != nil {
			return r.fail(update, err)
		}
		entries, err := r.listSelected(ctx, session)
		if err != nil {
			return r.fail(update, err)
		}
		outcome, err = r.saveSelected(ctx, update, session, entries)
		if err != nil {
			return r.fail(update, err)
		}
	}

	// Persist the resumed outcome before verification: fetchTargets and cleanup
	// read the per-entry save results from durable detail.
	r.recordOutcome(outcome)
	r.detail.Phase = sharePhaseVerify
	r.persist(update)

	state, _ := shareOutcomeResult(outcome)
	if state == task.StateFailed {
		r.detail.Phase = sharePhaseComplete
		r.persist(update)
		return r.fail(update, shareOutcomeError(outcome))
	}
	if !r.verifyDestination(ctx, outcome) {
		return r.fail(update, drive.NewShareError(drive.ShareErrDestinationUnverified, "",
			"saved entries did not appear in the destination"))
	}
	return nil
}

func (r *shareRun) openSession(ctx context.Context, update task.UpdateFunc) (drive.ShareSession, error) {
	session, err := r.receiver.OpenShare(ctx, r.detail.link())
	if err != nil {
		return drive.ShareSession{}, err
	}
	if session.SessionToken == "" {
		return drive.ShareSession{}, drive.NewShareError(drive.ShareErrUnsupported, "", "provider returned no session token")
	}
	r.detail.Owner = session.Share.Owner.Name
	if session.Share.Title != "" {
		r.detail.Title = session.Share.Title
	}
	if session.Share.LinkID != "" {
		r.detail.LinkID = session.Share.LinkID
	}
	update(func(item *task.Task) {
		if item.Name == "" {
			item.Name = r.detail.Title
		}
	})
	return session, nil
}

func (r *shareRun) listSelected(ctx context.Context, session drive.ShareSession) ([]drive.ShareEntry, error) {
	entries, err := r.listAll(ctx, session)
	if err != nil {
		return nil, err
	}
	selected := r.selected(entries)
	if len(selected) == 0 {
		return nil, drive.NewShareError(drive.ShareErrNotFound, "", "no share entry matched the selection")
	}
	return selected, nil
}

func (r *shareRun) saveSelected(ctx context.Context, update task.UpdateFunc, session drive.ShareSession, entries []drive.ShareEntry) (drive.ShareSaveOutcome, error) {
	if r.saver == nil {
		return drive.ShareSaveOutcome{}, fmt.Errorf("%w: mount %q", ErrShareUnsupported, r.detail.Mount)
	}
	// Persist the exact selection before submitting: a restart can then resume
	// the provider handle without reopening a link that may have expired.
	r.detail.SelectedEntries = shareSelectedEntries(entries)
	r.persist(update)
	destEntry, err := r.destinationEntry(ctx)
	if err != nil {
		return drive.ShareSaveOutcome{}, err
	}
	request := drive.ShareSaveRequest{
		Session: session,
		Entries: entries,
		Dest: drive.ShareDestination{
			Entry:   destEntry,
			SpaceID: drive.ProviderSpaceID(ctx, r.driver),
			Rename:  drive.RenameFail,
		},
	}
	request.Normalize()
	outcome, err := r.saver.SaveShareEntries(ctx, request)
	if err != nil {
		return outcome, err
	}
	r.detail.SaveHandle = outcome.Handle
	r.recordOutcome(outcome)
	r.persist(update)
	if !outcome.Complete() {
		outcome, err = r.pollSave(ctx, update, outcome.Handle, entries)
		if err != nil {
			return outcome, err
		}
	}
	outcome = reconcileShareOutcome(outcome, entries)
	r.recordOutcome(outcome)
	r.detail.Phase = sharePhaseVerify
	r.persist(update)
	return outcome, nil
}

// destinationEntry rebuilds the save destination. When the persisted entry
// lost its private metadata, a driver that can resolve entries by id restores
// it instead of requiring a parent listing.
func (r *shareRun) destinationEntry(ctx context.Context) (drive.Entry, error) {
	entry := r.detail.ToEntry.toDrive()
	if entry.Extra != nil || entry.ID == "" {
		return entry, nil
	}
	resolver, ok := r.driver.(drive.EntryResolver)
	if !ok {
		return entry, nil
	}
	resolved, err := resolver.Stat(ctx, entry.ID)
	if err != nil {
		return drive.Entry{}, fmt.Errorf("core: resolve share destination %q: %w", entry.ID, err)
	}
	resolved.IsDir = true
	return resolved, nil
}

func (r *shareRun) downloadPhase(ctx context.Context, update task.UpdateFunc, fromSave bool) error {
	targets, err := r.fetchTargets(ctx, update, fromSave)
	if err != nil {
		return err
	}
	destination, err := r.fetchDestination(ctx)
	if err != nil {
		return err
	}
	if destination.single && len(targets) != 1 {
		return fmt.Errorf("%w: a single-file download destination needs exactly one selected entry", task.ErrInvalidOperation)
	}

	var total int64
	for _, target := range targets {
		total += target.size
	}
	update(func(item *task.Task) {
		item.Progress.Phase = task.PhaseDownload
		item.Progress.TransferBytesTotal = total
		item.Progress.ItemsTotal = int64(len(targets))
	})

	plan := transferPlanFor(r.detail.Chunk, r.detail.Workers)
	// A resumed download keeps the entries a previous run already finished.
	// Re-downloading them would overwrite the persisted results and waste the
	// transfer.
	completed := make(map[string]shareSavedEntry, len(r.detail.Transferred))
	for _, entry := range r.detail.Transferred {
		if entry.ErrorCode == "" {
			completed[shareTransferKey(entry)] = entry
		}
	}
	results := make([]shareSavedEntry, 0, len(targets))
	var done int64
	for _, target := range targets {
		key := shareTransferKey(shareSavedEntry{Name: target.name, RemoteID: target.remoteID})
		if entry, ok := completed[key]; ok {
			results = append(results, entry)
			done += entry.Size
			r.detail.Transferred = results
			update(func(item *task.Task) {
				item.Progress.TransferBytesDone = done
				item.Progress.ItemsDone = int64(len(results))
			})
			continue
		}
		written, transferErr := r.transferTarget(ctx, update, destination, plan, target, done)
		done += written
		result := shareSavedEntry{Name: target.name, Size: written, RemoteID: target.remoteID}
		if transferErr != nil {
			result.ErrorCode = string(drive.ShareErrProvider)
			result.ErrorMessage = transferErr.Error()
			results = append(results, result)
			r.detail.Transferred = results
			r.persist(update)
			return transferErr
		}
		results = append(results, result)
		r.detail.Transferred = results
		r.persist(update)
		update(func(item *task.Task) {
			item.Progress.ItemsDone = int64(len(results))
		})
	}
	return nil
}

// shareTransferKey identifies one downloaded entry across a restart. The
// remote id is the stable identity; a name is only a fallback for providers
// that do not expose one.
func shareTransferKey(entry shareSavedEntry) string {
	if entry.RemoteID != "" {
		return "id:" + entry.RemoteID
	}
	return "name:" + entry.Name
}

func (r *shareRun) fetchTargets(ctx context.Context, update task.UpdateFunc, fromSave bool) ([]shareFetchTarget, error) {
	if fromSave {
		targets := make([]shareFetchTarget, 0, len(r.detail.Saved))
		for _, saved := range r.detail.Saved {
			if saved.ErrorCode != "" {
				continue
			}
			if saved.RemoteID == "" {
				return nil, fmt.Errorf("core: saved entry %q has no remote id to download", saved.Name)
			}
			targets = append(targets, shareFetchTarget{name: saved.Name, size: saved.Size, remoteID: saved.RemoteID})
		}
		if len(targets) == 0 {
			return nil, drive.NewShareError(drive.ShareErrNotFound, "", "the save phase produced no downloadable entry")
		}
		return targets, nil
	}

	if !drive.HasCapability(r.driver, drive.CapabilityShareFetch) {
		return nil, fmt.Errorf("%w: mount %q", ErrShareUnsupported, r.detail.Mount)
	}
	fetcher, ok := r.driver.(drive.ShareFetcher)
	if !ok {
		return nil, fmt.Errorf("%w: mount %q", ErrShareUnsupported, r.detail.Mount)
	}
	session, err := r.openSession(ctx, update)
	if err != nil {
		return nil, err
	}
	entries, err := r.listSelected(ctx, session)
	if err != nil {
		return nil, err
	}
	downloads, err := fetcher.ShareDownloadTargets(ctx, drive.ShareFetchRequest{Session: session, Entries: entries})
	if err != nil {
		return nil, err
	}
	if len(downloads) != len(entries) {
		return nil, fmt.Errorf("core: provider returned %d download targets for %d entries", len(downloads), len(entries))
	}
	targets := make([]shareFetchTarget, 0, len(entries))
	for i, entry := range entries {
		target := downloads[i]
		targets = append(targets, shareFetchTarget{name: entry.Name, size: entry.Size, remoteID: entry.ID, url: &target})
	}
	return targets, nil
}

func (r *shareRun) transferTarget(ctx context.Context, update task.UpdateFunc, destination *shareFetchDestination, plan offlineChunkPlan, target shareFetchTarget, base int64) (int64, error) {
	writer, err := destination.writer(ctx, target.name)
	if err != nil {
		return 0, err
	}
	offset := writer.Offset()

	if target.url != nil {
		size := target.size
		for attempt := 0; attempt < 3; attempt++ {
			result, streamErr := streamDownloadTarget(ctx, *target.url, offset, size, writer, base+offset, plan, update)
			offset += result.written
			if result.size > 0 {
				size = result.size
			}
			if streamErr != nil {
				continue
			}
			if result.done {
				if closeErr := writer.Close(); closeErr != nil {
					_ = writer.Abort()
					return offset, closeErr
				}
				return offset, nil
			}
		}
		_ = writer.Abort()
		return offset, fmt.Errorf("core: share download URL did not complete the file")
	}

	reader, err := r.driver.Read(ctx, drive.Entry{ID: target.remoteID, Name: target.name}, offset, sizeOrUnknown(target.size, offset))
	if err != nil {
		_ = writer.Abort()
		return 0, err
	}
	defer reader.Close()
	written, copyErr := copyShareReader(ctx, reader, writer, offset, base+offset, update)
	offset += written
	if copyErr != nil {
		_ = writer.Abort()
		return offset, copyErr
	}
	if err := writer.Close(); err != nil {
		_ = writer.Abort()
		return offset, err
	}
	return offset, nil
}

func sizeOrUnknown(size, offset int64) int64 {
	if size <= 0 {
		return -1
	}
	if size <= offset {
		return 0
	}
	return size - offset
}

func copyShareReader(ctx context.Context, reader io.Reader, writer offlineWriter, offset, base int64, update task.UpdateFunc) (int64, error) {
	buffer := make([]byte, offlineTransferChunk)
	var written int64
	for {
		if err := ctx.Err(); err != nil {
			return written, err
		}
		n, readErr := reader.Read(buffer)
		if n > 0 {
			if _, writeErr := writer.WriteAt(buffer[:n], offset+written); writeErr != nil {
				return written, writeErr
			}
			written += int64(n)
			update(func(item *task.Task) {
				item.Progress.TransferBytesDone = base + written
			})
		}
		if readErr == io.EOF {
			return written, nil
		}
		if readErr != nil {
			return written, readErr
		}
	}
}

// shareFetchDestination resolves --download-to into a writer factory.
type shareFetchDestination struct {
	core     *Core
	mount    string // "" = plain local path
	base     string // namespace virtual dir or local directory/file
	single   bool
	fileName string
}

func (r *shareRun) fetchDestination(ctx context.Context) (*shareFetchDestination, error) {
	raw := strings.TrimSpace(r.detail.DownloadTo)
	if raw == "" {
		return nil, fmt.Errorf("%w: share fetch requires a download destination", task.ErrInvalidOperation)
	}
	destination := &shareFetchDestination{core: r.core, mount: r.detail.DownloadMount, base: raw}
	asDirectory := strings.HasSuffix(raw, "/") || strings.HasSuffix(raw, string(filepath.Separator))

	if destination.mount != "" {
		destination.base = r.core.offlineVirtualPath(destination.mount, r.detail.DownloadDir)
		if !asDirectory {
			if entry, err := r.core.fs.Stat(ctx, destination.base); err == nil && entry.IsDir {
				asDirectory = true
			}
		}
	} else if !asDirectory {
		if info, err := os.Stat(raw); err == nil && info.IsDir() {
			asDirectory = true
		}
	}
	destination.single = !asDirectory
	destination.fileName = filepath.Base(raw)

	if asDirectory {
		if destination.mount != "" {
			if err := r.core.ensureShareNamespaceDir(ctx, destination.base); err != nil {
				return nil, err
			}
		} else if err := os.MkdirAll(destination.base, 0o755); err != nil {
			return nil, err
		}
		return destination, nil
	}
	if destination.mount != "" {
		if err := r.core.ensureShareNamespaceDir(ctx, namespaceDir(destination.base)); err != nil {
			return nil, err
		}
	} else if err := os.MkdirAll(filepath.Dir(destination.base), 0o755); err != nil {
		return nil, err
	}
	return destination, nil
}

func namespaceDir(path string) string {
	cleaned := vfstypes.CleanVirtualPath(path)
	if cleaned == "/" {
		return "/"
	}
	index := strings.LastIndex(cleaned, "/")
	if index <= 0 {
		return "/"
	}
	return cleaned[:index]
}

func (c *Core) ensureShareNamespaceDir(ctx context.Context, path string) error {
	path = vfstypes.CleanVirtualPath(path)
	if path == "/" {
		return nil
	}
	current := ""
	for _, part := range strings.Split(strings.Trim(path, "/"), "/") {
		if part == "" {
			continue
		}
		current += "/" + part
		if _, err := c.fs.Stat(ctx, current); err == nil {
			continue
		}
		if _, err := c.fs.Mkdir(ctx, current); err != nil {
			if _, statErr := c.fs.Stat(ctx, current); statErr == nil {
				continue
			}
			return err
		}
	}
	return nil
}

func (d *shareFetchDestination) writer(ctx context.Context, name string) (offlineWriter, error) {
	var path string
	switch {
	case d.single:
		path = d.base
	case d.mount != "":
		path = strings.TrimRight(d.base, "/") + "/" + name
	default:
		path = filepath.Join(d.base, name)
	}
	if d.mount != "" {
		if err := d.core.fs.Create(ctx, path); err != nil {
			return nil, err
		}
		writer, ok := d.core.fs.(vfs.Writer)
		if !ok {
			return nil, fmt.Errorf("core: filesystem is not writable")
		}
		return &offlineNamespaceWriter{ctx: ctx, fs: writer, path: path}, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	return openLocalWriter(path)
}

// cleanupFetch removes the cloud copy this fetch saved. A direct download never
// deletes anything, and a failed download never reaches this phase.
func (r *shareRun) cleanupFetch(ctx context.Context, update task.UpdateFunc, fromSave bool) error {
	if !fromSave || r.detail.KeepCloud {
		return nil
	}
	update(func(item *task.Task) {
		item.Progress.Phase = task.PhaseDelete
	})
	for _, saved := range r.detail.Saved {
		if saved.ErrorCode != "" || saved.RemoteID == "" {
			continue
		}
		entry := drive.Entry{ID: saved.RemoteID, Name: saved.Name}
		if err := r.driver.Remove(ctx, entry); err != nil && !errors.Is(err, drive.ErrNotFound) {
			return err
		}
	}
	return nil
}

func (r *shareRun) finishFetch(update task.UpdateFunc) error {
	items := make([]task.ItemTracking, 0, len(r.detail.Saved)+len(r.detail.Transferred))
	succeeded, failed := 0, 0
	var failures []shareSavedEntry

	// A save-phase failure is a real result even when the remaining entries
	// downloaded successfully. Keep it visible and prevent the task from
	// reporting full success.
	for _, saved := range r.detail.Saved {
		if saved.ErrorCode == "" {
			continue
		}
		items = append(items, task.ItemTracking{
			ItemID:           saved.SourceID,
			Path:             saved.Name,
			SourceBytesTotal: saved.Size,
			State:            task.ItemStateFailed,
			Error:            &task.Error{Code: saved.ErrorCode, Message: saved.ErrorMessage, Retryable: saved.Retryable},
		})
		failures = append(failures, saved)
		failed++
	}

	for _, result := range r.detail.Transferred {
		item := task.ItemTracking{
			ItemID:           result.SourceID,
			Path:             result.Name,
			RemoteID:         result.RemoteID,
			SourceBytesTotal: result.Size,
			State:            task.ItemStateSucceeded,
		}
		if result.ErrorCode != "" {
			item.State = task.ItemStateFailed
			item.Error = &task.Error{Code: result.ErrorCode, Message: result.ErrorMessage, Retryable: result.Retryable}
			failures = append(failures, result)
			failed++
		} else {
			succeeded++
		}
		items = append(items, item)
	}

	state := task.StateSucceeded
	switch {
	case succeeded == 0 && failed > 0:
		state = task.StateFailed
	case succeeded > 0 && failed > 0:
		state = task.StatePartialFailed
	}
	update(func(item *task.Task) {
		r.detail.apply(item)
		item.Error = nil
		item.Progress.Phase = task.PhaseComplete
		item.Progress.ItemsTotal = int64(len(items))
		item.Progress.ItemsDone = int64(succeeded)
		item.Tracking = task.Tracking{Items: items}
		item.State = state
	})
	if state == task.StateFailed {
		return r.fail(update, shareDetailError(failures))
	}
	return nil
}
