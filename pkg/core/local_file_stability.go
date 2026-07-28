package core

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"
)

const (
	defaultLocalFileStableQuiet = 2 * time.Second
	defaultLocalFileStablePoll  = 250 * time.Millisecond
)

type LocalFileStabilityOptions struct {
	QuietMS int `json:"quiet_ms,omitempty"`
	PollMS  int `json:"poll_ms,omitempty"`
}

type LocalFileStability struct {
	Path       string    `json:"path"`
	Stable     bool      `json:"stable"`
	Size       int64     `json:"size"`
	ModTime    time.Time `json:"mod_time"`
	ObservedAt time.Time `json:"observed_at"`
	QuietMS    int       `json:"quiet_ms"`
}

func (c *Core) WaitLocalFileStable(ctx context.Context, localPath string, opts LocalFileStabilityOptions) (LocalFileStability, error) {
	if c == nil {
		return LocalFileStability{}, fmt.Errorf("core: closed")
	}
	if strings.TrimSpace(localPath) == "" {
		return LocalFileStability{}, fmt.Errorf("core: local path required")
	}
	quiet, poll, err := localFileStabilityDurations(opts)
	if err != nil {
		return LocalFileStability{}, err
	}
	snapshot, err := statLocalFileStability(localPath, quiet)
	if err != nil {
		return LocalFileStability{}, err
	}
	if quiet == 0 {
		return snapshot, nil
	}
	stableSince := snapshot.ObservedAt
	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return snapshot, ctx.Err()
		case <-ticker.C:
			next, err := statLocalFileStability(localPath, quiet)
			if err != nil {
				return snapshot, err
			}
			if next.Size == snapshot.Size && next.ModTime.Equal(snapshot.ModTime) {
				snapshot = next
				if time.Since(stableSince) >= quiet {
					return snapshot, nil
				}
				continue
			}
			snapshot = next
			stableSince = next.ObservedAt
		}
	}
}

func localFileStabilityDurations(opts LocalFileStabilityOptions) (time.Duration, time.Duration, error) {
	quiet := defaultLocalFileStableQuiet
	if opts.QuietMS != 0 {
		if opts.QuietMS < 0 {
			return 0, 0, fmt.Errorf("core: quiet_ms must be non-negative")
		}
		quiet = time.Duration(opts.QuietMS) * time.Millisecond
	}
	poll := defaultLocalFileStablePoll
	if opts.PollMS != 0 {
		if opts.PollMS <= 0 {
			return 0, 0, fmt.Errorf("core: poll_ms must be positive")
		}
		poll = time.Duration(opts.PollMS) * time.Millisecond
	}
	return quiet, poll, nil
}

func statLocalFileStability(localPath string, quiet time.Duration) (LocalFileStability, error) {
	info, err := os.Stat(localPath)
	if err != nil {
		return LocalFileStability{}, err
	}
	if info.IsDir() {
		return LocalFileStability{}, fmt.Errorf("core: local path %q is a directory", localPath)
	}
	if !info.Mode().IsRegular() {
		return LocalFileStability{}, fmt.Errorf("core: local path %q is not a regular file", localPath)
	}
	return LocalFileStability{
		Path:       localPath,
		Stable:     true,
		Size:       info.Size(),
		ModTime:    info.ModTime(),
		ObservedAt: time.Now().UTC(),
		QuietMS:    int(quiet / time.Millisecond),
	}, nil
}
