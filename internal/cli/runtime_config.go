package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/yinzhenyu/qrypt/internal/config"
	"github.com/yinzhenyu/qrypt/internal/logging"
	"github.com/yinzhenyu/qrypt/internal/timeutil"
	"github.com/yinzhenyu/qrypt/pkg/osutil"
)

func initLogger(cfg *config.Config) error {
	if cfg == nil {
		return nil
	}
	level := cfg.Logging.LogLevel
	logFile := osutil.ExpandHome(cfg.Logging.LogFile)
	errFile := osutil.ExpandHome(cfg.Logging.ErrorFile)
	if level == "" && logFile == "" && errFile == "" {
		return nil
	}
	if level == "" {
		level = "info"
	}
	newLogger, err := logging.New(level, logFile, errFile, nil)
	if err != nil {
		return fmt.Errorf("initialize logging: %w", err)
	}
	logging.L = newLogger
	return nil
}

func initTime(ctx context.Context, loaded *config.Config) error {
	ntpConfig := timeutil.NTPConfig{Enabled: true}
	if loaded != nil {
		ntpConfig.Enabled = loaded.Time.EffectiveNTPEnabled()
		ntpConfig.Servers = loaded.Time.NTPServers
		timeout, err := config.ParseDuration(loaded.Time.NTPTimeout)
		if err != nil {
			return fmt.Errorf("config: invalid time.ntp_timeout: %w", err)
		}
		ntpConfig.Timeout = timeout
		poll, err := config.ParseDuration(loaded.Time.NTPPollInterval)
		if err != nil {
			return fmt.Errorf("config: invalid time.ntp_poll_interval: %w", err)
		}
		ntpConfig.PollInterval = poll
	}
	timeutil.StartNTP(ctx, ntpConfig)
	return nil
}

func mountPointFromLoadedConfig(cfg *config.Config) (string, error) {
	if cfg == nil {
		return "", configNotFoundError()
	}
	if mountPoint := cfg.EffectiveMountPoint(); mountPoint != "" {
		return mountPoint, nil
	}
	return "", fmt.Errorf("mount point not specified (set mount_point in the config or pass MOUNTPOINT)")
}

type cliMountConfig struct {
	VolumeName         string
	ReadOnly           bool
	AllowOther         bool
	DefaultPermissions bool
	NoAppleDouble      bool
	NoAppleXattr       bool
	AttrTimeout        time.Duration
	AttrTimeoutSet     bool
	EntryTimeout       time.Duration
	EntryTimeoutSet    bool
	NegativeTimeout    time.Duration
	TotalSpace         int64
	FreeSpace          int64
	Logging            config.LoggingConfig
}

func mountConfigFromConfig(configPath string) (cliMountConfig, error) {
	mountConfig := cliMountConfig{
		VolumeName:      "Qrypt",
		NoAppleDouble:   true,
		AttrTimeout:     time.Second,
		EntryTimeout:    time.Second,
		NegativeTimeout: 0,
	}
	if configPath == "" {
		return mountConfig, nil
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		return mountConfig, err
	}
	return mountConfigFromLoadedConfig(cfg)
}

func mountConfigFromLoadedConfig(cfg *config.Config) (cliMountConfig, error) {
	mountConfig := cliMountConfig{
		VolumeName:      "Qrypt",
		NoAppleDouble:   true,
		AttrTimeout:     time.Second,
		EntryTimeout:    time.Second,
		NegativeTimeout: 0,
	}
	if cfg == nil {
		return mountConfig, nil
	}
	mountConfig.VolumeName = cfg.EffectiveVolumeName()
	mountConfig.ReadOnly = cfg.ReadOnly
	mountConfig.AllowOther = cfg.AllowOther
	mountConfig.DefaultPermissions = cfg.DefaultPermissions
	mountConfig.NoAppleDouble = cfg.EffectiveNoAppleDouble()
	mountConfig.NoAppleXattr = cfg.EffectiveNoAppleXattr()
	attrTimeout, err := config.ParseDuration(cfg.AttrTimeout)
	if err != nil {
		return mountConfig, fmt.Errorf("config: invalid attr_timeout: %w", err)
	}
	if strings.TrimSpace(cfg.AttrTimeout) != "" {
		mountConfig.AttrTimeout = attrTimeout
		mountConfig.AttrTimeoutSet = true
	}
	entryTimeout, err := config.ParseDuration(cfg.EntryTimeout)
	if err != nil {
		return mountConfig, fmt.Errorf("config: invalid entry_timeout: %w", err)
	}
	if strings.TrimSpace(cfg.EntryTimeout) != "" {
		mountConfig.EntryTimeout = entryTimeout
		mountConfig.EntryTimeoutSet = true
	}
	negativeTimeout, err := config.ParseDuration(cfg.NegativeTimeout)
	if err != nil {
		return mountConfig, fmt.Errorf("config: invalid negative_timeout: %w", err)
	}
	mountConfig.NegativeTimeout = negativeTimeout
	totalSpace, freeSpace, err := cfg.EffectiveSpaceBytes()
	if err != nil {
		return mountConfig, err
	}
	mountConfig.TotalSpace = totalSpace
	mountConfig.FreeSpace = freeSpace
	mountConfig.Logging = cfg.Logging
	return mountConfig, nil
}
