package core

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"
	"sync"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

// Reservations cover uploads that have a destination but have not yet created
// a staging/remote entry. All upload services belonging to a Core share these.
type uploadDestinationReservations struct {
	mu    sync.Mutex
	paths map[string]int
}

func (r *uploadDestinationReservations) reserve(p string) func() {
	if r.paths == nil {
		r.paths = make(map[string]int)
	}
	r.paths[p]++
	var once sync.Once
	return func() {
		once.Do(func() {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.paths[p]--
			if r.paths[p] == 0 {
				delete(r.paths, p)
			}
		})
	}
}

// prepareDestination returns the final destination and a reservation release.
// The caller must retain the reservation until the upload stops using its path.
func (s *UploadService) prepareDestination(ctx context.Context, dest, policy string) (string, drive.Entry, bool, func(), error) {
	policy = normalizeUploadConflictPolicy(policy)
	if policy != "rename" {
		r := s.destinations
		r.mu.Lock()
		defer r.mu.Unlock()
		entry, skipped, err := s.applyConflictPolicy(ctx, dest, policy)
		if err != nil || skipped {
			return dest, entry, skipped, func() {}, err
		}
		// A not-yet-written reserved name is still occupied. Policies that
		// forbid replacement must not race a rename upload's first write.
		if r.paths[dest] > 0 && (policy == "fail" || policy == "error" || policy == "skip") {
			return dest, drive.Entry{}, false, func() {}, fmt.Errorf("core: upload destination %q is in use", dest)
		}
		return dest, entry, skipped, r.reserve(dest), nil
	}
	r := s.destinations
	r.mu.Lock()
	defer r.mu.Unlock()
	s.backend.RefreshPath(path.Dir(dest))
	base := path.Base(dest)
	ext := path.Ext(base)
	if ext == base {
		ext = ""
	}
	stem := strings.TrimSuffix(base, ext)
	for i := 0; ; i++ {
		if err := ctx.Err(); err != nil {
			return "", drive.Entry{}, false, func() {}, err
		}
		candidate := dest
		if i > 0 {
			candidate = path.Join(path.Dir(dest), fmt.Sprintf("%s (%d)%s", stem, i, ext))
		}
		if r.paths[candidate] > 0 {
			continue
		}
		_, err := s.backend.Stat(ctx, candidate)
		if err == nil {
			continue
		}
		if !errors.Is(err, vfs.ErrNotFound) {
			return "", drive.Entry{}, false, func() {}, err
		}
		return candidate, drive.Entry{}, false, r.reserve(candidate), nil
	}
}

func (b *uploadStreamBatch) releaseDestinations() {
	for _, release := range b.destinationReleases {
		release()
	}
}
