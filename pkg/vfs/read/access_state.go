package read

import (
	"context"
	"sync"
)

// Access tracking: sequential-run detection, access hints and per-read plan
// overrides.

type sequentialRead struct {
	end       int64
	lastChunk int64
	confirmed bool
	requestID uint64
	// run identifies one continuous forward pass over the file. It is handed to
	// the read cache so a chunk consumed by the pass that admitted it is not
	// mistaken for reuse (see readcache.Access).
	run uint64
}

type sequentialKey struct {
	cacheKey string
	session  uint64
}

type accessDecision struct {
	sequential bool
	stale      bool
	adaptive   bool
	// initial marks the first access of a session, which is a fresh stream
	// rather than a seek away from established lookahead.
	initial       bool
	discontinuous bool
	run           uint64
}

// sequentialState tracks recent per-file access patterns. It is only a
// prefetch hint: concurrent or jumping readers reset the hint without
// affecting read correctness.
type sequentialState struct {
	mu    sync.Mutex
	reads map[sequentialKey]sequentialRead
	order []sequentialKey
	// nextRun hands out process-wide run ids. Per-key counters would restart
	// when an entry is evicted by SequentialLimit and could collide with a run
	// id the cache still remembers for a chunk.
	nextRun uint64
}

func newSequentialState() *sequentialState {
	return &sequentialState{reads: map[sequentialKey]sequentialRead{}}
}

func (s *State) observeSequentialRead(cacheKey string, offset, size int64) bool {
	return s.observeReadAccess(cacheKey, AccessHint{}, offset, size).sequential
}

func (s *State) setPlanOverride(override PlanOverride) {
	s.override.Store(&override)
}

func (s *State) clearPlanOverride() {
	s.override.Store(nil)
}

func (s *State) planOverride() (PlanOverride, bool) {
	if value := s.override.Load(); value != nil {
		return *value, true
	}
	return PlanOverride{}, false
}

// nextRunFor reports the run id the access at offset belongs to: the previous
// run while the access continues it, a fresh id when it starts a new pass. It is
// the single definition of "a new run", shared by the recording path and by the
// hint the read cache asks for before data is fetched. Callers hold
// sequence.mu.
func (s *State) nextRunFor(previous sequentialRead, continues bool) uint64 {
	if continues {
		return previous.run
	}
	return s.sequence.nextRun + 1
}

// readRunHint returns the run id the next access at offset for this file will be
// recorded under, without recording anything.
//
// Reader.Read needs it for reads that carry no open-file hint: those only learn
// their own access decision after the data is read, but the chunks are admitted
// while it is being fetched. Predicting from the same rule the recording path
// uses keeps a continuous stream as one run instead of one run per read. Two
// concurrent readers of the same file can predict the same id for different
// passes; the effect is bounded to a window being treated as reuse.
func (s *State) readRunHint(cacheKey string, offset int64) uint64 {
	if cacheKey == "" {
		return 0
	}
	s.sequence.mu.Lock()
	defer s.sequence.mu.Unlock()
	previous, exists := s.sequence.reads[sequentialKey{cacheKey: cacheKey}]
	return s.nextRunFor(previous, exists && offset == previous.end)
}

func (s *State) observeReadAccess(cacheKey string, hint AccessHint, offset, size int64) accessDecision {
	adaptive := hint.SessionID != 0 && hint.RequestID != 0
	if cacheKey == "" || offset < 0 || size <= 0 {
		return accessDecision{adaptive: adaptive}
	}
	end := offset + size
	if end < offset {
		return accessDecision{adaptive: adaptive}
	}
	lastChunk := (end - 1) / ChunkSize
	key := sequentialKey{cacheKey: cacheKey}
	if adaptive {
		key.session = hint.SessionID
	}

	s.sequence.mu.Lock()
	defer s.sequence.mu.Unlock()
	previous, exists := s.sequence.reads[key]
	if adaptive && exists && hint.RequestID <= previous.requestID {
		return accessDecision{stale: true, adaptive: true, run: previous.run}
	}
	continues := exists && offset == previous.end
	// Kernel page alignment can repeat the terminal page of each unaligned
	// user read. Preserve a known session's forward coverage when the overlap
	// is bounded and does not move its end backward; canceling here would tear
	// down useful lookahead at every caller block boundary. Unidentified reads
	// retain their exact-adjacency rule.
	if adaptive && exists && offset < previous.end && end >= previous.end && previous.end-offset < ChunkSize {
		continues = true
	}
	discontinuous := !continues
	confirmed := !hint.Concurrent && exists && previous.confirmed && continues
	if !hint.Concurrent && continues && offset/ChunkSize > previous.lastChunk {
		confirmed = true
	}
	run := s.nextRunFor(previous, continues)
	if run > s.sequence.nextRun {
		s.sequence.nextRun = run
	}
	if !exists {
		s.sequence.order = append(s.sequence.order, key)
	}
	s.sequence.reads[key] = sequentialRead{
		end:       end,
		lastChunk: lastChunk,
		confirmed: confirmed,
		requestID: hint.RequestID,
		run:       run,
	}
	for len(s.sequence.order) > SequentialLimit {
		oldest := s.sequence.order[0]
		s.sequence.order = s.sequence.order[1:]
		delete(s.sequence.reads, oldest)
		if oldest.session != 0 {
			s.scopes.Cancel(WithAccessHint(context.Background(), AccessHint{SessionID: oldest.session}), oldest.cacheKey, "evicted")
		}
	}
	return accessDecision{sequential: confirmed, adaptive: adaptive, initial: !exists, discontinuous: discontinuous, run: run}
}

func (s *State) readAccessCurrent(cacheKey string, hint AccessHint) bool {
	if cacheKey == "" || hint.SessionID == 0 || hint.RequestID == 0 {
		return true
	}
	s.sequence.mu.Lock()
	defer s.sequence.mu.Unlock()
	current, ok := s.sequence.reads[sequentialKey{cacheKey: cacheKey, session: hint.SessionID}]
	return ok && current.requestID == hint.RequestID
}

// ReleaseReadSession drops access-pattern hints for a closed open-file
// handle. In-flight reads remain valid; only future prefetch decisions lose
// the retired session history.
func (s *State) ReleaseReadSession(sessionID uint64) {
	if sessionID == 0 {
		return
	}
	s.sequence.mu.Lock()
	for key := range s.sequence.reads {
		if key.session == sessionID {
			delete(s.sequence.reads, key)
		}
	}
	order := s.sequence.order[:0]
	for _, key := range s.sequence.order {
		if key.session != sessionID {
			order = append(order, key)
		}
	}
	s.sequence.order = order
	s.sequence.mu.Unlock()
	s.cancelScopesBySession(sessionID, "session_release")
}

// dropSequential forgets the access-pattern hints for one file so an
// invalidation also resets prefetch decisions.
func (s *State) dropSequential(fid string) {
	s.sequence.mu.Lock()
	kept := s.sequence.order[:0]
	for _, key := range s.sequence.order {
		if key.cacheKey == fid {
			delete(s.sequence.reads, key)
			continue
		}
		kept = append(kept, key)
	}
	s.sequence.order = kept
	s.sequence.mu.Unlock()
}
