package receive

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	cliruntime "github.com/yinzhenyu/qrypt/internal/cli/runtime"
	"github.com/yinzhenyu/qrypt/pkg/config"
	"github.com/yinzhenyu/qrypt/pkg/control"
	"github.com/yinzhenyu/qrypt/pkg/core"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	shareapi "github.com/yinzhenyu/qrypt/pkg/share"
	"github.com/yinzhenyu/qrypt/pkg/task"
)

type commandReceiveService struct {
	task.Controller
	created   shareapi.ReceiveRequest
	inspected shareapi.InspectRequest
	listed    shareapi.ReceiveListRequest
	waitedID  string
	waitBlock chan struct{}
	cancelled string
	retried   string
	state     task.State
	failure   error
}

func (s *commandReceiveService) CreateShareTask(_ context.Context, req task.OperationRequest) (task.Task, error) {
	if req.Share != nil {
		s.created = shareapi.ReceiveRequest{
			Link:       req.Share.Link,
			Passcode:   req.Share.Passcode,
			Mount:      req.Share.Mount,
			To:         req.Share.To,
			DownloadTo: req.Share.DownloadTo,
			Selection:  req.Share.Selection,
			Mkdir:      req.Share.Mkdir,
			KeepCloud:  req.Share.KeepCloud,
			Workers:    req.Share.Workers,
			ChunkSize:  req.Share.ChunkSize,
			New:        req.Share.New,
		}
	}
	if s.failure != nil {
		return task.Task{}, s.failure
	}
	return task.Task{ID: "share-test", Type: task.TypeShareSave, State: task.StateQueued}, nil
}

func (s *commandReceiveService) ListShareTasks(_ context.Context, filter task.Filter) ([]task.Task, error) {
	s.listed = shareapi.ReceiveListRequest{Mount: filter.Mount, Limit: filter.Limit, All: len(filter.States) == 0}
	return []task.Task{s.snapshot()}, s.failure
}

func (s *commandReceiveService) GetShareTask(_ context.Context, id string) (task.Task, error) {
	s.waitedID = id
	return s.snapshot(), s.failure
}

func (s *commandReceiveService) WaitShareTask(ctx context.Context, id string, timeout time.Duration) (task.Task, error) {
	s.waitedID = id
	if s.waitBlock != nil {
		select {
		case <-ctx.Done():
			return task.Task{}, ctx.Err()
		case <-s.waitBlock:
		}
	}
	if s.failure != nil {
		return task.Task{}, s.failure
	}
	return s.snapshot(), nil
}

func (s *commandReceiveService) CancelTask(_ context.Context, id string) error {
	s.cancelled = id
	return s.failure
}

func (s *commandReceiveService) RetryTask(_ context.Context, id string) error {
	s.retried = id
	return s.failure
}

func (s *commandReceiveService) CreateShareLink(context.Context, shareapi.LinkCreateRequest) (drive.Share, error) {
	return drive.Share{}, s.failure
}

func (s *commandReceiveService) ListShareLinks(context.Context, shareapi.LinkListRequest) (drive.SharePage, error) {
	return drive.SharePage{}, s.failure
}

func (s *commandReceiveService) GetShareLink(context.Context, string, string) (drive.Share, error) {
	return drive.Share{}, s.failure
}

func (s *commandReceiveService) UpdateShareLink(context.Context, shareapi.LinkUpdateRequest) (drive.Share, error) {
	return drive.Share{}, s.failure
}

func (s *commandReceiveService) RemoveShareLink(context.Context, string, string) error {
	return s.failure
}

func (s *commandReceiveService) InspectShareLink(_ context.Context, req shareapi.InspectRequest) (shareapi.InspectResult, error) {
	s.inspected = req
	return shareapi.InspectResult{
		Link:    drive.ShareLink{URL: req.Link},
		Share:   drive.Share{Title: "fixture", Passcode: "ab12", EntryCount: 1},
		Entries: []drive.ShareEntry{{Entry: drive.Entry{ID: "f1", Name: "a.bin", Size: 10}}, {Entry: drive.Entry{ID: "d1", Name: "dir", IsDir: true}}},
	}, s.failure
}

func (s *commandReceiveService) snapshot() task.Task {
	state := s.state
	if state == "" {
		state = task.StateSucceeded
	}
	return task.Task{
		ID:    "share-test",
		Type:  task.TypeShareSave,
		State: state,
		Mount: "quark",
		Detail: map[string]any{
			task.ShareDetailKey: map[string]any{
				"link":        "https://x/s/abc",
				"link_id":     "link00000001",
				"mount":       "quark",
				"passcode":    "ab12",
				"phase":       "complete",
				"to_mount":    "quark",
				"to_dir":      "/inbox",
				"save_handle": "handle1",
				"saved": []any{
					map[string]any{"name": "a.bin", "size": float64(10)},
				},
			},
		},
	}
}

type commandExit struct {
	code int
	err  error
}

func (e *commandExit) Error() string { return e.err.Error() }
func (e *commandExit) Unwrap() error { return e.err }

func receiveCommandRuntime(t *testing.T) cliruntime.TestRuntime {
	t.Helper()
	home, err := os.MkdirTemp("", "qrypt-receive-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	t.Setenv("QRYPT_HOME", home)
	cfg := &config.Config{}
	return cliruntime.TestRuntime{
		WithPersistentConfigFlagFn: func(cmd *cobra.Command) *cobra.Command { return cmd },
		CommandConfigFn: func(*cobra.Command) (cliruntime.ConfigState, error) {
			return cliruntime.ConfigState{Cfg: cfg}, nil
		},
		ShutdownContextFn: func(cmd *cobra.Command) (context.Context, func()) {
			return cmd.Context(), func() {}
		},
		UsageErrorFn: func(_ *cobra.Command, format string, args ...any) error {
			return &commandExit{code: 2, err: fmt.Errorf(format, args...)}
		},
		ExitErrorFn: func(code int, err error) error { return &commandExit{code: code, err: err} },
	}
}

func startReceiveCommandServer(t *testing.T, rt cliruntime.TestRuntime, service *commandReceiveService) {
	t.Helper()
	state, err := rt.CommandConfig(nil)
	if err != nil {
		t.Fatal(err)
	}
	endpoint := core.ControlSocketPath(state.Cfg, core.RuntimeLayout{RunDir: core.RuntimeRunDir("", state.Cfg)})
	server, err := control.NewCommandServer(endpoint, service)
	if err != nil {
		t.Fatal(err)
	}
	server.SetShareService(service)
	if err := server.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close(context.Background()) })
}

func executeReceiveCommand(t *testing.T, rt cliruntime.TestRuntime, stdin string, args ...string) (string, string, error) {
	t.Helper()
	return executeReceiveCommandContext(t, t.Context(), rt, stdin, args...)
}

func executeReceiveCommandContext(t *testing.T, ctx context.Context, rt cliruntime.TestRuntime, stdin string, args ...string) (string, string, error) {
	t.Helper()
	cmd := NewCommand(rt)
	var out, stderr bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&stderr)
	cmd.SetIn(strings.NewReader(stdin))
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	cmd.SetArgs(args)
	err := cmd.ExecuteContext(ctx)
	return out.String(), stderr.String(), err
}

func TestReceiveCommandsRoundTrip(t *testing.T) {
	rt := receiveCommandRuntime(t)
	service := &commandReceiveService{}
	startReceiveCommandServer(t, rt, service)

	// inspect
	out, _, err := executeReceiveCommand(t, rt, "", "inspect", "https://x/s/abc", "--passcode=pw", "--dir", "42", "--json")
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if service.inspected.Link != "https://x/s/abc" || service.inspected.Passcode != "pw" || service.inspected.Dir != "42" {
		t.Fatalf("inspect request = %+v", service.inspected)
	}
	var preview shareapi.InspectResult
	if err := json.Unmarshal([]byte(out), &preview); err != nil || len(preview.Entries) != 2 {
		t.Fatalf("preview = %+v err=%v", preview, err)
	}
	if preview.Share.Passcode != shareapi.RedactedPasscode {
		t.Fatalf("inspect leaked passcode: %+v", preview.Share)
	}

	// save
	_, _, err = executeReceiveCommand(t, rt, "", "save", "https://x/s/abc", "--to", "/quark/inbox", "--mkdir", "--select", "largest:2", "--json")
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if service.created.Link != "https://x/s/abc" || service.created.To != "/quark/inbox" || !service.created.Mkdir || service.created.Selection.Largest != 2 {
		t.Fatalf("save request = %+v", service.created)
	}
	if service.waitedID != "share-test" {
		t.Fatalf("save did not wait: %q", service.waitedID)
	}

	// fetch with a relative destination resolved against the caller's cwd
	_, stderr, err := executeReceiveCommand(t, rt, "", "fetch", "https://x/s/abc", "--download-to", "./out/", "--keep-cloud", "--workers", "2", "--chunk-size", "4M", "--no-wait")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	cwd, _ := os.Getwd()
	want := filepath.Join(cwd, "out") + string(filepath.Separator)
	if service.created.DownloadTo != want || !service.created.KeepCloud || service.created.Workers != 2 || service.created.ChunkSize != 4<<20 {
		t.Fatalf("fetch request = %+v", service.created)
	}
	if !strings.Contains(stderr, "share-test") {
		t.Fatalf("background hint missing: %q", stderr)
	}

	// list
	_, _, err = executeReceiveCommand(t, rt, "", "list", "--mount", "quark", "--all", "--limit", "7", "--json")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if service.listed.Mount != "quark" || service.listed.Limit != 7 || !service.listed.All {
		t.Fatalf("list request = %+v", service.listed)
	}

	// show
	_, _, err = executeReceiveCommand(t, rt, "", "show", "share-test", "--json")
	if err != nil {
		t.Fatalf("show: %v", err)
	}

	// cancel + retry
	if _, _, err := executeReceiveCommand(t, rt, "", "cancel", "share-test"); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if service.cancelled != "share-test" {
		t.Fatalf("cancelled = %q", service.cancelled)
	}
	if _, _, err := executeReceiveCommand(t, rt, "", "retry", "share-test"); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if service.retried != "share-test" {
		t.Fatalf("retried = %q", service.retried)
	}

	// wait
	if _, _, err := executeReceiveCommand(t, rt, "", "wait", "share-test", "--timeout", "1s"); err != nil {
		t.Fatalf("wait: %v", err)
	}
}

func TestReceivePasscodeFromStdin(t *testing.T) {
	rt := receiveCommandRuntime(t)
	service := &commandReceiveService{}
	startReceiveCommandServer(t, rt, service)

	_, _, err := executeReceiveCommand(t, rt, "secret\n", "inspect", "https://x/s/abc", "--passcode")
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if service.inspected.Passcode != "secret" {
		t.Fatalf("passcode = %q", service.inspected.Passcode)
	}
}

func TestReceiveOutputRedactsSecrets(t *testing.T) {
	rt := receiveCommandRuntime(t)
	service := &commandReceiveService{}
	startReceiveCommandServer(t, rt, service)

	out, _, err := executeReceiveCommand(t, rt, "", "show", "share-test")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "ab12") {
		t.Fatalf("default show leaked passcode: %q", out)
	}

	out, _, err = executeReceiveCommand(t, rt, "", "show", "share-test", "--show-secrets")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "ab12") {
		t.Fatalf("--show-secrets did not reveal: %q", out)
	}

	out, _, err = executeReceiveCommand(t, rt, "", "show", "share-test", "--json")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "ab12") {
		t.Fatalf("json leaked passcode: %q", out)
	}
}

func TestReceiveUsageErrors(t *testing.T) {
	rt := receiveCommandRuntime(t)
	service := &commandReceiveService{}
	startReceiveCommandServer(t, rt, service)

	cases := [][]string{
		{"inspect"},
		{"save", "https://x/s/abc"},
		{"fetch", "https://x/s/abc"},
		{"fetch", "https://x/s/abc", "--download-to", "./out", "--workers", "99"},
		{"fetch", "https://x/s/abc", "--download-to", "./out", "--select", "newest"},
		{"wait"},
	}
	for _, args := range cases {
		_, _, err := executeReceiveCommand(t, rt, "", args...)
		if err == nil {
			t.Fatalf("%v: expected a usage error", args)
		}
		var exit *commandExit
		if !errors.As(err, &exit) || exit.code != 2 {
			t.Fatalf("%v: error = %v, want exit 2", args, err)
		}
	}
}

func TestReceivePartialExitCode(t *testing.T) {
	rt := receiveCommandRuntime(t)
	service := &commandReceiveService{state: task.StatePartialFailed}
	startReceiveCommandServer(t, rt, service)

	_, _, err := executeReceiveCommand(t, rt, "", "wait", "share-test")
	var exit *commandExit
	if !errors.As(err, &exit) || exit.code != ExitPartial {
		t.Fatalf("error = %v, want exit %d", err, ExitPartial)
	}
}

// TestReceiveWaitInterruptStopsOnlyObservation pins the interrupt contract: a
// canceled wait returns context.Canceled (exit 130) and never cancels the task.
func TestReceiveWaitInterruptStopsOnlyObservation(t *testing.T) {
	rt := receiveCommandRuntime(t)
	service := &commandReceiveService{waitBlock: make(chan struct{})}
	startReceiveCommandServer(t, rt, service)

	ctx, cancel := context.WithCancel(t.Context())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	_, _, err := executeReceiveCommandContext(t, ctx, rt, "", "wait", "share-test")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if service.cancelled != "" {
		t.Fatalf("wait canceled the task: %q", service.cancelled)
	}
}
