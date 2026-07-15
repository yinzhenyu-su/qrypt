package vfs

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

var ErrPolicyDenied = errors.New("vfs: policy denied")

type Policy interface{}

// OperationHook observes VFS operation and metric events.
//
// AfterOperation can be called for events that did not pass through the same
// hook's BeforeOperation. VFS uses this path for already-completed metric
// events such as read and upload phase records.
type OperationHook interface {
	BeforeOperation(ctx context.Context, event drive.MetricEvent) (context.Context, error)
	AfterOperation(ctx context.Context, event drive.MetricEvent)
}

type PathPolicy interface {
	IsReadOnlyPath(path string) bool
	IgnorePath(path string) (ignore bool, reason string)
}

type UploadPolicy interface {
	UploadDelay(ctx context.Context, req UploadDecision) time.Duration
	UploadWorkers(defaultWorkers int) int
	ShouldIgnoreUpload(ctx context.Context, req UploadDecision) (ignore bool, reason string)
}

type DeletePolicy interface {
	DeleteDelay(ctx context.Context, req DeleteDecision) time.Duration
}

type ReadPolicy interface {
	ShouldUseReadCache(ctx context.Context, req ReadDecision) bool
	Prefetch(ctx context.Context, req ReadDecision) PrefetchDecision
}

type PathCategory string

const (
	PathCategoryRegular       PathCategory = "regular"
	PathCategoryDotFile       PathCategory = "dot_file"
	PathCategoryTempFile      PathCategory = "temp_file"
	PathCategoryAppleMetadata PathCategory = "apple_metadata"
	PathCategoryAppleXattr    PathCategory = "apple_xattr"
)

type UploadDecision struct {
	Path       string
	Name       string
	Category   PathCategory
	Size       int64
	IsZeroByte bool
	ModTime    time.Time
	Driver     string
}

type DeleteDecision struct {
	Path   string
	IsDir  bool
	Driver string
}

type ReadDecision struct {
	Path     string
	RemoteID string
	Offset   int64
	Size     int64
	FileSize int64
	Driver   string
}

type PrefetchDecision struct {
	Enabled      bool
	ChunksBefore int
	ChunksAfter  int
}

type PolicyError struct {
	Operation string
	Path      string
	Reason    string
}

func (e PolicyError) Error() string {
	if e.Reason == "" {
		return e.Operation + " " + e.Path + ": " + ErrPolicyDenied.Error()
	}
	return e.Operation + " " + e.Path + ": " + ErrPolicyDenied.Error() + ": " + e.Reason
}

func (e PolicyError) Unwrap() error {
	return ErrPolicyDenied
}

type policySet struct {
	hooks   []OperationHook
	paths   []PathPolicy
	uploads []UploadPolicy
	deletes []DeletePolicy
	reads   []ReadPolicy
}

func collectPolicies(policies []Policy) policySet {
	var set policySet
	for _, policy := range policies {
		if policy == nil {
			continue
		}
		if hook, ok := policy.(OperationHook); ok {
			set.hooks = append(set.hooks, hook)
		}
		if path, ok := policy.(PathPolicy); ok {
			set.paths = append(set.paths, path)
		}
		if upload, ok := policy.(UploadPolicy); ok {
			set.uploads = append(set.uploads, upload)
		}
		if deletePolicy, ok := policy.(DeletePolicy); ok {
			set.deletes = append(set.deletes, deletePolicy)
		}
		if read, ok := policy.(ReadPolicy); ok {
			set.reads = append(set.reads, read)
		}
	}
	return set
}

func (set policySet) uploadWorkers(defaultWorkers int) int {
	workers := defaultWorkers
	for _, policy := range set.uploads {
		workers = policy.UploadWorkers(workers)
		if workers < 1 {
			workers = 1
		}
	}
	return workers
}

func (set policySet) isReadOnlyPath(path string) bool {
	path = cleanVirtual(path)
	for _, policy := range set.paths {
		if policy.IsReadOnlyPath(path) {
			return true
		}
	}
	return false
}

func (set policySet) ignoredPath(path string) (bool, string) {
	path = cleanVirtual(path)
	for _, policy := range set.paths {
		if ignore, reason := policy.IgnorePath(path); ignore {
			return true, reason
		}
	}
	return false, ""
}

func (set policySet) uploadDelay(ctx context.Context, req UploadDecision, base time.Duration) time.Duration {
	delay := base
	for _, policy := range set.uploads {
		if candidate := policy.UploadDelay(ctx, req); candidate > delay {
			delay = candidate
		}
	}
	return delay
}

func (set policySet) ignoredUpload(ctx context.Context, req UploadDecision) (bool, string) {
	for _, policy := range set.uploads {
		if ignore, reason := policy.ShouldIgnoreUpload(ctx, req); ignore {
			return true, reason
		}
	}
	return false, ""
}

func (set policySet) deleteDelay(ctx context.Context, req DeleteDecision, base time.Duration) time.Duration {
	delay := base
	for _, policy := range set.deletes {
		if candidate := policy.DeleteDelay(ctx, req); candidate > delay {
			delay = candidate
		}
	}
	return delay
}

func (set policySet) useReadCache(ctx context.Context, req ReadDecision) bool {
	for _, policy := range set.reads {
		if !policy.ShouldUseReadCache(ctx, req) {
			return false
		}
	}
	return true
}

func (set policySet) prefetch(ctx context.Context, req ReadDecision, defaultBefore, defaultAfter int) PrefetchDecision {
	decision := PrefetchDecision{Enabled: true, ChunksBefore: defaultBefore, ChunksAfter: defaultAfter}
	for _, policy := range set.reads {
		next := policy.Prefetch(ctx, req)
		if !next.Enabled {
			return PrefetchDecision{Enabled: false}
		}
		if next.ChunksBefore > decision.ChunksBefore {
			decision.ChunksBefore = next.ChunksBefore
		}
		if next.ChunksAfter > decision.ChunksAfter {
			decision.ChunksAfter = next.ChunksAfter
		}
	}
	return decision
}

func pathCategory(path string) PathCategory {
	name := filepath.Base(cleanVirtual(path))
	lower := strings.ToLower(name)
	switch {
	case strings.HasPrefix(lower, "com.apple."):
		return PathCategoryAppleXattr
	case isAppleMetadataName(name):
		return PathCategoryAppleMetadata
	case strings.HasPrefix(name, "."):
		return PathCategoryDotFile
	case strings.HasPrefix(name, "~$") || strings.HasSuffix(name, "~") || strings.HasSuffix(lower, ".tmp") || strings.HasSuffix(lower, ".temp"):
		return PathCategoryTempFile
	default:
		return PathCategoryRegular
	}
}

type operationHooks []OperationHook

func (v *VFS) beginOperation(ctx context.Context, operation, path string, bytes, offset int64) (context.Context, time.Time, operationHooks, error) {
	started := time.Now()
	event := v.operationEvent(operation, path, started, bytes, offset)
	var ran operationHooks
	for _, hook := range v.policy.hooks {
		ran = append(ran, hook)
		nextCtx, err := hook.BeforeOperation(ctx, event)
		if err != nil {
			denied := policyDeniedError(operation, path, err)
			finished := v.finishOperationEvent(event, started, denied)
			ran.after(ctx, finished)
			return ctx, started, nil, denied
		}
		if nextCtx != nil {
			ctx = nextCtx
		}
	}
	return ctx, started, ran, nil
}

func (hooks operationHooks) after(ctx context.Context, event drive.MetricEvent) {
	for i := len(hooks) - 1; i >= 0; i-- {
		hooks[i].AfterOperation(ctx, event)
	}
}

func (v *VFS) finishOperation(ctx context.Context, hooks operationHooks, operation, path string, started time.Time, bytes, offset int64, err error) {
	if len(hooks) == 0 {
		return
	}
	event := v.finishOperationEvent(v.operationEvent(operation, path, started, bytes, offset), started, err)
	hooks.after(ctx, event)
}

func (v *VFS) dispatchMetricEvent(ctx context.Context, event drive.MetricEvent) {
	if len(v.policy.hooks) == 0 {
		return
	}
	operationHooks(v.policy.hooks).after(ctx, event)
}

func (v *VFS) operationEvent(operation, path string, started time.Time, bytes, offset int64) drive.MetricEvent {
	return drive.MetricEvent{
		At:        started,
		Kind:      "vfs_operation",
		Operation: operation,
		Phase:     "operation",
		State:     "started",
		OK:        true,
		Mount:     v.name,
		Path:      cleanVirtual(path),
		Bytes:     bytes,
		Offset:    offset,
		StartedAt: started,
	}
}

func (v *VFS) finishOperationEvent(event drive.MetricEvent, started time.Time, err error) drive.MetricEvent {
	finished := time.Now()
	duration := finished.Sub(started)
	event.At = finished
	event.FinishedAt = finished
	event.Duration = duration.String()
	event.DurationMS = durationMillis(duration)
	event.State = "completed"
	event.OK = true
	if err != nil {
		event.State = "failed"
		event.OK = false
		event.Error = err.Error()
		event.ErrorCategory = drive.ErrorCategory(err)
	}
	return event
}

func policyDeniedError(operation, path string, err error) error {
	if err == nil {
		return nil
	}
	if drive.IsNonRetryable(err) {
		return err
	}
	var policyErr PolicyError
	if errors.As(err, &policyErr) {
		return drive.NonRetryable(err)
	}
	return drive.NonRetryable(PolicyError{Operation: operation, Path: cleanVirtual(path), Reason: err.Error()})
}

func (v *VFS) IsReadOnlyPath(path string) bool {
	return v.isReadOnlyPath(path)
}

func (v *VFS) isReadOnlyPath(path string) bool {
	return v.policy.isReadOnlyPath(path)
}

// filterIgnoredEntries filters entries in place and returns a slice backed by
// the same array.
func (v *VFS) filterIgnoredEntries(parentPath string, entries []drive.Entry) []drive.Entry {
	if len(entries) == 0 || len(v.policy.paths) == 0 {
		return entries
	}
	filtered := entries[:0]
	for _, entry := range entries {
		if ignore, _ := v.policy.ignoredPath(joinVirtual(parentPath, entry.Name)); ignore {
			continue
		}
		filtered = append(filtered, entry)
	}
	return filtered
}

func (v *VFS) uploadDecision(pending PendingFile) UploadDecision {
	return UploadDecision{
		Path:       cleanVirtual(pending.Path),
		Name:       pending.Name,
		Category:   pathCategory(pending.Path),
		Size:       pending.Size,
		IsZeroByte: pending.Size == 0,
		ModTime:    pendingModTime(pending),
		Driver:     v.policyDriver(),
	}
}

func (v *VFS) deleteDecision(path string, entry drive.Entry) DeleteDecision {
	return DeleteDecision{
		Path:   cleanVirtual(path),
		IsDir:  entry.IsDir,
		Driver: v.policyDriver(),
	}
}

func (v *VFS) readDecision(path string, entry drive.Entry, offset, size int64) ReadDecision {
	return ReadDecision{
		Path:     cleanVirtual(path),
		RemoteID: entry.ID,
		Offset:   offset,
		Size:     size,
		FileSize: entry.Size,
		Driver:   v.policyDriver(),
	}
}

func (v *VFS) policyDriver() string {
	return fmt.Sprintf("%T", v.driver)
}
