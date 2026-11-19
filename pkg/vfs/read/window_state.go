package read

import (
	"context"
	"sync"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

// Window registry: in-flight window loads, their waiters and the read
// admission helpers that bound them.

type windowChunk struct {
	ready chan struct{}
	data  []byte // Immutable after ready closes.
}

// windowLoad coalesces concurrent reads of the same cache window.
// The registry lock guards ownership; block readiness guards published bytes.
type windowLoad struct {
	fid string
	// key is the coalescing key this load was registered under. It lets a load
	// unregister only itself, so a late speculative cleanup cannot remove a
	// foreground load that took the window over.
	key     string
	start   int64
	end     int64
	streams int
	// prefetch marks a speculative load. A timed-out prefetch is a silent
	// cancellation for foreground readers, not a read failure.
	prefetch bool
	// intent is the read plan's intent (interactive or bulk). It only feeds
	// the runtime shape counters and diagnostics.
	intent    ReadIntent
	trace     *drive.ReadTrace
	createdAt time.Time
	sessionID uint64
	waiters   int
	abandoned bool
	// cancel stops this load. It is set for speculative loads so a foreground
	// reader that gives up waiting can also stop the work it is duplicating.
	cancel      context.CancelFunc
	chunks      []windowChunk
	progressive bool // Known-size loads can publish before the window finishes.
	done        chan struct{}
	data        map[int64][]byte
	extra       map[string]any
	err         error
}

// readyFor also wakes on terminal failure, which closes every missing block.
func (w *windowLoad) readyFor(index int64) <-chan struct{} {
	if len(w.chunks) != 0 {
		return w.chunks[index-w.start].ready
	}
	return w.done
}

func (w *windowLoad) result(index int64) ([]byte, error) {
	if len(w.chunks) != 0 {
		if data := w.chunks[index-w.start].data; data != nil {
			return data, nil
		}
	}
	// No block was published: readyFor only closes after err has been set.
	return w.data[index], w.err
}

func (w *windowLoad) publish(index int64, data []byte) {
	chunk := &w.chunks[index-w.start]
	chunk.data = data
	close(chunk.ready)
}

func (w *windowLoad) finishChunks() {
	for i := range w.chunks {
		if w.chunks[i].data == nil {
			close(w.chunks[i].ready)
		}
	}
}

// published reports the block at index once its fetch has published it.
// Reading the data after the ready channel closes is ordered by that close.
func (w *windowLoad) published(index int64) ([]byte, bool) {
	if len(w.chunks) == 0 {
		return nil, false
	}
	chunk := &w.chunks[index-w.start]
	select {
	case <-chunk.ready:
		if chunk.data != nil {
			return chunk.data, true
		}
	default:
	}
	return nil, false
}

func (w *windowLoad) chunkAvailable(index int64) bool {
	if len(w.chunks) == 0 {
		return false
	}
	select {
	case <-w.chunks[index-w.start].ready:
		return w.chunks[index-w.start].data != nil
	default:
		return false
	}
}

// windowState coalesces concurrent reads of the same cache window.
// Owned by the read domain; mu guards loads and the lookahead accounting.
type windowState struct {
	mu    sync.Mutex
	loads map[string]*windowLoad
	// changed wakes Quiesce waiters whenever the set of in-flight loads or
	// speculative jobs changes. Buffered so signaling never blocks a reader.
	changed chan struct{}
	// dedupedChunks counts chunks the scheduler did not admit because an
	// in-flight window already covers them; overlappingChunks counts chunks a
	// newly admitted window shares with another in-flight window, which the
	// dedupe should keep at zero.
	dedupedChunks     int64
	overlappingChunks int64
	// Window shape counters split by read intent. Playback and downloads share
	// one execution path, so the only way to tell whether a bulk download ran
	// with the shape its driver asked for is to count the windows each intent
	// actually admitted.
	interactiveLoads  int64
	interactiveChunks int64
	bulkLoads         int64
	bulkChunks        int64
}

func newWindowState() *windowState {
	return &windowState{
		loads:   map[string]*windowLoad{},
		changed: make(chan struct{}, 1),
	}
}

// quiesceSignal wakes Quiesce waiters. The send is non-blocking, so callers
// may hold windows.mu or scope locks.
func (s *State) quiesceSignal() {
	if s == nil || s.windows == nil {
		return
	}
	select {
	case s.windows.changed <- struct{}{}:
	default:
	}
}

// quiesceChanged returns the channel signaled on every quiesce-relevant change.
func (s *State) quiesceChanged() <-chan struct{} {
	return s.windows.changed
}

// windowsEmpty reports whether no window load is in flight.
func (s *State) windowsEmpty() bool {
	s.windows.mu.Lock()
	defer s.windows.mu.Unlock()
	return len(s.windows.loads) == 0
}

func (s *State) beginWindowLoad(key string, load *windowLoad) (*windowLoad, bool) {
	s.windows.mu.Lock()
	defer s.windows.mu.Unlock()
	if existing := s.windows.loads[key]; existing != nil {
		return existing, true
	}
	s.noteWindowOverlapLocked(load)
	load.key = key
	s.windows.loads[key] = load
	chunks := load.end - load.start + 1
	if load.intent == ReadIntentBulk {
		s.windows.bulkLoads++
		s.windows.bulkChunks += chunks
	} else {
		s.windows.interactiveLoads++
		s.windows.interactiveChunks += chunks
	}
	s.quiesceSignal()
	return load, false
}

// noteWindowOverlapLocked counts chunks a new load shares with loads already
// in flight. schedulePrefetch checks coverage before admitting, so a nonzero
// count means a window was admitted over ground another window already fetches.
// Callers hold windows.mu.
func (s *State) noteWindowOverlapLocked(load *windowLoad) {
	for _, other := range s.windows.loads {
		if other.fid != load.fid {
			continue
		}
		start, end := max(load.start, other.start), min(load.end, other.end)
		if end >= start {
			s.windows.overlappingChunks += end - start + 1
		}
	}
}

// noteDedupedLookahead records chunks the scheduler skipped because an
// admitted window already covers them.
func (s *State) noteDedupedLookahead(chunks int64) {
	if chunks <= 0 {
		return
	}
	s.windows.mu.Lock()
	s.windows.dedupedChunks += chunks
	s.windows.mu.Unlock()
}

// LookaheadStats reports the dedupe counters: chunks the scheduler skipped as
// already covered, and chunks overlapping windows that should never be
// admitted together.
func (s *State) LookaheadStats() (dedupedChunks, overlappingChunks int) {
	s.windows.mu.Lock()
	dedupedChunks = int(s.windows.dedupedChunks)
	overlappingChunks = int(s.windows.overlappingChunks)
	s.windows.mu.Unlock()
	return dedupedChunks, overlappingChunks
}

// WindowShapeStats reports admitted window counts and covered chunks split by
// read intent. A bulk download whose window size or worker count is overridden
// shows up here, not in the read events, which are rate limited.
func (s *State) WindowShapeStats() (interactiveLoads, interactiveChunks, bulkLoads, bulkChunks int) {
	s.windows.mu.Lock()
	interactiveLoads = int(s.windows.interactiveLoads)
	interactiveChunks = int(s.windows.interactiveChunks)
	bulkLoads = int(s.windows.bulkLoads)
	bulkChunks = int(s.windows.bulkChunks)
	s.windows.mu.Unlock()
	return interactiveLoads, interactiveChunks, bulkLoads, bulkChunks
}

// endWindowLoad unregisters load. The removal is identity-checked: a
// speculative load that finished late, or a foreground reader that gave up on
// one, must not unregister a newer load of the same window.
func (s *State) endWindowLoad(load *windowLoad) {
	if load == nil || load.key == "" {
		return
	}
	s.windows.mu.Lock()
	removed := s.windows.loads[load.key] == load
	if removed {
		delete(s.windows.loads, load.key)
	}
	s.windows.mu.Unlock()
	if removed {
		s.quiesceSignal()
	}
}

func (s *State) findWindow(cacheKey string, index int64, streams int, maxChunks int64, session uint64) *windowLoad {
	s.windows.mu.Lock()
	defer s.windows.mu.Unlock()
	for _, candidate := range s.windows.loads {
		if candidate.fid != cacheKey || index < candidate.start || index > candidate.end {
			continue
		}
		// Another session's lookahead may be stalled or pointed far from this
		// caller, so only its head block (which every progressive window
		// publishes first) or an already published block is worth waiting for.
		// Its own session may wait for any covered block: that window is the
		// one the caller's lookahead would have issued anyway.
		if candidate.prefetch && candidate.sessionID != session &&
			index != candidate.start && !candidate.chunkAvailable(index) {
			continue
		}
		candidateStreams := candidate.streams
		if candidateStreams < 1 {
			candidateStreams = 1
		}
		// Speculative windows always use one stream, even when sequential
		// foreground plans allow more. Their validated bytes are reusable;
		// hiding them also prevents the foreground's bounded takeover timer
		// from canceling its own stalled lookahead when the budget is full.
		// Progressive loads publish completed blocks while their window is in
		// flight, so joining one to read a covered block never adds provider
		// work regardless of the shape the window was admitted with. Only
		// buffered loads need the stream shape to match.
		if streams > 0 && candidateStreams != streams && !candidate.prefetch && !candidate.progressive {
			continue
		}
		// A window far larger than the caller's own window is not worth
		// waiting for unless the requested block is already published or is
		// the head of a progressive load. maxChunks <= 0 accepts any span.
		if maxChunks > 0 && candidate.end-candidate.start+1 > maxChunks &&
			(len(candidate.chunks) == 0 || index != candidate.start && !candidate.chunkAvailable(index)) {
			continue
		}
		return candidate
	}
	return nil
}

func (s *State) reservePrefetch() bool { return s.scheduler.TryAcquirePrefetch() }

func (s *State) releasePrefetch() { s.scheduler.ReleasePrefetch() }

func (s *State) beginForegroundRead(cacheKey string) {
	if cacheKey != "" {
		s.scheduler.BeginForeground()
	}
}

func (s *State) endForegroundRead(cacheKey string) {
	if cacheKey != "" {
		s.scheduler.EndForeground()
	}
}

// adaptivePrefetchLimit keeps a small lookahead during active foreground
// reads and allows a wider pipeline when the reader is otherwise idle.
func (s *State) adaptivePrefetchLimit(sequential bool) int {
	stats := s.scheduler.Stats()
	limit := stats.PrefetchCapacity
	if s.scheduler.ForegroundCount() > max(1, stats.NormalCapacity/2) {
		return 1
	}
	// Keep the configured pipeline for both initial and confirmed sequential
	// reads while the foreground is idle. The sequential flag is retained in
	// the API for future bandwidth-aware policies.
	_ = sequential
	return limit
}

// cancelPrefetch cancels only speculative reads for one file. Foreground
// reads use their caller context and are never canceled by a seek elsewhere.
func (s *State) cancelPrefetch(cacheKey string) {
	s.cancelScopesByCacheKey(cacheKey, "seek")
}

// attachWindow registers a live foreground dependency under the same lock used
// for abandonment, so cancellation cannot race a newly attached waiter.
func (s *State) attachWindow(load *windowLoad) (func(), bool) {
	s.windows.mu.Lock()
	defer s.windows.mu.Unlock()
	if load.abandoned {
		return nil, false
	}
	load.waiters++
	s.quiesceSignal()
	var once sync.Once
	return func() {
		once.Do(func() {
			s.windows.mu.Lock()
			load.waiters--
			s.windows.mu.Unlock()
			s.quiesceSignal()
		})
	}, true
}

func (s *State) abandonWindow(load *windowLoad, session uint64, scopeEnded bool) bool {
	s.windows.mu.Lock()
	defer s.windows.mu.Unlock()
	allowed := scopeEnded && load.waiters == 0 || !scopeEnded && session != 0 && load.sessionID == session && load.waiters == 1
	if !allowed || load.abandoned {
		return false
	}
	load.abandoned = true
	if s.windows.loads[load.key] == load {
		delete(s.windows.loads, load.key)
	}
	return true
}
