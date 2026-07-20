package cli

import (
	"context"

	"github.com/yinzhenyu/qrypt/internal/config"
	"github.com/yinzhenyu/qrypt/pkg/core"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

func buildFileSystem(ctx context.Context, configPath string) (vfs.FileSystem, func(), error) {
	if configPath == "" {
		return nil, nil, configNotFoundError()
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		return nil, nil, err
	}
	return buildFileSystemFromConfig(ctx, cfg)
}

func buildFileSystemFromConfig(ctx context.Context, cfg *config.Config) (vfs.FileSystem, func(), error) {
	return buildFileSystemFromConfigMount(ctx, cfg, "")
}

func buildFileSystemFromConfigMount(ctx context.Context, cfg *config.Config, mountName string) (vfs.FileSystem, func(), error) {
	return buildFileSystemFromConfigMountMode(ctx, cfg, mountName, false)
}

func buildFileSystemFromConfigMountNamespace(ctx context.Context, cfg *config.Config, mountName string) (vfs.FileSystem, func(), error) {
	return buildFileSystemFromConfigMountMode(ctx, cfg, mountName, true)
}

func buildFileSystemFromConfigMountMode(ctx context.Context, cfg *config.Config, mountName string, forceNamespace bool) (vfs.FileSystem, func(), error) {
	return core.BuildFileSystem(ctx, cfg, core.Options{
		MountName:      mountName,
		ForceNamespace: forceNamespace,
	})
}

func defaultCacheDir() string {
	return core.DefaultCacheDir()
}
