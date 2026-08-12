package debug

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/spf13/cobra"
	cliruntime "github.com/yinzhenyu/qrypt/internal/cli/runtime"
	"github.com/yinzhenyu/qrypt/pkg/control"
)

func NewCommand(rt cliruntime.Runtime) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "debug",
		Short: "Collect AI-oriented diagnostic data",
		Args: cliruntime.CommandGroupArgs(rt, map[string]string{
			"driver": "debug driver was removed; use 'qrypt debug test crud --socket PATH' or 'qrypt debug test xfer --source SRC --dest DST --socket PATH'",
			"probe":  "debug probe was removed; use 'qrypt debug test crud --socket PATH' or 'qrypt debug test xfer --source SRC --dest DST --socket PATH'",
		}),
		RunE: cliruntime.ShowHelp,
	}
	rt.WithPersistentConfigFlag(cmd)
	cmd.AddCommand(NewBenchCmd(rt))
	cmd.AddCommand(withDebugSocketFlag(rt, NewBundleCmd(rt)))
	cmd.AddCommand(withDebugSocketFlag(rt, NewCollectCmd(rt)))
	cmd.AddCommand(newRemovedDebugInspectCmd())
	cmd.AddCommand(withDebugSocketFlag(rt, NewWatchCmd(rt)))
	cmd.AddCommand(withDebugSocketFlag(rt, NewUploadMemoryCmd(rt)))
	cmd.AddCommand(withDebugSocketFlag(rt, NewReadMemoryCmd(rt)))
	cmd.AddCommand(NewTestCmd(rt))
	cmd.AddCommand(withDebugSocketFlag(rt, newDebugRawCmd(rt)))
	return cmd
}

type DebugSocketContextKey struct{}

func withDebugSocketFlag(rt cliruntime.Runtime, cmd *cobra.Command) *cobra.Command {
	cmd.Flags().StringP("socket", "s", "", "debug socket path (required)")
	cmd.Flags().String("url", "", "debug HTTP URL")
	run := cmd.RunE
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		socket, err := cmd.Flags().GetString("socket")
		if err != nil {
			return err
		}
		debugURL, err := cmd.Flags().GetString("url")
		if err != nil {
			return err
		}
		if socket != "" && debugURL != "" {
			return fmt.Errorf("--socket and --url are mutually exclusive")
		}
		endpoint := socket
		if endpoint == "" {
			endpoint = debugURL
		}
		if endpoint == "" {
			state, err := rt.CommandConfig(cmd)
			if err != nil {
				return err
			}
			if state.Cfg != nil && state.Cfg.Debug.Enabled {
				endpoint = state.Cfg.Debug.EffectiveListen()
			}
		}
		if endpoint == "" {
			return rt.MissingSocketError(cmd)
		}
		ctx := cmd.Context()
		if ctx == nil {
			ctx = context.Background()
		}
		cmd.SetContext(context.WithValue(ctx, DebugSocketContextKey{}, endpoint))
		return run(cmd, args)
	}
	return cmd
}

func debugSocketFromContext(ctx context.Context) string {
	socket, _ := ctx.Value(DebugSocketContextKey{}).(string)
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

func newDebugRawCmd(rt cliruntime.Runtime) *cobra.Command {
	return &cobra.Command{
		Use:               "raw ENDPOINT",
		Short:             "Fetch a raw debug socket endpoint",
		Args:              cliruntime.ExactNamedArgs(rt, "ENDPOINT"),
		ValidArgsFunction: cliruntime.NoFileCompletions,
		RunE: func(cmd *cobra.Command, args []string) error {
			socket := debugSocketFromContext(cmd.Context())
			endpoint := args[0]
			switch {
			case endpoint == "":
				return fmt.Errorf("endpoint required")
			case strings.HasPrefix(endpoint, "/v1/"):
			case endpoint[0] == '/':
				return fmt.Errorf("debug raw expects a /v1 endpoint, got virtual path %q; use 'qrypt debug collect %s --socket %s' or 'qrypt debug raw /v1/resolve?path=%s --socket %s'",
					endpoint, endpoint, socket, url.QueryEscape(endpoint), socket)
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
				return fmt.Errorf("debug inspect was removed; use 'qrypt debug collect REMOTE --socket PATH' for path diagnostics")
			}
			return fmt.Errorf("debug inspect was removed; use 'qrypt debug collect %s --socket PATH' instead", args[0])
		},
	}
}
