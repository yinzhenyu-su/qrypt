package read

import (
	"sync"
	"sync/atomic"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

// Bounded metric history: summary and detail event rings.

// historyEntry pairs a debug event with its append order so the two rings
// can be merged back into one chronological list.
type historyEntry struct {
	seq   uint64
	event drive.MetricEvent
}

// eventRing is a bounded FIFO of history entries, grown lazily toward limit
// so mounts that never read do not preallocate a full ring.
type eventRing struct {
	entries []historyEntry // lazily grown toward limit
	pos     int            // ring index of the next write slot
	count   int            // number of live entries (<= len(entries))
}

// append records one entry, growing the ring toward limit.
func (r *eventRing) append(seq uint64, event drive.MetricEvent, limit int) {
	if limit < 1 {
		return
	}
	if r.entries == nil {
		r.entries = make([]historyEntry, min(64, limit))
	}
	if r.count == len(r.entries) && len(r.entries) < limit {
		// Ring full but not at the limit yet: double, preserving order.
		next := make([]historyEntry, min(2*len(r.entries), limit))
		for i := 0; i < r.count; i++ {
			next[i] = r.entries[(r.pos-r.count+i+len(r.entries))%len(r.entries)]
		}
		r.entries = next
		r.pos = r.count
	}
	r.entries[r.pos] = historyEntry{seq: seq, event: event}
	r.pos = (r.pos + 1) % len(r.entries)
	if r.count < len(r.entries) {
		r.count++
	}
}

// snapshot returns live entries in append order.
func (r *eventRing) snapshot() []historyEntry {
	if r.count == 0 {
		return nil
	}
	out := make([]historyEntry, r.count)
	for i := 0; i < r.count; i++ {
		out[i] = r.entries[(r.pos-r.count+i+len(r.entries))%len(r.entries)]
	}
	return out
}

// historyState is the bounded read-event history for debug snapshots: a
// summary ring (one event per read) and a detail ring (per chunk and per
// window), merged by append order when read. Keeping the two apart makes
// "a slow read's detail burst cannot evict earlier reads' summaries" a
// structural property instead of a capacity guess.
type historyState struct {
	mu        sync.Mutex
	summaries eventRing
	details   eventRing
	appendSeq uint64
	opSeq     uint64 // read-op sequence, atomic (NextSequence)
}

func newHistoryState() *historyState {
	return &historyState{}
}

// NextSequence returns the next read-op sequence number.
func (h *historyState) NextSequence() uint64 {
	return atomic.AddUint64(&h.opSeq, 1)
}

// AppendSummary records one read-summary event.
func (h *historyState) AppendSummary(event drive.MetricEvent) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.appendSeq++
	h.summaries.append(h.appendSeq, event, SummaryHistoryLimit)
}

// AppendDetail records one phase or per-chunk detail event.
func (h *historyState) AppendDetail(event drive.MetricEvent) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.appendSeq++
	h.details.append(h.appendSeq, event, DetailHistoryLimit)
}

// Snapshot returns live events in append order, with summary and detail
// events interleaved by when they were recorded.
func (h *historyState) Snapshot() []drive.MetricEvent {
	h.mu.Lock()
	defer h.mu.Unlock()
	summaries := h.summaries.snapshot()
	details := h.details.snapshot()
	if len(summaries) == 0 && len(details) == 0 {
		return nil
	}
	out := make([]drive.MetricEvent, 0, len(summaries)+len(details))
	i, j := 0, 0
	for i < len(summaries) && j < len(details) {
		if summaries[i].seq < details[j].seq {
			out = append(out, summaries[i].event)
			i++
			continue
		}
		out = append(out, details[j].event)
		j++
	}
	for ; i < len(summaries); i++ {
		out = append(out, summaries[i].event)
	}
	for ; j < len(details); j++ {
		out = append(out, details[j].event)
	}
	return out
}

// reset clears both rings.
func (h *historyState) reset() {
	h.mu.Lock()
	h.summaries = eventRing{}
	h.details = eventRing{}
	h.mu.Unlock()
}

// NextSequence returns the next read-op sequence number.
func (s *State) NextSequence() uint64 {
	return s.history.NextSequence()
}

// AppendHistory records one read event.
// AppendHistory records one read-summary debug event.
func (s *State) AppendHistory(event drive.MetricEvent) {
	s.history.AppendSummary(event)
}

// AppendDetailHistory records one read detail debug event (phase or chunk).
func (s *State) AppendDetailHistory(event drive.MetricEvent) {
	s.history.AppendDetail(event)
}

// HistorySnapshot returns live read events in append order.
func (s *State) HistorySnapshot() []drive.MetricEvent {
	return s.history.Snapshot()
}

// ResetHistory clears the read-event history.
func (s *State) ResetHistory() {
	s.history.reset()
}
