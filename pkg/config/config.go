package config

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/yinzhenyu/qrypt/pkg/crypt"
	"github.com/yinzhenyu/qrypt/pkg/util"
)

type Config struct {
	Version            string          `toml:"version"`
	MountPoint         string          `toml:"mount_point"`
	VolumeName         string          `toml:"volume_name"`
	ReadOnly           bool            `toml:"read_only"`
	AllowOther         bool            `toml:"allow_other"`
	DefaultPermissions bool            `toml:"default_permissions"`
	NoAppleDouble      *bool           `toml:"no_apple_double"`
	NoAppleXattr       *bool           `toml:"no_apple_xattr"`
	AttrTimeout        string          `toml:"attr_timeout"`
	EntryTimeout       string          `toml:"entry_timeout"`
	NegativeTimeout    string          `toml:"negative_timeout"`
	TotalSpace         string          `toml:"total_space"`
	FreeSpace          string          `toml:"free_space"`
	Logging            LoggingConfig   `toml:"logging"`
	Debug              DebugConfig     `toml:"debug"`
	Time               TimeConfig      `toml:"time"`
	Bandwidth          BandwidthConfig `toml:"bandwidth"`
	Storage            StorageConfig   `toml:"storage"`
	ReadCache          ReadCacheConfig `toml:"read_cache"`
	ThumbnailCache     ReadCacheConfig `toml:"thumbnail_cache"`
	Upload             UploadConfig    `toml:"upload"`
	Encryption         crypt.Config    `toml:"encryption"`
	Defaults           Defaults        `toml:"defaults"`
	Mounts             []MountConfig   `toml:"mounts"`
}

type Defaults struct {
	Encryption crypt.Config `toml:"encryption"`
}

type MountConfig struct {
	Name        string           `toml:"name"`
	Type        string           `toml:"type"`
	TestEnabled bool             `toml:"test_enabled"`
	Params      ParamMap         `toml:"params"`
	Encryption  *crypt.Config    `toml:"encryption"`
	ReadCache   *ReadCacheConfig `toml:"read_cache"`
	Upload      *UploadConfig    `toml:"upload"`
}

type ParamMap map[string]string

func (p *ParamMap) UnmarshalTOML(data any) error {
	values, ok := data.(map[string]any)
	if !ok {
		return fmt.Errorf("params must be a table")
	}
	out := make(map[string]string, len(values))
	for key, value := range values {
		switch typed := value.(type) {
		case string:
			out[key] = typed
		case bool:
			out[key] = strconv.FormatBool(typed)
		case int64:
			out[key] = strconv.FormatInt(typed, 10)
		case int:
			out[key] = strconv.Itoa(typed)
		case float64:
			out[key] = strconv.FormatFloat(typed, 'f', -1, 64)
		default:
			return fmt.Errorf("params.%s must be string, bool, int, or float", key)
		}
	}
	*p = out
	return nil
}

type StorageConfig struct {
	ReadCacheDir      string `toml:"read_cache_dir"`
	ThumbnailCacheDir string `toml:"thumbnail_cache_dir"`
	UploadDir         string `toml:"upload_dir"`
	StateDir          string `toml:"state_dir"`
	LogDir            string `toml:"log_dir"`
	TmpDir            string `toml:"tmp_dir"`
}

type ReadCacheConfig struct {
	MaxSize string `toml:"max_size"`
}

type UploadConfig struct {
	UploadDelay   string `toml:"upload_delay"`
	UploadWorkers int    `toml:"upload_workers"`
	DeleteDelay   string `toml:"delete_delay"`
	DefaultMount  string `toml:"default_mount"`
	DefaultPath   string `toml:"default_path"`
}

// MaxSizeBytes parses MaxSize (e.g. "512M", "1G", "2T") and returns bytes.
// Returns 0 if MaxSize is empty or unparseable.
func (c ReadCacheConfig) MaxSizeBytes() int64 {
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

type DebugConfig struct {
	Enabled bool   `toml:"enabled"`
	Listen  string `toml:"listen"`
}

const DefaultDebugListen = "127.0.0.1:19090"

func (c DebugConfig) EffectiveListen() string {
	if c.Listen != "" {
		return c.Listen
	}
	return DefaultDebugListen
}

type TimeConfig struct {
	NTPEnabled      *bool    `toml:"ntp_enabled"`
	NTPServers      []string `toml:"ntp_servers"`
	NTPTimeout      string   `toml:"ntp_timeout"`
	NTPPollInterval string   `toml:"ntp_poll_interval"`
}

func (c TimeConfig) EffectiveNTPEnabled() bool {
	if c.NTPEnabled == nil {
		return true
	}
	return *c.NTPEnabled
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
	metadata, err := toml.Decode(string(data), &cfg)
	if err != nil {
		return nil, err
	}
	if undecoded := metadata.Undecoded(); len(undecoded) > 0 {
		keys := make([]string, 0, len(undecoded))
		for _, key := range undecoded {
			keys = append(keys, key.String())
		}
		return nil, fmt.Errorf("unknown configuration keys: %s", strings.Join(keys, ", "))
	}
	return &cfg, nil
}

var saveMu sync.Mutex

// Save writes cfg to path atomically: the data is written to a temporary
// file in the same directory, synced, and renamed over the target. A reader
// never observes a truncated or half-written config. Concurrent saves are
// serialized.
func Save(path string, cfg *Config) error {
	if cfg == nil {
		return fmt.Errorf("config: configuration is empty")
	}
	data, err := toml.Marshal(cfg)
	if err != nil {
		return err
	}
	saveMu.Lock()
	defer saveMu.Unlock()
	return util.WriteAtomicWithOptions(path, util.AtomicWriteOptions{
		Pattern:      filepath.Base(path) + ".tmp-*",
		Mode:         0o600,
		Replace:      true,
		CreateParent: true,
		ParentMode:   0o700,
		SyncFile:     true,
	}, func(file *os.File) error {
		_, err := file.Write(data)
		return err
	})
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

func (c *Config) ReadCacheFor(mountName string) ReadCacheConfig {
	var cache ReadCacheConfig
	if c == nil {
		return cache
	}
	cache = c.ReadCache
	for _, mount := range c.Mounts {
		if mount.Name == mountName && mount.ReadCache != nil {
			cache = *mount.ReadCache
			break
		}
	}
	return cache
}

func (c *Config) UploadFor(mountName string) UploadConfig {
	var upload UploadConfig
	if c == nil {
		return upload
	}
	upload = c.Upload
	for _, mount := range c.Mounts {
		if mount.Name == mountName && mount.Upload != nil {
			upload = *mount.Upload
			break
		}
	}
	return upload
}

func (c *Config) EffectiveMountPoint() string {
	if c == nil {
		return ""
	}
	if c.MountPoint != "" {
		return c.MountPoint
	}
	return ""
}

// EffectiveLogFile resolves logging.log_file. When log_file is unset it
// defaults to <storage.log_dir>/qrypt.log; when no log directory is
// configured either, it returns "" so callers keep stderr logging.
func (c *Config) EffectiveLogFile() string {
	if c == nil {
		return ""
	}
	if f := strings.TrimSpace(c.Logging.LogFile); f != "" {
		return f
	}
	if c.Storage.LogDir == "" {
		return ""
	}
	return filepath.Join(c.Storage.LogDir, "qrypt.log")
}

// EffectiveErrorFile resolves logging.error_file. When error_file is unset
// it defaults to <storage.log_dir>/qrypt-error.log; when no log directory
// is configured either, it returns "" so callers keep stderr logging.
func (c *Config) EffectiveErrorFile() string {
	if c == nil {
		return ""
	}
	if f := strings.TrimSpace(c.Logging.ErrorFile); f != "" {
		return f
	}
	if c.Storage.LogDir == "" {
		return ""
	}
	return filepath.Join(c.Storage.LogDir, "qrypt-error.log")
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

func (c *Config) EffectiveNoAppleXattr() bool {
	if c == nil || c.NoAppleXattr == nil {
		return false
	}
	return *c.NoAppleXattr
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
	if err != nil || number < 0 || math.IsNaN(number) {
		// ParseFloat accepts "NaN", which slips past the number < 0 check
		// (NaN compares false) and turns int64(NaN) into an unspecified
		// value at the call site.
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
