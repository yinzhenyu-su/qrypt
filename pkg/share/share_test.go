package share

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/task"
)

func TestReceiveRequestSelectsOperation(t *testing.T) {
	t.Parallel()

	save := ReceiveRequest{Link: "https://x/s/abc", To: "/quark/inbox", Selection: task.OfflineSelection{Mode: task.OfflineSelectionLargest, Largest: 2}}
	request := save.OperationRequest()
	if request.Operation != task.OperationShare || request.Share == nil {
		t.Fatalf("save operation = %+v", request)
	}
	if request.Share.Link != save.Link || request.Share.To != save.To || request.Share.Selection.Largest != 2 {
		t.Fatalf("save inputs = %+v", request.Share)
	}

	fetch := save
	fetch.DownloadTo = "/tmp/out"
	request = fetch.OperationRequest()
	if request.Operation != task.OperationDownload || request.Share == nil || request.Share.DownloadTo != "/tmp/out" {
		t.Fatalf("fetch operation = %+v", request)
	}
}

func TestErrorClassOf(t *testing.T) {
	t.Parallel()

	cases := []struct {
		err  error
		want ErrorClass
	}{
		{drive.NewShareError(drive.ShareErrUnsupported, "", "no"), ErrorUsage},
		{drive.NewShareError(drive.ShareErrNotFound, "", "gone"), ErrorNotFound},
		{drive.NewShareError(drive.ShareErrProvider, "", "boom"), ErrorRuntime},
		{&RequestError{Class: ErrorUsage, Message: "bad"}, ErrorUsage},
		{task.ErrNotFound, ErrorNotFound},
		{errors.New("other"), ErrorRuntime},
	}
	for _, tc := range cases {
		if got := ErrorClassOf(tc.err); got != tc.want {
			t.Fatalf("ErrorClassOf(%v) = %q, want %q", tc.err, got, tc.want)
		}
	}
}

func TestRedaction(t *testing.T) {
	t.Parallel()

	link := drive.Share{LinkID: "abc", Passcode: "ab12"}
	if got := RedactLink(link, false); got.Passcode != RedactedPasscode {
		t.Fatalf("redacted passcode = %q", got.Passcode)
	}
	if got := RedactLink(link, true); got.Passcode != "ab12" {
		t.Fatalf("revealed passcode = %q", got.Passcode)
	}

	item := task.Task{ID: "share-1", Detail: map[string]any{
		task.ShareDetailKey: map[string]any{"link": "u", "passcode": "ab12"},
	}}
	redacted := RedactTask(item, false)
	detail := TaskDetail(redacted)
	if detail["passcode"] != RedactedPasscode {
		t.Fatalf("task passcode = %v", detail["passcode"])
	}
	if original := TaskDetail(item); original["passcode"] != "ab12" {
		t.Fatalf("RedactTask mutated the original: %v", original["passcode"])
	}
}

func TestLinkCreateRequestJSONRoundTrip(t *testing.T) {
	t.Parallel()

	original := LinkCreateRequest{Paths: []string{"/quark/a.bin"}, Mount: "quark", Title: "t", VisitLimit: 3}
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	var decoded LinkCreateRequest
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Paths) != 1 || decoded.Paths[0] != "/quark/a.bin" || decoded.Mount != "quark" || decoded.VisitLimit != 3 {
		t.Fatalf("decoded = %+v", decoded)
	}
}

type typedShareDetail struct {
	Link     string `json:"link"`
	Passcode string `json:"passcode,omitempty"`
}

// TestRedactTaskHandlesTypedDetail pins that the helper is safe for the typed
// detail Core stores in memory, not only for a JSON-decoded map.
func TestRedactTaskHandlesTypedDetail(t *testing.T) {
	t.Parallel()

	item := task.Task{ID: "share-1", Detail: map[string]any{
		task.ShareDetailKey: typedShareDetail{Link: "u", Passcode: "ab12"},
	}}
	redacted := RedactTask(item, false)
	detail := TaskDetail(redacted)
	if detail["passcode"] != RedactedPasscode {
		t.Fatalf("typed detail passcode = %v", detail["passcode"])
	}
	if detail["link"] != "u" {
		t.Fatalf("typed detail link = %v", detail["link"])
	}
	original, ok := item.Detail[task.ShareDetailKey].(typedShareDetail)
	if !ok || original.Passcode != "ab12" {
		t.Fatalf("RedactTask mutated the original typed detail: %+v", item.Detail[task.ShareDetailKey])
	}
	if exposed := TaskDetail(item); exposed["passcode"] != "ab12" {
		t.Fatalf("TaskDetail changed the original: %v", exposed["passcode"])
	}
}
