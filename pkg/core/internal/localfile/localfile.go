// Package localfile implements the local-file stability observation service
// behind core.Core's WaitLocalFileStable facade: it polls a local file's
// size and modification time until both stay unchanged for a quiet window,
// and rejects directories and non-regular files.
//
// The package is an application service hidden behind core.Core and must not
// import pkg/core; the facade converts the public DTO to Options and maps
// Snapshot back to the public result type. Errors that reach a client keep
// the exact pkg/core namespace and wording ("core: ...") so the stable
// mobile JSON contract is unchanged. Filesystem errors from os.Stat and
// context errors pass through unwrapped.
package localfile

import (
	"context"
	"fmt"
	"os"
	"time"
)

const (
	// defaultQuiet is the quiet window used when the client does not
	// override quiet_ms.
	defaultQuiet = 2 * time.Second
	// defaultPoll is the observation interval used when the client does
	// not override poll_ms.
	defaultPoll = 250 * time.Millisecond
)

// Options is the resolved request for one stability observation.
type Options struct {
	// Quiet is the window during which size and mtime must stay
	// unchanged before the file is reported stable.
	Quiet time.Duration
	// Poll is the interval between observations while waiting.
	Poll time.Duration
}

// Snapshot is one observed state of a local file.
type Snapshot struct {
	Path       string
	Stable     bool
	Size       int64
	ModTime    time.Time
	ObservedAt time.Time
	Quiet      time.Duration
}

// ParseOptions resolves client quiet_ms/poll_ms values into durations,
// applying the package defaults for unset (zero) values and rejecting
// invalid overrides. The returned errors carry the stable client-facing
// wording.
func ParseOptions(quietMS, pollMS int) (Options, error) {
	quiet := defaultQuiet
	if quietMS != 0 {
		if quietMS < 0 {
			return Options{}, fmt.Errorf("core: quiet_ms must be non-negative")
		}
		quiet = time.Duration(quietMS) * time.Millisecond
	}
	poll := defaultPoll
	if pollMS != 0 {
		if pollMS <= 0 {
			return Options{}, fmt.Errorf("core: poll_ms must be positive")
		}
		poll = time.Duration(pollMS) * time.Millisecond
	}
	return Options{Quiet: quiet, Poll: poll}, nil
}

// Wait observes path until its size and mtime stay unchanged for opts.Quiet,
// re-checking every opts.Poll. A zero Quiet returns the first observation
// immediately without polling. The first observation happens before the
// wait loop, so a context that is already done still yields a snapshot (with
// its error) unless the initial stat already failed. On context cancellation
// or a mid-wait stat failure the returned snapshot is the most recent
// successful observation.
func Wait(ctx context.Context, path string, opts Options) (Snapshot, error) {
	snapshot, err := stat(path, opts)
	if err != nil {
		return Snapshot{}, err
	}
	if opts.Quiet == 0 {
		return snapshot, nil
	}
	stableSince := snapshot.ObservedAt
	ticker := time.NewTicker(opts.Poll)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return snapshot, ctx.Err()
		case <-ticker.C:
			next, err := stat(path, opts)
			if err != nil {
				return snapshot, err
			}
			if next.Size == snapshot.Size && next.ModTime.Equal(snapshot.ModTime) {
				snapshot = next
				if time.Since(stableSince) >= opts.Quiet {
					return snapshot, nil
				}
				continue
			}
			snapshot = next
			stableSince = next.ObservedAt
		}
	}
}

// stat observes the file once, rejecting directories and other non-regular
// files with the stable client-facing wording.
func stat(path string, opts Options) (Snapshot, error) {
	info, err := os.Stat(path)
	if err != nil {
		return Snapshot{}, err
	}
	if info.IsDir() {
		return Snapshot{}, fmt.Errorf("core: local path %q is a directory", path)
	}
	if !info.Mode().IsRegular() {
		return Snapshot{}, fmt.Errorf("core: local path %q is not a regular file", path)
	}
	return Snapshot{
		Path:       path,
		Stable:     true,
		Size:       info.Size(),
		ModTime:    info.ModTime(),
		ObservedAt: time.Now().UTC(),
		Quiet:      opts.Quiet,
	}, nil
}
