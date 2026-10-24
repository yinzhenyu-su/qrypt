package core

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/task"
	"github.com/yinzhenyu/qrypt/pkg/vfs/vfstypes"
)

// Offline runner tuning. The poll interval starts at defaultOfflinePollInterval
// (raised toward the provider hint, capped at offlinePollIntervalMax), the
// missing-task window bounds how long a disappeared provider task is given
// before its destination is re-resolved, and the transient threshold bounds
// how many consecutive provider errors are tolerated before failing.
const (
	offlineTransientFailureLimit = 5
	defaultOfflineMissingWindow  = 30 * time.Second
	// A provider's listing can lag its own finished task, so destination
	// verification refreshes the listing and retries briefly before recording
	// the destination as unverified. The budget stays small because it is on
	// the path that finishes the task.
	offlineVerifyAttempts = 3
	offlineVerifyInterval = 300 * time.Millisecond
)

// offlineRun is one execution generation of a durable offline task. It owns the
// provider interaction and the destination reconciliation; the manager owns
// the task's terminal state.
type offlineRun struct {
	core   *Core
	taskID string
	fetch  bool
	detail offlineDetail
	driver drive.Driver

	pollInterval    time.Duration
	missingWindow   time.Duration
	transientErrors int
	// available records that the provider reported the staged result set is
	// complete, so a fetch may transfer before the task terminalizes.
	available bool
	// localRoot is the pinned local destination root for the duration of one
	// transfer. It is nil for namespace destinations and between phases.
	localRoot     *os.Root
	localRootPath string
}

func (c *Core) newOfflineRun(ctx context.Context, taskID string, fetch bool) (*offlineRun, error) {
	current, err := c.GetTask(ctx, taskID)
	if err != nil {
		return nil, err
	}
	detail, ok := decodeOfflineDetail(current.Detail)
	if !ok {
		return nil, fmt.Errorf("core: offline task %q has no offline detail", taskID)
	}
	driver, ok := c.offlineDriver(detail.ParseMount)
	if !ok {
		return nil, fmt.Errorf("%w: mount %q", ErrOfflineMountNotFound, detail.ParseMount)
	}
	missingWindow := defaultOfflineMissingWindow
	if c.offlineMissingWindow > 0 {
		missingWindow = c.offlineMissingWindow
	}
	// A fresh driver instance has no in-process memory of this task's output
	// tree. Hand it the identity Core persisted so it can resolve the same
	// staged layout after recovery.
	if restorer, ok := driver.(drive.OfflineLayoutRestorer); ok && detail.ProviderID != "" {
		restorer.RestoreOfflineLayout(drive.OfflineLayout{
			TaskID:    detail.ProviderID,
			RootID:    detail.ProviderRootID,
			ParentID:  detail.ParseRemoteID,
			Available: detail.Available,
		})
	}
	return &offlineRun{
		core:          c,
		taskID:        taskID,
		fetch:         fetch,
		detail:        detail,
		driver:        driver,
		pollInterval:  c.offlinePollEvery(),
		missingWindow: missingWindow,
		available:     detail.Available,
	}, nil
}

// runOfflineTask is the manager run function for both offline types. The active
// registry prevents two runners from observing the same task.
func (c *Core) runOfflineTask(ctx context.Context, update task.UpdateFunc, taskID string, fetch bool) error {
	if !c.beginOfflineRun(taskID) {
		return fmt.Errorf("core: offline task %q already has an active runner", taskID)
	}
	defer c.endOfflineRun(taskID)
	run, err := c.newOfflineRun(ctx, taskID, fetch)
	if err != nil {
		return err
	}
	if fetch {
		return run.runFetch(ctx, update)
	}
	return run.runDownload(ctx, update)
}

func (r *offlineRun) provider() (drive.OfflineDownloader, error) {
	offline, ok := r.driver.(drive.OfflineDownloader)
	if !ok {
		return nil, fmt.Errorf("%w: provider does not implement offline download", ErrOfflineUnsupported)
	}
	return offline, nil
}

// runDownload stages a source and observes it to a terminal state.
func (r *offlineRun) runDownload(ctx context.Context, update task.UpdateFunc) error {
	if err := r.ensureProvider(ctx, update); err != nil {
		return r.fail(update, err)
	}
	observation, err := r.pollToTerminal(ctx, update)
	if err != nil {
		return r.fail(update, err)
	}
	if observation.State == drive.OfflineStateCanceled {
		return r.failCanceled(update)
	}
	return r.completeStaging(ctx, update, observation)
}

// runFetch stages a source, then transfers the result to its final destination.
func (r *offlineRun) runFetch(ctx context.Context, update task.UpdateFunc) error {
	if r.detail.Phase == "" {
		r.detail.Phase = offlinePhaseStaging
	}
	if r.detail.Phase == offlinePhaseStaging {
		if err := r.ensureProvider(ctx, update); err != nil {
			return r.fail(update, err)
		}
		// Always take a fresh observation. The create response can already say
		// the staged results are complete, but it may also be stale: a latest
		// failed or canceled state must win before any transfer or cleanup.
		observation, err := r.observeStagedFiles(ctx, update)
		if err != nil {
			if ctx.Err() != nil {
				return r.failInterrupted(update, ctx.Err())
			}
			// A single lookup may be transient; the polling loop owns retries
			// and the missing-task window instead of turning that into a
			// no-files failure here.
			observation, err = r.pollToTerminal(ctx, update)
			if err != nil {
				return r.fail(update, err)
			}
		} else if !offlineProviderTerminal(observation.State) && !r.fetchReady() {
			observation, err = r.pollToTerminal(ctx, update)
			if err != nil {
				return r.fail(update, err)
			}
		}
		switch observation.State {
		case drive.OfflineStateCanceled:
			return r.failCanceled(update)
		case drive.OfflineStateSucceeded:
			if err := r.completeStaging(ctx, update, observation); err != nil {
				return r.fail(update, err)
			}
		case drive.OfflineStateFailed:
			if err := r.completeStaging(ctx, update, observation); err != nil {
				return r.fail(update, err)
			}
		default:
			// Availability means the complete result set is known; without it
			// there is nothing safe to transfer.
			if !r.fetchReady() {
				return r.fail(update, fmt.Errorf("core: offline fetch result set is not available"))
			}
		}
		r.detail.Phase = offlinePhaseTransfer
		r.persist(update, r.detail)
	}
	if r.detail.Phase == offlinePhaseTransfer {
		if err := r.transferPhase(ctx, update); err != nil {
			return r.failInterrupted(update, err)
		}
		r.detail.Phase = offlinePhaseCleanup
		r.detail.TransferDone = true
		r.persist(update, r.detail)
	}
	if r.detail.Phase == offlinePhaseCleanup {
		if err := r.cleanupPhase(ctx, update); err != nil {
			return r.failInterrupted(update, err)
		}
		r.detail.Phase = offlinePhaseComplete
		r.persist(update, r.detail)
	}
	return nil
}

// ensureProvider submits the source once and persists the provider identifier
// before any polling. A cancel that arrives before the identifier is known
// prevents or removes the provider task.
func (r *offlineRun) ensureProvider(ctx context.Context, update task.UpdateFunc) error {
	if r.detail.ProviderID != "" {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	provider, err := r.provider()
	if err != nil {
		return err
	}
	request := drive.OfflineDownloadRequest{
		SourceKind:    drive.OfflineSourceKind(r.detail.SourceKind),
		Source:        r.detail.Source,
		Title:         r.detail.Title,
		Destination:   r.detail.ParseDir,
		DestinationID: r.detail.ParseRemoteID,
		Selection:     r.detail.Selection.toDrive(),
		Accelerated:   r.fetch && r.detail.Accelerated,
	}
	created, err := provider.CreateOfflineDownload(ctx, request)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
	if created.ID == "" {
		return fmt.Errorf("core: provider returned no task identifier")
	}
	r.detail.ProviderID = created.ID
	if created.Destination != "" {
		r.detail.ParseRemoteID = created.Destination
	}
	// The provider's submit response names the entry the task produced (the
	// resource root). A fetch walks down from it to resolve each result file's
	// staged relative path.
	if created.RemoteID != "" {
		r.detail.ProviderRootID = created.RemoteID
	}
	if created.Available {
		r.available = true
		r.detail.Available = true
	}
	// A provider may report the staged files immediately; keep them so a fetch
	// can resolve the layout without a second observation.
	if len(created.Files) > 0 {
		r.detail.ResultFiles = mergeOfflineResultFiles(r.detail.ResultFiles, offlineResultFilesFromDrive(created.Files))
	}
	r.detail.setObservation(observationFromDownload(created))
	r.persist(update, r.detail)
	r.applyObservation(update, created, offlinePhaseStaging)
	if ctx.Err() != nil {
		// Canceled between the provider accepting the task and the
		// identifier being journaled: remove the provider task only when
		// the user asked for cancellation. A shutdown leaves the provider
		// task intact so restart recovery can resume it.
		if errors.Is(ctx.Err(), context.Canceled) && r.cancelRequested() {
			r.cancelProviderTask(created.ID)
		}
		return ctx.Err()
	}
	return nil
}

// observeStagedFiles records one provider observation so a fetch can resolve
// the staged files and the provider's availability signal before the task
// reaches a terminal state. A lookup error is returned so the caller can retry
// through the polling loop instead of treating it as an empty result set.
func (r *offlineRun) observeStagedFiles(ctx context.Context, update task.UpdateFunc) (drive.OfflineDownload, error) {
	provider, err := r.provider()
	if err != nil {
		return drive.OfflineDownload{}, err
	}
	observation, err := provider.GetOfflineDownload(ctx, r.detail.ProviderID)
	if err != nil {
		return drive.OfflineDownload{}, err
	}
	r.recordObservation(update, observation)
	return observation, nil
}

// fetchReady reports whether a fetch may transfer: the provider signaled a
// complete staged result set and that set has at least one file.
func (r *offlineRun) fetchReady() bool {
	return r.available && len(r.detail.ResultFiles) > 0
}

// recordObservation folds one provider observation into the retained detail.
func (r *offlineRun) recordObservation(update task.UpdateFunc, observation drive.OfflineDownload) {
	if observation.Available {
		// Availability is monotonic: a later thin observation that omits the
		// flag must not discard a complete set the provider already reported.
		r.available = true
		r.detail.Available = true
	}
	// Some providers learn the produced entry id only after creation (115
	// reports it on the task list). Persist the first non-empty identity so
	// cleanup can remove the resource folder that identity owns.
	if observation.RemoteID != "" && r.detail.ProviderRootID == "" {
		r.detail.ProviderRootID = observation.RemoteID
	}
	r.detail.setObservation(observationFromDownload(observation))
	if len(observation.Files) > 0 {
		r.detail.ResultFiles = mergeOfflineResultFiles(r.detail.ResultFiles, offlineResultFilesFromDrive(observation.Files))
	}
	r.persist(update, r.detail)
	r.applyObservation(update, observation, offlinePhaseStaging)
}

// pollToTerminal observes the provider task until it reaches a terminal state,
// retrying transient errors with backoff and reconciling a disappeared task.
func (r *offlineRun) pollToTerminal(ctx context.Context, update task.UpdateFunc) (drive.OfflineDownload, error) {
	provider, err := r.provider()
	if err != nil {
		return drive.OfflineDownload{}, err
	}
	interval := r.pollInterval
	if interval <= 0 {
		interval = defaultOfflinePollInterval
	}
	missingSince := time.Time{}
	for {
		if err := ctx.Err(); err != nil {
			r.cancelOnInterrupt()
			return drive.OfflineDownload{}, err
		}
		observation, err := provider.GetOfflineDownload(ctx, r.detail.ProviderID)
		if err != nil {
			if ctx.Err() != nil {
				r.cancelOnInterrupt()
				return drive.OfflineDownload{}, ctx.Err()
			}
			if offlineMissing(err) {
				if missingSince.IsZero() {
					missingSince = time.Now()
				}
				if resolved, ok := r.reconcileMissing(ctx, update); ok {
					return resolved, nil
				}
				if time.Since(missingSince) >= r.missingWindow {
					return drive.OfflineDownload{}, drive.NewOfflineError(
						drive.OfflineErrProviderTaskMissing, "",
						fmt.Sprintf("provider task %s disappeared and no destination entry was found", r.detail.ProviderID))
				}
			} else if !offlineRetryable(err) {
				return drive.OfflineDownload{}, err
			} else {
				r.transientErrors++
				if r.transientErrors > offlineTransientFailureLimit {
					return drive.OfflineDownload{}, err
				}
				interval = backoffInterval(interval)
			}
			if err := sleepContext(ctx, interval); err != nil {
				r.cancelOnInterrupt()
				return drive.OfflineDownload{}, err
			}
			continue
		}
		r.transientErrors = 0
		r.recordObservation(update, observation)
		switch observation.State {
		case drive.OfflineStateSucceeded, drive.OfflineStateFailed, drive.OfflineStateCanceled:
			return observation, nil
		}
		// A fetch may stop polling once the complete result set is known; a
		// cloud-only download still waits for the provider's terminal state.
		if r.fetch && r.fetchReady() {
			return observation, nil
		}
		hint := interval
		if hint > offlinePollIntervalMax {
			hint = offlinePollIntervalMax
		}
		if err := sleepContext(ctx, hint); err != nil {
			r.cancelOnInterrupt()
			return drive.OfflineDownload{}, err
		}
	}
}

// completeStaging folds a terminal provider observation into the task. Provider
// success is authoritative: a lagging destination listing still records
// success with the observation marked unverified.
func (r *offlineRun) completeStaging(ctx context.Context, update task.UpdateFunc, observation drive.OfflineDownload) error {
	if observation.State != drive.OfflineStateSucceeded {
		message := observation.Error
		if message == "" {
			message = fmt.Sprintf("provider task ended with state %s", observation.State)
		}
		if observation.ErrorCode != "" {
			return drive.NewOfflineError(drive.OfflineErrorCode(observation.ErrorCode), observation.ErrorCode, message)
		}
		return drive.NewOfflineError(drive.OfflineErrProvider, "", message)
	}
	// A provider may report success without repeating the file list. Keep the
	// identities and sizes already observed instead of replacing them with an
	// empty result set.
	r.detail.ResultFiles = mergeOfflineResultFiles(r.detail.ResultFiles, offlineResultFilesFromDrive(observation.Files))
	verified := r.verifyDestination(ctx)
	r.persist(update, r.detail)
	update(func(item *task.Task) {
		r.detail.apply(item)
		item.Progress.Phase = offlinePhaseComplete.display()
		item.Progress.ItemsTotal = int64(len(r.detail.ResultFiles))
		item.Progress.ItemsDone = int64(len(r.detail.ResultFiles))
		item.Tracking.Items = offlineResultItems(r.detail)
		if r.detail.Observation != nil {
			r.detail.Observation.DestinationVerified = verified
			item.Detail[task.OfflineDetailKey] = r.detail
		}
	})
	return nil
}

// reconcileMissing resolves the destination entry when the provider no longer
// exposes the task. It returns a synthetic success observation when the entry
// exists, so completion is never reported without either a provider success or
// a resolved destination.
func (r *offlineRun) reconcileMissing(ctx context.Context, update task.UpdateFunc) (drive.OfflineDownload, bool) {
	if !r.verifyDestination(ctx) {
		return drive.OfflineDownload{}, false
	}
	// Synthesize success from this task's own expected files. Carrying the
	// retained identities and sizes keeps the completion snapshot complete.
	observation := drive.OfflineDownload{
		ID:    r.detail.ProviderID,
		State: drive.OfflineStateSucceeded,
		Files: offlineFilesFromDetail(r.detail.ResultFiles),
	}
	r.detail.setObservation(observationFromDownload(observation))
	r.persist(update, r.detail)
	r.applyObservation(update, observation, offlinePhaseStaging)
	return observation, true
}

// verifyDestination reports whether the parse destination exposes the result
// files the provider reported. A provider's listing can lag its own task by a
// moment, so the check refreshes and retries briefly before answering. It never
// fails the task: a false result only marks the observation unverified.
func (r *offlineRun) verifyDestination(ctx context.Context) bool {
	if r.detail.ParseDir == "" || len(r.detail.ResultFiles) == 0 {
		return false
	}
	for attempt := 0; attempt < offlineVerifyAttempts; attempt++ {
		// A cached listing would hide the entry the provider just created, so
		// every attempt, including the first, refreshes the directory.
		r.core.fs.RefreshPath(trimTrailingSlash(r.stagingRoot()))
		if attempt > 0 {
			if err := sleepContext(ctx, offlineVerifyInterval); err != nil {
				return false
			}
			r.core.fs.RefreshPath(trimTrailingSlash(r.stagingRoot()))
		}
		if r.destinationHasResults(ctx) {
			return true
		}
	}
	return false
}

// destinationHasResults lists the staging directory tree and matches it against
// the reported result files by staged relative path. Reconciliation may only
// claim success when every file this task expects is present; other entries in
// the directory are not evidence about this task.
func (r *offlineRun) destinationHasResults(ctx context.Context) bool {
	if len(r.detail.ResultFiles) == 0 {
		return false
	}
	available, err := r.listStagedTree(ctx, r.stagingRoot(), 0)
	if err != nil {
		return false
	}
	for _, file := range r.detail.ResultFiles {
		rel := strings.ReplaceAll(stagedPathOf(file), "\\", "/")
		rel = strings.TrimPrefix(rel, "/")
		if rel == "" {
			// An unnamed identity cannot be verified against a listing.
			return false
		}
		size, ok := available[rel]
		if !ok {
			return false
		}
		if file.Size > 0 && size > 0 && size != file.Size {
			return false
		}
	}
	return true
}

// listStagedTree maps every file under root, recursively, to its size keyed by
// its path relative to root.
func (r *offlineRun) listStagedTree(ctx context.Context, root string, depth int) (map[string]int64, error) {
	out := map[string]int64{}
	if depth > offlineMaxPathDepth {
		return out, nil
	}
	entries, err := r.core.fs.List(ctx, trimTrailingSlash(root))
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if entry.IsDir {
			nested, err := r.listStagedTree(ctx, vfstypes.JoinVirtualPath(root, entry.Name), depth+1)
			if err != nil {
				return nil, err
			}
			for name, size := range nested {
				out[entry.Name+"/"+name] = size
			}
			continue
		}
		out[entry.Name] = entry.Size
	}
	return out, nil
}

// cancelProviderTask removes the provider task on cancellation, using a context
// that outlives the canceled run so the cleanup request is actually sent.
func (r *offlineRun) cancelProviderTask(id string) {
	if id == "" {
		return
	}
	provider, err := r.provider()
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_ = provider.CancelOfflineDownload(ctx, id)
}

// fail classifies a runner error and records a stable code before returning it
// so the manager preserves the code on the failed task. Context cancellation is
// not a provider failure: the manager decides whether it was a user cancel or
// an interruption, and the runner must not overwrite that decision.
func (r *offlineRun) fail(update task.UpdateFunc, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	code, retryable := offlineErrorCode(err)
	message := err.Error()
	update(func(item *task.Task) {
		// Error.Retryable reflects whether an automatic retry is worthwhile;
		// Capabilities.Retryable gates the user-initiated retry action, which
		// stays available for every failed offline task.
		item.Error = &task.Error{Code: code, Message: message, Retryable: retryable}
		item.Capabilities.Retryable = true
	})
	return err
}

// failInterrupted classifies an error from a phase that can also be stopped by
// shutdown. It only removes the provider task when the user explicitly asked
// to cancel; an interruption leaves it for restart recovery.
func (r *offlineRun) failInterrupted(update task.UpdateFunc, err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		r.cancelOnInterrupt()
	}
	return r.fail(update, err)
}

// cancelOnInterrupt removes the provider task only when the user explicitly
// asked to cancel. A shutdown cancels the run context too, but must leave the
// provider task and its durable identity intact for restart recovery.
func (r *offlineRun) cancelOnInterrupt() {
	if r.cancelRequested() {
		r.cancelProviderTask(r.detail.ProviderID)
	}
}

// cancelRequested reports whether the canonical task state shows an explicit
// user cancellation rather than an interrupted runner.
func (r *offlineRun) cancelRequested() bool {
	if r == nil || r.core == nil {
		return false
	}
	current, err := r.core.GetTask(context.Background(), r.taskID)
	if err != nil {
		return false
	}
	return current.State == task.StateCanceling
}

func (r *offlineRun) failCanceled(update task.UpdateFunc) error {
	r.cancelProviderTask(r.detail.ProviderID)
	update(func(item *task.Task) {
		item.Error = &task.Error{Code: string(drive.OfflineStateCanceled), Message: "offline task was canceled by the provider"}
		item.Capabilities.Retryable = true
	})
	// ErrCanceled asks the manager for the canonical canceled state without
	// touching the run context (which may belong to unrelated work).
	return fmt.Errorf("%w: offline task %s was canceled by the provider", task.ErrCanceled, r.taskID)
}

// persist writes the durable detail. It is the single place that journals a
// provider identifier or phase change.
func (r *offlineRun) persist(update task.UpdateFunc, detail offlineDetail) {
	update(func(item *task.Task) {
		detail.apply(item)
		item.Progress.Phase = detail.Phase.display()
	})
}

// applyObservation folds a provider observation into progress counters. It
// leaves the task state to the manager so a terminal task is never rewritten.
func (r *offlineRun) applyObservation(update task.UpdateFunc, observation drive.OfflineDownload, phase offlinePhase) {
	update(func(item *task.Task) {
		progress := observation.Progress
		if phase == offlinePhaseTransfer {
			item.Progress.TransferBytesDone = progress.BytesDone
			item.Progress.TransferBytesTotal = progress.BytesTotal
		} else {
			item.Progress.CloudBytesDone = progress.BytesDone
			item.Progress.CloudBytesTotal = progress.BytesTotal
		}
		if progress.FilesTotal > 0 || progress.FilesDone > 0 {
			item.Progress.ItemsTotal = progress.FilesTotal
			item.Progress.ItemsDone = progress.FilesDone
		}
		item.Progress.Phase = phase.display()
		r.detail.apply(item)
	})
}

func observationFromDownload(download drive.OfflineDownload) offlineObservation {
	return offlineObservation{
		State:         string(download.State),
		Percent:       download.Progress.Percent,
		BytesDone:     download.Progress.BytesDone,
		BytesTotal:    download.Progress.BytesTotal,
		FilesDone:     download.Progress.FilesDone,
		FilesTotal:    download.Progress.FilesTotal,
		ErrorCode:     download.ErrorCode,
		ProviderError: download.Error,
	}
}

// offlineProviderTerminal reports whether a provider state ends observation.
func offlineProviderTerminal(state drive.OfflineState) bool {
	switch state {
	case drive.OfflineStateSucceeded, drive.OfflineStateFailed, drive.OfflineStateCanceled:
		return true
	default:
		return false
	}
}

func offlineMissing(err error) bool {
	// Only an explicit "task not found" may be reconciled from the destination.
	// A transient provider-unavailable error (including an incomplete layout
	// walk) must stay an error: reconciling retained result files could
	// synthesize success for a partial set and then transfer and clean it up.
	offline, ok := drive.AsOfflineError(err)
	return ok && offline.Code == drive.OfflineErrProviderTaskMissing
}

func offlineRetryable(err error) bool {
	offline, ok := drive.AsOfflineError(err)
	if !ok {
		return false
	}
	return offline.Retryable
}

// offlineErrorCode maps a runner error to a stable task error code and its
// retryability. A classified offline error keeps its code; anything else is a
// generic provider error unless it names a known core rejection.
func offlineErrorCode(err error) (string, bool) {
	if offline, ok := drive.AsOfflineError(err); ok {
		return string(offline.Code), offline.Retryable
	}
	switch {
	case errors.Is(err, ErrOfflineSameDestination):
		return string(drive.OfflineErrSameDestination), false
	case errors.Is(err, ErrOfflinePathInvalid):
		return string(drive.OfflineErrPathInvalid), false
	case errors.Is(err, ErrOfflineUnsupported), errors.Is(err, ErrOfflineSourceUnsupported):
		return string(drive.OfflineErrSourceUnsupported), false
	case errors.Is(err, ErrOfflineEncryptedParseDestination):
		return string(drive.OfflineErrCrypto), false
	case errors.Is(err, ErrOfflineParseDestinationMissing), errors.Is(err, ErrOfflineMountNotFound),
		errors.Is(err, ErrOfflineMountRequired), errors.Is(err, ErrOfflineMountConflict):
		return string(drive.OfflineErrSourceInvalid), false
	}
	return string(drive.OfflineErrProvider), false
}

func backoffInterval(current time.Duration) time.Duration {
	if current <= 0 {
		current = defaultOfflinePollInterval
	}
	next := current * 2
	if next > offlinePollIntervalMax {
		next = offlinePollIntervalMax
	}
	return next
}

func sleepContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		d = defaultOfflinePollInterval
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// stagingRoot is the Core-view path of the parse directory.
func (r *offlineRun) stagingRoot() string {
	if r.detail.ParseDir == "" {
		return "/"
	}
	return r.core.offlineVirtualPath(r.detail.ParseMount, r.detail.ParseDir)
}

func trimTrailingSlash(p string) string {
	for len(p) > 1 && p[len(p)-1] == '/' {
		p = p[:len(p)-1]
	}
	return p
}
