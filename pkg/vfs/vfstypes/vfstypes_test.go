package vfstypes

import (
	"encoding/json"
	"testing"
)

func TestDebugActiveOpSerializesReadPolicyFields(t *testing.T) {
	data, err := json.Marshal(DebugActiveOp{
		OpID:                 "op",
		Kind:                 "vfs_prefetch",
		ReadIntent:           "bulk",
		ReadStreams:          4,
		PrefetchCancelReason: "session_release",
	})
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["read_intent"] != "bulk" {
		t.Fatalf("read_intent = %v", decoded["read_intent"])
	}
	if decoded["read_streams"] != float64(4) {
		t.Fatalf("read_streams = %v", decoded["read_streams"])
	}
	if decoded["prefetch_cancel_reason"] != "session_release" {
		t.Fatalf("prefetch_cancel_reason = %v", decoded["prefetch_cancel_reason"])
	}
}
