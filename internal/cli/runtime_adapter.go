package cli

import (
	"context"
	"os/signal"
	"time"

	"github.com/spf13/cobra"
	clidebug "github.com/yinzhenyu/qrypt/internal/cli/debug"
	cliruntime "github.com/yinzhenyu/qrypt/internal/cli/runtime"
	"github.com/yinzhenyu/qrypt/pkg/config"
	"github.com/yinzhenyu/qrypt/pkg/mount"
)

type cliRuntime struct{}

func (cliRuntime) BuildFileSystemForMount(ctx context.Context, cfg *config.Config, mountName string) (cliruntime.MountFileSystem, func(), error) {
	return buildFileSystemFromConfigMount(ctx, cfg, mountName)
}

func (cliRuntime) OpenFileSystem(cmd *cobra.Command) (context.Context, cliruntime.FileSystem, func(), error) {
	return openFileSystem(cmd)
}

func (cliRuntime) CommandConfig(cmd *cobra.Command) (cliruntime.ConfigState, error) {
	state, err := commandConfig(cmd)
	if err != nil {
		return cliruntime.ConfigState{}, err
	}
	return cliruntime.ConfigState{Path: state.path, Cfg: state.cfg}, nil
}

func (cliRuntime) CommandConfigPath(cmd *cobra.Command) (string, error) {
	return commandConfigPath(cmd)
}

func (cliRuntime) ConfigNotFoundError() error {
	return configNotFoundError()
}

func (cliRuntime) DefaultCacheDir() string {
	return defaultCacheDir()
}

func (cliRuntime) DebugReportSchemaVersion() int {
	return clidebug.DebugAIReportSchemaVersion
}

func (cliRuntime) ExitError(code int, err error) error {
	return &ExitError{Code: code, Err: err}
}

func (cliRuntime) MissingSocketError(cmd *cobra.Command) error {
	return missingSocketError(cmd)
}

func (cliRuntime) MountOptionsFromConfig(cfg *config.Config) (mount.Options, error) {
	mountCfg, err := mountConfigFromLoadedConfig(cfg)
	if err != nil {
		return mount.Options{}, err
	}
	return mount.Options{
		ReadOnly:            mountCfg.ReadOnly,
		AllowOther:          mountCfg.AllowOther,
		DefaultPermissions:  mountCfg.DefaultPermissions,
		VolumeName:          mountCfg.VolumeName,
		IgnoreAppleMetadata: mountCfg.IgnoreAppleMetadata,
		DelegateAppleXattr:  mountCfg.DelegateAppleXattr,
		AttrTimeout:         mountCfg.AttrTimeout,
		AttrTimeoutSet:      mountCfg.AttrTimeoutSet,
		EntryTimeout:        mountCfg.EntryTimeout,
		EntryTimeoutSet:     mountCfg.EntryTimeoutSet,
		NegativeTimeout:     mountCfg.NegativeTimeout,
		TotalSpace:          mountCfg.TotalSpace,
		FreeSpace:           mountCfg.FreeSpace,
	}, nil
}

func (cliRuntime) MountPointFromConfig(cfg *config.Config) (string, error) {
	return mountPointFromLoadedConfig(cfg)
}

func (cliRuntime) ShutdownContext(cmd *cobra.Command) (context.Context, func()) {
	return signal.NotifyContext(commandContext(cmd), shutdownSignals()...)
}

func (cliRuntime) UsageError(cmd *cobra.Command, format string, args ...any) error {
	return commandUsageError(cmd, format, args...)
}

func (cliRuntime) WaitFileSystemIdle(ctx context.Context, fs any, timeout time.Duration) error {
	return waitFileSystemIdle(ctx, fs, timeout)
}

func (cliRuntime) WithConfigFlag(cmd *cobra.Command) *cobra.Command {
	return withConfigFlag(cmd)
}

func (cliRuntime) WithFSBandwidthFlags(cmd *cobra.Command) *cobra.Command {
	addFSBandwidthFlags(cmd)
	return cmd
}

func (cliRuntime) WithPersistentConfigFlag(cmd *cobra.Command) *cobra.Command {
	cmd.PersistentFlags().StringP("config", "c", "", "config file path (auto-discovered when omitted)")
	cmd.PersistentPreRunE = prepareConfig
	return cmd
}

func (cliRuntime) WithPersistentRuntimeConfigFlag(cmd *cobra.Command) *cobra.Command {
	return withPersistentRuntimeConfigFlag(cmd)
}

func (cliRuntime) WithRuntimeConfigFlag(cmd *cobra.Command) *cobra.Command {
	return withRuntimeConfigFlag(cmd)
}
