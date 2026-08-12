package config

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"
	cliruntime "github.com/yinzhenyu/qrypt/internal/cli/runtime"
	qconfig "github.com/yinzhenyu/qrypt/pkg/config"
	"github.com/yinzhenyu/qrypt/pkg/mount"
)

type testRuntime struct{}

func (testRuntime) BuildFileSystemForMount(context.Context, *qconfig.Config, string) (cliruntime.MountFileSystem, func(), error) {
	return nil, nil, fmt.Errorf("unexpected BuildFileSystemForMount call")
}

func (testRuntime) CommandConfig(*cobra.Command) (cliruntime.ConfigState, error) {
	return cliruntime.ConfigState{}, fmt.Errorf("unexpected CommandConfig call")
}

func (testRuntime) CommandConfigPath(cmd *cobra.Command) (string, error) {
	return cmd.Flags().GetString("config")
}

func (testRuntime) ConfigNotFoundError() error {
	return fmt.Errorf("config not found")
}

func (testRuntime) DefaultCacheDir() string {
	return ""
}

func (testRuntime) DebugReportSchemaVersion() int {
	return 0
}

func (testRuntime) ExitError(_ int, err error) error {
	return err
}

func (testRuntime) MissingSocketError(*cobra.Command) error {
	return fmt.Errorf("unexpected MissingSocketError call")
}

func (testRuntime) MountOptionsFromConfig(*qconfig.Config) (mount.Options, error) {
	return mount.Options{}, fmt.Errorf("unexpected MountOptionsFromConfig call")
}

func (testRuntime) MountPointFromConfig(*qconfig.Config) (string, error) {
	return "", fmt.Errorf("unexpected MountPointFromConfig call")
}

func (testRuntime) OpenFileSystem(*cobra.Command) (context.Context, cliruntime.FileSystem, func(), error) {
	return nil, nil, nil, fmt.Errorf("unexpected OpenFileSystem call")
}

func (testRuntime) ShutdownContext(cmd *cobra.Command) (context.Context, func()) {
	return cmd.Context(), func() {}
}

func (testRuntime) UsageError(_ *cobra.Command, format string, args ...any) error {
	return fmt.Errorf(format, args...)
}

func (testRuntime) WaitFileSystemIdle(context.Context, any, time.Duration) error {
	return fmt.Errorf("unexpected WaitFileSystemIdle call")
}

func (testRuntime) WithFSBandwidthFlags(cmd *cobra.Command) *cobra.Command {
	return cmd
}

func (testRuntime) WithConfigFlag(cmd *cobra.Command) *cobra.Command {
	cmd.Flags().StringP("config", "c", "", "config file path")
	return cmd
}

func (testRuntime) WithPersistentConfigFlag(cmd *cobra.Command) *cobra.Command {
	cmd.PersistentFlags().StringP("config", "c", "", "config file path")
	return cmd
}

func (testRuntime) WithPersistentRuntimeConfigFlag(cmd *cobra.Command) *cobra.Command {
	cmd.PersistentFlags().StringP("config", "c", "", "config file path")
	return cmd
}

func (testRuntime) WithRuntimeConfigFlag(cmd *cobra.Command) *cobra.Command {
	return testRuntime{}.WithConfigFlag(cmd)
}
