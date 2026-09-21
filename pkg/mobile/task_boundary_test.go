package mobile

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/core"
	"github.com/yinzhenyu/qrypt/pkg/util"
)

// TestParseTaskFilterKeepsExplicitFields pins the JSON keys mobile accepts
// for a task filter and the default-scope rule: an empty filter defaults to
// user scope; any explicit narrowing (id, types, scope) skips defaulting.
func TestParseTaskFilterKeepsExplicitFields(t *testing.T) {
	filter, err := parseTaskFilter(`{"id":"t1","types":["upload_stream_batch","download"],"states":["running"],"scope":"sync","mount":"quark","path":"/a","limit":7}`)
	if err != nil {
		t.Fatal(err)
	}
	if filter.ID != "t1" || filter.Scope != "sync" || filter.Mount != "quark" || filter.Path != "/a" || filter.Limit != 7 {
		t.Fatalf("filter = %+v, want explicit fields kept", filter)
	}
	if len(filter.Types) != 2 || filter.Types[0] != "upload_stream_batch" || filter.Types[1] != "download" {
		t.Fatalf("filter types = %v, want the two requested types", filter.Types)
	}
	if len(filter.States) != 1 || filter.States[0] != "running" {
		t.Fatalf("filter states = %v, want running", filter.States)
	}

	// An explicitly requested scope is preserved: defaulting only applies to
	// an otherwise-empty filter.
	applyDefaultMobileTaskFilter(&filter)
	if filter.Scope != "sync" {
		t.Fatalf("explicit scope changed by defaulting: %q", filter.Scope)
	}

	// An empty raw filter parses to the zero filter, which then defaults to
	// user scope (the mobile UI list contract).
	empty, err := parseTaskFilter("")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(empty, core.TaskFilter{}) {
		t.Fatalf("empty filter = %+v, want zero", empty)
	}
	applyDefaultMobileTaskFilter(&empty)
	if empty.Scope != core.TaskScopeUser {
		t.Fatalf("default scope = %q, want %q", empty.Scope, core.TaskScopeUser)
	}

	// A filter narrowed by id or types must not be silently widened to a
	// scope-restricted list.
	byID := core.TaskFilter{ID: "t1"}
	applyDefaultMobileTaskFilter(&byID)
	if byID.Scope != "" {
		t.Fatalf("filter narrowed by id gained scope %q", byID.Scope)
	}
	byTypes := core.TaskFilter{Types: []core.TaskType{"download"}}
	applyDefaultMobileTaskFilter(&byTypes)
	if byTypes.Scope != "" {
		t.Fatalf("filter narrowed by types gained scope %q", byTypes.Scope)
	}
}

// TestParseTaskItemFilterKeepsExplicitFields pins the JSON keys of an item
// filter (item_id, states, limit).
func TestParseTaskItemFilterKeepsExplicitFields(t *testing.T) {
	filter, err := parseTaskItemFilter(`{"item_id":"local-1","states":["waiting_input","running"],"limit":1}`)
	if err != nil {
		t.Fatal(err)
	}
	if filter.ItemID != "local-1" || filter.Limit != 1 || len(filter.States) != 2 {
		t.Fatalf("item filter = %+v, want explicit fields kept", filter)
	}
	empty, err := parseTaskItemFilter("")
	if err != nil || !reflect.DeepEqual(empty, core.TaskItemFilter{}) {
		t.Fatalf("empty item filter = %+v err=%v, want zero", empty, err)
	}
}

// TestOpenTaskEventsFromJSONRejectsNegativeSequence pins the wire guard on
// the replay cursor: negative after-seq is refused before any core access.
func TestOpenTaskEventsFromJSONRejectsNegativeSequence(t *testing.T) {
	raw := OpenTaskEventsFromJSON("no-such-core", `{}`, -1, 0)
	var response struct {
		OK    bool `json:"ok"`
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(raw), &response); err != nil {
		t.Fatal(err)
	}
	if response.OK {
		t.Fatalf("OpenTaskEventsFromJSON(-1) = %s, want error", raw)
	}
	if !strings.Contains(response.Error.Message, "non-negative") {
		t.Fatalf("error message = %q, want non-negative sequence hint", response.Error.Message)
	}
}

// TestMobileTaskWireShapePinned pins the mobile-facing JSON of a created
// task through the boundary: the core-owned Task/ItemResult field names the
// app relies on (state machine fields, capabilities, progress counters) and
// the default user scope.
func TestMobileTaskWireShapePinned(t *testing.T) {
	tmp := t.TempDir()
	remote := filepath.Join(tmp, "remote")
	if err := os.MkdirAll(remote, 0o755); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(tmp, "qrypt.toml")
	if err := os.WriteFile(configPath, []byte(`
[[mounts]]
name = "quark"
type = "localfs"
[mounts.params]
root_path = `+util.TOMLPath(remote)+`

[upload]
upload_delay = "10ms"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	coreID, err := openCore(configPath, testRuntimeJSON(tmp))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = closeCore(coreID) }()

	raw := CreateTaskJSON(coreID, `{"type":"upload_stream_batch","items":[{"item_id":"local-1","dest_path":"/quark/pinned.bin","size":5}]}`, 0)
	var created struct {
		OK   bool `json:"ok"`
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(raw), &created); err != nil {
		t.Fatal(err)
	}
	taskID := created.Data.ID

	getRaw := GetTaskJSON(coreID, taskID)
	// Decode into a generic map to assert exact key presence at the wire.
	var envelope struct {
		OK   bool           `json:"ok"`
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal([]byte(getRaw), &envelope); err != nil {
		t.Fatal(err)
	}
	if !envelope.OK {
		t.Fatalf("GetTaskJSON = %s", getRaw)
	}
	data := envelope.Data
	for _, key := range []string{"id", "type", "state", "scope", "created_at", "capabilities", "progress", "result"} {
		if _, ok := data[key]; !ok {
			t.Fatalf("GetTaskJSON missing %q: %s", key, getRaw)
		}
	}
	if data["scope"] != "user" {
		t.Fatalf("task scope = %v, want user", data["scope"])
	}
	if data["type"] != "upload_stream_batch" {
		t.Fatalf("task type = %v, want upload_stream_batch", data["type"])
	}
	// Capability and progress counters keep their exact wire names.
	capabilities, ok := data["capabilities"].(map[string]any)
	if !ok || capabilities["cancelable"] != true || capabilities["persistent"] != true {
		t.Fatalf("capabilities = %v, want cancelable+persistent", data["capabilities"])
	}
	progress, ok := data["progress"].(map[string]any)
	if !ok {
		t.Fatalf("progress = %v, want object", data["progress"])
	}
	// The task is pinned before any write lands: staging counters are
	// present and their names/values are exact; cloud counters are omitted
	// while zero (omitempty).
	if progress["staging_bytes_total"] != float64(5) || progress["items_total"] != float64(1) {
		t.Fatalf("progress = %v, want staging_bytes_total 5 and items_total 1", progress)
	}
	if _, ok := progress["cloud_bytes_total"]; ok {
		t.Fatalf("progress leaked a zero cloud counter: %s", getRaw)
	}
	// The item list and item-level capability names are part of the wire.
	result, ok := data["result"].(map[string]any)
	if !ok {
		t.Fatalf("result = %v, want object: %s", data["result"], getRaw)
	}
	items, ok := result["items"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("result items = %v, want one item: %s", result["items"], getRaw)
	}
	item, ok := items[0].(map[string]any)
	if !ok {
		t.Fatalf("result item = %v, want object: %s", items[0], getRaw)
	}
	for _, key := range []string{"path", "item_id", "dest_path", "state", "capabilities"} {
		if _, ok := item[key]; !ok {
			t.Fatalf("result item missing %q: %s", key, getRaw)
		}
	}
	// The upload stream request did not carry idempotency metadata, so the
	// fingerprint must not leak into the wire list.
	if _, ok := data["operation_fingerprint"]; ok {
		t.Fatalf("GetTaskJSON leaked operation_fingerprint: %s", getRaw)
	}
}

// TestTaskEventsReadCloseRace exercises concurrent blocking reads, event
// generation and Close on a task-event handle through the mobile JSON API.
// Run under -race: the registry mutex and the subscription contract keep
// every response a well-formed envelope even while the handle is closed.
func TestTaskEventsReadCloseRace(t *testing.T) {
	tmp := t.TempDir()
	remote := filepath.Join(tmp, "remote")
	if err := os.MkdirAll(remote, 0o755); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(tmp, "qrypt.toml")
	if err := os.WriteFile(configPath, []byte(`
[[mounts]]
name = "quark"
type = "localfs"
[mounts.params]
root_path = `+util.TOMLPath(remote)+`

[upload]
upload_delay = "10ms"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	coreID, err := openCore(configPath, testRuntimeJSON(tmp))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = closeCore(coreID) }()

	openRaw := OpenTaskEventsJSON(coreID, `{"types":["upload_stream_batch"]}`, 0)
	var opened struct {
		OK   bool   `json:"ok"`
		Data string `json:"data"`
	}
	if err := json.Unmarshal([]byte(openRaw), &opened); err != nil {
		t.Fatal(err)
	}
	if !opened.OK || opened.Data == "" {
		t.Fatalf("OpenTaskEventsJSON = %s", openRaw)
	}
	handleID := opened.Data

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 12; j++ {
				raw := ReadTaskEventsJSON(handleID, 200)
				if !strings.Contains(raw, `"ok":`) {
					t.Errorf("malformed read response: %s", raw)
					return
				}
			}
		}()
	}
	// Generate task events while readers are blocked in Read.
	for i := 1; i <= 3; i++ {
		raw := CreateTaskJSON(coreID, fmt.Sprintf(`{"type":"upload_stream_batch","items":[{"item_id":"race-%d","dest_path":"/quark/race-%d.bin","size":1}]}`, i, i), 2000)
		if !strings.Contains(raw, `"ok":true`) {
			t.Errorf("CreateTaskJSON failed: %s", raw)
			return
		}
	}
	closeRaw := CloseTaskEventsJSON(handleID)
	if !strings.Contains(closeRaw, `"ok":true`) {
		t.Errorf("CloseTaskEventsJSON = %s", closeRaw)
	}
	wg.Wait()

	// A second close surfaces the take-once guard as a classified error.
	if raw := CloseTaskEventsJSON(handleID); strings.Contains(raw, `"ok":true`) {
		t.Errorf("second CloseTaskEventsJSON = %s, want unknown handle", raw)
	}
}
