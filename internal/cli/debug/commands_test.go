package debug

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/yinzhenyu/qrypt/pkg/contracttest"
	"github.com/yinzhenyu/qrypt/pkg/control"
	"github.com/yinzhenyu/qrypt/pkg/logging"
	"github.com/yinzhenyu/qrypt/pkg/vfs/diagnostics"
	"github.com/yinzhenyu/qrypt/pkg/vfs/vfstypes"
)

func TestDiagnosticsEnvelopeIsStable(t *testing.T) {
	body := []byte(`{"schema_version":2,"generated_at":"2026-11-19T05:00:00Z","mounts":[]}`)
	envelope := newDiagnosticsEnvelope("debug events", "/run/qrypt-debug.sock", body)
	if envelope.SchemaVersion != diagnosticsSchemaVersion {
		t.Fatalf("envelope schema version = %d, want %d", envelope.SchemaVersion, diagnosticsSchemaVersion)
	}
	if envelope.Command != "debug events" || envelope.Socket != "/run/qrypt-debug.sock" {
		t.Fatalf("unexpected envelope identity: %+v", envelope)
	}
	if string(envelope.Data) != string(body) {
		t.Fatalf("envelope data = %s, want raw payload %s", envelope.Data, body)
	}
	if !envelope.GeneratedAt.Equal(time.Date(2026, 11, 19, 5, 0, 0, 0, time.UTC)) {
		t.Fatalf("envelope generated_at = %s, want the payload timestamp", envelope.GeneratedAt)
	}

	var out bytes.Buffer
	if err := writeDiagnosticsEnvelope(&out, envelope, true); err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatalf("envelope is not valid JSON: %v\n%s", err, out.String())
	}
	for _, key := range []string{"schema_version", "command", "generated_at", "socket", "data"} {
		if _, ok := decoded[key]; !ok {
			t.Fatalf("envelope missing %q: %s", key, out.String())
		}
	}
}

func TestDiagnosticsEnvelopeFallsBackForNonJSONBody(t *testing.T) {
	envelope := newDiagnosticsEnvelope("debug ops", "", []byte("not json"))
	if !strings.Contains(string(envelope.Data), "not json") {
		t.Fatalf("fallback data = %s, want the raw body", envelope.Data)
	}
	if envelope.GeneratedAt.IsZero() {
		t.Fatalf("fallback envelope generated_at is zero")
	}
}

func TestEventsEndpointCarriesFilters(t *testing.T) {
	endpoint := eventsEndpoint([]string{"Cloud", " home "}, "/cloud/movie.mp4", "quark", "warn", 25)
	for _, want := range []string{
		"/v1/events?",
		"level=warn",
		"limit=25",
		"path=%2Fcloud%2Fmovie.mp4",
		"component=quark",
		"mount=Cloud",
		"mount=home",
	} {
		if !strings.Contains(endpoint, want) {
			t.Fatalf("events endpoint %q missing %q", endpoint, want)
		}
	}
}

func TestPrintDebugEvents(t *testing.T) {
	var out bytes.Buffer
	events := []logging.Event{{
		Time:      time.Date(2026, 11, 19, 5, 0, 0, 0, time.UTC),
		Level:     "WARN",
		Component: "QUARK",
		Mount:     "cloud",
		Message:   "read body slow",
	}}
	if err := printDebugEvents(&out, events); err != nil {
		t.Fatal(err)
	}
	line := out.String()
	for _, want := range []string{"2026-11-19T05:00:00Z", "WARN", "QUARK", "cloud", "read body slow"} {
		if !strings.Contains(line, want) {
			t.Fatalf("event line %q missing %q", line, want)
		}
	}

	out.Reset()
	if err := printDebugEvents(&out, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "no events") {
		t.Fatalf("empty event output = %q, want a no-events line", out.String())
	}
}

func TestPrintDebugOps(t *testing.T) {
	var out bytes.Buffer
	response := boxActiveOps()
	if err := printDebugOps(&out, response); err != nil {
		t.Fatal(err)
	}
	line := out.String()
	for _, want := range []string{"cloud", "read_window", "fetching", "running", "prefetch", "/cloud/movie.mp4", "op=op-1"} {
		if !strings.Contains(line, want) {
			t.Fatalf("ops line %q missing %q", line, want)
		}
	}
}

func TestActiveOpDetail(t *testing.T) {
	detail := activeOpDetail(vfstypes.DebugActiveOp{
		OpID: "op-1", RemoteID: "remote-9", Offset: 4096, Requested: 524288,
		Background: true, WaitFor: "fetch", AgeMS: 1500,
	})
	for _, want := range []string{"op=op-1", "id=remote-9", "offset=4096", "requested=524288", "background=true", "wait_for=fetch", "age_ms=1500"} {
		if !strings.Contains(detail, want) {
			t.Fatalf("op detail %q missing %q", detail, want)
		}
	}
}

func TestCompareVerifyReadOK(t *testing.T) {
	report := &verifyReadReport{
		Requested: verifyReadRequested{
			WindowBytes: 4 << 20, WindowChunks: 8, Streams: 3, PrefetchDepth: 2, PartBytes: 1 << 20,
		},
		Counters: verifyReadCounters{PrefetchesDelta: 2, WindowLoadsDelta: 1},
	}
	measurement := contracttest.MountedReadMeasurement{
		ExecutedStreams:         3,
		ExecutedWindowChunks:    8,
		ExecutedWindowBytes:     4 << 20,
		ExecutedPartBytes:       1 << 20,
		ExecutedPrefetchWindows: 2,
		ReadCalls:               16,
	}
	compareVerifyRead(report, &measurement, 3, 2, 1<<20)
	if !report.OK || len(report.Mismatches) != 0 {
		t.Fatalf("verify report = %+v, want ok without mismatches", report)
	}
}

func TestCompareVerifyReadMismatch(t *testing.T) {
	report := &verifyReadReport{
		Requested: verifyReadRequested{
			WindowBytes: 4 << 20, WindowChunks: 8, Streams: 3, PrefetchDepth: 2, PartBytes: 1 << 20,
		},
		Counters: verifyReadCounters{},
	}
	measurement := contracttest.MountedReadMeasurement{
		ExecutedStreams:      1,
		ExecutedWindowChunks: 1,
		ExecutedWindowBytes:  512 << 10,
		ExecutedPartBytes:    512 << 10,
		ReadCalls:            16,
	}
	compareVerifyRead(report, &measurement, 3, 2, 1<<20)
	if report.OK {
		t.Fatalf("verify report = %+v, want mismatches", report)
	}
	joined := strings.Join(report.Mismatches, "\n")
	for _, want := range []string{"window:", "streams:", "prefetch:", "part size:"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("mismatches %q missing %q", joined, want)
		}
	}
}

func TestParseDriverTestRuns(t *testing.T) {
	single := []byte(`{"spec":"read","mount":"cloud","pass":true,"read":{"op_id":"op-1"}}`)
	runs, err := parseDriverTestRuns(single)
	if err != nil || len(runs) != 1 || !runs[0].Pass {
		t.Fatalf("single run = %+v, err = %v", runs, err)
	}
	multiple := []byte(`[{"spec":"read","pass":true},{"spec":"read","pass":false}]`)
	runs, err = parseDriverTestRuns(multiple)
	if err != nil || len(runs) != 2 || runs[1].Pass {
		t.Fatalf("multiple runs = %+v, err = %v", runs, err)
	}
}

func TestFindReadRun(t *testing.T) {
	runs := []contracttest.TestRun{{Spec: "crud"}, {Spec: "read", Read: &contracttest.MountedReadTestDetails{OpID: "op-1"}}}
	run := findReadRun(runs)
	if run == nil || run.Spec != "read" {
		t.Fatalf("findReadRun = %+v, want the read run", run)
	}
	if findReadRun(nil) != nil {
		t.Fatalf("findReadRun(nil) should be nil")
	}
}

func boxActiveOps() control.ActiveOpsResponse {
	return control.ActiveOpsResponse{
		SchemaVersion: diagnostics.DebugSnapshotSchemaVersion,
		GeneratedAt:   time.Unix(0, 0),
		Mounts: []diagnostics.DebugActiveMount{{
			Mount: "cloud",
			Ops: []vfstypes.DebugActiveOp{{
				OpID: "op-1", Kind: "read_window", Phase: "fetching", State: "running",
				ReadIntent: "prefetch", Path: "/cloud/movie.mp4",
			}},
		}},
	}
}

func TestPrintCacheFill(t *testing.T) {
	cmd := &cobra.Command{}
	var out bytes.Buffer
	cmd.SetOut(&out)
	err := printCacheFill(cmd, control.DebugCacheFillResponse{
		Mount: "cloud", Path: "/cloud/movie.mp4", FileSize: 3 << 20, Bytes: 1 << 20,
		ReadCalls: 1, DurationMS: 12, CacheBytesBefore: 0, CacheBytesAfter: 1 << 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	line := out.String()
	for _, want := range []string{"cloud", "/cloud/movie.mp4", "1 read calls", "12ms", "read cache"} {
		if !strings.Contains(line, want) {
			t.Fatalf("cache fill line %q missing %q", line, want)
		}
	}
}
