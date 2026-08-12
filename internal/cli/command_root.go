package cli

import (
	"github.com/spf13/cobra"
	cliconfig "github.com/yinzhenyu/qrypt/internal/cli/config"
	clidebug "github.com/yinzhenyu/qrypt/internal/cli/debug"
	clidriver "github.com/yinzhenyu/qrypt/internal/cli/driver"
	clifs "github.com/yinzhenyu/qrypt/internal/cli/fs"
	climount "github.com/yinzhenyu/qrypt/internal/cli/mount"
	cliversion "github.com/yinzhenyu/qrypt/internal/cli/version"
	_ "github.com/yinzhenyu/qrypt/pkg/drivers/all" // registers all drivers via their init functions
)

// NewRootCommand builds the qrypt command tree.
func NewRootCommand() *cobra.Command {
	build := currentBuildInfo()
	cmd := &cobra.Command{
		Use:          "qrypt",
		Short:        "Mounts encrypted cloud drives as a local filesystem",
		SilenceUsage: true,
		Version:      build.Version,
		Long: `qrypt exposes configured cloud drives as one local FUSE mount point.

Each drive appears as a directory under the mount point, with optional
rclone-compatible content and filename encryption.

Use a command's --config flag to point to a TOML config file, then mount to
start the filesystem, or use fs list/cat/get/put for one-shot operations.
When --config is omitted, qrypt searches ./qrypt.toml, ~/.qrypt/qrypt.toml,
then the platform config directory: $XDG_CONFIG_HOME/qrypt/qrypt.toml
(default: ~/.config/qrypt/qrypt.toml) on Unix, or
%AppData%\qrypt\qrypt.toml on Windows.`,
		Args: commandGroupArgs(nil),
		RunE: func(c *cobra.Command, args []string) error {
			return c.Help()
		},
	}

	cmd.AddCommand(climount.NewCommand(cliRuntime{}))
	cmd.AddCommand(cliconfig.NewCommand(cliRuntime{}))
	cmd.AddCommand(clidriver.NewCommand(cliRuntime{}))
	cmd.AddCommand(clifs.NewCommand(cliRuntime{}))
	cmd.AddCommand(clidebug.NewCommand(cliRuntime{}))
	cmd.AddCommand(cliversion.NewCommand(cliRuntime{}, build))
	installFlagErrorHelp(cmd)

	return cmd
}
