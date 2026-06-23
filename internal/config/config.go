package config

import (
	"os"

	"github.com/BurntSushi/toml"
	"github.com/yinzhenyu/qrypt/pkg/crypt"
)

type Config struct {
	MountPoint string        `toml:"mount_point"`
	CacheDir   string        `toml:"cache_dir"`
	Encryption crypt.Config  `toml:"encryption"`
	Defaults   Defaults      `toml:"defaults"`
	Mounts     []MountConfig `toml:"mounts"`
}

type Defaults struct {
	Encryption crypt.Config `toml:"encryption"`
	Cache      CacheConfig  `toml:"cache"`
}

type MountConfig struct {
	Name string `toml:"name"`
	Type string `toml:"type"`
	// MountPoint is deprecated. Use Config.MountPoint because qrypt has one
	// OS mount point whose root contains all named driver directories.
	MountPoint string            `toml:"mount_point"`
	Params     map[string]string `toml:"params"`
	Encryption *crypt.Config     `toml:"encryption"`
	Cache      *CacheConfig      `toml:"cache"`
}

type CacheConfig struct {
	Dir          string `toml:"dir"`
	MaxSizeBytes int64  `toml:"max_size_bytes"`
}

type EncryptionOverrides struct {
	Password           *string
	Salt               *string
	FileNameEncryption *string
	FileNameEncoding   *string
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg Config
	if _, err := toml.Decode(string(data), &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// EncryptionFor returns encryption config for one mount.
// Precedence: [[mounts]].encryption > [encryption] > [defaults.encryption].
// When mountName is empty and no global/default encryption exists, it falls
// back to the first mount encryption for single-mount compatibility.
// CLI overrides are applied later by ApplyEncryptionOverrides.
func (c *Config) EncryptionFor(mountName string) crypt.Config {
	var cfg crypt.Config
	if c == nil {
		return cfg
	}
	cfg = c.Defaults.Encryption
	if c.Encryption != (crypt.Config{}) {
		cfg = c.Encryption
	}
	if mountName != "" {
		for _, mount := range c.Mounts {
			if mount.Name == mountName && mount.Encryption != nil {
				cfg = *mount.Encryption
				break
			}
		}
	} else if c.Encryption == (crypt.Config{}) && c.Defaults.Encryption == (crypt.Config{}) {
		for _, mount := range c.Mounts {
			if mount.Encryption != nil {
				cfg = *mount.Encryption
				break
			}
		}
	}
	return cfg.WithDefaults()
}

func (c *Config) CacheFor(mountName string) CacheConfig {
	var cache CacheConfig
	if c == nil {
		return cache
	}
	cache = c.Defaults.Cache
	for _, mount := range c.Mounts {
		if mount.Name == mountName && mount.Cache != nil {
			cache = *mount.Cache
			break
		}
	}
	return cache
}

func (c *Config) EffectiveMountPoint() string {
	if c == nil {
		return ""
	}
	if c.MountPoint != "" {
		return c.MountPoint
	}
	for _, mount := range c.Mounts {
		if mount.MountPoint != "" {
			return mount.MountPoint
		}
	}
	return ""
}

func ApplyEncryptionOverrides(cfg crypt.Config, overrides EncryptionOverrides) crypt.Config {
	if overrides.Password != nil {
		cfg.Password = *overrides.Password
	}
	if overrides.Salt != nil {
		cfg.Salt = *overrides.Salt
	}
	if overrides.FileNameEncryption != nil {
		cfg.FileNameEncryption = *overrides.FileNameEncryption
	}
	if overrides.FileNameEncoding != nil {
		cfg.FileNameEncoding = *overrides.FileNameEncoding
	}
	return cfg.WithDefaults()
}
