package runtime

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"
	"github.com/yinzhenyu/qrypt/pkg/config"
	"github.com/yinzhenyu/qrypt/pkg/mount"
)

// TestRuntime is a programmable stand-in for Runtime that test packages in
// other packages configure per call. Unset fields panic, so a test only
// overrides the methods the command under test actually exercises.
type TestRuntime struct {
	BuildFileSystemForMountFn         func(context.Context, *config.Config, string) (MountFileSystem, func(), error)
	CommandConfigFn                   func(*cobra.Command) (ConfigState, error)
	CommandConfigPathFn               func(*cobra.Command) (string, error)
	ConfigNotFoundErrorFn             func() error
	DefaultCacheDirFn                 func() string
	DebugReportSchemaVersionFn        func() int
	ExitErrorFn                       func(int, error) error
	MissingSocketErrorFn              func(*cobra.Command) error
	MountOptionsFromConfigFn          func(*config.Config) (mount.Options, error)
	MountPointFromConfigFn            func(*config.Config) (string, error)
	OpenFileSystemFn                  func(*cobra.Command) (context.Context, OpenedFileSystem, func(), error)
	ShutdownContextFn                 func(*cobra.Command) (context.Context, func())
	UsageErrorFn                      func(*cobra.Command, string, ...any) error
	WaitFileSystemIdleFn              func(context.Context, OpenedFileSystem, time.Duration) error
	WithFSBandwidthFlagsFn            func(*cobra.Command) *cobra.Command
	WithConfigFlagFn                  func(*cobra.Command) *cobra.Command
	WithPersistentConfigFlagFn        func(*cobra.Command) *cobra.Command
	WithPersistentRuntimeConfigFlagFn func(*cobra.Command) *cobra.Command
	WithRuntimeConfigFlagFn           func(*cobra.Command) *cobra.Command
}

func (t TestRuntime) BuildFileSystemForMount(ctx context.Context, cfg *config.Config, mountName string) (MountFileSystem, func(), error) {
	if t.BuildFileSystemForMountFn == nil {
		panic(unsetRuntimeMethod("BuildFileSystemForMount"))
	}
	return t.BuildFileSystemForMountFn(ctx, cfg, mountName)
}

func (t TestRuntime) CommandConfig(cmd *cobra.Command) (ConfigState, error) {
	if t.CommandConfigFn == nil {
		panic(unsetRuntimeMethod("CommandConfig"))
	}
	return t.CommandConfigFn(cmd)
}

func (t TestRuntime) CommandConfigPath(cmd *cobra.Command) (string, error) {
	if t.CommandConfigPathFn == nil {
		panic(unsetRuntimeMethod("CommandConfigPath"))
	}
	return t.CommandConfigPathFn(cmd)
}

func (t TestRuntime) ConfigNotFoundError() error {
	if t.ConfigNotFoundErrorFn == nil {
		panic(unsetRuntimeMethod("ConfigNotFoundError"))
	}
	return t.ConfigNotFoundErrorFn()
}

func (t TestRuntime) DefaultCacheDir() string {
	if t.DefaultCacheDirFn == nil {
		panic(unsetRuntimeMethod("DefaultCacheDir"))
	}
	return t.DefaultCacheDirFn()
}

func (t TestRuntime) DebugReportSchemaVersion() int {
	if t.DebugReportSchemaVersionFn == nil {
		panic(unsetRuntimeMethod("DebugReportSchemaVersion"))
	}
	return t.DebugReportSchemaVersionFn()
}

func (t TestRuntime) ExitError(code int, err error) error {
	if t.ExitErrorFn == nil {
		panic(unsetRuntimeMethod("ExitError"))
	}
	return t.ExitErrorFn(code, err)
}

func (t TestRuntime) MissingSocketError(cmd *cobra.Command) error {
	if t.MissingSocketErrorFn == nil {
		panic(unsetRuntimeMethod("MissingSocketError"))
	}
	return t.MissingSocketErrorFn(cmd)
}

func (t TestRuntime) MountOptionsFromConfig(cfg *config.Config) (mount.Options, error) {
	if t.MountOptionsFromConfigFn == nil {
		panic(unsetRuntimeMethod("MountOptionsFromConfig"))
	}
	return t.MountOptionsFromConfigFn(cfg)
}

func (t TestRuntime) MountPointFromConfig(cfg *config.Config) (string, error) {
	if t.MountPointFromConfigFn == nil {
		panic(unsetRuntimeMethod("MountPointFromConfig"))
	}
	return t.MountPointFromConfigFn(cfg)
}

func (t TestRuntime) OpenFileSystem(cmd *cobra.Command) (context.Context, OpenedFileSystem, func(), error) {
	if t.OpenFileSystemFn == nil {
		panic(unsetRuntimeMethod("OpenFileSystem"))
	}
	return t.OpenFileSystemFn(cmd)
}

func (t TestRuntime) ShutdownContext(cmd *cobra.Command) (context.Context, func()) {
	if t.ShutdownContextFn == nil {
		panic(unsetRuntimeMethod("ShutdownContext"))
	}
	return t.ShutdownContextFn(cmd)
}

func (t TestRuntime) UsageError(cmd *cobra.Command, format string, args ...any) error {
	if t.UsageErrorFn == nil {
		panic(unsetRuntimeMethod("UsageError"))
	}
	return t.UsageErrorFn(cmd, format, args...)
}

func (t TestRuntime) WaitFileSystemIdle(ctx context.Context, fs OpenedFileSystem, timeout time.Duration) error {
	if t.WaitFileSystemIdleFn == nil {
		panic(unsetRuntimeMethod("WaitFileSystemIdle"))
	}
	return t.WaitFileSystemIdleFn(ctx, fs, timeout)
}

func (t TestRuntime) WithFSBandwidthFlags(cmd *cobra.Command) *cobra.Command {
	if t.WithFSBandwidthFlagsFn == nil {
		panic(unsetRuntimeMethod("WithFSBandwidthFlags"))
	}
	return t.WithFSBandwidthFlagsFn(cmd)
}

func (t TestRuntime) WithConfigFlag(cmd *cobra.Command) *cobra.Command {
	if t.WithConfigFlagFn == nil {
		panic(unsetRuntimeMethod("WithConfigFlag"))
	}
	return t.WithConfigFlagFn(cmd)
}

func (t TestRuntime) WithPersistentConfigFlag(cmd *cobra.Command) *cobra.Command {
	if t.WithPersistentConfigFlagFn == nil {
		panic(unsetRuntimeMethod("WithPersistentConfigFlag"))
	}
	return t.WithPersistentConfigFlagFn(cmd)
}

func (t TestRuntime) WithPersistentRuntimeConfigFlag(cmd *cobra.Command) *cobra.Command {
	if t.WithPersistentRuntimeConfigFlagFn == nil {
		panic(unsetRuntimeMethod("WithPersistentRuntimeConfigFlag"))
	}
	return t.WithPersistentRuntimeConfigFlagFn(cmd)
}

func (t TestRuntime) WithRuntimeConfigFlag(cmd *cobra.Command) *cobra.Command {
	if t.WithRuntimeConfigFlagFn == nil {
		panic(unsetRuntimeMethod("WithRuntimeConfigFlag"))
	}
	return t.WithRuntimeConfigFlagFn(cmd)
}

func unsetRuntimeMethod(name string) error {
	return fmt.Errorf("runtime.TestRuntime: %s not configured; set %sFn or fake at a higher level", name, name)
}
