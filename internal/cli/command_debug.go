package cli

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/spf13/cobra"
	"github.com/yinzhenyu/qrypt/internal/control"
)

func newDebugCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "debug",
		Short: "Collect AI-oriented diagnostic data",
		Args: commandGroupArgs(map[string]string{
			"driver": "debug driver was removed; use 'qrypt debug test crud --socket PATH' or 'qrypt debug test xfer --source SRC --dest DST --socket PATH'",
			"probe":  "debug probe was removed; use 'qrypt debug test crud --socket PATH' or 'qrypt debug test xfer --source SRC --dest DST --socket PATH'",
		}),
		RunE: showHelp,
	}
	cmd.AddCommand(newDebugBenchCmd())
	cmd.AddCommand(withDebugSocketFlag(newDebugBundleCmd()))
	cmd.AddCommand(withDebugSocketFlag(newDebugCollectCmd()))
	cmd.AddCommand(newRemovedDebugInspectCmd())
	cmd.AddCommand(withDebugSocketFlag(newDebugWatchCmd()))
	cmd.AddCommand(newDebugTestCmd())
	cmd.AddCommand(withDebugSocketFlag(newDebugRawCmd()))
	return cmd
}

type debugSocketContextKey struct{}

func withDebugSocketFlag(cmd *cobra.Command) *cobra.Command {
	cmd.Flags().StringP("socket", "s", "", "debug socket path (required)")
	run := cmd.RunE
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		socket, err := cmd.Flags().GetString("socket")
		if err != nil {
			return err
		}
		if socket == "" {
			return missingSocketError(cmd)
		}
		cmd.SetContext(context.WithValue(commandContext(cmd), debugSocketContextKey{}, socket))
		return run(cmd, args)
	}
	return cmd
}

func debugSocketFromContext(ctx context.Context) string {
	socket, _ := ctx.Value(debugSocketContextKey{}).(string)
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

func newDebugRawCmd() *cobra.Command {
	return &cobra.Command{
		Use:               "raw ENDPOINT",
		Short:             "Fetch a raw debug socket endpoint",
		Args:              exactNamedArgs("ENDPOINT"),
		ValidArgsFunction: noFileCompletions,
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
		Args:   maxArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return fmt.Errorf("debug inspect was removed; use 'qrypt debug collect REMOTE --socket PATH' for path diagnostics")
			}
			return fmt.Errorf("debug inspect was removed; use 'qrypt debug collect %s --socket PATH' instead", args[0])
		},
	}
}
