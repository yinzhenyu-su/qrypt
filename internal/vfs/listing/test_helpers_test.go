package listing

import (
	"context"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

// stubHost is a no-op Host for unit tests of listing-domain state.
type stubHost struct {
	cache map[string]listCacheEntry
}

func (stubHost) Resolve(context.Context, string) (drive.Entry, error) {
	return drive.Entry{}, nil
}

func (stubHost) RecordHealth(string, error) {}

func (stubHost) ListChildren(context.Context, string) ([]drive.Entry, error) {
	return nil, nil
}

func (stubHost) IsUnavailable(string) bool { return false }

func (stubHost) UpdateOverlay(string, []drive.Entry) {}

func (stubHost) GetEntry(string) (drive.Entry, bool) { return drive.Entry{}, false }

func (h stubHost) FreshListCache(parentPath string, now time.Time) ([]drive.Entry, bool) {
	if h.cache == nil {
		return nil, false
	}
	cached, ok := h.cache[parentPath]
	if !ok || !now.Before(cached.expires) {
		return nil, false
	}
	return cached.entries, true
}

func (stubHost) CommitRemoteChildren(string, []drive.Entry, time.Time) []drive.Entry {
	return nil
}

func (stubHost) ProjectChildren(string, []drive.Entry) []drive.Entry { return nil }
