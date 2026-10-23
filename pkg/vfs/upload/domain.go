package upload

import (
	"context"
	"strings"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

// DomainOptions supplies the true cross-domain seams needed by the upload
// domain. Pending state, hashes, scheduling, admission, target coordination,
// observation and execution are constructed and owned inside Domain.
type DomainOptions struct {
	Store                 *PendingStore
	UploadDelay           time.Duration
	UploadWorkers         int
	Done                  chan struct{}
	Remote                RemoteOps
	View                  UploadView
	Invalidations         InvalidationSink
	Snapshot              Snapshotter
	Faults                FaultController
	ModTimeFor            func(string) time.Time
	SchedulingEnabled     func() bool
	SourceUploadSupported bool
	HealthResult          func(string, error)
}

// Domain is the single owner of upload state and transitions for one mount.
type Domain struct {
	service               *Service
	hashes                *HashTracker
	engine                *Engine
	schedulingEnabled     func() bool
	sourceUploadSupported bool
}

func NewDomain(opts DomainOptions) *Domain {
	hashes := NewHashTracker()
	service := NewService(ServiceOptions{
		UploadDelay:   opts.UploadDelay,
		UploadWorkers: opts.UploadWorkers,
		Store:         opts.Store,
		Done:          opts.Done,
		HashOps:       hashes,
	})
	domain := &Domain{
		service:               service,
		hashes:                hashes,
		schedulingEnabled:     opts.SchedulingEnabled,
		sourceUploadSupported: opts.SourceUploadSupported,
	}
	domain.engine = NewEngine(EngineDeps{
		Remote:        opts.Remote,
		Targets:       NewTargetIndex(DefaultTargetIndexTTL),
		Observer:      domainObserver{debug: service.DebugState(), healthResult: opts.HealthResult},
		Pending:       NewStoreAdapter(opts.Store),
		Runtime:       domainRuntime{domain: domain, modTimeFor: opts.ModTimeFor},
		View:          opts.View,
		Invalidations: opts.Invalidations,
		Snapshot:      opts.Snapshot,
		Faults:        opts.Faults,
	})
	return domain
}

func (d *Domain) schedulingAllowed() bool {
	return d.schedulingEnabled == nil || d.schedulingEnabled()
}

func (d *Domain) ProcessPending(ctx context.Context, pending PendingUpload) error {
	return d.service.ProcessPending(ctx, pending, ProcessOptions{
		SourceUploadSupported: d.sourceUploadSupported,
		SchedulingEnabled:     d.schedulingEnabled,
		Execute:               d.engine.Execute,
	})
}

func (d *Domain) Execute(ctx context.Context, pending PendingUpload) error {
	return d.engine.Execute(ctx, pending)
}

func (d *Domain) Receive(ctx context.Context) (PendingUpload, bool) {
	return d.service.Receive(ctx)
}

func (d *Domain) CompleteReceive(path string) { d.service.CompleteReceive(path) }
func (d *Domain) Close()                      { d.service.Close() }
func (d *Domain) Wait()                       { d.service.Wait() }
func (d *Domain) WorkerCount() int            { return d.service.WorkerCount() }
func (d *Domain) DefaultDelay() time.Duration { return d.service.DefaultDelay() }
func (d *Domain) QueuedCount() int            { return len(d.service.Queue()) }

func (d *Domain) Enqueue(p PendingUpload) {
	if d.schedulingAllowed() {
		d.service.Enqueue(p)
	}
}

func (d *Domain) EnqueueAfter(p PendingUpload, delay time.Duration) bool {
	if !d.schedulingAllowed() {
		return false
	}
	d.service.EnqueueAfter(p, delay)
	return true
}

func (d *Domain) Cancel(path string) { d.service.CancelUpload(path) }

func (d *Domain) PendingUploads() []PendingUpload { return d.service.PendingUploads() }
func (d *Domain) PendingByID(id string) (PendingUpload, bool) {
	return d.service.PendingByID(id)
}
func (d *Domain) PendingByPath(path string) (PendingUpload, bool) {
	return d.service.store.UploadByPath(path)
}
func (d *Domain) PendingByRemoteID(id string) (PendingUpload, bool) {
	return d.service.store.PendingByID(id)
}
func (d *Domain) ScheduledDeadlines() map[string]time.Time {
	return d.service.ScheduledDeadlines()
}
func (d *Domain) DebugActiveSnapshots() map[string]UploadSnapshot {
	return d.service.DebugState().ActiveSnapshots()
}
func (d *Domain) DebugHistorySnapshots() []UploadSnapshot {
	return d.service.DebugState().HistorySnapshots()
}
func (d *Domain) DebugJournal() *DebugJournal { return d.service.store.DebugJournal() }
func (d *Domain) StagingDir() string          { return d.service.store.StagingDir() }
func (d *Domain) TaskRecords() []UploadTaskRecord {
	return d.service.TaskRecords(d.service.PendingUploads())
}
func (d *Domain) Discard(path string) error {
	d.service.CancelUpload(path)
	if err := d.service.RemoveUpload(path); err != nil {
		return err
	}
	d.hashes.RemovePath(path)
	return nil
}

func (d *Domain) RemoveAfterRemoteDelete(path string) {
	if err := d.service.RemoveUpload(path); err == nil {
		d.hashes.RemovePath(path)
	}
}

func (d *Domain) RemovePending(path string) error {
	if err := d.service.store.RemoveUpload(path); err != nil {
		return err
	}
	d.service.CancelUpload(path)
	d.hashes.RemovePath(path)
	return nil
}

func (d *Domain) RemovePendingUnder(path string) error {
	if err := d.service.store.RemoveUploadsUnder(path); err != nil {
		return err
	}
	d.service.CancelChildUploads(path)
	d.hashes.RemoveUnder(path)
	return nil
}

func (d *Domain) CancelUnder(path string) {
	d.service.CancelChildUploads(path)
	d.hashes.RemoveUnder(path)
}

func (d *Domain) RenamePending(oldPath, newPath string, pending PendingUpload) error {
	if err := d.service.store.RenameUpload(oldPath, pending); err != nil {
		return err
	}
	d.hashes.RenamePath(oldPath, newPath, pending)
	if d.schedulingAllowed() {
		d.service.CancelUpload(oldPath)
		if pending.Frozen {
			d.service.Enqueue(pending)
		}
	}
	return nil
}

type RebasedUpload struct {
	OldPath string
	Pending PendingUpload
}

func (d *Domain) RebasePending(oldPath, newPath string, parents map[string]string) ([]RebasedUpload, error) {
	moved, err := d.service.store.RebaseUploadsUnder(oldPath, newPath, parents)
	if err != nil {
		return nil, err
	}
	result := make([]RebasedUpload, 0, len(moved))
	for _, next := range moved {
		oldChild := oldPath + strings.TrimPrefix(next.Path, newPath)
		d.hashes.RenamePath(oldChild, next.Path, next)
		if d.schedulingAllowed() {
			d.service.CancelUpload(oldChild)
			if next.Frozen {
				d.service.Enqueue(next)
			}
		}
		result = append(result, RebasedUpload{OldPath: oldChild, Pending: next})
	}
	return result, nil
}

func (d *Domain) RetryByID(id string) error {
	pending, ok := d.service.PendingByID(id)
	if !ok {
		return drive.ErrNotFound
	}
	return d.service.Retry(pending)
}

// Staging transitions form the write-side interface of the upload domain.
func (d *Domain) CreateStaging(fid string) (string, error) {
	return d.service.store.CreateStaging(fid)
}
func (d *Domain) RemoveStaging(path string) error { return d.service.store.RemoveStaging(path) }
func (d *Domain) RemoveStagingIfUnreferenced(path string) {
	d.service.store.RemoveStagingIfUnreferenced(path)
}
func (d *Domain) SavePending(p PendingUpload) error { return d.service.store.SaveUpload(p) }
func (d *Domain) SavePendingExact(p PendingUpload) error {
	return d.service.store.SaveUploadExact(p)
}
func (d *Domain) UpdatePendingTransient(p PendingUpload) {
	d.service.store.UpdateUploadTransient(p)
}
func (d *Domain) TryReuseFrozen(p PendingUpload) (PendingUpload, bool) {
	return d.service.store.TryReuseFrozenStaging(p)
}
func (d *Domain) SyncStaging(path string) error { return d.service.store.SyncStaging(path) }
func (d *Domain) StagingSize(path string) (int64, error) {
	return d.service.store.StagingSize(path)
}
func (d *Domain) WriteStagingAt(path string, data []byte, off int64) (int, error) {
	return d.service.store.WriteStagingAt(path, data, off)
}
func (d *Domain) TruncateStaging(path string, size int64) error {
	return d.service.store.TruncateStaging(path, size)
}
func (d *Domain) ClaimPending(p PendingUpload) (PendingUpload, bool) {
	return d.service.store.MarkUploadStarted(p)
}
func (d *Domain) RecordReplacement(p PendingUpload, replacement UploadReplacement) (PendingUpload, bool, error) {
	return d.service.store.RecordUploadReplacementIfUnchanged(p, replacement)
}

func (d *Domain) StartHashes(p PendingUpload, algorithms []drive.HashAlgorithm) {
	d.hashes.Start(p, algorithms)
}
func (d *Domain) WriteHashes(p PendingUpload, data []byte, off int64, algorithms []drive.HashAlgorithm) {
	d.hashes.Write(p, data, off, algorithms)
}
func (d *Domain) DirtyHashes(p PendingUpload)  { d.hashes.Dirty(p) }
func (d *Domain) RemoveHashesByFID(fid string) { d.hashes.RemoveFID(fid) }
func (d *Domain) SnapshotHashes(p PendingUpload, algorithms []drive.HashAlgorithm) (drive.SourceHashes, bool) {
	return d.hashes.Snapshot(p, algorithms)
}

func (d *Domain) QuietWindow(p PendingUpload) time.Duration { return d.service.QuietWindow(p) }
func (d *Domain) DebugStateByID(id string) (string, bool) {
	return d.service.DebugStateByID(id)
}
func (d *Domain) DebugActivePathByID(id string) (string, bool) {
	return d.service.DebugActivePathByID(id)
}
func (d *Domain) RemoveHistoryByID(id string) bool { return d.service.RemoveHistoryByID(id) }
func (d *Domain) Retry(p PendingUpload) error      { return d.service.Retry(p) }

type domainRuntime struct {
	domain     *Domain
	modTimeFor func(string) time.Time
}

func (r domainRuntime) ClearUploadHashes(fid string) { r.domain.hashes.RemoveFID(fid) }
func (r domainRuntime) ModTimeFor(path string) time.Time {
	if r.modTimeFor == nil {
		return time.Time{}
	}
	return r.modTimeFor(path)
}
func (r domainRuntime) RetryDelay(retryCount int) time.Duration {
	return RetryDelay(retryCount, r.domain.service.DefaultDelay())
}
func (r domainRuntime) Requeue(p PendingUpload) { r.domain.Enqueue(p) }
func (r domainRuntime) RequeueIfFrozen(p PendingUpload) {
	if p.Frozen {
		r.domain.Enqueue(p)
	}
}

type domainObserver struct {
	debug        *DebugState
	healthResult func(string, error)
}

func (o domainObserver) Start(p PendingUpload) { o.debug.Start(p) }
func (o domainObserver) Event(path, phase string, started time.Time, bytes int64, extra map[string]any) {
	o.debug.RecordEvent(path, phase, started, bytes, extra)
}
func (o domainObserver) Extra(path, key string, value any)  { o.debug.SetExtra(path, key, value) }
func (o domainObserver) State(path, state string)           { o.debug.SetState(path, state) }
func (o domainObserver) Uploaded(path string, n int)        { o.debug.UpdateBytes(path, n) }
func (o domainObserver) Finish(path, state, lastErr string) { o.debug.Finish(path, state, lastErr) }
func (o domainObserver) Metadata(path, remoteID string, hashes []string) {
	o.debug.SetMetadata(path, remoteID, hashes)
}
func (o domainObserver) HealthResult(op string, err error) {
	if o.healthResult != nil {
		o.healthResult(op, err)
	}
}
