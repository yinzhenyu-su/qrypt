package config

import (
	"fmt"
	"strings"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

// Validate checks the whole configuration strictly and returns the first
// problem found. It is used by the CLI (config validate) and by config
// updates, where a typo in any mount must abort the whole operation.
func Validate(cfg *Config) error {
	if err := ValidateGlobal(cfg); err != nil {
		return err
	}
	for index, mountCfg := range cfg.Mounts {
		if err := ValidateMount(cfg, index, mountCfg); err != nil {
			return err
		}
	}
	return nil
}

// ValidateGlobal checks the configuration parts that are not scoped to a
// single mount: version, bandwidth, durations, logging level, cache sizes,
// mount name sanity and uniqueness, and the upload.default_mount references.
func ValidateGlobal(cfg *Config) error {
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
		"attr_timeout":     cfg.EffectiveAttrTimeout(),
		"entry_timeout":    cfg.EffectiveEntryTimeout(),
		"negative_timeout": cfg.EffectiveNegativeTimeout(),
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
	if cfg.ThumbnailCache.MaxSize != "" {
		if _, err := ParseSize(cfg.ThumbnailCache.MaxSize); err != nil {
			return fmt.Errorf("config: invalid thumbnail_cache.max_size: %w", err)
		}
	}
	if len(cfg.Mounts) == 0 {
		return fmt.Errorf("config: at least one [[mounts]] entry is required")
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
	}
	if strings.TrimSpace(cfg.Upload.DefaultMount) == "" {
		if strings.TrimSpace(cfg.Upload.DefaultPath) != "" {
			return fmt.Errorf("config: upload.default_path requires upload.default_mount")
		}
	} else {
		if !seenMounts[cfg.Upload.DefaultMount] {
			return fmt.Errorf("config: upload.default_mount %q does not match any mount", cfg.Upload.DefaultMount)
		}
		if cfg.Upload.DefaultPath != "" && !strings.HasPrefix(cfg.Upload.DefaultPath, "/") {
			return fmt.Errorf("config: upload.default_path must be absolute")
		}
	}
	return nil
}

// ValidateMount checks a single mount's driver, parameters, and encryption.
// The core open path applies it per mount so one broken mount (missing
// driver, expiring credentials, invalid params) is skipped and reported
// instead of blocking the whole namespace.
func ValidateMount(cfg *Config, index int, mountCfg MountConfig) error {
	knownDrivers := make(map[string]bool)
	for _, name := range drive.Names() {
		knownDrivers[name] = true
	}
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
	readCache := cfg.ReadCacheFor(mountCfg.Name)
	if readCache.MaxSize != "" {
		if _, err := ParseSize(readCache.MaxSize); err != nil {
			return fmt.Errorf("config: mount %q invalid read_cache.max_size: %w", mountCfg.Name, err)
		}
	}
	upload := cfg.UploadFor(mountCfg.Name)
	if _, err := ParseDuration(upload.UploadDelay); err != nil {
		return fmt.Errorf("config: mount %q invalid upload.upload_delay: %w", mountCfg.Name, err)
	}
	if _, err := ParseDuration(upload.DeleteDelay); err != nil {
		return fmt.Errorf("config: mount %q invalid upload.delete_delay: %w", mountCfg.Name, err)
	}
	if upload.UploadWorkers < 0 {
		return fmt.Errorf("config: mount %q invalid upload.upload_workers: must be non-negative", mountCfg.Name)
	}
	if mountCfg.Upload != nil && (strings.TrimSpace(mountCfg.Upload.DefaultMount) != "" || strings.TrimSpace(mountCfg.Upload.DefaultPath) != "") {
		return fmt.Errorf("config: mount %q upload.default_mount and upload.default_path are only supported in top-level [upload]", mountCfg.Name)
	}
	return nil
}
