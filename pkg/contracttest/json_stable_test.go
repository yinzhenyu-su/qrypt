package contracttest

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestTestRunJSONStable verifies the unified envelope serializes with the
// documented field names (stable for storage/alerting consumers).
func TestTestRunJSONStable(t *testing.T) {
	tr := TestRun{
		Spec:     "crud",
		Mount:    "local",
		Pass:     true,
		Steps:    []TestStep{{Operation: "mkdir", OK: true, Duration: "1s", DurationMS: 1000}},
		Duration: "1s", DurationMS: 1000,
	}
	body, err := json.Marshal(tr)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"spec"`, `"mount"`, `"pass"`, `"steps"`, `"started_at"`, `"duration_ms"`, `"operation"`, `"ok"`} {
		if !strings.Contains(string(body), want) {
			t.Fatalf("TestRun JSON missing %s: %s", want, body)
		}
	}
}
