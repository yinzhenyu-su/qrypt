package config

import (
	"fmt"
	"strings"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

func Validate(cfg *Config) error {
	if cfg == nil {
		return fmt.Errorf("config: configuration is empty")
	}
	if cfg.Version != "" && cfg.Version != "1" {
		return fmt.Errorf("config: unsupported version %q", cfg.Version)
	}
	if _, err := cfg.EffectiveBandwidthLimits(); err != nil {
		return err
	}
	for name, value := range map[string]string{
		"attr_timeout":     cfg.AttrTimeout,
		"entry_timeout":    cfg.EntryTimeout,
		"negative_timeout": cfg.NegativeTimeout,
	} {
		if _, err := ParseDuration(value); err != nil {
			return fmt.Errorf("config: invalid %s: %w", name, err)
		}
	}
	if _, _, err := cfg.EffectiveSpaceBytes(); err != nil {
		return err
	}
	switch strings.ToLower(strings.TrimSpace(cfg.Logging.LogLevel)) {
	case "", "debug", "info", "warn", "warning", "error", "off", "none":
	default:
		return fmt.Errorf("config: invalid logging.log_level %q", cfg.Logging.LogLevel)
	}
	for name, value := range map[string]string{
		"time.ntp_timeout":       cfg.Time.NTPTimeout,
		"time.ntp_poll_interval": cfg.Time.NTPPollInterval,
	} {
		if strings.TrimSpace(value) == "" {
			continue
		}
		duration, err := ParseDuration(value)
		if err != nil {
			return fmt.Errorf("config: invalid %s: %w", name, err)
		}
		if duration <= 0 {
			return fmt.Errorf("config: %s must be greater than 0", name)
		}
	}
	if len(cfg.Mounts) == 0 {
		return fmt.Errorf("config: at least one [[mounts]] entry is required")
	}
	knownDrivers := make(map[string]bool)
	for _, name := range drive.Names() {
		knownDrivers[name] = true
	}
	seenMounts := make(map[string]bool)
	for index, mountCfg := range cfg.Mounts {
		label := fmt.Sprintf("mounts[%d]", index)
		if mountCfg.Name == "" {
			return fmt.Errorf("config: %s.name is required", label)
		}
		if mountCfg.Name == "." || mountCfg.Name == ".." || strings.ContainsAny(mountCfg.Name, `/\`) {
			return fmt.Errorf("config: %s.name %q must be a single path component", label, mountCfg.Name)
		}
		if seenMounts[mountCfg.Name] {
			return fmt.Errorf("config: duplicate mount name %q", mountCfg.Name)
		}
		seenMounts[mountCfg.Name] = true
		if !knownDrivers[mountCfg.Type] {
			return fmt.Errorf("config: mount %q has unknown driver %q", mountCfg.Name, mountCfg.Type)
		}
		allowedParams := make(map[string]bool)
		for _, param := range drive.ParamSchema(mountCfg.Type) {
			allowedParams[param.Name] = true
			if param.Required && strings.TrimSpace(mountCfg.Params[param.Name]) == "" {
				return fmt.Errorf("config: mount %q missing required parameter %q", mountCfg.Name, param.Name)
			}
		}
		for name := range mountCfg.Params {
			if !allowedParams[name] {
				return fmt.Errorf("config: mount %q has unknown parameter %q for driver %q", mountCfg.Name, name, mountCfg.Type)
			}
		}
		params := drive.Params{}
		for key, value := range mountCfg.Params {
			params[key] = value
		}
		if _, err := drive.New(mountCfg.Type, params); err != nil {
			return fmt.Errorf("config: mount %q: %w", mountCfg.Name, err)
		}
		enc := cfg.EncryptionFor(mountCfg.Name)
		if enc.Password != "" {
			if err := enc.Validate(); err != nil {
				return fmt.Errorf("config: mount %q: %w", mountCfg.Name, err)
			}
		}
		cache := cfg.CacheFor(mountCfg.Name)
		if cache.MaxSize != "" {
			if _, err := ParseSize(cache.MaxSize); err != nil {
				return fmt.Errorf("config: mount %q invalid cache.max_size: %w", mountCfg.Name, err)
			}
		}
		if _, err := ParseDuration(cache.UploadDelay); err != nil {
			return fmt.Errorf("config: mount %q invalid cache.upload_delay: %w", mountCfg.Name, err)
		}
		if _, err := ParseDuration(cache.DeleteDelay); err != nil {
			return fmt.Errorf("config: mount %q invalid cache.delete_delay: %w", mountCfg.Name, err)
		}
		if cache.UploadWorkers < 0 {
			return fmt.Errorf("config: mount %q invalid cache.upload_workers: must be non-negative", mountCfg.Name)
		}
	}
	return nil
}
