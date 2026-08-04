package core

import (
	"context"
	"fmt"

	"github.com/yinzhenyu/qrypt/internal/config"
	"github.com/yinzhenyu/qrypt/pkg/drive"
)

// ConfigSummary is a settings-UI friendly view of the current qrypt.toml.
// Secret driver parameters are masked.
type ConfigSummary struct {
	ConfigPath     string               `json:"config_path,omitempty"`
	Version        string               `json:"version,omitempty"`
	Mounts         []ConfigMountSummary `json:"mounts"`
	ReadCache      ReadCacheSummary     `json:"read_cache"`
	ThumbnailCache ReadCacheSummary     `json:"thumbnail_cache"`
	Upload         UploadSummary        `json:"upload"`
}

type ConfigMountSummary struct {
	Name      string            `json:"name"`
	Type      string            `json:"type"`
	Params    map[string]string `json:"params,omitempty"`
	Encrypted bool              `json:"encrypted"`
}

type ReadCacheSummary struct {
	MaxSize string `json:"max_size,omitempty"`
}

type UploadSummary struct {
	UploadDelay   string `json:"upload_delay,omitempty"`
	UploadWorkers int    `json:"upload_workers,omitempty"`
	DeleteDelay   string `json:"delete_delay,omitempty"`
	DefaultMount  string `json:"default_mount,omitempty"`
	DefaultPath   string `json:"default_path,omitempty"`
}

// ConfigMountUpdate mutates one mount entry. Action is one of
// "add", "update", or "remove". add/update accept Type and Params; remove
// only needs Name.
type ConfigMountUpdate struct {
	Action string            `json:"action"`
	Name   string            `json:"name"`
	Type   string            `json:"type,omitempty"`
	Params map[string]string `json:"params,omitempty"`
}

// ConfigUpdateRequest applies mount changes and optional global settings.
// Changes are validated before the config file is written, so a failed
// update leaves the previous config untouched.
type ConfigUpdateRequest struct {
	Mounts    []ConfigMountUpdate `json:"mounts,omitempty"`
	ReadCache *ReadCacheSummary   `json:"read_cache,omitempty"`
	Upload    *UploadSummary      `json:"upload,omitempty"`
}

func (c *Core) loadConfig() (*config.Config, error) {
	if c == nil || c.configPath == "" {
		return nil, fmt.Errorf("core: config path unavailable")
	}
	return config.Load(c.configPath)
}

func (c *Core) configSummary() (ConfigSummary, error) {
	cfg, err := c.loadConfig()
	if err != nil {
		return ConfigSummary{}, err
	}
	return summarizeConfig(c.configPath, cfg), nil
}

func summarizeConfig(path string, cfg *config.Config) ConfigSummary {
	summary := ConfigSummary{
		ConfigPath:     path,
		Version:        cfg.Version,
		ReadCache:      ReadCacheSummary{MaxSize: cfg.ReadCache.MaxSize},
		ThumbnailCache: ReadCacheSummary{MaxSize: cfg.ThumbnailCache.MaxSize},
		Upload: UploadSummary{
			UploadDelay:   cfg.Upload.UploadDelay,
			UploadWorkers: cfg.Upload.UploadWorkers,
			DeleteDelay:   cfg.Upload.DeleteDelay,
			DefaultMount:  cfg.Upload.DefaultMount,
			DefaultPath:   cfg.Upload.DefaultPath,
		},
	}
	for _, mount := range cfg.Mounts {
		params := map[string]string{}
		for key, value := range mount.Params {
			params[key] = value
		}
		for _, param := range drive.ParamSchema(mount.Type) {
			if param.Secret {
				if _, ok := params[param.Name]; ok {
					params[param.Name] = maskedSecretValue
				}
			}
		}
		encrypted := cfg.EncryptionFor(mount.Name).Password != ""
		summary.Mounts = append(summary.Mounts, ConfigMountSummary{
			Name:      mount.Name,
			Type:      mount.Type,
			Params:    params,
			Encrypted: encrypted,
		})
	}
	return summary
}

// maskedSecretValue is the placeholder used in ConfigSummary for secret
// driver params. When an update submits this value for an existing secret
// param, the previous value is kept instead of being overwritten.
const maskedSecretValue = "***"

// ConfigSummaryJSON returns a settings-UI friendly view of the current
// config file. It does not require a running core beyond the session that
// records the config path.
func (c *Core) ConfigSummaryJSON() (string, error) {
	summary, err := c.configSummary()
	if err != nil {
		return "", err
	}
	return marshalJSON(summary)
}

// ApplyConfigUpdate mutates the config file with mount changes and optional
// global settings, validates the result, and saves it. The returned summary
// reflects the saved config. Changes take effect on the next core open (or
// after a mobile ReloadConfigJSON).
func (c *Core) ApplyConfigUpdate(req ConfigUpdateRequest) (ConfigSummary, error) {
	cfg, err := c.loadConfig()
	if err != nil {
		return ConfigSummary{}, err
	}
	if err := applyConfigUpdate(cfg, req); err != nil {
		return ConfigSummary{}, err
	}
	if err := config.Validate(cfg); err != nil {
		return ConfigSummary{}, err
	}
	if err := config.Save(c.configPath, cfg); err != nil {
		return ConfigSummary{}, err
	}
	return summarizeConfig(c.configPath, cfg), nil
}

func applyConfigUpdate(cfg *config.Config, req ConfigUpdateRequest) error {
	for _, update := range req.Mounts {
		switch update.Action {
		case "remove":
			if err := removeConfigMount(cfg, update.Name); err != nil {
				return err
			}
		case "add", "update":
			if err := upsertConfigMount(cfg, update); err != nil {
				return err
			}
		default:
			return fmt.Errorf("core: unsupported mount action %q", update.Action)
		}
	}
	if req.ReadCache != nil {
		if req.ReadCache.MaxSize != "" {
			if _, err := config.ParseSize(req.ReadCache.MaxSize); err != nil {
				return fmt.Errorf("core: invalid read_cache.max_size: %w", err)
			}
		}
		cfg.ReadCache.MaxSize = req.ReadCache.MaxSize
	}
	if req.Upload != nil {
		cfg.Upload.UploadDelay = req.Upload.UploadDelay
		cfg.Upload.UploadWorkers = req.Upload.UploadWorkers
		cfg.Upload.DeleteDelay = req.Upload.DeleteDelay
		cfg.Upload.DefaultMount = req.Upload.DefaultMount
		cfg.Upload.DefaultPath = req.Upload.DefaultPath
	}
	return nil
}

func removeConfigMount(cfg *config.Config, name string) error {
	if name == "" {
		return fmt.Errorf("core: mount name required")
	}
	for i, mount := range cfg.Mounts {
		if mount.Name == name {
			cfg.Mounts = append(cfg.Mounts[:i], cfg.Mounts[i+1:]...)
			return nil
		}
	}
	return fmt.Errorf("core: mount %q not found", name)
}

func upsertConfigMount(cfg *config.Config, update ConfigMountUpdate) error {
	if update.Name == "" {
		return fmt.Errorf("core: mount name required")
	}
	if update.Type == "" {
		return fmt.Errorf("core: mount %q requires a driver type", update.Name)
	}
	for i, mount := range cfg.Mounts {
		if mount.Name == update.Name {
			cfg.Mounts[i].Type = update.Type
			cfg.Mounts[i].Params = mergeMountParams(mount.Type, mount.Params, update.Params)
			return nil
		}
	}
	if update.Action == "update" {
		return fmt.Errorf("core: mount %q not found", update.Name)
	}
	cfg.Mounts = append(cfg.Mounts, config.MountConfig{
		Name:   update.Name,
		Type:   update.Type,
		Params: config.ParamMap(update.Params),
	})
	return nil
}

// mergeMountParams applies an update on top of the existing params with
// field-level semantics: params not mentioned in the update are kept, and a
// masked secret placeholder for an existing secret keeps the previous value.
// This prevents a settings-UI round trip (summary -> edit plain fields ->
// submit) from erasing credentials with the "***" placeholder.
func mergeMountParams(driverType string, existing, update config.ParamMap) config.ParamMap {
	merged := config.ParamMap{}
	for key, value := range existing {
		merged[key] = value
	}
	for key, value := range update {
		if value == maskedSecretValue && isSecretParam(driverType, key) {
			if _, ok := existing[key]; ok {
				continue // keep the previous secret value
			}
		}
		merged[key] = value
	}
	return merged
}

func isSecretParam(driverType, name string) bool {
	for _, param := range drive.ParamSchema(driverType) {
		if param.Name == name && param.Secret {
			return true
		}
	}
	return false
}

// Reload reopens the core from the current config file with the same runtime
// layout. The old filesystem and task manager are closed. Callers own the new
// core and must Close it.
func (c *Core) Reload(ctx context.Context) (*Core, error) {
	if c == nil || c.configPath == "" {
		return nil, fmt.Errorf("core: config path unavailable")
	}
	return Open(ctx, Options{ConfigPath: c.configPath, Runtime: c.runtimeLayout})
}
