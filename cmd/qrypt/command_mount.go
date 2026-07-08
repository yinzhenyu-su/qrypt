package main

import (
	"context"
	"fmt"
	"os/signal"

	"github.com/spf13/cobra"
	"github.com/yinzhenyu/qrypt/internal/control"
	"github.com/yinzhenyu/qrypt/internal/logging"
	"github.com/yinzhenyu/qrypt/internal/mount"
	"github.com/yinzhenyu/qrypt/pkg/osutil"
)

func newMountCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "mount [MOUNTPOINT]",
		Short: "Mount configured drives with FUSE",
		Long:  "Mount all configured drives as one local FUSE filesystem. Uses mount_point from config, or accepts a path argument.",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireConfig(); err != nil {
				return err
			}
			ctx, stop := signal.NotifyContext(context.Background(), shutdownSignals()...)
			defer stop()

			debugSocket, _ := cmd.Flags().GetString("debug-socket")

			fs, cleanup, err := buildFileSystem(ctx, configPath)
			if err != nil {
				return err
			}
			defer cleanup()
			fs.Start(ctx)

			if debugSocket != "" {
				snapshotter, ok := fs.(control.Snapshotter)
				if !ok {
					return fmt.Errorf("debug socket requires filesystem debug snapshots")
				}
				controlServer, err := control.NewServer(debugSocket, snapshotter)
				if err != nil {
					return err
				}
				if err := controlServer.Start(ctx); err != nil {
					return err
				}
				defer controlServer.Close(context.Background())
			}

			mountPoint := ""
			if len(args) == 1 {
				mountPoint = args[0]
			} else {
				var err error
				mountPoint, err = mountPointFromConfig(configPath)
				if err != nil {
					return err
				}
			}

			mountCfg, err := mountConfigFromConfig(configPath)
			if err != nil {
				return err
			}

			mountPointExpanded := osutil.ExpandHome(mountPoint)
			logging.L.Infof("Mounting at %s ...", mountPointExpanded)
			fmt.Printf("Mounting at %s ...\n", mountPointExpanded)
			session, err := mount.NewMounter().Mount(ctx, fs, mount.Options{
				MountPoint:         mountPointExpanded,
				ReadOnly:           mountCfg.ReadOnly,
				AllowOther:         mountCfg.AllowOther,
				DefaultPermissions: mountCfg.DefaultPermissions,
				VolumeName:         mountCfg.VolumeName,
				NoAppleDouble:      mountCfg.NoAppleDouble,
				NoAppleXattr:       mountCfg.NoAppleXattr,
				AttrTimeout:        mountCfg.AttrTimeout,
				AttrTimeoutSet:     mountCfg.AttrTimeoutSet,
				EntryTimeout:       mountCfg.EntryTimeout,
				EntryTimeoutSet:    mountCfg.EntryTimeoutSet,
				NegativeTimeout:    mountCfg.NegativeTimeout,
				TotalSpace:         mountCfg.TotalSpace,
				FreeSpace:          mountCfg.FreeSpace,
				Foreground:         true,
			})
			if err != nil {
				logging.L.Errorf("Mount failed: %v", err)
				return err
			}
			logging.L.Infof("Mounted at %s. Press Ctrl+C to unmount.", mountPointExpanded)
			fmt.Printf("Mounted at %s. Press Ctrl+C to unmount.\n", mountPointExpanded)
			if prefetcher, ok := fs.(interface{ StartDirectoryPrefetch(context.Context) }); ok {
				prefetcher.StartDirectoryPrefetch(ctx)
			}
			<-ctx.Done()
			logging.L.Infof("Unmounting %s ...", mountPointExpanded)
			fmt.Println("Unmounting ...")
			return mount.NewMounter().Unmount(ctx, session)
		},
	}
	cmd.Flags().String("debug-socket", "", "local debug control socket (start a debug server)")
	return cmd
}
