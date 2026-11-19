// Package cassette records provider read behavior and replays it
// deterministically, for tests that must reproduce a real read incident without
// the network, credentials or a live provider.
//
// # Recording
//
// A Recorder embeds a drive.Driver and overrides only Read, the provider Range
// call the read layer is measured on. Every other method is forwarded to the
// wrapped driver unchanged, so recording never changes production behavior. For
// each Read it captures the requested entry, offset and size, the open latency
// (time to first byte, roughly the provider TTFB), each 512 KiB body chunk with
// its delivery time, the concurrency observed at open, and how the request
// ended: an open error, a mid-body error, a short body, or a caller that closed
// the body early. The result is a versioned Cassette in plain JSON that can be
// committed under testdata and reviewed in a diff.
//
//	recorder := cassette.NewRecorder(driver)
//	recorder.Notes = "incident 2024-05: 140 KB/s per connection, 4s TTFB"
//	source, err := drive.NewReadSource(recorder, nil)
//	// ... run the workload under test ...
//	if err := recorder.Cassette().SaveFile("testdata/incident.json"); err != nil { ... }
//
// # Replaying
//
// A Replayer is a read-only drive.Driver backed by a cassette. It matches each
// Read on the exact (entry, offset, size) triple, keeping recorded attempts in
// order, and then serves the recorded body with the recorded pacing scaled by a
// time factor: replayed duration = recorded duration x scale, so WithTimeScale(0.002)
// replays a four second stall in eight milliseconds. Chunks are delivered at their recorded
// offsets from body hand-off rather than by sleeping the recorded gaps, so a
// slow caller that falls behind does not accumulate drift. A time scale of zero
// disables pacing entirely when a test only cares about bytes and shape.
//
//	c, err := cassette.LoadFile("testdata/incident.json")
//	replayer, err := cassette.NewReplayer(c, cassette.WithTimeScale(0.002))
//	source, err := drive.NewReadSource(replayer, nil)
//	trace := &drive.ReadTrace{}
//	ctx := drive.WithReadTrace(ctx, trace)
//	_, err = source.Fetch(ctx, entry, plan) // same window and retry shape as the recording
//	stats := replayer.Stats()
//
// Replay never fabricates bytes. A Read with no recorded counterpart returns
// ErrNoRecord: if the replayed window shape diverges from the recording, the
// test fails instead of silently measuring a different execution. Likewise a
// body recorded as closed before EOF returns ErrIncompleteRecord when replay
// tries to read past its recorded bytes.
//
// Cassettes describe reads only; replay does not implement listing, writing,
// caching, coalescing or repair. That keeps a replay run a pure function of the
// cassette: same requests, same bytes, same relative timing.
package cassette
