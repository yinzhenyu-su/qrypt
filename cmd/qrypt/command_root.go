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

var (
	configPath         string
	driverName         string
	root               string
	mountName          string
	password           string
	salt               string
	fileNameEncryption string
	fileNameEncoding   string
	journalCacheDir    string
)

func newRootCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "qrypt",
		Short: "Mounts encrypted cloud drives as a local filesystem",
		Long: `qrypt exposes configured cloud drives as one local FUSE mount point.

Each drive appears as a directory under the mount point, with optional
rclone-compatible content and filename encryption.

Use --config to point to a TOML config file, then mount to start the
filesystem, or use fs list/cat/put for one-shot operations.`,
		PersistentPreRunE: func(c *cobra.Command, args []string) error {
			initLoggerFromConfig(configPath)
			return nil
		},
		RunE: func(c *cobra.Command, args []string) error {
			return c.Help()
		},
	}

	cmd.CompletionOptions.HiddenDefaultCmd = true

	cmd.PersistentFlags().StringVar(&configPath, "config", "", "config file path")

	cmd.AddCommand(newMountCmd())
	cmd.AddCommand(newConfigCmd())
	cmd.AddCommand(newDriverCmd())
	cmd.AddCommand(newFsCmd())
	cmd.AddCommand(newJournalCmd())
	cmd.AddCommand(newDebugCmd())

	return cmd
}
