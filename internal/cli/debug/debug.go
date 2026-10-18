package debug

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/spf13/cobra"
	cliruntime "github.com/yinzhenyu/qrypt/internal/cli/runtime"
	"github.com/yinzhenyu/qrypt/pkg/control"
	"github.com/yinzhenyu/qrypt/pkg/core"
)

func NewCommand(rt cliruntime.Runtime) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "debug",
		Short: "Collect read-only diagnostic data",
		Long: `Collect read-only diagnostic data from a running Core's debug socket:
health, state, uploads, reads, events, cache, and staging snapshots.`,
		Example: `  qrypt debug collect /cloud/photos
  qrypt debug bundle --out /tmp/qrypt-debug.zip
  qrypt debug watch /cloud/photos --duration 30s`,
		Args: cliruntime.CommandGroupArgs(rt, nil),
		RunE: cliruntime.ShowHelp,
	}
	rt.WithPersistentConfigFlag(cmd)
	cmd.AddCommand(withDebugSocketFlag(rt, NewBundleCmd(rt)))
	cmd.AddCommand(withDebugSocketFlag(rt, NewCollectCmd(rt)))
	cmd.AddCommand(newRemovedDebugInspectCmd())
	cmd.AddCommand(withDebugSocketFlag(rt, NewWatchCmd(rt)))
	cmd.AddCommand(withDebugSocketFlag(rt, NewUploadMemoryCmd(rt)))
	cmd.AddCommand(withDebugSocketFlag(rt, NewReadMemoryCmd(rt)))
	cmd.AddCommand(withDebugSocketFlag(rt, newDebugRawCmd(rt)))
	return cmd
}

// NewProbeCommand owns active diagnostics that may write remote or VFS test
// data. It deliberately shares the implementation with the former debug
// test/bench commands while using the mutable control socket.
func NewProbeCommand(rt cliruntime.Runtime) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "probe",
		Short: "Run active diagnostics and benchmarks",
		Long: `Run active diagnostics and benchmarks that may write remote or VFS test
data. Unlike debug, these commands use the mutable control socket.`,
		Example: `  qrypt probe test crud --mount cloud
  qrypt probe bench fs --mount cloud --size 64m`,
		Args: cliruntime.CommandGroupArgs(rt, nil), RunE: cliruntime.ShowHelp}
	rt.WithPersistentConfigFlag(cmd)
	cmd.AddCommand(NewBenchCmd(rt))
	cmd.AddCommand(NewTestCmd(rt))
	return cmd
}

type EndpointContextKey struct{}

// DebugSocketContextKey is retained as a source-compatible alias for tests
// and integrations that used the old internal command seam. It now carries
// either the debug or control endpoint.
type DebugSocketContextKey = EndpointContextKey

func withDebugSocketFlag(rt cliruntime.Runtime, cmd *cobra.Command) *cobra.Command {
	return withEndpoint(rt, cmd, func(state cliruntime.ConfigState) string {
		return core.DebugSocketPath(state.Cfg, core.RuntimeLayout{RunDir: core.RuntimeRunDir(state.Path, state.Cfg)})
	})
}

func withProbeSocketFlag(rt cliruntime.Runtime, cmd *cobra.Command) *cobra.Command {
	return withEndpoint(rt, cmd, func(state cliruntime.ConfigState) string {
		return core.ProbeSocketPath(state.Cfg, core.RuntimeLayout{RunDir: core.RuntimeRunDir(state.Path, state.Cfg)})
	})
}

func withEndpoint(rt cliruntime.Runtime, cmd *cobra.Command, resolve func(cliruntime.ConfigState) string) *cobra.Command {
	run := cmd.RunE
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		state, err := rt.CommandConfig(cmd)
		if err != nil {
			return err
		}
		if state.Cfg == nil {
			return rt.ConfigNotFoundError()
		}
		endpoint := resolve(state)
		ctx := cmd.Context()
		if ctx == nil {
			ctx = context.Background()
		}
		cmd.SetContext(context.WithValue(ctx, EndpointContextKey{}, endpoint))
		return run(cmd, args)
	}
	return cmd
}

func debugSocketFromContext(ctx context.Context) string {
	socket, _ := ctx.Value(EndpointContextKey{}).(string)
	return socket
}

func debugSocketGet(ctx context.Context, endpoint string) ([]byte, error) {
	socket := debugSocketFromContext(ctx)
	if socket == "" {
		return nil, fmt.Errorf("missing debug socket in command context")
	}
	client, err := control.NewClient(socket)
	if err != nil {
		return nil, err
	}
	return client.Get(ctx, endpoint)
}

func debugSocketPostJSON(ctx context.Context, endpoint string, value any) ([]byte, error) {
	socket := debugSocketFromContext(ctx)
	if socket == "" {
		return nil, fmt.Errorf("missing debug socket in command context")
	}
	client, err := control.NewClient(socket)
	if err != nil {
		return nil, err
	}
	return client.PostJSON(ctx, endpoint, value)
}

// debugSocket404Detail classifies a 404 from the socket's control server: a
// handler-level 404 (e.g. `mount "x" not found`) carries the failure reason
// in the body, while a route-level 404 (mux's bare "page not found") means
// the endpoint itself is absent, e.g. the socket is served by an older
// binary. It returns the explanation body, or routeMissing=true when the
// caller should emit the "endpoint not available" hint instead.
func debugSocket404Detail(err error) (body string, routeMissing bool) {
	msg := err.Error()
	prefix := "returned status 404:"
	i := strings.Index(msg, prefix)
	if i == -1 {
		return "", false
	}
	detail := msg[i+len(prefix):]
	if strings.TrimSpace(detail) == "" || strings.Contains(detail, "page not found") {
		return "", true
	}
	return strings.TrimSpace(detail), false
}

func newDebugRawCmd(rt cliruntime.Runtime) *cobra.Command {
	return &cobra.Command{
		Use:   "raw ENDPOINT",
		Short: "Fetch a raw debug socket endpoint",
		Long: `Fetch one debug socket endpoint verbatim. ENDPOINT is a /v1 path such as
/v1/health or /v1/state; the leading /v1/ is added when omitted.`,
		Example: `  qrypt debug raw /v1/health
  qrypt debug raw state`,
		Args:              cliruntime.ExactNamedArgs(rt, "ENDPOINT"),
		ValidArgsFunction: cliruntime.NoFileCompletions,
		RunE: func(cmd *cobra.Command, args []string) error {
			endpoint := args[0]
			switch {
			case endpoint == "":
				return fmt.Errorf("endpoint required")
			case strings.HasPrefix(endpoint, "/v1/"):
			case endpoint[0] == '/':
				return fmt.Errorf("debug raw expects a /v1 endpoint, got virtual path %q; use 'qrypt debug collect %s' or 'qrypt debug raw /v1/resolve?path=%s'",
					endpoint, endpoint, url.QueryEscape(endpoint))
			case len(endpoint) >= 3 && endpoint[:3] == "v1/":
				endpoint = "/" + endpoint
			default:
				endpoint = "/v1/" + endpoint
			}
			body, err := debugSocketGet(cmd.Context(), endpoint)
			if err != nil {
				return err
			}
			_, err = cmd.OutOrStdout().Write(body)
			return err
		},
	}
}

func newRemovedDebugInspectCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "inspect REMOTE",
		Hidden: true,
		Args:   cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return fmt.Errorf("debug inspect was removed; use 'qrypt debug collect REMOTE' for path diagnostics")
			}
			return fmt.Errorf("debug inspect was removed; use 'qrypt debug collect %s' instead", args[0])
		},
	}
}
