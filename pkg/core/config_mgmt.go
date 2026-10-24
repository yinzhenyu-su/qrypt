package core

import (
	"context"
	"fmt"

	"github.com/yinzhenyu/qrypt/pkg/config"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/logging"
)

// ConfigSummary is a settings-UI friendly view of the current qrypt.toml.
// Secret driver parameters are masked.
type ConfigSummary struct {
	ConfigPath     string                    `json:"config_path,omitempty"`
	Version        string                    `json:"version,omitempty"`
	Mounts         []ConfigMountSummary      `json:"mounts"`
	Credentials    []ConfigCredentialSummary `json:"credentials,omitempty"`
	ReadCache      ReadCacheSummary          `json:"read_cache"`
	ThumbnailCache ReadCacheSummary          `json:"thumbnail_cache"`
	Upload         UploadSummary             `json:"upload"`
}

type ConfigMountSummary struct {
	Name       string            `json:"name"`
	Type       string            `json:"type"`
	Credential string            `json:"credential,omitempty"`
	Params     map[string]string `json:"params,omitempty"`
	Encrypted  bool              `json:"encrypted"`
}

// ConfigCredentialSummary describes a shared credential. Secret params are
// masked exactly like mount params.
type ConfigCredentialSummary struct {
	Name   string            `json:"name"`
	Type   string            `json:"type"`
	Params map[string]string `json:"params,omitempty"`
	UsedBy []string          `json:"used_by,omitempty"`
}

type ReadCacheSummary struct {
	MaxSize string `json:"max_size,omitempty"`
}

// UploadSummary carries upload settings. Pointer fields follow field-level
// merge semantics in ApplyConfigUpdate: nil means "leave unchanged", so a
// partial update never zeroes unmentioned settings.
type UploadSummary struct {
	UploadDelay   *string `json:"upload_delay,omitempty"`
	UploadWorkers *int    `json:"upload_workers,omitempty"`
	DeleteDelay   *string `json:"delete_delay,omitempty"`
	DefaultMount  *string `json:"default_mount,omitempty"`
	DefaultPath   *string `json:"default_path,omitempty"`
}

// ConfigMountUpdate mutates one mount entry. Action is one of
// "add", "update", or "remove". add/update accept Type and Params; remove
// only needs Name. Credential, when non-nil, sets or clears the credential
// reference.
type ConfigMountUpdate struct {
	Action     string            `json:"action"`
	Name       string            `json:"name"`
	Type       string            `json:"type,omitempty"`
	Credential *string           `json:"credential,omitempty"`
	Params     map[string]string `json:"params,omitempty"`
}

// ConfigCredentialUpdate mutates one [[credentials]] entry. Action is one of
// "add", "update", or "remove". Removing a credential still referenced by a
// mount is rejected.
type ConfigCredentialUpdate struct {
	Action string            `json:"action"`
	Name   string            `json:"name"`
	Type   string            `json:"type,omitempty"`
	Params map[string]string `json:"params,omitempty"`
}

// ConfigUpdateRequest applies mount and credential changes and optional global
// settings. Changes are validated before the config file is written, so a
// failed update leaves the previous config untouched.
type ConfigUpdateRequest struct {
	Mounts      []ConfigMountUpdate      `json:"mounts,omitempty"`
	Credentials []ConfigCredentialUpdate `json:"credentials,omitempty"`
	ReadCache   *ReadCacheSummary        `json:"read_cache,omitempty"`
	Upload      *UploadSummary           `json:"upload,omitempty"`
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
			UploadDelay:   &cfg.Upload.UploadDelay,
			UploadWorkers: &cfg.Upload.UploadWorkers,
			DeleteDelay:   &cfg.Upload.DeleteDelay,
			DefaultMount:  &cfg.Upload.DefaultMount,
			DefaultPath:   &cfg.Upload.DefaultPath,
		},
	}
	for _, mount := range cfg.Mounts {
		encrypted := cfg.EncryptionFor(mount.Name).Password != ""
		summary.Mounts = append(summary.Mounts, ConfigMountSummary{
			Name:       mount.Name,
			Type:       mount.Type,
			Credential: mount.Credential,
			Params:     maskParams(mount.Type, mount.Params),
			Encrypted:  encrypted,
		})
	}
	for _, cred := range cfg.Credentials {
		usedBy := []string{}
		for _, mount := range cfg.Mounts {
			if mount.Credential == cred.Name {
				usedBy = append(usedBy, mount.Name)
			}
		}
		summary.Credentials = append(summary.Credentials, ConfigCredentialSummary{
			Name:   cred.Name,
			Type:   cred.Type,
			Params: maskParams(cred.Type, cred.Params),
			UsedBy: usedBy,
		})
	}
	return summary
}

// maskParams returns a copy of params with every secret driver parameter
// replaced by the mask placeholder.
func maskParams(driverType string, params config.ParamMap) map[string]string {
	masked := map[string]string{}
	for key, value := range params {
		masked[key] = value
	}
	for _, param := range drive.ParamSchema(driverType) {
		if param.Secret {
			if _, ok := masked[param.Name]; ok {
				masked[param.Name] = maskedSecretValue
			}
		}
	}
	return masked
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
	for _, update := range req.Credentials {
		switch update.Action {
		case "remove":
			if err := removeConfigCredential(cfg, update.Name); err != nil {
				return err
			}
		case "add", "update":
			if err := upsertConfigCredential(cfg, update); err != nil {
				return err
			}
		default:
			return fmt.Errorf("core: unsupported credential action %q", update.Action)
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
		if req.Upload.UploadDelay != nil {
			cfg.Upload.UploadDelay = *req.Upload.UploadDelay
		}
		if req.Upload.UploadWorkers != nil {
			cfg.Upload.UploadWorkers = *req.Upload.UploadWorkers
		}
		if req.Upload.DeleteDelay != nil {
			cfg.Upload.DeleteDelay = *req.Upload.DeleteDelay
		}
		if req.Upload.DefaultMount != nil {
			cfg.Upload.DefaultMount = *req.Upload.DefaultMount
		}
		if req.Upload.DefaultPath != nil {
			cfg.Upload.DefaultPath = *req.Upload.DefaultPath
		}
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
			if update.Credential != nil {
				cfg.Mounts[i].Credential = *update.Credential
			}
			return nil
		}
	}
	if update.Action == "update" {
		return fmt.Errorf("core: mount %q not found", update.Name)
	}
	mount := config.MountConfig{
		Name:   update.Name,
		Type:   update.Type,
		Params: config.ParamMap(update.Params),
	}
	if update.Credential != nil {
		mount.Credential = *update.Credential
	}
	cfg.Mounts = append(cfg.Mounts, mount)
	return nil
}

func removeConfigCredential(cfg *config.Config, name string) error {
	if name == "" {
		return fmt.Errorf("core: credential name required")
	}
	for _, mount := range cfg.Mounts {
		if mount.Credential == name {
			return fmt.Errorf("core: credential %q is still referenced by mount %q", name, mount.Name)
		}
	}
	for i, cred := range cfg.Credentials {
		if cred.Name == name {
			cfg.Credentials = append(cfg.Credentials[:i], cfg.Credentials[i+1:]...)
			return nil
		}
	}
	return fmt.Errorf("core: credential %q not found", name)
}

func upsertConfigCredential(cfg *config.Config, update ConfigCredentialUpdate) error {
	if update.Name == "" {
		return fmt.Errorf("core: credential name required")
	}
	if update.Type == "" {
		return fmt.Errorf("core: credential %q requires a driver type", update.Name)
	}
	for i, cred := range cfg.Credentials {
		if cred.Name == update.Name {
			cfg.Credentials[i].Type = update.Type
			cfg.Credentials[i].Params = mergeMountParams(cred.Type, cred.Params, update.Params)
			return nil
		}
	}
	if update.Action == "update" {
		return fmt.Errorf("core: credential %q not found", update.Name)
	}
	cfg.Credentials = append(cfg.Credentials, config.CredentialConfig{
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
// layout. The old core's filesystem, task manager, caches, and debug server
// are closed once the replacement is ready; callers own the new core and
// must Close it. If the config fails to load, the old core keeps running and
// an error is returned.
func (c *Core) Reload(ctx context.Context) (*Core, error) {
	if c == nil || c.configPath == "" {
		return nil, fmt.Errorf("core: config path unavailable")
	}
	newCore, err := Open(ctx, Options{ConfigPath: c.configPath, Runtime: c.runtimeLayout})
	if err != nil {
		return nil, err
	}
	if err := c.Close(ctx); err != nil {
		logging.L.Warnf("[CORE] reload: close old core: %v", err)
	}
	return newCore, nil
}
