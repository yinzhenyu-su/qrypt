package cli

import (
	"context"

	"github.com/yinzhenyu/qrypt/internal/config"
	"github.com/yinzhenyu/qrypt/pkg/core"
)

func buildFileSystem(ctx context.Context, configPath string) (builtFS, func(), error) {
	if configPath == "" {
		return nil, nil, configNotFoundError()
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		return nil, nil, err
	}
	return buildFileSystemFromConfig(ctx, cfg)
}

func buildFileSystemFromConfig(ctx context.Context, cfg *config.Config) (builtFS, func(), error) {
	return buildFileSystemFromConfigMount(ctx, cfg, "")
}

func buildFileSystemFromConfigMount(ctx context.Context, cfg *config.Config, mountName string) (builtFS, func(), error) {
	return buildFileSystemFromConfigMountMode(ctx, cfg, mountName, false)
}

func buildFileSystemFromConfigMountMode(ctx context.Context, cfg *config.Config, mountName string, forceNamespace bool) (builtFS, func(), error) {
	return buildFileSystemWithBandwidth(ctx, cfg, mountName, forceNamespace, nil)
}

// buildFileSystemWithBandwidth builds a filesystem with an optional CLI
// bandwidth override (nil means use the config [bandwidth] section).
func buildFileSystemWithBandwidth(ctx context.Context, cfg *config.Config, mountName string, forceNamespace bool, bandwidth *config.BandwidthLimits) (builtFS, func(), error) {
	return core.BuildFileSystem(ctx, cfg, core.Options{
		MountName:      mountName,
		ForceNamespace: forceNamespace,
		Bandwidth:      bandwidth,
	})
}

func defaultCacheDir() string {
	return core.DefaultCacheDir()
}
