package config

import (
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/yinzhenyu/qrypt/pkg/crypt"
)

type Config struct {
	MountPoint    string          `toml:"mount_point"`
	CacheDir      string          `toml:"cache_dir"`
	VolumeName    string          `toml:"volume_name"`
	NoAppleDouble *bool           `toml:"no_apple_double"`
	TotalSpace    string          `toml:"total_space"`
	FreeSpace     string          `toml:"free_space"`
	Logging       LoggingConfig   `toml:"logging"`
	Bandwidth     BandwidthConfig `toml:"bandwidth"`
	Encryption    crypt.Config    `toml:"encryption"`
	Defaults      Defaults        `toml:"defaults"`
	Mounts        []MountConfig   `toml:"mounts"`
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
	Dir           string `toml:"dir"`
	MaxSize       string `toml:"max_size"`
	UploadDelay   string `toml:"upload_delay"`
	UploadWorkers int    `toml:"upload_workers"`
	DeleteDelay   string `toml:"delete_delay"`
}

// MaxSizeBytes parses MaxSize (e.g. "512M", "1G", "2T") and returns bytes.
// Returns 0 if MaxSize is empty or unparseable.
func (c CacheConfig) MaxSizeBytes() int64 {
	if c.MaxSize == "" {
		return 0
	}
	return ParseMaxSize(c.MaxSize)
}

// ParseMaxSize parses a human-readable size string (e.g. "512M", "1G", "2T")
// and returns the number of bytes. Returns 0 if empty or unparseable.
func ParseMaxSize(s string) int64 {
	n, _ := ParseSize(s)
	return n
}

type LoggingConfig struct {
	LogLevel  string `toml:"log_level"`
	LogFile   string `toml:"log_file"`
	ErrorFile string `toml:"error_file"`
}

type BandwidthConfig struct {
	Download string `toml:"download"`
	Upload   string `toml:"upload"`
}

type BandwidthLimits struct {
	DownloadBytesPerSecond int64
	UploadBytesPerSecond   int64
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

func (c *Config) EffectiveVolumeName() string {
	if c == nil || c.VolumeName == "" {
		return "Qrypt"
	}
	return c.VolumeName
}

func (c *Config) EffectiveNoAppleDouble() bool {
	if c == nil || c.NoAppleDouble == nil {
		return true
	}
	return *c.NoAppleDouble
}

func (c *Config) EffectiveSpaceBytes() (int64, int64, error) {
	if c == nil {
		return 0, 0, nil
	}
	total, err := ParseSize(c.TotalSpace)
	if err != nil {
		return 0, 0, fmt.Errorf("config: invalid total_space: %w", err)
	}
	free, err := ParseSize(c.FreeSpace)
	if err != nil {
		return 0, 0, fmt.Errorf("config: invalid free_space: %w", err)
	}
	return total, free, nil
}

func (c *Config) EffectiveBandwidthLimits() (BandwidthLimits, error) {
	if c == nil {
		return BandwidthLimits{}, nil
	}
	download, err := ParseSize(c.Bandwidth.Download)
	if err != nil {
		return BandwidthLimits{}, fmt.Errorf("config: invalid bandwidth.download: %w", err)
	}
	upload, err := ParseSize(c.Bandwidth.Upload)
	if err != nil {
		return BandwidthLimits{}, fmt.Errorf("config: invalid bandwidth.upload: %w", err)
	}
	return BandwidthLimits{
		DownloadBytesPerSecond: download,
		UploadBytesPerSecond:   upload,
	}, nil
}

func ParseSize(value string) (int64, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, nil
	}
	upper := strings.ToUpper(value)
	upper = strings.TrimSuffix(upper, "B")

	multiplier := int64(1)
	switch {
	case strings.HasSuffix(upper, "K"):
		multiplier = 1 << 10
		upper = strings.TrimSuffix(upper, "K")
	case strings.HasSuffix(upper, "M"):
		multiplier = 1 << 20
		upper = strings.TrimSuffix(upper, "M")
	case strings.HasSuffix(upper, "G"):
		multiplier = 1 << 30
		upper = strings.TrimSuffix(upper, "G")
	case strings.HasSuffix(upper, "T"):
		multiplier = 1 << 40
		upper = strings.TrimSuffix(upper, "T")
	case strings.HasSuffix(upper, "P"):
		multiplier = 1 << 50
		upper = strings.TrimSuffix(upper, "P")
	}

	number, err := strconv.ParseFloat(strings.TrimSpace(upper), 64)
	if err != nil || number < 0 {
		return 0, fmt.Errorf("size must be a non-negative number with optional K/M/G/T/P suffix")
	}
	bytes := number * float64(multiplier)
	if bytes > float64(math.MaxInt64) {
		return 0, fmt.Errorf("size is too large")
	}
	return int64(bytes), nil
}

// ParseDuration parses a duration string (e.g. "5s", "10m", "1h").  Returns 0
// for empty input and an error for negative durations.
func ParseDuration(s string) (time.Duration, error) {
	if strings.TrimSpace(s) == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, err
	}
	if d < 0 {
		return 0, fmt.Errorf("duration must be non-negative")
	}
	return d, nil
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
