package core

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/core/internal/localfile"
)

// LocalFileStabilityOptions configures the stability observation window.
// Zero fields use the service defaults (2000ms quiet, 250ms poll).
type LocalFileStabilityOptions struct {
	QuietMS int `json:"quiet_ms,omitempty"`
	PollMS  int `json:"poll_ms,omitempty"`
}

// LocalFileStability is the observed state of a local file once it has been
// reported stable.
type LocalFileStability struct {
	Path       string    `json:"path"`
	Stable     bool      `json:"stable"`
	Size       int64     `json:"size"`
	ModTime    time.Time `json:"mod_time"`
	ObservedAt time.Time `json:"observed_at"`
	QuietMS    int       `json:"quiet_ms"`
}

// WaitLocalFileStable waits until localPath's size and modification time
// have stayed unchanged for the quiet window, polling every poll interval.
// A zero quiet_ms (the default) is 2000ms; a zero poll_ms (the default) is
// 250ms. Directories and non-regular files are rejected; a cancelled context
// returns the most recent observation together with its error.
func (c *Core) WaitLocalFileStable(ctx context.Context, localPath string, opts LocalFileStabilityOptions) (LocalFileStability, error) {
	if c == nil {
		return LocalFileStability{}, fmt.Errorf("core: closed")
	}
	if strings.TrimSpace(localPath) == "" {
		return LocalFileStability{}, fmt.Errorf("core: local path required")
	}
	svcOpts, err := localfile.ParseOptions(opts.QuietMS, opts.PollMS)
	if err != nil {
		return LocalFileStability{}, err
	}
	snapshot, err := localfile.Wait(ctx, localPath, svcOpts)
	if err != nil {
		return toLocalFileStability(snapshot), err
	}
	return toLocalFileStability(snapshot), nil
}

// toLocalFileStability converts the service's internal snapshot to the
// public client DTO.
func toLocalFileStability(s localfile.Snapshot) LocalFileStability {
	return LocalFileStability{
		Path:       s.Path,
		Stable:     s.Stable,
		Size:       s.Size,
		ModTime:    s.ModTime,
		ObservedAt: s.ObservedAt,
		QuietMS:    int(s.Quiet / time.Millisecond),
	}
}
