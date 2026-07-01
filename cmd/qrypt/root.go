package main

import (
	"github.com/spf13/cobra"

	_ "github.com/yinzhenyu/qrypt/internal/driver/aliyundrive"
	_ "github.com/yinzhenyu/qrypt/internal/driver/baidunetdisk"
	_ "github.com/yinzhenyu/qrypt/internal/driver/localfs"
	_ "github.com/yinzhenyu/qrypt/internal/driver/p115"
	_ "github.com/yinzhenyu/qrypt/internal/driver/quark"
	_ "github.com/yinzhenyu/qrypt/internal/driver/webdav"
	_ "github.com/yinzhenyu/qrypt/internal/driver/yun139"
)

// Package-level flag values shared across commands.
var (
	configPath         string
	cacheDir           string
	driverName         string
	root               string
	mountName          string
	password           string
	salt               string
	fileNameEncryption string
	fileNameEncoding   string
	debugSocket        string
)

func newRootCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "qrypt",
		Short: "Mounts encrypted cloud drives as a local filesystem",
		Long: `qrypt is an encrypted virtual filesystem for cloud drives.

It mounts one local namespace with FUSE, exposes each configured drive as
a directory, and can wrap each drive with rclone-compatible crypt encryption.

Configuration is provided via a TOML file. Use --config to specify the path.
See 'qrypt config init' for a template.`,

		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			initLoggerFromConfig(configPath)
			return nil
		},
		SilenceUsage: true,
	}

	cmd.CompletionOptions.HiddenDefaultCmd = true

	cmd.PersistentFlags().StringVar(&configPath, "config", "", "TOML config file")
	cmd.PersistentFlags().StringVar(&cacheDir, "cache", "", "cache directory override")

	cmd.AddCommand(newMountCmd())
	cmd.AddCommand(newConfigCmd())
	cmd.AddCommand(newDriverCmd())
	cmd.AddCommand(newFsCmd())
	cmd.AddCommand(newDebugCmd())

	// Hidden deprecated top-level aliases.
	for _, c := range []*cobra.Command{newListCmd(), newCatCmd(), newPutCmd(), newPendingCmd()} {
		c.Hidden = true
		cmd.AddCommand(c)
	}

	return cmd
}
