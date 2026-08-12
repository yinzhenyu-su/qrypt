package mount

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/spf13/cobra"
	cliruntime "github.com/yinzhenyu/qrypt/internal/cli/runtime"
	"github.com/yinzhenyu/qrypt/pkg/control"
	"github.com/yinzhenyu/qrypt/pkg/logging"
	"github.com/yinzhenyu/qrypt/pkg/mount"
	"github.com/yinzhenyu/qrypt/pkg/util"
)

func NewCommand(rt cliruntime.Runtime) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "mount [MOUNT_NAME]",
		Short: "Mount configured drives with FUSE",
		Long:  "Mount all configured drives as one local FUSE filesystem, or mount one configured drive by name. Uses mount_point from config unless --mount-point is set.",
		Args:  cliruntime.MaxArgs(rt, 1),
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

			socket, _ := cmd.Flags().GetString("socket")
			debugListen := socket
			if debugListen == "" && state.Cfg.Debug.Enabled {
				debugListen = state.Cfg.Debug.EffectiveListen()
			}
			mountPointFlag, _ := cmd.Flags().GetString("mount-point")
			selectedMount := ""
			if len(args) == 1 {
				selectedMount = args[0]
			}

			fs, cleanup, err := rt.BuildFileSystemForMount(ctx, state.Cfg, selectedMount)
			if err != nil {
				return err
			}
			defer func() {
				// Shut down in dependency order: the unmount defer runs first
				// (stops new FUSE requests), then wait for the filesystem's
				// workers to finish, then clean up external resources.
				if err := fs.Close(context.Background()); err != nil {
					logging.L.Warnf("[CLI] filesystem close: %v", err)
				}
				cleanup()
			}()
			fs.Start(ctx)

			if debugListen != "" {
				snapshotter, ok := fs.(control.Snapshotter)
				if !ok {
					return fmt.Errorf("debug socket requires filesystem debug snapshots")
				}
				controlServer, err := control.NewServer(debugListen, snapshotter)
				if err != nil {
					return err
				}
				if err := controlServer.Start(ctx); err != nil {
					return err
				}
				defer controlServer.Close(context.Background())
			}

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
			logging.L.Infof("Mounting at %s ...", mountPointExpanded)
			fmt.Fprintf(cmd.ErrOrStderr(), "Mounting at %s ...\n", mountPointExpanded)
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
					fmt.Fprintln(cmd.ErrOrStderr(), "Unmounting ...")
					uctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
					defer cancel()
					if err := mount.NewMounter().Unmount(uctx, session); err != nil {
						logging.L.Warnf("Unmount failed: %v", err)
					}
				})
			}
			defer unmount()
			logging.L.Infof("Mounted at %s. Press Ctrl+C to unmount.", mountPointExpanded)
			fmt.Fprintf(cmd.ErrOrStderr(), "Mounted at %s. Press Ctrl+C to unmount.\n", mountPointExpanded)
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
	cmd.Flags().StringP("socket", "s", "", "local debug control socket (start a debug server)")
	return cmd
}
