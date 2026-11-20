// Package unmount detaches a FUSE volume hosted by the resident Core without
// stopping the Core or dropping its caches.
package unmount

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"
	cliruntime "github.com/yinzhenyu/qrypt/internal/cli/runtime"
	"github.com/yinzhenyu/qrypt/pkg/control"
	corepkg "github.com/yinzhenyu/qrypt/pkg/core"
	"github.com/yinzhenyu/qrypt/pkg/util"
)

func controlEndpoint(state cliruntime.ConfigState) string {
	runDir := corepkg.RuntimeRunDir(state.Path, state.Cfg)
	return corepkg.ControlSocketPath(state.Cfg, corepkg.RuntimeLayout{RunDir: runDir})
}

func NewCommand(rt cliruntime.Runtime) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "unmount [MOUNT_POINT]",
		Short: "Detach a mount hosted by the resident Core",
		Long: `Detach one FUSE volume hosted by the resident Core. The Core keeps
running, so its caches, upload workers and other mounts stay warm. Without an
argument the command detaches the only active mount.`,
		Example: `  qrypt unmount /mnt/qrypt`,
		Args:    cliruntime.MaxArgs(rt, 1),
		RunE: func(cmd *cobra.Command, args []string) error {
			state, err := rt.CommandConfig(cmd)
			if err != nil {
				return err
			}
			if state.Cfg == nil {
				return rt.ConfigNotFoundError()
			}
			client, err := control.NewClient(controlEndpoint(state))
			if err != nil {
				return fmt.Errorf("no running Core: %w (start `qrypt mount` or `qrypt core serve`)", err)
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 15*time.Second)
			defer cancel()
			mounts, err := client.ListMounts(ctx)
			if err != nil {
				return fmt.Errorf("no running Core: %w (start `qrypt mount` or `qrypt core serve`)", err)
			}
			if len(args) == 0 && len(mounts) > 1 {
				return fmt.Errorf("multiple mounts are active; specify a mount point")
			}
			target := ""
			if len(args) == 1 {
				target = util.ExpandHome(args[0])
			}
			matched := -1
			for i, hosted := range mounts {
				if target == "" || hosted.MountPoint == target || hosted.Name == target || hosted.ID == target {
					matched = i
					break
				}
			}
			if matched < 0 {
				if target == "" && len(mounts) == 0 {
					fmt.Fprintln(cmd.OutOrStdout(), "no mounts are active")
					return nil
				}
				return fmt.Errorf("no active mount matches %q", target)
			}
			hosted := mounts[matched]
			if err := client.DetachMount(ctx, hosted.ID); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "unmounted %s\n", hosted.MountPoint)
			return nil
		},
	}
	rt.WithRuntimeConfigFlag(cmd)
	return cmd
}
