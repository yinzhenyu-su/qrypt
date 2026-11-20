package mount

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/spf13/cobra"
	clicore "github.com/yinzhenyu/qrypt/internal/cli/core"
	"github.com/yinzhenyu/qrypt/internal/cli/i18n"
	cliruntime "github.com/yinzhenyu/qrypt/internal/cli/runtime"
	"github.com/yinzhenyu/qrypt/pkg/control"
	corepkg "github.com/yinzhenyu/qrypt/pkg/core"
	"github.com/yinzhenyu/qrypt/pkg/logging"
	"github.com/yinzhenyu/qrypt/pkg/util"
)

// controlEndpointFor resolves the command endpoint of the Core that owns this
// configuration's state root.
func controlEndpointFor(state cliruntime.ConfigState) string {
	runDir := corepkg.RuntimeRunDir(state.Path, state.Cfg)
	return corepkg.ControlSocketPath(state.Cfg, corepkg.RuntimeLayout{RunDir: runDir})
}

// runAttach asks the resident Core to host the FUSE session instead of opening
// a second filesystem in this process. Ctrl+C detaches the session but leaves
// the Core (and every cache) alive for the other clients.
func runAttach(rt cliruntime.Runtime, cmd *cobra.Command, state cliruntime.ConfigState, ctx context.Context, selectedMount, mountPointFlag string, stopCore bool) error {
	loc := i18n.FromCommand(cmd)
	mountPoint := mountPointFlag
	if mountPoint == "" {
		var err error
		mountPoint, err = rt.MountPointFromConfig(state.Cfg)
		if err != nil {
			return err
		}
	}
	mountPoint = util.ExpandHome(mountPoint)

	logging.L.Infof("Mounting at %s ...", mountPoint)
	fmt.Fprintf(cmd.ErrOrStderr(), "%s\n", loc.T("out.mount.mounting", mountPoint))
	if err := clicore.EnsureCore(ctx, rt, state); err != nil {
		return err
	}
	client, err := control.NewClient(controlEndpointFor(state))
	if err != nil {
		return err
	}
	attachCtx, cancelAttach := context.WithTimeout(ctx, 30*time.Second)
	defer cancelAttach()
	info, err := client.AttachMount(attachCtx, control.MountAttachRequest{
		Name:       selectedMount,
		MountPoint: mountPoint,
	})
	if err != nil {
		return err
	}

	var detachOnce sync.Once
	detach := func() {
		detachOnce.Do(func() {
			detachCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			logging.L.Infof("Unmounting %s ...", mountPoint)
			fmt.Fprintf(cmd.ErrOrStderr(), "%s\n", loc.T("out.mount.unmounting", mountPoint))
			if err := client.DetachMount(detachCtx, info.ID); err != nil {
				logging.L.Warnf("Unmount failed: %v", err)
			}
			if stopCore {
				if _, err := client.PostJSON(detachCtx, "/v1/control/shutdown", map[string]bool{"drain": true}); err != nil {
					logging.L.Warnf("Stop core failed: %v", err)
				}
			}
		})
	}
	defer detach()

	logging.L.Infof("Mounted at %s. Press Ctrl+C to unmount.", mountPoint)
	fmt.Fprintf(cmd.ErrOrStderr(), "%s\n", loc.T("out.mount.mounted", mountPoint))

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			detach()
			return nil
		case <-ticker.C:
			listCtx, cancelList := context.WithTimeout(context.Background(), 5*time.Second)
			mounts, err := client.ListMounts(listCtx)
			cancelList()
			if err != nil {
				logging.L.Warnf("Mount stopped unexpectedly: %v", err)
				detach()
				return fmt.Errorf("%s: %w", mountPoint, errMountSessionEnded)
			}
			found := false
			for _, hosted := range mounts {
				if hosted.ID == info.ID {
					found = true
					break
				}
			}
			if !found {
				// Another client detached the volume (qrypt unmount); this is a
				// normal end of the attach session, so consume detach and exit 0.
				detachOnce.Do(func() {})
				logging.L.Infof("Mount %s was detached by another client", mountPoint)
				return nil
			}
		}
	}
}
