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
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}
	cmd.AddCommand(withDebugSocketFlag(newDebugBundleCmd()))
	cmd.AddCommand(withDebugSocketFlag(newDebugCollectCmd()))
	cmd.AddCommand(withDebugSocketFlag(newDebugInspectCmd()))
	cmd.AddCommand(withDebugSocketFlag(newDebugWatchCmd()))
	cmd.AddCommand(withConfigFlag(newJournalCmdWithUse("journal")))
	cmd.AddCommand(withDebugSocketFlag(newDebugRawCmd()))
	return cmd
}

type debugSocketContextKey struct{}

func withDebugSocketFlag(cmd *cobra.Command) *cobra.Command {
	cmd.Flags().StringP("socket", "s", "", "debug socket path (required)")
	if err := cmd.MarkFlagRequired("socket"); err != nil {
		panic(err)
	}
	run := cmd.RunE
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		socket, err := cmd.Flags().GetString("socket")
		if err != nil {
			return err
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
		return nil, fmt.Errorf("this command requires --socket")
	}
	client, err := control.NewClient(socket)
	if err != nil {
		return nil, err
	}
	return client.Get(ctx, endpoint)
}

func newDebugRawCmd() *cobra.Command {
	return &cobra.Command{
		Use:               "raw ENDPOINT",
		Short:             "Fetch a raw debug socket endpoint",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: noFileCompletions,
		RunE: func(cmd *cobra.Command, args []string) error {
			socket := debugSocketFromContext(cmd.Context())
			endpoint := args[0]
			switch {
			case endpoint == "":
				return fmt.Errorf("endpoint required")
			case strings.HasPrefix(endpoint, "/v1/"):
			case endpoint[0] == '/':
				return fmt.Errorf("debug raw expects a /v1 endpoint, got virtual path %q; use 'qrypt debug inspect %s --socket %s' or 'qrypt debug raw /v1/resolve?path=%s --socket %s'",
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
