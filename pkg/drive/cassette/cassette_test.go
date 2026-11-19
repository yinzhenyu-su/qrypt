package cassette_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"reflect"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/drive/cassette"
)

// incidentPayload returns deterministic bytes so recorded chunks and replayed
// bodies can be compared exactly.
func incidentPayload(size int) []byte {
	data := make([]byte, size)
	for i := range data {
		data[i] = byte((i*37 + i/251) % 251)
	}
	return data
}

// pacedBody serves data in fixed-size blocks, sleeping delay before each block
// after the first. It emulates a provider whose TTFB is already paid (the open
// latency) but whose body arrives at a steady per-connection rate.
type pacedBody struct {
	ctx    context.Context
	data   []byte
	offset int
	block  int
	delay  time.Duration
}

func (b *pacedBody) Read(p []byte) (int, error) {
	if b.offset >= len(b.data) {
		return 0, io.EOF
	}
	if b.offset > 0 && b.offset%b.block == 0 {
		if err := sleepContext(b.ctx, b.delay); err != nil {
			return 0, err
		}
	}
	end := min(len(b.data), b.offset+b.block)
	n := copy(p, b.data[b.offset:end])
	b.offset += n
	return n, nil
}

func (b *pacedBody) Close() error { return nil }

func sleepContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// seedReadFile seeds a FakeDriver with one file and returns its entry.
func seedReadFile(t *testing.T, d *drive.FakeDriver, name string, payload []byte) drive.Entry {
	t.Helper()
	if err := d.Seed(map[string]string{name: string(payload)}); err != nil {
		t.Fatalf("seed fake driver: %v", err)
	}
	return drive.Entry{ID: "0/" + name, Name: name, Size: int64(len(payload))}
}

// TestRecordReplayRoundTrip records a two-part parallel window from a driver
// with 25 ms TTFB and a 15 ms-per-512 KiB body, then replays it at 1x. The
// replay must preserve the request count, the delivered bytes and the relative
// timing shape (open latency and per-chunk delivery) within a tolerance; the
// cassette must survive a JSON round-trip.
func TestRecordReplayRoundTrip(t *testing.T) {
	const (
		fileSize   = 2 << 20
		partBytes  = 1 << 20
		openDelay  = 25 * time.Millisecond
		blockDelay = 15 * time.Millisecond
	)
	payload := incidentPayload(fileSize)

	fake := drive.NewFakeDriver(drive.FakeWithCapabilities(drive.CapabilityParallelRange))
	entry := seedReadFile(t, fake, "movie.bin", payload)
	fake.ReadDelay = openDelay
	fake.ReadBehavior = drive.ReadBehavior{
		BodyFunc: func(ctx context.Context, data []byte) io.ReadCloser {
			return &pacedBody{ctx: ctx, data: data, block: drive.ReadChunkSize, delay: blockDelay}
		},
	}

	recorder := cassette.NewRecorder(fake)
	recorder.Notes = "round-trip: 25ms TTFB, 512KiB per 15ms"
	recordSource, err := drive.NewReadSource(recorder, nil)
	if err != nil {
		t.Fatalf("new recording source: %v", err)
	}
	defer recordSource.Close()

	plan := drive.RangePlan{Size: fileSize, Workers: 2, Part: partBytes}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	recorded, err := recordSource.Fetch(ctx, entry, plan)
	if err != nil {
		t.Fatalf("recording fetch: %v", err)
	}
	if !bytes.Equal(recorded, payload) {
		t.Fatal("recording fetch returned wrong bytes")
	}

	recordedCassette := recorder.Cassette()
	if err := recordedCassette.Validate(); err != nil {
		t.Fatalf("recorded cassette is invalid: %v", err)
	}
	if got := len(recordedCassette.Requests); got != 2 {
		t.Fatalf("recorded %d requests, want 2", got)
	}
	if len(recordedCassette.Files) != 1 || recordedCassette.Files[0].Size != fileSize {
		t.Fatalf("recorded files = %+v", recordedCassette.Files)
	}

	// The cassette is committed testdata, so its JSON form must round-trip.
	encoded, err := recordedCassette.Marshal()
	if err != nil {
		t.Fatalf("marshal cassette: %v", err)
	}
	parsed, err := cassette.Parse(encoded)
	if err != nil {
		t.Fatalf("parse cassette: %v", err)
	}
	if !reflect.DeepEqual(parsed, recordedCassette) {
		t.Fatalf("cassette JSON round-trip changed the recording\n got: %+v\nwant: %+v", parsed, recordedCassette)
	}

	replayer, err := cassette.NewReplayer(parsed)
	if err != nil {
		t.Fatalf("new replayer: %v", err)
	}
	replaySource, err := drive.NewReadSource(replayer, nil)
	if err != nil {
		t.Fatalf("new replay source: %v", err)
	}
	defer replaySource.Close()

	replayed, err := replaySource.Fetch(ctx, entry, plan)
	if err != nil {
		t.Fatalf("replay fetch: %v", err)
	}
	if !bytes.Equal(replayed, payload) {
		t.Fatal("replay returned wrong bytes")
	}
	stats := replayer.Stats()
	if stats.Requests != len(recordedCassette.Requests) || stats.Misses != 0 {
		t.Fatalf("replay matched %d requests (%d misses), want %d", stats.Requests, stats.Misses, len(recordedCassette.Requests))
	}
	if stats.Bytes != int64(fileSize) || stats.ProviderBytes != int64(fileSize) {
		t.Fatalf("replay bytes = %d / provider %d, want %d", stats.Bytes, stats.ProviderBytes, fileSize)
	}

	// Compare timing shape per request key. Sleeps only overshoot on the lower
	// bound, so the replayed values must be at least the recorded ones while
	// staying within a generous latency budget; a replayed pacing bug would
	// show up as either an undershoot or a large overshoot.
	recordedByKey := make(map[[2]int64]cassette.RequestRecord, len(recordedCassette.Requests))
	for _, record := range recordedCassette.Requests {
		recordedByKey[[2]int64{record.Offset, record.Size}] = record
	}
	const timingSlack = 200 * time.Millisecond
	for _, result := range stats.Results {
		record, ok := recordedByKey[[2]int64{result.Offset, result.Size}]
		if !ok {
			t.Fatalf("replayed request offset=%d size=%d was not recorded", result.Offset, result.Size)
		}
		if result.Open < openDelay/2 || result.Open > timingSlack {
			t.Errorf("replayed open = %v, want about %v", result.Open, openDelay)
		}
		if len(result.Chunks) != len(record.Chunks) {
			t.Fatalf("replayed %d chunks, recorded %d", len(result.Chunks), len(record.Chunks))
		}
		if len(record.Chunks) < 2 {
			t.Fatalf("recorded only %d chunks for a 1 MiB part", len(record.Chunks))
		}
		wantLast := time.Duration(record.Chunks[len(record.Chunks)-1].AtNS)
		gotLast := result.Chunks[len(result.Chunks)-1]
		if gotLast < wantLast/2 || gotLast > wantLast+timingSlack {
			t.Errorf("replayed last chunk at %v, recorded %v", gotLast, wantLast)
		}
	}
}

// TestReplayReconstructsErrors checks that a recorded open failure replays as
// an error the read layer classifies the same way: the HTTP status and the
// underlying drive sentinel are both reachable.
func TestReplayReconstructsErrors(t *testing.T) {
	fake := drive.NewFakeDriver()
	entry := seedReadFile(t, fake, "movie.bin", incidentPayload(4096))
	fake.ErrRead = &drive.HTTPStatusError{Status: 429, Err: drive.ErrRateLimit}

	recorder := cassette.NewRecorder(fake)
	if _, err := recorder.Read(context.Background(), entry, 0, 4096); err == nil {
		t.Fatal("recording read unexpectedly succeeded")
	}
	recorded := recorder.Cassette()
	if len(recorded.Requests) != 1 {
		t.Fatalf("recorded %d requests, want 1", len(recorded.Requests))
	}
	// The sentinel classification wins but the status is carried, so replay
	// rebuilds an HTTPStatusError that still unwraps to drive.ErrRateLimit.
	if recorded.Requests[0].ErrorKind != cassette.ErrorKindRateLimit || recorded.Requests[0].Status != 429 {
		t.Fatalf("recorded error = %q status %d", recorded.Requests[0].ErrorKind, recorded.Requests[0].Status)
	}
	if err := recorded.Validate(); err != nil {
		t.Fatalf("recorded cassette invalid: %v", err)
	}

	replayer, err := cassette.NewReplayer(recorded, cassette.WithTimeScale(0))
	if err != nil {
		t.Fatalf("new replayer: %v", err)
	}
	_, replayErr := replayer.Read(context.Background(), entry, 0, 4096)
	if replayErr == nil {
		t.Fatal("replayed read unexpectedly succeeded")
	}
	var statusErr *drive.HTTPStatusError
	if !errors.As(replayErr, &statusErr) || statusErr.Status != 429 {
		t.Fatalf("replayed error = %v, want HTTP 429", replayErr)
	}
	if !errors.Is(replayErr, drive.ErrRateLimit) {
		t.Fatalf("replayed error = %v, want errors.Is(drive.ErrRateLimit)", replayErr)
	}
}

// TestReplayRejectsUnknownRanges checks that replay never fabricates bytes: an
// unrecorded range fails with ErrNoRecord and is counted as a miss.
func TestReplayRejectsUnknownRanges(t *testing.T) {
	recorded := &cassette.Cassette{
		Version: cassette.FormatVersion,
		Files: []cassette.FileRecord{{
			ID: "0/movie.bin", Size: 4096,
			Capabilities: []drive.Capability{drive.CapabilityParallelRange},
		}},
		Requests: []cassette.RequestRecord{{
			EntryID: "0/movie.bin", Offset: 0, Size: 4096,
			Chunks: []cassette.ChunkRecord{{Data: incidentPayload(4096)}},
		}},
	}
	replayer, err := cassette.NewReplayer(recorded, cassette.WithTimeScale(0))
	if err != nil {
		t.Fatalf("new replayer: %v", err)
	}
	_, err = replayer.Read(context.Background(), drive.Entry{ID: "0/movie.bin", Size: 4096}, 1024, 2048)
	if !errors.Is(err, cassette.ErrNoRecord) {
		t.Fatalf("read at unrecorded offset = %v, want ErrNoRecord", err)
	}
	if stats := replayer.Stats(); stats.Misses != 1 || stats.Requests != 0 {
		t.Fatalf("stats = %+v, want 1 miss and 0 matches", stats)
	}
}

// TestReplayRejectsIncompleteBodyReadPastEnd checks that a body recorded as
// closed before EOF reports a deterministic error when replay tries to read
// past what the recording delivered.
func TestReplayRejectsIncompleteBodyReadPastEnd(t *testing.T) {
	recorded := &cassette.Cassette{
		Version: cassette.FormatVersion,
		Files: []cassette.FileRecord{{
			ID: "0/movie.bin", Size: 8192,
			Capabilities: []drive.Capability{drive.CapabilityParallelRange},
		}},
		Requests: []cassette.RequestRecord{{
			EntryID: "0/movie.bin", Offset: 0, Size: 8192,
			Incomplete: true,
			Chunks:     []cassette.ChunkRecord{{Data: incidentPayload(4096)}},
		}},
	}
	replayer, err := cassette.NewReplayer(recorded, cassette.WithTimeScale(0))
	if err != nil {
		t.Fatalf("new replayer: %v", err)
	}
	body, err := replayer.Read(context.Background(), drive.Entry{ID: "0/movie.bin", Size: 8192}, 0, 8192)
	if err != nil {
		t.Fatalf("replayed open: %v", err)
	}
	defer body.Close()
	if _, err := io.ReadAll(body); !errors.Is(err, cassette.ErrIncompleteRecord) {
		t.Fatalf("reading past an incomplete recorded body = %v, want ErrIncompleteRecord", err)
	}
}

// TestValidationRejectsBadCassettes covers the schema guards that keep a
// committed cassette trustworthy.
func TestValidationRejectsBadCassettes(t *testing.T) {
	validFile := []cassette.FileRecord{{ID: "0/movie.bin", Size: 4096}}
	validRequest := func() []cassette.RequestRecord {
		return []cassette.RequestRecord{{
			EntryID: "0/movie.bin", Offset: 0, Size: 4096,
			Chunks: []cassette.ChunkRecord{{Data: incidentPayload(4096)}},
		}}
	}

	tests := []struct {
		name     string
		cassette *cassette.Cassette
	}{
		{"unsupported version", &cassette.Cassette{Version: 99, Files: validFile, Requests: validRequest()}},
		{"no files", &cassette.Cassette{Version: cassette.FormatVersion}},
		{"duplicate file", &cassette.Cassette{Version: cassette.FormatVersion, Files: append(validFile, validFile...), Requests: validRequest()}},
		{"empty entry", &cassette.Cassette{Version: cassette.FormatVersion, Files: validFile, Requests: []cassette.RequestRecord{{Offset: 0, Size: 1}}}},
		{"version 0", &cassette.Cassette{Files: validFile, Requests: validRequest()}},
		{"status without error", &cassette.Cassette{Version: cassette.FormatVersion, Files: validFile, Requests: []cassette.RequestRecord{{
			EntryID: "0/movie.bin", Size: 4096, Status: 500,
			Chunks: []cassette.ChunkRecord{{Data: incidentPayload(4096)}},
		}}}},
		{"http status without code", &cassette.Cassette{Version: cassette.FormatVersion, Files: validFile, Requests: []cassette.RequestRecord{{
			EntryID: "0/movie.bin", Size: 4096, ErrorKind: cassette.ErrorKindHTTPStatus,
		}}}},
		{"body larger than range", &cassette.Cassette{Version: cassette.FormatVersion, Files: validFile, Requests: []cassette.RequestRecord{{
			EntryID: "0/movie.bin", Size: 1024,
			Chunks: []cassette.ChunkRecord{{Data: incidentPayload(4096)}},
		}}}},
		{"non-monotonic chunks", &cassette.Cassette{Version: cassette.FormatVersion, Files: validFile, Requests: []cassette.RequestRecord{{
			EntryID: "0/movie.bin", Size: 8192,
			Chunks: []cassette.ChunkRecord{
				{Data: incidentPayload(4096), AtNS: int64(2 * time.Second)},
				{Data: incidentPayload(4096), AtNS: int64(time.Second)},
			},
		}}}},
		{"empty chunk", &cassette.Cassette{Version: cassette.FormatVersion, Files: validFile, Requests: []cassette.RequestRecord{{
			EntryID: "0/movie.bin", Size: 4096,
			Chunks: []cassette.ChunkRecord{{Data: nil}},
		}}}},
		{"unknown error kind", &cassette.Cassette{Version: cassette.FormatVersion, Files: validFile, Requests: []cassette.RequestRecord{{
			EntryID: "0/movie.bin", Size: 4096, ErrorKind: "mystery",
		}}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.cassette.Validate(); err == nil {
				t.Fatalf("Validate() = nil, want error")
			}
			if _, err := test.cassette.Marshal(); err == nil {
				t.Fatalf("Marshal() = nil error, want error")
			}
		})
	}
}

// TestNewReplayerRejectsNegativeScale documents that a negative time factor is a
// configuration error rather than a silently inverted pace.
func TestNewReplayerRejectsNegativeScale(t *testing.T) {
	c := &cassette.Cassette{
		Version: cassette.FormatVersion,
		Files:   []cassette.FileRecord{{ID: "0/movie.bin", Size: 1}},
	}
	if _, err := cassette.NewReplayer(c, cassette.WithTimeScale(-1)); err == nil {
		t.Fatal("NewReplayer with a negative time scale = nil error, want error")
	}
	if _, err := cassette.NewReplayer(c, cassette.WithTimeScale(0)); err != nil {
		t.Fatalf("NewReplayer with an instant time scale: %v", err)
	}
}
