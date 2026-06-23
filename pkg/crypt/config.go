package crypt

import "fmt"

const (
	FileNameEncryptionStandard  = "standard"
	FileNameEncryptionOff       = "off"
	FileNameEncryptionObfuscate = "obfuscate"

	FileNameEncodingBase32 = "base32"
	FileNameEncodingBase64 = "base64"
)

// Config mirrors rclone crypt's relevant encryption settings.
type Config struct {
	Password           string `toml:"password"`
	Salt               string `toml:"salt"`
	FileNameEncryption string `toml:"filename_encryption"`
	FileNameEncoding   string `toml:"filename_encoding"`
}

func (c Config) WithDefaults() Config {
	if c.FileNameEncryption == "" {
		c.FileNameEncryption = FileNameEncryptionStandard
	}
	if c.FileNameEncoding == "" {
		c.FileNameEncoding = FileNameEncodingBase32
	}
	return c
}

func (c Config) Validate() error {
	c = c.WithDefaults()
	if c.Password == "" {
		return fmt.Errorf("crypt: encryption password required")
	}
	switch c.FileNameEncryption {
	case FileNameEncryptionStandard, FileNameEncryptionOff, FileNameEncryptionObfuscate:
	default:
		return fmt.Errorf("crypt: unsupported filename_encryption %q", c.FileNameEncryption)
	}
	switch c.FileNameEncoding {
	case FileNameEncodingBase32, FileNameEncodingBase64:
	default:
		return fmt.Errorf("crypt: unsupported filename_encoding %q", c.FileNameEncoding)
	}
	return nil
}

func NewRcloneCipherFromConfig(cfg Config) (*RcloneCipher, error) {
	cfg = cfg.WithDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return NewRcloneCipher(cfg.Password, cfg.Salt, cfg.FileNameEncoding, cfg.FileNameEncryption)
}
