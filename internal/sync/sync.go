package sync

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

// ErrNoSession reports that --resume found no resumable session for the
// given source/destination pair. The CLI maps it to a usage error.
var ErrNoSession = errors.New("sync: no resumable sync session")

// Request is everything a sync run needs beyond the filesystem. The CLI
// builds it from cobra flags; tests build it directly.
type Request struct {
	Source      Target
	Destination Target
	DryRun      bool
	Delete      bool
	Hash        bool
	CompareMode string
	Conflict    string
	Resume      bool
}

// WaitIdle drains asynchronous VFS work (uploads, queued deletes) so the
// destination is actually synced when Run returns. The CLI passes its
// filesystem-activity waiter; tests can pass a no-op.
type WaitIdle func(ctx context.Context, fs vfs.FileSystem) error

// Run performs a sync: snapshot both sides, compare, plan, execute, and
// manage the resumable session. The filesystem must already be started.
func Run(ctx context.Context, fs vfs.FileSystem, waitIdle WaitIdle, req Request) (Result, error) {
	if req.Source.Kind == TargetLocal && req.Destination.Kind == TargetLocal {
		return Result{}, fmt.Errorf("at least one side must be a virtual path (/MOUNT/...): got %q and %q", req.Source.Raw, req.Destination.Raw)
	}
	switch req.Conflict {
	case "error", "skip", "source":
	default:
		return Result{}, fmt.Errorf("--conflict must be error, skip, or source (got %q)", req.Conflict)
	}
	// A destination nested under its own source would recurse forever.
	if err := CheckContainment(req.Source, req.Destination); err != nil {
		return Result{}, err
	}
	if req.Resume {
		return resume(ctx, fs, waitIdle, req)
	}

	skipSize, modeHash, err := ParseCompareMode(req.CompareMode)
	if err != nil {
		return Result{}, err
	}
	forceHash := req.Hash || modeHash
	snapA, err := SnapshotTarget(ctx, fs, req.Source)
	if err != nil {
		return Result{}, err
	}
	// The destination may not exist yet; sync treats it as an empty tree so
	// the whole tree is added (mkdir happens during execution).
	snapB, err := SnapshotTarget(ctx, fs, req.Destination)
	if err != nil && !errors.Is(err, vfs.ErrNotFound) && !errors.Is(err, os.ErrNotExist) {
		return Result{}, err
	}
	if snapB == nil {
		snapB = Snapshot{}
	}
	// Sync always attempts content-hash comparison; AutoHash degrades to
	// size/mtime when the backends cannot provide hashes. --hash forces it
	// (missing hash support becomes an error).
	opts := CompareOptions{AsHash: true, AutoHash: !forceHash, SkipSize: skipSize}
	opts.Hash = func(ctx context.Context, rel string) (bool, string, error) {
		return CompareHashPair(ctx, fs, req.Source, req.Destination, rel, opts.AutoHash)
	}
	diffs, err := CompareTrees(ctx, snapA, snapB, opts)
	if err != nil {
		return Result{}, err
	}

	// mtime-only forces mtime-driven updates even when the destination
	// backend cannot persist mtimes (the user explicitly opted into the
	// strategy; on such backends every sync re-updates files because the
	// stored mtime is the upload time).
	compareMTime := TargetSupportsMTime(fs, req.Destination) || req.CompareMode == "mtime-only"
	plan := Plan(diffs, snapA, snapB, req.Delete, req.Conflict, compareMTime)
	result := Result{
		Source:      req.Source.Raw,
		Destination: req.Destination.Raw,
		Entries:     plan,
	}
	for _, entry := range plan {
		result.Summary.Add(entry)
	}

	result.DryRun = req.DryRun
	if !req.DryRun {
		// A fresh run supersedes any interrupted session for this pair.
		PruneExpired()
		persist, err := NewSession(req.Source, req.Destination, SessionFlags{
			Delete: req.Delete, Hash: forceHash, Conflict: req.Conflict,
		}, plan)
		if err != nil {
			return Result{}, err
		}
		if err := EnsureRoot(ctx, fs, req.Destination); err != nil {
			persist.Remove()
			return Result{}, fmt.Errorf("create sync destination: %w", err)
		}
		failed := ExecutePlan(ctx, fs, plan, req.Source, req.Destination, persist, waitIdle)
		result.Summary.Failed = failed
		// Wait for async VFS uploads to land so the destination is actually
		// synced when the command returns. No deadline: transfer size is
		// unbounded and Ctrl-C interrupts (the session stays for --resume).
		if err := waitIdle(ctx, fs); err != nil {
			persist.Close()
			return result, err
		}
		// A clean run removes the session; partial failures stay on disk so
		// a later --resume retries the remaining and failed items.
		if !persist.TransferPending() {
			persist.Remove()
		} else {
			persist.Close()
		}
	}
	result.OK = result.Summary.Failed == 0 && !(result.Summary.Conflict > 0 && req.Conflict == "error")
	return result, nil
}

// resume continues an interrupted sync session: it loads the saved plan,
// skips ops that already finished OK (failed ops are retried) and executes
// the remainder without re-scanning either tree.
func resume(ctx context.Context, fs vfs.FileSystem, waitIdle WaitIdle, req Request) (Result, error) {
	persist, found, err := LoadSession(req.Source, req.Destination)
	if err != nil {
		return Result{}, err
	}
	if !found {
		return Result{}, fmt.Errorf("%w for %q -> %q", ErrNoSession, req.Source.Raw, req.Destination.Raw)
	}
	defer persist.Close()

	// The session plan carries the original semantics; the caller does not
	// need to repeat --delete/--hash/--conflict.
	pending := persist.PendingOps()
	result := Result{
		Source:      req.Source.Raw,
		Destination: req.Destination.Raw,
		Entries:     pending,
	}
	for _, entry := range pending {
		result.Summary.Add(entry)
	}

	if err := EnsureRoot(ctx, fs, req.Destination); err != nil {
		return result, fmt.Errorf("create sync destination: %w", err)
	}
	failed := ExecutePlan(ctx, fs, pending, req.Source, req.Destination, persist, waitIdle)
	result.Summary.Failed = failed
	// No deadline: the original run had no bound on transfer size and a
	// fixed timeout would report a slow upload as failed.
	if err := waitIdle(ctx, fs); err != nil {
		return result, err
	}
	result.OK = result.Summary.Failed == 0 && !(result.Summary.Conflict > 0 && persist.Flags().Conflict == "error")
	if !persist.TransferPending() {
		persist.Remove()
	}
	return result, nil
}
