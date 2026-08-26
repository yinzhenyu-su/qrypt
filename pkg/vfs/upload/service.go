package upload

import (
	"maps"
	"sort"
	"sync"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/logging"
	"github.com/yinzhenyu/qrypt/pkg/util"
	"github.com/yinzhenyu/qrypt/pkg/vfs/scheduler"
)

const (
	LargeUploadQuietThreshold = 16 << 20
	LargeUploadQuietDelay     = 5 * time.Second
	maxUploadRetryDelay       = 15 * time.Minute
	snapshotHistoryLimit      = 100
)

// --- state holders ---

// DebugState keeps the bounded ring of upload snapshots for diagnostics.
type DebugState struct {
	Mu      sync.Mutex
	Active  map[string]*SnapshotState
	History []UploadSnapshot
}

func NewDebugState() *DebugState {
	return &DebugState{Active: map[string]*SnapshotState{}}
}

func (d *DebugState) RemoveHistoryByID(id string) bool {
	if id == "" {
		return false
	}
	d.Mu.Lock()
	defer d.Mu.Unlock()
	for i, upload := range d.History {
		if upload.OpID != id {
			continue
		}
		copy(d.History[i:], d.History[i+1:])
		d.History = d.History[:len(d.History)-1]
		return true
	}
	return false
}

// ActiveSnapshots returns deep copies of the in-flight upload snapshots so
// consumers can read them outside the debug lock.
func (d *DebugState) ActiveSnapshots() map[string]UploadSnapshot {
	d.Mu.Lock()
	defer d.Mu.Unlock()
	active := make(map[string]UploadSnapshot, len(d.Active))
	for path, state := range d.Active {
		active[path] = cloneSnapshot(state.Upload)
	}
	return active
}

// HistorySnapshots returns deep copies of the completed-upload history,
// oldest first.
func (d *DebugState) HistorySnapshots() []UploadSnapshot {
	d.Mu.Lock()
	defer d.Mu.Unlock()
	out := make([]UploadSnapshot, len(d.History))
	for i, upload := range d.History {
		out[i] = cloneSnapshot(upload)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].UpdatedAt.Equal(out[j].UpdatedAt) {
			return out[i].Path < out[j].Path
		}
		return out[i].UpdatedAt.Before(out[j].UpdatedAt)
	})
	return out
}

// cloneSnapshot deep-copies the slice and map fields that are mutated under
// the debug lock, so snapshot consumers can read their copy outside the
// lock without racing the upload pipeline.
func cloneSnapshot(u UploadSnapshot) UploadSnapshot {
	u.Events = append([]drive.MetricEvent(nil), u.Events...)
	u.Hashes = append([]string(nil), u.Hashes...)
	u.Extra = maps.Clone(u.Extra)
	u.StageDurations = maps.Clone(u.StageDurations)
	return u
}

// Start opens an in-flight snapshot for a newly staged pending upload.
func (d *DebugState) Start(p PendingUpload) {
	now := util.Now()
	d.Mu.Lock()
	d.Active[p.Path] = &SnapshotState{
		StageStartedAt: now,
		Upload: UploadSnapshot{
			OpID:           p.FID,
			Path:           p.Path,
			Name:           p.Name,
			State:          "starting",
			BytesTotal:     p.Size,
			StartedAt:      now,
			UpdatedAt:      now,
			RetryCount:     p.RetryCount,
			LastError:      p.LastError,
			LastAttemptAt:  p.LastAttemptAt,
			NextAttemptAt:  p.NextAttemptAt,
			ParentRemoteID: p.ParentID,
		},
	}
	d.Mu.Unlock()
}

// SetState records the phase transition of the active upload at path.
func (d *DebugState) SetState(path, state string) {
	d.Mu.Lock()
	if upload := d.Active[path]; upload != nil {
		upload.RecordStageDuration(util.Now())
		upload.Upload.State = state
		if state == string(drive.UploadPhaseInstant) {
			upload.Upload.Instant = true
		}
		upload.Upload.UpdatedAt = upload.StageStartedAt
	}
	d.Mu.Unlock()
}

// SetExtra attaches an arbitrary key to the active upload at path.
func (d *DebugState) SetExtra(path, key string, value any) {
	if key == "" {
		return
	}
	d.Mu.Lock()
	if state := d.Active[path]; state != nil {
		if state.Upload.Extra == nil {
			state.Upload.Extra = map[string]any{}
		}
		state.Upload.Extra[key] = value
		state.Upload.UpdatedAt = util.Now()
	}
	d.Mu.Unlock()
}

// SetMetadata records the committed remote ID and content hashes of the
// active upload at path.
func (d *DebugState) SetMetadata(path, resultRemoteID string, hashes []string) {
	d.Mu.Lock()
	if state := d.Active[path]; state != nil {
		if resultRemoteID != "" {
			state.Upload.ResultRemoteID = resultRemoteID
		}
		if len(hashes) > 0 {
			state.Upload.Hashes = append([]string(nil), hashes...)
		}
		state.Upload.UpdatedAt = util.Now()
	}
	d.Mu.Unlock()
}

// UpdateBytes bumps the uploaded byte count of the active upload at path.
func (d *DebugState) UpdateBytes(path string, n int) {
	if n <= 0 {
		return
	}
	d.Mu.Lock()
	if state := d.Active[path]; state != nil {
		state.Upload.BytesUploaded += int64(n)
		if state.Upload.BytesTotal >= 0 && state.Upload.BytesUploaded > state.Upload.BytesTotal {
			state.Upload.BytesUploaded = state.Upload.BytesTotal
		}
		state.Upload.UpdatedAt = util.Now()
	}
	d.Mu.Unlock()
}

// RecordEvent appends a phase event to the active upload at path.
func (d *DebugState) RecordEvent(path, phase string, start time.Time, bytes int64, extra map[string]any) {
	if phase == "" || start.IsZero() {
		return
	}
	finished := util.Now()
	duration := finished.Sub(start)
	event := drive.MetricEvent{
		At:         finished,
		Kind:       "vfs_upload",
		Operation:  "upload",
		Phase:      phase,
		State:      "completed",
		OK:         true,
		Bytes:      bytes,
		Duration:   duration.String(),
		DurationMS: durationMillis(duration),
		StartedAt:  start,
		FinishedAt: finished,
		Extra:      extra,
	}
	if message, ok := extra["error"].(string); ok && message != "" {
		event.State = "failed"
		event.OK = false
		event.Error = message
		event.ErrorCategory = drive.ErrorCategoryMessage(message)
	}
	if bytes > 0 && duration > 0 {
		event.Throughput = int64(float64(bytes) / duration.Seconds())
	}
	d.Mu.Lock()
	if state := d.Active[path]; state != nil {
		state.Upload.Events = append(state.Upload.Events, event)
	}
	d.Mu.Unlock()
}

// Finish moves the active upload at path into the bounded history.
func (d *DebugState) Finish(path, state, lastError string) {
	d.Mu.Lock()
	if upload := d.Active[path]; upload != nil {
		now := util.Now()
		upload.RecordStageDuration(now)
		upload.Upload.State = state
		upload.Upload.LastError = lastError
		if lastError != "" {
			upload.Upload.ErrorCategory = drive.ErrorCategoryMessage(lastError)
		}
		upload.Upload.UpdatedAt = now
		upload.Upload.CompletedAt = upload.Upload.UpdatedAt
		d.History = append(d.History, upload.Upload)
		if len(d.History) > snapshotHistoryLimit {
			copy(d.History, d.History[len(d.History)-snapshotHistoryLimit:])
			d.History = d.History[:snapshotHistoryLimit]
		}
		delete(d.Active, path)
	}
	d.Mu.Unlock()
}

// durationMillis converts a duration to whole milliseconds, rounding
// sub-ms deltas up (zero only when no time elapsed).
func durationMillis(d time.Duration) int64 {
	if d <= 0 {
		return 0
	}
	return int64((d + time.Millisecond - 1) / time.Millisecond)
}

// ComposeSnapshots merges the persisted pending set with the in-flight
// active snapshots into the diagnostics upload list, deriving the queued /
// scheduled / retry_wait states from the journal timers.
func ComposeSnapshots(pending []PendingUpload, active map[string]UploadSnapshot, timerPaths map[string]time.Time) []UploadSnapshot {
	uploads := make([]UploadSnapshot, 0, len(pending)+len(active))
	seen := map[string]bool{}
	for _, item := range pending {
		if upload, ok := active[item.Path]; ok {
			uploads = append(uploads, upload)
			seen[item.Path] = true
			continue
		}
		state := "queued"
		if item.PermanentFail {
			state = "failed"
		} else if _, ok := timerPaths[item.Path]; ok {
			state = "scheduled"
			if item.LastError != "" && item.NextAttemptAt > util.Now().UnixNano() {
				state = "retry_wait"
			}
		}
		uploads = append(uploads, UploadSnapshot{
			OpID:           item.FID,
			Path:           item.Path,
			Name:           item.Name,
			State:          state,
			BytesTotal:     item.Size,
			UpdatedAt:      TimeFromUnixNano(item.UpdatedAt),
			RetryCount:     item.RetryCount,
			LastError:      item.LastError,
			LastAttemptAt:  item.LastAttemptAt,
			NextAttemptAt:  item.NextAttemptAt,
			ParentRemoteID: item.ParentID,
		})
		seen[item.Path] = true
	}
	for path, upload := range active {
		if !seen[path] {
			uploads = append(uploads, upload)
		}
	}
	sort.Slice(uploads, func(i, j int) bool {
		return uploads[i].Path < uploads[j].Path
	})
	return uploads
}

type UploadSnapshot struct {
	OpID           string              `json:"op_id"`
	Mount          string              `json:"mount,omitempty"`
	Driver         string              `json:"driver,omitempty"`
	Path           string              `json:"path"`
	Name           string              `json:"name"`
	State          string              `json:"state"`
	BytesTotal     int64               `json:"bytes_total"`
	BytesUploaded  int64               `json:"bytes_uploaded"`
	StartedAt      time.Time           `json:"started_at,omitempty"`
	UpdatedAt      time.Time           `json:"updated_at,omitempty"`
	RetryCount     int                 `json:"retry_count"`
	LastError      string              `json:"last_error,omitempty"`
	LastAttemptAt  int64               `json:"last_attempt_at,omitempty"`
	NextAttemptAt  int64               `json:"next_attempt_at,omitempty"`
	CompletedAt    time.Time           `json:"completed_at,omitempty"`
	StageDurations map[string]string   `json:"stage_durations,omitempty"`
	Events         []drive.MetricEvent `json:"events,omitempty"`
	Extra          map[string]any      `json:"extra,omitempty"`
	ParentRemoteID string              `json:"parent_remote_id,omitempty"`
	ResultRemoteID string              `json:"result_remote_id,omitempty"`
	Hashes         []string            `json:"hashes,omitempty"`
	Instant        bool                `json:"instant,omitempty"`
	ErrorCategory  string              `json:"error_category,omitempty"`
}

type SnapshotState struct {
	Upload         UploadSnapshot
	StageStartedAt time.Time
	StageDurations map[string]time.Duration
}

func (s *SnapshotState) RecordStageDuration(now time.Time) {
	if s.StageStartedAt.IsZero() || s.Upload.State == "" {
		s.StageStartedAt = now
		return
	}
	if s.Upload.StageDurations == nil {
		s.Upload.StageDurations = map[string]string{}
	}
	if s.StageDurations == nil {
		s.StageDurations = map[string]time.Duration{}
	}
	s.StageDurations[s.Upload.State] += now.Sub(s.StageStartedAt)
	s.Upload.StageDurations[s.Upload.State] = s.StageDurations[s.Upload.State].String()
	s.StageStartedAt = now
}

// Admission limits concurrent uploads: one large at a time, or many small.
type Admission struct {
	mu          sync.Mutex
	activeSmall int
	activeLarge bool
}

func (a *Admission) TryAcquire(p PendingUpload, workers int) bool {
	if workers <= 0 {
		workers = 1
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if isLargeUpload(p) {
		if a.activeLarge || a.activeSmall > 0 {
			return false
		}
		a.activeLarge = true
		return true
	}
	if a.activeLarge || a.activeSmall >= workers {
		return false
	}
	a.activeSmall++
	return true
}

func (a *Admission) Release(p PendingUpload) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if isLargeUpload(p) {
		a.activeLarge = false
		return
	}
	if a.activeSmall > 0 {
		a.activeSmall--
	}
}

func isLargeUpload(p PendingUpload) bool { return p.Size >= LargeUploadQuietThreshold }

// HashOps is the subset of the write-path hash tracker the service needs.
type HashOps interface {
	RemovePath(path string)
	RemoveUnder(path string)
	RenamePath(oldPath, newPath string, p PendingUpload)
}

// UploadScheduler is the upload domain's consumer-side scheduling seam
// (per-path debounce semantics). The shared real implementation lives in
// pkg/vfs/scheduler; tests inject fakes.
type UploadScheduler = scheduler.KeyedScheduler

// ServiceOptions is the subset of vfs.Options the upload service needs.
type ServiceOptions struct {
	UploadDelay   time.Duration
	UploadWorkers int
	Store         *PendingStore
	Done          chan struct{}
	HashOps       HashOps
	// Scheduler drives the per-path upload debounce timers. When nil, the
	// real timer-backed scheduler (pkg/vfs/scheduler) is used; tests
	// inject a fake for deterministic scheduling without real time.
	Scheduler UploadScheduler
}

// --- service ---

// Service groups the upload-domain state: the persistent store, the pending
// queue, the debounce scheduler, debug snapshots, hash tracking and
// admission. Fault injection lives in pkg/vfs/faultinject; the
// engine consumes it through the FaultController interface.
type Service struct {
	store    *PendingStore
	queue    chan PendingUpload
	schedule UploadScheduler
	// scheduledMu keeps the upload generation paired with each scheduler
	// task. Schedule replaces both under this lock, so a displaced timer's
	// staging ownership can be retired exactly once.
	scheduledMu sync.Mutex
	scheduled   map[string]*scheduledUpload
	debug       *DebugState
	hashes      HashOps
	admit       Admission
	delay       time.Duration
	workers     int
	done        chan struct{}

	// enqueueMu guards enqueueWG.Add against a concurrent Wait: Close
	// locks it, marks the service stopped, and waits; sendUpload locks it
	// before spawning a background enqueue goroutine.
	enqueueMu sync.Mutex
	enqueueWG sync.WaitGroup
	stopped   bool
}

type scheduledUpload struct {
	pending PendingUpload
}

// NewService builds the upload domain state together.
func NewService(opts ServiceOptions) *Service {
	if opts.Scheduler == nil {
		opts.Scheduler = scheduler.NewTimeKeyedScheduler()
	}
	return &Service{
		store:     opts.Store,
		queue:     make(chan PendingUpload, 128),
		schedule:  opts.Scheduler,
		scheduled: map[string]*scheduledUpload{},
		debug:     NewDebugState(),
		hashes:    opts.HashOps,
		delay:     opts.UploadDelay,
		workers:   opts.UploadWorkers,
		done:      opts.Done,
	}
}

// --- store accessors ---

func (s *Service) SaveUpload(pending PendingUpload) error { return s.store.SaveUpload(pending) }
func (s *Service) SaveUploadExact(pending PendingUpload) error {
	return s.store.SaveUploadExact(pending)
}
func (s *Service) UploadByPath(path string) (PendingUpload, bool) { return s.store.UploadByPath(path) }
func (s *Service) PendingByID(id string) (PendingUpload, bool)    { return s.store.PendingByID(id) }
func (s *Service) PendingUploads() []PendingUpload                { return s.store.PendingUploads() }
func (s *Service) RemoveUpload(path string) error                 { return s.store.RemoveUpload(path) }
func (s *Service) RemoveUploadsUnder(path string) error           { return s.store.RemoveUploadsUnder(path) }
func (s *Service) RenameUpload(oldPath string, p PendingUpload) error {
	return s.store.RenameUpload(oldPath, p)
}
func (s *Service) RemoveStagingIfUnreferenced(localPath string) {
	s.store.RemoveStagingIfUnreferenced(localPath)
}

// --- hash tracker ---

func (s *Service) HashRemovePath(path string)  { s.hashes.RemovePath(path) }
func (s *Service) HashRemoveUnder(path string) { s.hashes.RemoveUnder(path) }
func (s *Service) HashRenamePath(oldPath, newPath string, p PendingUpload) {
	s.hashes.RenamePath(oldPath, newPath, p)
}

// --- admission ---

func (s *Service) TryAcquire(p PendingUpload, workers int) bool {
	return s.admit.TryAcquire(p, workers)
}
func (s *Service) Release(p PendingUpload) { s.admit.Release(p) }

// --- queue ---

func (s *Service) Queue() chan PendingUpload { return s.queue }

// SetQueue replaces the worker inbox (test seam / queue sizing).
func (s *Service) SetQueue(q chan PendingUpload) { s.queue = q }
func (s *Service) WorkerCount() int              { return s.workers }
func (s *Service) Store() *PendingStore          { return s.store }

// ScheduledDeadlines returns the pending upload-debounce deadlines per
// path (debug/diagnostics surface).
func (s *Service) ScheduledDeadlines() map[string]time.Time { return s.schedule.Deadlines() }

// --- debug state ---

func (s *Service) DebugStateByID(id string) (string, bool) {
	s.debug.Mu.Lock()
	defer s.debug.Mu.Unlock()
	for _, state := range s.debug.Active {
		if state.Upload.OpID == id {
			return state.Upload.State, true
		}
	}
	for _, snap := range s.debug.History {
		if snap.OpID == id {
			return snap.State, true
		}
	}
	return "", false
}

func (s *Service) DebugActivePathByID(id string) (string, bool) {
	s.debug.Mu.Lock()
	defer s.debug.Mu.Unlock()
	for path, state := range s.debug.Active {
		if state.Upload.OpID == id {
			return path, true
		}
	}
	return "", false
}

func (s *Service) DebugState() *DebugState { return s.debug }

// --- lifecycle ---

func (s *Service) Close() {
	s.scheduledMu.Lock()
	s.schedule.CancelAll()
	// Do not retire cancelled tasks here: a callback may already own its
	// generation even though CancelAll can no longer see its timer. Current
	// pending staging remains journal-owned; an older unreferenced generation
	// in the replace-before-next-flush window is reclaimed by the startup
	// sweep when the store is opened again.
	clear(s.scheduled)
	s.scheduledMu.Unlock()
	s.enqueueMu.Lock()
	s.stopped = true
	s.enqueueMu.Unlock()
}

// Wait blocks until background enqueue goroutines spawned when the upload
// queue was full have exited. Call after Close so no new goroutines are
// created; the VFS closes its done channel to wake the stragglers.
func (s *Service) Wait() {
	s.enqueueMu.Lock()
	defer s.enqueueMu.Unlock()
	s.enqueueWG.Wait()
}

func (s *Service) SetDone(done chan struct{}) { s.done = done }

// Retry resets a failed pending upload so it re-enters the queue immediately.
func (s *Service) Retry(pending PendingUpload) error {
	now := util.Now()
	if qw := s.QuietWindow(pending); qw > 0 {
		pending.UpdatedAt = now.Add(-qw - time.Nanosecond).UnixNano()
	} else {
		pending.UpdatedAt = now.UnixNano()
	}
	pending.PermanentFail = false
	pending.LastError = ""
	pending.NextAttemptAt = 0
	if err := s.SaveUploadExact(pending); err != nil {
		return err
	}
	if latest, ok := s.UploadByPath(pending.Path); ok {
		pending = latest
	}
	s.CancelUpload(pending.Path)
	s.EnqueueAfter(pending, 0)
	return nil
}

func (s *Service) RemoveHistoryByID(id string) bool { return s.debug.RemoveHistoryByID(id) }

// --- quiet window ---

func (s *Service) QuietDelay(p PendingUpload) time.Duration {
	qw := s.QuietWindow(p)
	if qw <= 0 || p.UpdatedAt <= 0 {
		return 0
	}
	qf := time.Since(time.Unix(0, p.UpdatedAt))
	if qf >= qw {
		return 0
	}
	return qw - qf
}

// QuietWindow returns the configured debounce window for an upload (the
// large-file minimum when the record exceeds the threshold).
func (s *Service) QuietWindow(p PendingUpload) time.Duration {
	qw := s.delay
	if p.Size >= LargeUploadQuietThreshold && qw < LargeUploadQuietDelay {
		qw = LargeUploadQuietDelay
	}
	return qw
}

func (s *Service) DefaultDelay() time.Duration { return s.delay }

// --- scheduling ---

// Enqueue sends a pending upload to the worker pool, honoring the debounce
// delay derived from the record.
func (s *Service) Enqueue(p PendingUpload) {
	if p.PermanentFail {
		logging.L.WarnfEvery("vfs.enqueue_permanent_failure", time.Second, "[VFS] skip permanently failed upload op_id=%q path=%q size=%d retry=%d last_error=%q", p.FID, p.Path, p.Size, p.RetryCount, p.LastError)
		return
	}
	s.EnqueueAfter(p, uploadDelayForRecord(p, s.delay))
}

// EnqueueAfter sends a pending upload to the worker pool after a delay.
func (s *Service) EnqueueAfter(p PendingUpload, delay time.Duration) {
	if delay > 0 {
		s.scheduleUpload(p, delay)
		return
	}
	s.sendUpload(p)
}

func (s *Service) CancelUpload(path string) {
	path = cleanVirtual(path)
	s.scheduledMu.Lock()
	s.schedule.Cancel(path)
	delete(s.scheduled, path)
	s.scheduledMu.Unlock()
}

func (s *Service) CancelChildUploads(dir string) {
	dir = cleanVirtual(dir)
	s.scheduledMu.Lock()
	s.schedule.CancelUnder(dir)
	for path := range s.scheduled {
		if path == dir || isPathUnder(path, dir) {
			delete(s.scheduled, path)
		}
	}
	s.scheduledMu.Unlock()
}

func (s *Service) scheduleUpload(p PendingUpload, delay time.Duration) {
	task := &scheduledUpload{pending: p}
	s.scheduledMu.Lock()
	old := s.scheduled[p.Path]
	replaced := s.schedule.Schedule(p.Path, delay, func() {
		s.scheduledMu.Lock()
		if s.scheduled[p.Path] == task {
			delete(s.scheduled, p.Path)
		}
		s.scheduledMu.Unlock()
		s.sendUpload(p)
	})
	s.scheduled[p.Path] = task
	s.scheduledMu.Unlock()

	if replaced {
		logging.L.DebugfEvery("vfs.reschedule_upload", time.Second, "[VFS] reschedule upload op_id=%q path=%q size=%d delay=%s", p.FID, p.Path, p.Size, delay)
	} else {
		logging.L.DebugfEvery("vfs.schedule_upload", time.Second, "[VFS] schedule upload op_id=%q path=%q size=%d delay=%s", p.FID, p.Path, p.Size, delay)
	}
	if replaced && old != nil && !sameStagingGeneration(old.pending, p) {
		s.store.RemoveStagingIfUnreferenced(old.pending.LocalPath)
	}
}

func sameStagingGeneration(a, b PendingUpload) bool {
	return a.FID == b.FID && a.LocalPath == b.LocalPath
}

func (s *Service) sendUpload(p PendingUpload) {
	select {
	case s.queue <- p:
		logging.L.DebugfEvery("vfs.Upload_enqueued", time.Second, "[VFS] upload enqueued op_id=%q path=%q size=%d retry=%d", p.FID, p.Path, p.Size, p.RetryCount)
	default:
		logging.L.Warnf("[VFS] upload queue full; blocking enqueue in background op_id=%q path=%q size=%d", p.FID, p.Path, p.Size)
		s.enqueueMu.Lock()
		if s.stopped {
			s.enqueueMu.Unlock()
			return
		}
		s.enqueueWG.Add(1)
		s.enqueueMu.Unlock()
		go func() {
			defer s.enqueueWG.Done()
			select {
			case s.queue <- p:
			case <-s.done:
			}
		}()
	}
}

func uploadDelayForRecord(p PendingUpload, fallback time.Duration) time.Duration {
	if p.NextAttemptAt <= 0 {
		return fallback
	}
	next := time.Unix(0, p.NextAttemptAt)
	if delay := time.Until(next); delay > 0 {
		return delay
	}
	return 0
}

type UploadTaskRecord struct {
	ID             string
	Mount          string
	Path           string
	Name           string
	State          string
	BytesTotal     int64
	BytesUploaded  int64
	StartedAt      time.Time
	UpdatedAt      time.Time
	CompletedAt    time.Time
	RetryCount     int
	LastError      string
	LastAttemptAt  int64
	NextAttemptAt  int64
	ParentRemoteID string
	ResultRemoteID string
	Instant        bool
	LocalPath      string
}

func UploadTaskRecordFromSnapshot(upload UploadSnapshot) UploadTaskRecord {
	record := UploadTaskRecord{
		ID:             upload.OpID,
		Mount:          upload.Mount,
		Path:           upload.Path,
		Name:           upload.Name,
		State:          upload.State,
		BytesTotal:     upload.BytesTotal,
		BytesUploaded:  upload.BytesUploaded,
		StartedAt:      upload.StartedAt,
		UpdatedAt:      upload.UpdatedAt,
		CompletedAt:    upload.CompletedAt,
		RetryCount:     upload.RetryCount,
		LastError:      upload.LastError,
		LastAttemptAt:  upload.LastAttemptAt,
		NextAttemptAt:  upload.NextAttemptAt,
		ParentRemoteID: upload.ParentRemoteID,
		ResultRemoteID: upload.ResultRemoteID,
		Instant:        upload.Instant,
	}
	if upload.Extra != nil {
		if localPath, ok := upload.Extra["local_path"].(string); ok {
			record.LocalPath = localPath
		}
	}
	return record
}

func TimeFromUnixNano(v int64) time.Time {
	if v <= 0 {
		return time.Time{}
	}
	return time.Unix(0, v)
}

func (s *Service) TaskRecords(pending []PendingUpload) []UploadTaskRecord {
	active := map[string]UploadTaskRecord{}
	s.debug.Mu.Lock()
	for path, state := range s.debug.Active {
		active[path] = UploadTaskRecordFromSnapshot(state.Upload)
	}
	history := make([]UploadTaskRecord, 0, len(s.debug.History))
	for _, upload := range s.debug.History {
		history = append(history, UploadTaskRecordFromSnapshot(upload))
	}
	s.debug.Mu.Unlock()

	timerPaths := s.schedule.Keys()

	records := make([]UploadTaskRecord, 0, len(pending)+len(active)+len(history))
	seenPath := map[string]bool{}
	for _, item := range pending {
		if upload, ok := active[item.Path]; ok {
			records = append(records, upload)
			seenPath[item.Path] = true
			continue
		}
		state := "queued"
		if item.PermanentFail {
			state = "failed"
		} else if timerPaths[item.Path] {
			state = "scheduled"
			if item.LastError != "" && item.NextAttemptAt > util.Now().UnixNano() {
				state = "retry_wait"
			}
		}
		records = append(records, UploadTaskRecord{
			ID:             item.FID,
			Path:           item.Path,
			Name:           item.Name,
			State:          state,
			BytesTotal:     item.Size,
			UpdatedAt:      TimeFromUnixNano(item.UpdatedAt),
			RetryCount:     item.RetryCount,
			LastError:      item.LastError,
			LastAttemptAt:  item.LastAttemptAt,
			NextAttemptAt:  item.NextAttemptAt,
			ParentRemoteID: item.ParentID,
			LocalPath:      item.LocalPath,
		})
		seenPath[item.Path] = true
	}
	for path, upload := range active {
		if !seenPath[path] {
			records = append(records, upload)
		}
	}
	records = append(records, history...)
	sort.Slice(records, func(i, j int) bool {
		if records[i].UpdatedAt.Equal(records[j].UpdatedAt) {
			return records[i].Path < records[j].Path
		}
		return records[i].UpdatedAt.After(records[j].UpdatedAt)
	})
	return records
}

// RetryDelay computes the exponential backoff for a retry count.
func RetryDelay(retryCount int, minimum time.Duration) time.Duration {
	if retryCount < 1 {
		retryCount = 1
	}
	delay := util.ExponentialBackoff(retryCount - 1)
	if delay < minimum {
		delay = minimum
	}
	if delay > maxUploadRetryDelay {
		delay = maxUploadRetryDelay
	}
	return delay
}
