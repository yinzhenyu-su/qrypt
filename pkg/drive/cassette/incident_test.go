package cassette_test

import (
	"bytes"
	"context"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/drive/cassette"
)

// incidentFile is the committed synthetic incident cassette: a 32 KiB object
// read as two 16 KiB parallel parts over connections limited to ~140 KB/s with
// a ~4 s TTFB, where the first attempt returns a clean short body.
const incidentFile = "testdata/incident-throttle.json"

const (
	incidentFileSize = 32768
	incidentPartSize = 16384
	// incidentScale replays the four-second TTFB as eight milliseconds.
	incidentScale = 0.002
)

// replayIncident loads the committed incident cassette and runs the read layer
// over it with the given time scale. It returns the delivered bytes, the read
// trace snapshot, the replay counters and the wall-clock fetch duration.
func replayIncident(t *testing.T, scale float64) ([]byte, map[string]any, cassette.ReplayStats, time.Duration) {
	t.Helper()
	c, err := cassette.LoadFile(incidentFile)
	if err != nil {
		t.Fatalf("load incident cassette: %v", err)
	}
	replayer, err := cassette.NewReplayer(c, cassette.WithTimeScale(scale))
	if err != nil {
		t.Fatalf("new replayer: %v", err)
	}
	source, err := drive.NewReadSource(replayer, nil)
	if err != nil {
		t.Fatalf("new replay source: %v", err)
	}
	defer source.Close()

	entry := drive.Entry{ID: "0/movie.bin", Name: "movie.bin", Size: incidentFileSize}
	trace := &drive.ReadTrace{}
	ctx, cancel := context.WithTimeout(drive.WithReadTrace(context.Background(), trace), 10*time.Second)
	defer cancel()
	plan := drive.RangePlan{
		Size:    incidentFileSize,
		Workers: 2,
		Part:    incidentPartSize,
		// Scaled timeouts: the recorded 4 s TTFB becomes 8 ms, so the idle bound
		// stays far above the paced body gaps while a real stall would trip it.
		Timeouts: drive.FetchTimeouts{Idle: 60 * time.Millisecond, Total: 600 * time.Millisecond},
	}

	started := time.Now()
	data, err := source.Fetch(ctx, entry, plan)
	elapsed := time.Since(started)
	if err != nil {
		t.Fatalf("incident fetch: %v", err)
	}
	return data, trace.Snapshot(), replayer.Stats(), elapsed
}

// snapshotInt reads one numeric snapshot field, accepting the integer types the
// diagnostics map uses for counts and the int64 types it uses for durations.
func snapshotInt(t *testing.T, snapshot map[string]any, key string) int64 {
	t.Helper()
	value, ok := snapshot[key]
	if !ok {
		t.Fatalf("trace snapshot has no %q: %v", key, snapshot)
	}
	switch n := value.(type) {
	case int:
		return int64(n)
	case int64:
		return n
	default:
		t.Fatalf("trace snapshot %q = %T, want an integer", key, value)
		return 0
	}
}

// TestIncidentCassetteDrivesReadLayer replays the incident at 1/500 speed and
// checks that the read layer reproduces the recorded shape: two parallel parts,
// the truncated first attempt replayed as a whole-range retry, capped window
// splitting, and provider traffic 25% above delivered bytes.
func TestIncidentCassetteDrivesReadLayer(t *testing.T) {
	data, snapshot, stats, elapsed := replayIncident(t, incidentScale)

	if want := incidentPayload(incidentFileSize); !bytes.Equal(data, want) {
		t.Fatal("incident replay delivered the wrong bytes")
	}
	if stats.Requests != 3 || stats.Misses != 0 {
		t.Fatalf("replay matched %d requests (%d misses), want 3", stats.Requests, stats.Misses)
	}
	// Bytes counts every body byte the read layer consumed, including the
	// truncated first attempt, so it exceeds the bytes Fetch ultimately returns.
	if stats.Bytes != 40960 {
		t.Fatalf("replay consumed %d body bytes, want 40960", stats.Bytes)
	}
	// The truncated first attempt was followed by a full re-request: 8 KiB +
	// 16 KiB + 16 KiB of provider traffic for 32 KiB delivered.
	if stats.ProviderBytes != 40960 {
		t.Fatalf("replay provider traffic = %d bytes, want 40960", stats.ProviderBytes)
	}
	if stats.PeakConcurrent != 2 {
		t.Fatalf("replay peak concurrency = %d, want 2", stats.PeakConcurrent)
	}

	checks := map[string]int64{
		"granted_streams":  2,
		"window_bytes":     incidentFileSize,
		"part_bytes":       incidentPartSize,
		"range_tasks":      2,
		"actual_in_flight": 2,
		"body_bytes":       40960,
		"progress_end":     incidentFileSize,
		"fetch_attempts":   2,
	}
	for key, want := range checks {
		if got := snapshotInt(t, snapshot, key); got != want {
			t.Errorf("trace %s = %d, want %d", key, got, want)
		}
	}
	if reason, _ := snapshot["fetch_retry_reason"].(string); reason != "truncated_body" {
		t.Errorf("trace fetch_retry_reason = %q, want %q", reason, "truncated_body")
	}
	if _, ok := snapshot["fetch_timeouts"]; ok {
		t.Errorf("trace reported fetch_timeouts despite no stall: %v", snapshot)
	}

	// Amplification: provider bytes per delivered byte. The replayed window must
	// show the same 25% overhead that made the incident visible.
	amplification := float64(snapshotInt(t, snapshot, "body_bytes")) / float64(snapshotInt(t, snapshot, "progress_end"))
	if want := 1.25; amplification < want-0.001 || amplification > want+0.001 {
		t.Errorf("replay amplification = %.3f, want %.3f", amplification, want)
	}

	// The scaled TTFB dominates the fetch: two sequential attempts at 8 ms each
	// cannot finish sooner, and nothing should take anywhere near a second.
	if elapsed < 10*time.Millisecond {
		t.Fatalf("incident replay finished in %v, too fast to include the two scaled TTFBs", elapsed)
	}
	if elapsed > time.Second {
		t.Fatalf("incident replay took %v, want a few milliseconds at %vx", elapsed, incidentScale)
	}
}

// TestIncidentReplayDeterministic runs the same cassette twice and checks that
// the structural outcome is identical: same bytes, same window shape, same
// retry counters and same provider traffic. Timing keys are excluded because
// they are wall-clock observations, not execution shape.
func TestIncidentReplayDeterministic(t *testing.T) {
	firstData, firstSnapshot, firstStats, _ := replayIncident(t, incidentScale)
	secondData, secondSnapshot, secondStats, _ := replayIncident(t, incidentScale)

	if !bytes.Equal(firstData, secondData) {
		t.Fatal("two replays of the same cassette delivered different bytes")
	}
	structuralKeys := []string{
		"granted_streams", "window_bytes", "part_bytes", "range_tasks",
		"fetch_attempts", "fetch_retry_reason", "actual_in_flight",
		"body_bytes", "progress_end",
	}
	first := make(map[string]any, len(structuralKeys))
	second := make(map[string]any, len(structuralKeys))
	for _, key := range structuralKeys {
		first[key], _ = firstSnapshot[key]
		second[key], _ = secondSnapshot[key]
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("replays differ structurally:\n first: %v\nsecond: %v", first, second)
	}

	if firstStats.Requests != secondStats.Requests || firstStats.Misses != secondStats.Misses ||
		firstStats.Bytes != secondStats.Bytes || firstStats.ProviderBytes != secondStats.ProviderBytes ||
		firstStats.PeakConcurrent != secondStats.PeakConcurrent {
		t.Fatalf("replay counters differ:\n first: %+v\nsecond: %+v", firstStats, secondStats)
	}
	// Parallel parts complete in whatever order the scheduler grants them, so
	// compare the per-request shapes as a sorted multiset rather than by order.
	shape := func(stats cassette.ReplayStats) [][5]int64 {
		out := make([][5]int64, len(stats.Results))
		for i, result := range stats.Results {
			out[i] = [5]int64{result.Offset, result.Size, int64(result.Attempt), result.Bytes, result.ProviderBytes}
		}
		sort.Slice(out, func(i, j int) bool {
			if out[i][0] != out[j][0] {
				return out[i][0] < out[j][0]
			}
			if out[i][1] != out[j][1] {
				return out[i][1] < out[j][1]
			}
			return out[i][2] < out[j][2]
		})
		return out
	}
	if !reflect.DeepEqual(shape(firstStats), shape(secondStats)) {
		t.Fatalf("replayed request shapes differ:\n first: %v\nsecond: %v", shape(firstStats), shape(secondStats))
	}
}
