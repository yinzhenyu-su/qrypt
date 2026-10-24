package share

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
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

type commandShareService struct {
	task.Controller
	link      shareapi.LinkCreateRequest
	list      shareapi.LinkListRequest
	updated   shareapi.LinkUpdateRequest
	removed   []string
	share     drive.Share
	failure   error
	getMount  string
	getID     string
	removeIDs chan string
}

func (s *commandShareService) CreateShareLink(_ context.Context, req shareapi.LinkCreateRequest) (drive.Share, error) {
	s.link = req
	return s.share, s.failure
}

func (s *commandShareService) ListShareLinks(_ context.Context, req shareapi.LinkListRequest) (drive.SharePage, error) {
	s.list = req
	return drive.SharePage{Shares: []drive.Share{s.share}}, s.failure
}

func (s *commandShareService) GetShareLink(_ context.Context, mount, id string) (drive.Share, error) {
	s.getMount, s.getID = mount, id
	return s.share, s.failure
}

func (s *commandShareService) UpdateShareLink(_ context.Context, req shareapi.LinkUpdateRequest) (drive.Share, error) {
	s.updated = req
	return s.share, s.failure
}

func (s *commandShareService) RemoveShareLink(_ context.Context, mount, id string) error {
	s.removed = append(s.removed, id)
	s.removeIDs <- id
	return s.failure
}

func (s *commandShareService) ResolveShareLink(context.Context, shareapi.ResolveRequest) (shareapi.ResolveResult, error) {
	return shareapi.ResolveResult{}, s.failure
}

func (s *commandShareService) InspectShareLink(context.Context, shareapi.InspectRequest) (shareapi.InspectResult, error) {
	return shareapi.InspectResult{}, s.failure
}

func (s *commandShareService) CreateShareTask(context.Context, task.OperationRequest) (task.Task, error) {
	return task.Task{}, s.failure
}

func (s *commandShareService) ListShareTasks(context.Context, task.Filter) ([]task.Task, error) {
	return nil, s.failure
}

func (s *commandShareService) GetShareTask(context.Context, string) (task.Task, error) {
	return task.Task{}, s.failure
}

func (s *commandShareService) WaitShareTask(context.Context, string, time.Duration) (task.Task, error) {
	return task.Task{}, s.failure
}

func (s *commandShareService) CancelTask(context.Context, string) error { return s.failure }

func (s *commandShareService) RetryTask(context.Context, string) error { return s.failure }

type commandExit struct {
	code int
	err  error
}

func (e *commandExit) Error() string { return e.err.Error() }
func (e *commandExit) Unwrap() error { return e.err }

func shareCommandRuntime(t *testing.T) cliruntime.TestRuntime {
	t.Helper()
	home, err := os.MkdirTemp("", "qrypt-share-test-")
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

func startShareCommandServer(t *testing.T, rt cliruntime.TestRuntime, service *commandShareService) {
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

func executeShareCommand(t *testing.T, rt cliruntime.TestRuntime, args ...string) (string, string, error) {
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

func TestShareCommandsRoundTrip(t *testing.T) {
	tests := []struct {
		args []string
	}{
		{[]string{"create", "/quark/a.bin", "--mount", "quark", "--expires", "7d", "--passcode", "--limit-visits", "3", "--name", "release", "--json"}},
		{[]string{"create", "/quark/a.bin", "--mount", "quark", "--expires", "30d", "--passcode=ab12", "--json"}},
		{[]string{"list", "--mount", "quark", "--all", "--limit", "5", "--json"}},
		{[]string{"show", "link00000001", "--mount", "quark", "--json"}},
		{[]string{"update", "link00000001", "--expires", "1d", "--name", "renamed", "--json"}},
		{[]string{"remove", "link00000001", "link00000002", "--json"}},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.args[0], func(t *testing.T) {
			rt := shareCommandRuntime(t)
			service := &commandShareService{
				share:     drive.Share{LinkID: "link00000001", URL: "https://x/s/link00000001", Passcode: "ab12", ExpiresAt: time.Now().Add(time.Hour), EntryCount: 1},
				removeIDs: make(chan string, 4),
			}
			startShareCommandServer(t, rt, service)
			out, stderr, err := executeShareCommand(t, rt, tc.args...)
			if err != nil {
				t.Fatalf("command failed: %v stderr=%s", err, stderr)
			}
			if !strings.Contains(out, "link00000001") {
				t.Fatalf("output = %q", out)
			}
			switch tc.args[0] {
			case "create":
				if len(service.link.Paths) != 1 || service.link.Paths[0] != "/quark/a.bin" {
					t.Fatalf("create paths = %+v", service.link)
				}
				if service.link.Mount != "quark" {
					t.Fatalf("create mount = %q, want quark", service.link.Mount)
				}
				if strings.Contains(strings.Join(tc.args, " "), "7d") {
					if service.link.Expiry != drive.ShareExpiryWeek || !service.link.GeneratePasscode || service.link.VisitLimit != 3 || service.link.Title != "release" {
						t.Fatalf("create options = %+v", service.link)
					}
				} else if service.link.Passcode != "ab12" || service.link.Expiry != drive.ShareExpiryMonth || service.link.GeneratePasscode {
					t.Fatalf("explicit passcode options = %+v", service.link)
				}
				var decoded drive.Share
				if err := json.Unmarshal([]byte(out), &decoded); err != nil {
					t.Fatalf("json: %v", err)
				}
				if decoded.Passcode != shareapi.RedactedPasscode {
					t.Fatalf("json leaked passcode: %+v", decoded)
				}
				if !strings.Contains(stderr, "--show-secrets") {
					t.Fatalf("hidden-passcode hint missing: %q", stderr)
				}
			case "list":
				if service.list.Mount != "quark" || !service.list.IncludeExpired || service.list.Limit != 5 {
					t.Fatalf("list request = %+v", service.list)
				}
			case "show":
				if service.getMount != "quark" || service.getID != "link00000001" {
					t.Fatalf("show = %q %q", service.getMount, service.getID)
				}
			case "update":
				if service.updated.ID != "link00000001" || service.updated.Title == nil || *service.updated.Title != "renamed" || service.updated.Expiry == nil || *service.updated.Expiry != drive.ShareExpiryDay {
					t.Fatalf("update request = %+v", service.updated)
				}
			case "remove":
				if len(service.removed) != 2 {
					t.Fatalf("removed = %+v", service.removed)
				}
			}
		})
	}
}

func TestShareShowRevealsSecrets(t *testing.T) {
	rt := shareCommandRuntime(t)
	service := &commandShareService{
		share:     drive.Share{LinkID: "link00000001", Passcode: "ab12"},
		removeIDs: make(chan string, 1),
	}
	startShareCommandServer(t, rt, service)

	out, _, err := executeShareCommand(t, rt, "show", "link00000001")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "ab12") || !strings.Contains(out, shareapi.RedactedPasscode) {
		t.Fatalf("default show leaked: %q", out)
	}

	out, _, err = executeShareCommand(t, rt, "show", "link00000001", "--show-secrets")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "ab12") {
		t.Fatalf("--show-secrets did not reveal: %q", out)
	}
}

func TestShareCommandUsageErrors(t *testing.T) {
	rt := shareCommandRuntime(t)
	service := &commandShareService{removeIDs: make(chan string, 1)}
	startShareCommandServer(t, rt, service)

	cases := [][]string{
		{"create"},
		{"create", "/quark/a.bin", "--expires", "2d"},
		{"create", "/quark/a.bin", "--passcode=ab cd"},
		{"update", "link00000001"},
		{"remove"},
	}
	for _, args := range cases {
		_, _, err := executeShareCommand(t, rt, args...)
		if err == nil {
			t.Fatalf("%v: expected a usage error", args)
		}
		var exit *commandExit
		if !errors.As(err, &exit) || exit.code != 2 {
			t.Fatalf("%v: error = %v, want exit 2", args, err)
		}
	}
}
