package control

import (
	"context"

	"github.com/spf13/cobra"
	cliruntime "github.com/yinzhenyu/qrypt/internal/cli/runtime"
	"github.com/yinzhenyu/qrypt/pkg/control"
	"github.com/yinzhenyu/qrypt/pkg/core"
)

// NewCommand exposes mutable task controls through the Core-owned control
// socket. It intentionally does not expose diagnostics or probe endpoints;
// those have their own command groups and authorization boundary.
func NewCommand(rt cliruntime.Runtime) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "control",
		Short: "Control tasks in a running Core",
		Args:  cliruntime.CommandGroupArgs(rt, nil),
		RunE:  cliruntime.ShowHelp,
	}
	rt.WithPersistentConfigFlag(cmd)
	cmd.AddCommand(newTaskCommand(rt))
	return cmd
}

func newTaskCommand(rt cliruntime.Runtime) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "task",
		Short: "Cancel or retry a task",
		Args:  cliruntime.CommandGroupArgs(rt, nil),
		RunE:  cliruntime.ShowHelp,
	}
	cmd.AddCommand(withControlEndpoint(rt, newTaskActionCommand(rt, "cancel")))
	cmd.AddCommand(withControlEndpoint(rt, newTaskActionCommand(rt, "retry")))
	return cmd
}

func newTaskActionCommand(rt cliruntime.Runtime, action string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   action + " TASK_ID",
		Short: "" + action + " a task",
		Args:  cliruntime.ExactNamedArgs(rt, "TASK_ID"),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := control.NewClient(endpointFromContext(cmd.Context()))
			if err != nil {
				return err
			}
			body, err := client.PostJSON(cmd.Context(), "/v1/control/tasks/"+action, map[string]string{"id": args[0]})
			if err != nil {
				return err
			}
			_, err = cmd.OutOrStdout().Write(body)
			return err
		},
	}
	return cmd
}

type endpointContextKey struct{}

func withControlEndpoint(rt cliruntime.Runtime, cmd *cobra.Command) *cobra.Command {
	run := cmd.RunE
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		state, err := rt.CommandConfig(cmd)
		if err != nil {
			return err
		}
		if state.Cfg == nil {
			return rt.ConfigNotFoundError()
		}
		runDir := core.RuntimeRunDir(state.Path, state.Cfg)
		endpoint := core.ControlSocketPath(state.Cfg, core.RuntimeLayout{RunDir: runDir})
		ctx := cmd.Context()
		if ctx == nil {
			ctx = context.Background()
		}
		cmd.SetContext(context.WithValue(ctx, endpointContextKey{}, endpoint))
		return run(cmd, args)
	}
	return cmd
}

func endpointFromContext(ctx context.Context) string {
	endpoint, _ := ctx.Value(endpointContextKey{}).(string)
	if endpoint == "" {
		return ""
	}
	return endpoint
}
