package cli

import (
	"github.com/spf13/cobra"
	_ "github.com/yinzhenyu/qrypt/pkg/drivers/all"
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

	cmd.AddCommand(newMountCmd())
	cmd.AddCommand(newConfigCmd())
	cmd.AddCommand(newDriverCmd())
	cmd.AddCommand(newFsCmd())
	cmd.AddCommand(newDebugCmd())
	cmd.AddCommand(newVersionCmd(build))
	installFlagErrorHelp(cmd)

	return cmd
}
