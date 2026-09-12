package logging

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestScopeStampsMountAndDerivesComponent(t *testing.T) {
	var buf bytes.Buffer
	l := &Logger{level: LevelDebug, writer: &buf, errWriter: &buf}
	scope := l.WithMount("quark")

	scope.Warnf("[CACHE] compact pending journal failed: %v", "boom")
	l.Warnf("[CACHE] unscoped failure")

	events := l.Events(LevelWarn, 10)
	if len(events) != 2 {
		t.Fatalf("events = %d, want 2", len(events))
	}
	if events[0].Mount != "quark" {
		t.Fatalf("scoped event mount = %q, want quark", events[0].Mount)
	}
	if events[0].Component != "CACHE" {
		t.Fatalf("scoped event component = %q, want CACHE", events[0].Component)
	}
	if events[1].Mount != "" {
		t.Fatalf("unscoped event mount = %q, want empty", events[1].Mount)
	}
	// The mount must be in the text too, so the durable log file is
	// attributable without the event ring.
	if !strings.Contains(buf.String(), `mount="quark" [CACHE] compact pending journal failed`) {
		t.Fatalf("log text missing the mount field:\n%s", buf.String())
	}
	if strings.Contains(buf.String(), `mount=""`) {
		t.Fatalf("unscoped line rendered an empty mount field:\n%s", buf.String())
	}
}

func TestComponentOf(t *testing.T) {
	tests := []struct {
		msg  string
		want string
	}{
		{"[VFS] upload start path=/a", "VFS"},
		{"[quark] lower tag", "QUARK"},
		{"no prefix here", ""},
		{"[]", ""},
		{"[", ""},
		{"[multi word] tail", "MULTI WORD"},
	}
	for _, tt := range tests {
		if got := componentOf(tt.msg); got != tt.want {
			t.Fatalf("componentOf(%q) = %q, want %q", tt.msg, got, tt.want)
		}
	}
}

// A scope must follow the logger across ReplaceDefault, which mutates the
// global in place rather than swapping the pointer.
func TestScopeFollowsLoggerReplacement(t *testing.T) {
	previous := L
	t.Cleanup(func() { L = previous })

	var buf bytes.Buffer
	L = &Logger{level: LevelInfo, writer: &buf, errWriter: &buf}
	scope := L.WithMount("local")

	replacement := &Logger{level: LevelDebug, writer: &buf, errWriter: &buf}
	ReplaceDefault(replacement)

	scope.Debugf("[VFS] after replacement")
	if !strings.Contains(buf.String(), "after replacement") {
		t.Fatalf("scope did not write through the replacement: %s", buf.String())
	}
	if got := scope.Mount(); got != "local" {
		t.Fatalf("scope mount = %q after replacement, want local", got)
	}
}

// Two mounts running the same sampled call site must not suppress each other.
func TestScopeSamplingIsIsolatedPerMount(t *testing.T) {
	var buf bytes.Buffer
	l := &Logger{level: LevelDebug, writer: &buf, errWriter: &buf, samples: map[string]sampleState{}}

	a := l.WithMount("mount-a")
	b := l.WithMount("mount-b")
	a.WarnfEvery("shared.key", time.Minute, "[CACHE] queue full mount=a")
	b.WarnfEvery("shared.key", time.Minute, "[CACHE] queue full mount=b")
	// A second call on the same mount is inside the interval, so only it is
	// suppressed; the other mount's state is untouched.
	a.WarnfEvery("shared.key", time.Minute, "suppressed for a")

	output := buf.String()
	if !strings.Contains(output, "mount=a") || !strings.Contains(output, "mount=b") {
		t.Fatalf("per-mount sampling swallowed a mount's first line:\n%s", output)
	}
	if strings.Contains(output, "suppressed for a") {
		t.Fatalf("second call within the interval should be suppressed:\n%s", output)
	}
	if strings.Contains(output, "suppressed=") {
		t.Fatalf("no mount should report another mount's suppression:\n%s", output)
	}
}

// The sampling key of an unscoped call site is unchanged, so existing
// suppression behavior is untouched.
func TestUnscopedSamplingKeyUnchanged(t *testing.T) {
	if got := sampleKey("", "k"); got != "k" {
		t.Fatalf("sampleKey without mount = %q, want k", got)
	}
	if got := sampleKey("m", "k"); got != "m\x00k" {
		t.Fatalf("sampleKey with mount = %q, want m\\x00k", got)
	}
}

// A nil scope must drop its line rather than panic: zero-value owners exist
// (a benchmark builds a bare store) and diagnostics must never break the data
// path they instrument.
func TestNilScopeIsSafe(t *testing.T) {
	var scope *Scope
	scope.Debugf("d")
	scope.Infof("i")
	scope.Warnf("w")
	scope.Errorf("e")
	scope.DebugfEvery("k", time.Second, "d")
	scope.InfofEvery("k", time.Second, "i")
	scope.WarnfEvery("k", time.Second, "w")
	scope.DebugfEveryFunc("k", time.Second, func(int) string { return "d" })
	if got := scope.Mount(); got != "" {
		t.Fatalf("nil scope mount = %q, want empty", got)
	}
}
