package mobile

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/core"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

// The priority mapping is a mobile JSON compatibility contract: "" and
// "normal" select the default class, "high" the UI-critical class, and any
// other value rejects the open. The mapped values flow into the core/client
// read-priority surface and from there into the VFS read scheduler; see
// pkg/core/read_priority_test.go for the scheduler-side lock.
func TestOpenFilePriorityMapping(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    core.ReadPriority
		wantErr bool
	}{
		{name: "empty defaults to normal", raw: "", want: core.PriorityNormal},
		{name: "explicit normal", raw: "normal", want: core.PriorityNormal},
		{name: "high", raw: "high", want: core.PriorityHigh},
		{name: "unknown rejected", raw: "urgent", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := openFileOptions{Priority: tt.raw}.priority()
			if tt.wantErr {
				if err == nil {
					t.Fatalf("priority() = %v, want error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("priority() error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("priority() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestOpenFileIntentMapping pins the optional intent contract: an absent or
// "interactive" value keeps the latency-sensitive default, "bulk" opts into
// parallel range reads, and unknown values are rejected before any file work.
func TestOpenFileIntentMapping(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    vfs.ReadIntent
		wantErr bool
	}{
		{name: "empty defaults to interactive", raw: "", want: vfs.ReadIntentInteractive},
		{name: "explicit interactive", raw: "interactive", want: vfs.ReadIntentInteractive},
		{name: "bulk", raw: "bulk", want: vfs.ReadIntentBulk},
		{name: "probe", raw: "probe", want: vfs.ReadIntentProbe},
		{name: "unknown rejected", raw: "fast", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := openFileOptions{Intent: tt.raw}.intent()
			if tt.wantErr {
				if err == nil {
					t.Fatalf("intent() = %v, want error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("intent() error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("intent() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestOpenFileJSONRejectsUnknownPriority exercises the JSON-level contract:
// an unknown priority fails before any session or file work, with ok=false
// and an error message naming the offending value. The parse rejects the
// options before the core lookup, so no live core is needed.
func TestOpenFileJSONRejectsUnknownPriority(t *testing.T) {
	out := OpenFileJSON("no-such-core", "/x", `{"priority":"urgent"}`)
	var env struct {
		OK    bool `json:"ok"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error,omitempty"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.OK {
		t.Fatal("OpenFileJSON with unknown priority returned ok=true")
	}
	if env.Error == nil || !strings.Contains(env.Error.Message, `unknown file priority "urgent"`) {
		t.Fatalf("error envelope = %+v, want message containing unknown file priority", env.Error)
	}
}
