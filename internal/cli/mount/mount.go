package mount

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/spf13/cobra"
	"github.com/yinzhenyu/qrypt/internal/cli/i18n"
	cliruntime "github.com/yinzhenyu/qrypt/internal/cli/runtime"
	"github.com/yinzhenyu/qrypt/pkg/logging"
	"github.com/yinzhenyu/qrypt/pkg/mount"
	"github.com/yinzhenyu/qrypt/pkg/util"
)

func NewCommand(rt cliruntime.Runtime) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "mount [MOUNT_NAME]",
		Short: "Mount configured drives with FUSE",
		Long:  "Mount all configured drives as one local FUSE filesystem, or mount one configured drive by name. Uses mount_point from config unless --mount-point is set.",
		Example: `  qrypt mount
  qrypt mount cloud --mount-point /mnt/qrypt`,
		Args: cliruntime.MaxArgs(rt, 1),
		RunE: func(cmd *cobra.Command, args []string) error {
			state, err := rt.CommandConfig(cmd)
			if err != nil {
				return err
			}
			if state.Cfg == nil {
				return rt.ConfigNotFoundError()
			}
			ctx, stop := rt.ShutdownContext(cmd)
			defer stop()

			debugListen := ""
			mountPointFlag, _ := cmd.Flags().GetString("mount-point")
			selectedMount := ""
			if len(args) == 1 {
				selectedMount = args[0]
			}

			coreHost, err := rt.OpenCoreForMount(ctx, state.Cfg, state.Path, selectedMount, debugListen, "")
			if err != nil {
				return err
			}
			defer func() {
				// Core owns the filesystem, task manager, debug server and
				// external driver cleanup. It is closed only after the unmount
				// defer has stopped new FUSE requests.
				if err := coreHost.Close(context.Background()); err != nil {
					logging.L.Warnf("[CLI] core close: %v", err)
				}
			}()
			fs := coreHost.FileSystem()

			mountPoint := ""
			if mountPointFlag != "" {
				mountPoint = mountPointFlag
			} else {
				var err error
				mountPoint, err = rt.MountPointFromConfig(state.Cfg)
				if err != nil {
					return err
				}
			}

			mountOpts, err := rt.MountOptionsFromConfig(state.Cfg)
			if err != nil {
				return err
			}

			mountPointExpanded := util.ExpandHome(mountPoint)
			loc := i18n.FromCommand(cmd)
			logging.L.Infof("Mounting at %s ...", mountPointExpanded)
			fmt.Fprintf(cmd.ErrOrStderr(), "%s\n", loc.T("out.mount.mounting", mountPointExpanded))
			mountOpts.MountPoint = mountPointExpanded
			mountOpts.Foreground = true
			session, err := mount.NewMounter().Mount(ctx, fs, mountOpts)
			if err != nil {
				logging.L.Errorf("Mount failed: %v", err)
				return err
			}
			// Unmount exactly once on every exit path. The signal path below
			// calls unmount() explicitly; the deferred call covers panics and
			// unexpected returns, which would otherwise leave the mount point
			// behind (the FUSE kernel drops the connection when the process
			// dies, but the directory and in-flight writes are not cleaned).
			// sync.Once keeps the two paths from double-unmounting.
			var unmountOnce sync.Once
			unmount := func() {
				unmountOnce.Do(func() {
					logging.L.Infof("Unmounting %s ...", mountPointExpanded)
					fmt.Fprintf(cmd.ErrOrStderr(), "%s\n", loc.T("out.mount.unmounting", mountPointExpanded))
					uctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
					defer cancel()
					if err := mount.NewMounter().Unmount(uctx, session); err != nil {
						logging.L.Warnf("Unmount failed: %v", err)
					}
				})
			}
			defer unmount()
			logging.L.Infof("Mounted at %s. Press Ctrl+C to unmount.", mountPointExpanded)
			fmt.Fprintf(cmd.ErrOrStderr(), "%s\n", loc.T("out.mount.mounted", mountPointExpanded))
			if prefetcher, ok := fs.(interface{ StartDirectoryPrefetch(context.Context) }); ok {
				prefetcher.StartDirectoryPrefetch(ctx)
			}
			<-ctx.Done()
			unmount()
			return nil
		},
	}
	rt.WithRuntimeConfigFlag(cmd)
	cmd.Flags().String("mount-point", "", "local FUSE mount point (defaults to config mount_point)")
	return cmd
}
