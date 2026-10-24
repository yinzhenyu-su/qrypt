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
	seenCredentials := make(map[string]bool)
	for index, cred := range cfg.Credentials {
		if cred.Name != "" && seenCredentials[cred.Name] {
			return fmt.Errorf("config: duplicate credential name %q", cred.Name)
		}
		if err := validateCredentialDefinition(index, cred); err != nil {
			return err
		}
		seenCredentials[cred.Name] = true
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
	if cfg == nil {
		cfg = &Config{}
	}
	if !drive.Registered(mountCfg.Type) {
		return fmt.Errorf("config: mount %q has unknown driver %q", mountCfg.Name, mountCfg.Type)
	}
	spec := drive.ParamSpecFor(mountCfg.Type)
	label := fmt.Sprintf("mount %q", mountCfg.Name)
	if mountCfg.Credential != "" {
		cred, ok := cfg.CredentialFor(mountCfg.Credential)
		if !ok {
			return fmt.Errorf("config: mount %q references unknown credential %q", mountCfg.Name, mountCfg.Credential)
		}
		if cred.Type != mountCfg.Type {
			return fmt.Errorf("config: mount %q type %q does not match credential %q type %q",
				mountCfg.Name, mountCfg.Type, cred.Name, cred.Type)
		}
	}
	params, err := cfg.ResolveMountParams(mountCfg)
	if err != nil {
		return err
	}
	if err := validateRequirements(label, spec, params); err != nil {
		return err
	}
	if mountCfg.Credential != "" {
		if err := validateParamNames(label, spec, mountCfg.Params, drive.ParamDef.AllowsMount); err != nil {
			return err
		}
	} else if err := validateParamNames(label, spec, mountCfg.Params, func(drive.ParamDef) bool { return true }); err != nil {
		// Legacy mounts carry their credential inline as an implicit
		// credential, so the scope split does not apply to them yet.
		return err
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

// validateParamNames rejects unknown driver parameters and parameters used in a
// place their declared scope forbids.
func validateParamNames(label string, spec drive.ParamSpec, params ParamMap, allow func(drive.ParamDef) bool) error {
	known := make(map[string]drive.ParamDef, len(spec.Params))
	for _, param := range spec.Params {
		known[param.Name] = param
	}
	for name := range params {
		param, ok := known[name]
		if !ok {
			return fmt.Errorf("config: %s has unknown parameter %q", label, name)
		}
		if !allow(param) {
			return fmt.Errorf("config: %s parameter %q has scope %q and cannot be configured there", label, name, param.Scope)
		}
	}
	return nil
}

func validateRequirements(label string, spec drive.ParamSpec, params drive.Params) error {
	for _, name := range spec.Required {
		if strings.TrimSpace(params[name]) == "" {
			return fmt.Errorf("config: %s missing required parameter %q", label, name)
		}
	}
	if len(spec.AnyOf) > 0 {
		satisfied := false
		for _, group := range spec.AnyOf {
			complete := true
			for _, name := range group {
				if strings.TrimSpace(params[name]) == "" {
					complete = false
					break
				}
			}
			if complete {
				satisfied = true
				break
			}
		}
		if !satisfied {
			return fmt.Errorf("config: %s must satisfy one of the alternative parameter groups", label)
		}
	}
	return nil
}

// validateCredentialDefinition checks one [[credentials]] entry in isolation so
// validation does not depend on which mounts happen to reference it.
func validateCredentialDefinition(index int, cred CredentialConfig) error {
	label := fmt.Sprintf("credential %q", cred.Name)
	if cred.Name == "" {
		return fmt.Errorf("config: credentials[%d].name is required", index)
	}
	if cred.Name == "." || cred.Name == ".." || strings.ContainsAny(cred.Name, `/\`) {
		return fmt.Errorf("config: credentials[%d].name %q must be a single path component", index, cred.Name)
	}
	if cred.Type == "" {
		return fmt.Errorf("config: credential %q type is required", cred.Name)
	}
	if !drive.Registered(cred.Type) {
		return fmt.Errorf("config: credential %q has unknown driver %q", cred.Name, cred.Type)
	}
	spec := drive.ParamSpecFor(cred.Type)
	if err := validateParamNames(label, spec, cred.Params, drive.ParamDef.AllowsCredential); err != nil {
		return err
	}
	return validateCredentialRequirements(label, spec, cred.Params)
}

// validateCredentialRequirements checks only the credential-scope parts of the
// driver's requirements. Params with "both" scope may be supplied by a mount,
// so the merged params are validated on the mount instead.
func validateCredentialRequirements(label string, spec drive.ParamSpec, params ParamMap) error {
	scopes := make(map[string]string, len(spec.Params))
	for _, param := range spec.Params {
		scopes[param.Name] = param.Scope
	}
	for _, name := range spec.Required {
		if scopes[name] != drive.ParamScopeCredential {
			continue
		}
		if strings.TrimSpace(params[name]) == "" {
			return fmt.Errorf("config: %s missing required parameter %q", label, name)
		}
	}
	credentialGroup := false
	for _, group := range spec.AnyOf {
		for _, name := range group {
			if scopes[name] == drive.ParamScopeCredential {
				credentialGroup = true
				break
			}
		}
		if credentialGroup {
			break
		}
	}
	if !credentialGroup {
		return nil
	}
	for _, group := range spec.AnyOf {
		complete := true
		member := false
		for _, name := range group {
			if scopes[name] != drive.ParamScopeCredential {
				continue
			}
			member = true
			if strings.TrimSpace(params[name]) == "" {
				complete = false
				break
			}
		}
		if member && complete {
			return nil
		}
	}
	return fmt.Errorf("config: %s must satisfy one of the alternative parameter groups", label)
}
