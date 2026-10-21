package offline

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	cliruntime "github.com/yinzhenyu/qrypt/internal/cli/runtime"
	"github.com/yinzhenyu/qrypt/pkg/config"
	"github.com/yinzhenyu/qrypt/pkg/control"
	"github.com/yinzhenyu/qrypt/pkg/core"
	"github.com/yinzhenyu/qrypt/pkg/task"
)

type commandOfflineService struct {
	task.Controller // Unexpected non-offline task operations fail loudly.
	current         task.Task
	failure         error
	calls           chan any
}

func (s *commandOfflineService) CreateOfflineTask(_ context.Context, req task.OperationRequest) (task.Task, error) {
	s.calls <- req
	return s.current, s.failure
}

func (s *commandOfflineService) ListOfflineTasks(_ context.Context, filter task.Filter) ([]task.Task, error) {
	s.calls <- filter
	return []task.Task{s.current}, s.failure
}

func (s *commandOfflineService) GetOfflineTask(_ context.Context, id string) (task.Task, error) {
	s.calls <- "get:" + id
	return s.current, s.failure
}

func (s *commandOfflineService) InspectOffline(_ context.Context, req task.OfflineOperationRequest) (control.OfflinePreviewResult, error) {
	s.calls <- req
	return control.OfflinePreviewResult{Source: req.Source, Mount: req.Mount, ParseUsable: true}, s.failure
}

func (s *commandOfflineService) WaitOfflineTask(_ context.Context, id string, _ time.Duration) (task.Task, error) {
	s.calls <- "wait:" + id
	return s.current, s.failure
}

func (s *commandOfflineService) CancelTask(_ context.Context, id string) error {
	s.calls <- "cancel:" + id
	return s.failure
}

func (s *commandOfflineService) RetryTask(_ context.Context, id string) error {
	s.calls <- "retry:" + id
	return s.failure
}

type offlineCommandExit struct {
	code int
	err  error
}

func (e *offlineCommandExit) Error() string { return e.err.Error() }
func (e *offlineCommandExit) Unwrap() error { return e.err }

func offlineCommandRuntime(t *testing.T) (cliruntime.TestRuntime, *config.Config) {
	t.Helper()
	// Short paths keep Unix sockets below their platform length limit.
	home, err := os.MkdirTemp("", "qrypt-offline-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	t.Setenv("QRYPT_HOME", home)
	cfg := &config.Config{}
	rt := cliruntime.TestRuntime{
		WithPersistentConfigFlagFn: func(cmd *cobra.Command) *cobra.Command { return cmd },
		CommandConfigFn: func(*cobra.Command) (cliruntime.ConfigState, error) {
			return cliruntime.ConfigState{Cfg: cfg}, nil
		},
		ShutdownContextFn: func(cmd *cobra.Command) (context.Context, func()) {
			return cmd.Context(), func() {}
		},
		UsageErrorFn: func(_ *cobra.Command, format string, args ...any) error {
			return &offlineCommandExit{code: 2, err: fmt.Errorf(format, args...)}
		},
		ExitErrorFn: func(code int, err error) error { return &offlineCommandExit{code: code, err: err} },
	}
	return rt, cfg
}

func startOfflineCommandServer(t *testing.T, cfg *config.Config, service *commandOfflineService) {
	t.Helper()
	endpoint := core.ControlSocketPath(cfg, core.RuntimeLayout{RunDir: core.RuntimeRunDir("", cfg)})
	server, err := control.NewCommandServer(endpoint, service)
	if err != nil {
		t.Fatal(err)
	}
	server.SetOfflineService(service)
	if err := server.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close(context.Background()) })
}

func executeOfflineCommand(t *testing.T, rt cliruntime.TestRuntime, args ...string) (string, string, error) {
	t.Helper()
	cmd := NewCommand(rt)
	var out, stderr bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&stderr)
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	cmd.SetArgs(args)
	err := cmd.ExecuteContext(t.Context())
	return out.String(), stderr.String(), err
}

func TestOfflineCommandsRoundTrip(t *testing.T) {
	for _, name := range []string{"add", "fetch", "inspect", "list", "show", "cancel", "retry", "wait"} {
		t.Run(name, func(t *testing.T) {
			rt, cfg := offlineCommandRuntime(t)
			service := &commandOfflineService{current: task.Task{ID: "offline-test", Type: task.TypeOfflineDownload, State: task.StateSucceeded}, calls: make(chan any, 8)}
			startOfflineCommandServer(t, cfg, service)
			args := []string{name}
			switch name {
			case "add", "fetch", "inspect":
				args = append(args, "https://example.com/file.iso", "--mount", "cloud", "--select", "largest:2", "--name", "resource")
				if name == "fetch" {
					args = append(args, "--download-to", "./out/", "--keep-cloud", "--flat", "--workers", "2", "--chunk-size", "4M")
				}
				if name != "inspect" {
					args = append(args, "--mkdir", "--new", "--idempotency-key", "request-key")
				}
			case "list":
				args = append(args, "--all", "--state", "failed", "--mount", "cloud", "--limit", "7")
			default:
				args = append(args, "offline-test")
			}
			if name != "cancel" && name != "retry" {
				args = append(args, "--json")
			}
			out, stderr, err := executeOfflineCommand(t, rt, args...)
			if err != nil {
				t.Fatalf("command failed: %v stderr=%s", err, stderr)
			}
			var call any
			select {
			case call = <-service.calls:
			default:
				t.Fatal("command did not reach the control service")
			}
			switch name {
			case "add", "fetch":
				req, ok := call.(task.OperationRequest)
				if !ok || req.Offline == nil {
					t.Fatalf("create request = %#v", call)
				}
				input := req.Offline
				if req.Operation != task.OperationDownload || req.IdempotencyKey != "request-key" || !input.Mkdir || !input.New || input.Source != "https://example.com/file.iso" || input.Mount != "cloud" || input.Title != "resource" || input.Selection.Mode != task.OfflineSelectionLargest || input.Selection.Largest != 2 {
					t.Fatalf("creation flags lost: %+v offline=%+v", req, input)
				}
				if name == "fetch" {
					cwd, err := os.Getwd()
					if err != nil {
						t.Fatal(err)
					}
					want := filepath.Join(cwd, "out") + string(filepath.Separator)
					if input.DownloadTo != want || !input.KeepCloud || !input.Flat || input.Workers != 2 || input.ChunkSize != 4<<20 {
						t.Fatalf("fetch transfer flags lost: %+v", input)
					}
					if !strings.Contains(stderr, "offline-test") {
						t.Fatalf("background task hint missing: %q", stderr)
					}
				}
			case "inspect":
				req, ok := call.(task.OfflineOperationRequest)
				if !ok || req.Mount != "cloud" || req.Source != "https://example.com/file.iso" || req.Selection.Largest != 2 {
					t.Fatalf("inspect request = %#v", call)
				}
				var preview control.OfflinePreviewResult
				if err := json.Unmarshal([]byte(out), &preview); err != nil || !preview.ParseUsable || preview.Mount != "cloud" {
					t.Fatalf("preview JSON = %q err=%v", out, err)
				}
			case "list":
				filter, ok := call.(task.Filter)
				if !ok || filter.Mount != "cloud" || filter.Limit != 7 || !reflect.DeepEqual(filter.States, []task.State{task.StateFailed}) {
					t.Fatalf("list filter = %#v", call)
				}
				var listed []task.Task
				if err := json.Unmarshal([]byte(out), &listed); err != nil || len(listed) != 1 || listed[0].ID != "offline-test" {
					t.Fatalf("list JSON = %q err=%v", out, err)
				}
			case "show", "wait":
				if call != "get:offline-test" {
					t.Fatalf("task lookup = %#v", call)
				}
			case "cancel", "retry":
				if call != name+":offline-test" || !strings.Contains(out, "offline-test") {
					t.Fatalf("action=%#v output=%q", call, out)
				}
			}
			if name == "add" || name == "fetch" || name == "show" || name == "wait" {
				var current task.Task
				if err := json.Unmarshal([]byte(out), &current); err != nil || current.ID != "offline-test" || current.State != task.StateSucceeded {
					t.Fatalf("task JSON = %q err=%v", out, err)
				}
			}
		})
	}
}

func TestOfflineCommandsRejectInvalidInputBeforeRPC(t *testing.T) {
	for _, args := range [][]string{
		{"add", "https://example.com/file"},
		{"inspect", "https://example.com/file", "--mount", "cloud", "--select", "largest:bad"},
		{"add", "not-a-source", "--mount", "cloud"},
		{"fetch", "https://example.com/file", "--mount", "cloud"},
		{"fetch", "https://example.com/file", "--mount", "cloud", "--download-to", "out", "--workers", "-1"},
		{"fetch", "https://example.com/file", "--mount", "cloud", "--download-to", "out", "--include", "*.mkv", "--select", "all"},
		{"show"}, {"cancel", "one", "two"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			rt, _ := offlineCommandRuntime(t)
			// No server: accidentally reaching RPC cannot masquerade as a
			// validation success, because transport failure has exit code 1.
			_, _, err := executeOfflineCommand(t, rt, args...)
			var exit *offlineCommandExit
			if !errors.As(err, &exit) || exit.code != 2 {
				t.Fatalf("invalid input error = %v, want usage exit 2", err)
			}
		})
	}
}

func TestOfflineCommandServiceErrorsHaveStableExitCodes(t *testing.T) {
	for _, tt := range []struct {
		name    string
		failure error
		code    int
	}{
		{name: "usage", failure: &control.OfflineError{Class: control.OfflineErrorUsage, Message: "invalid selection"}, code: 2},
		{name: "runtime", failure: errors.New("provider unavailable"), code: 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rt, cfg := offlineCommandRuntime(t)
			service := &commandOfflineService{failure: tt.failure, calls: make(chan any, 16)}
			startOfflineCommandServer(t, cfg, service)
			for _, args := range [][]string{
				{"add", "https://example.com/file", "--mount", "cloud"},
				{"fetch", "https://example.com/file", "--mount", "cloud", "--download-to", "out"},
				{"inspect", "https://example.com/file", "--mount", "cloud"},
				{"list"}, {"show", "offline-test"}, {"cancel", "offline-test"}, {"retry", "offline-test"}, {"wait", "offline-test"},
			} {
				t.Run(args[0], func(t *testing.T) {
					_, _, err := executeOfflineCommand(t, rt, args...)
					var exit *offlineCommandExit
					if !errors.As(err, &exit) || exit.code != tt.code || !strings.Contains(err.Error(), tt.failure.Error()) {
						t.Fatalf("service error = %v, want exit=%d message=%q", err, tt.code, tt.failure.Error())
					}
				})
			}
		})
	}
}

func TestOfflineTerminalFailuresReturnTaskAndFailure(t *testing.T) {
	for _, state := range []task.State{task.StateFailed, task.StateCanceled} {
		t.Run(string(state), func(t *testing.T) {
			rt, cfg := offlineCommandRuntime(t)
			service := &commandOfflineService{current: task.Task{ID: "offline-test", State: state}, calls: make(chan any, 16)}
			startOfflineCommandServer(t, cfg, service)
			for _, args := range [][]string{
				{"add", "https://example.com/file", "--mount", "cloud", "--json"},
				{"add", "https://example.com/file", "--mount", "cloud", "--json", "--wait"},
				{"fetch", "https://example.com/file", "--mount", "cloud", "--download-to", "out", "--json"},
				{"fetch", "https://example.com/file", "--mount", "cloud", "--download-to", "out", "--json", "--wait"},
				{"wait", "offline-test", "--json"},
			} {
				out, _, err := executeOfflineCommand(t, rt, args...)
				var exit *offlineCommandExit
				if !errors.As(err, &exit) || exit.code != 1 {
					t.Fatalf("%v terminal state error = %v, want exit 1", args, err)
				}
				var current task.Task
				if err := json.Unmarshal([]byte(out), &current); err != nil || current.State != state {
					t.Fatalf("terminal snapshot lost: %q err=%v", out, err)
				}
			}
		})
	}
}
