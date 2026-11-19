package crypt

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

const (
	FileNameEncryptionStandard  = "standard"
	FileNameEncryptionOff       = "off"
	FileNameEncryptionObfuscate = "obfuscate"

	FileNameEncodingBase32 = "base32"
	FileNameEncodingBase64 = "base64"

	PasswordHashArgon2id = "argon2id"
)

// Config mirrors rclone crypt's relevant encryption settings.
type Config struct {
	Password           string `toml:"password"`
	Salt               string `toml:"salt"`
	PasswordObscured   bool   `toml:"password_obscured"`
	SaltObscured       bool   `toml:"salt_obscured"`
	PasswordHash       string `toml:"password_hash"`
	FileNameEncryption string `toml:"filename_encryption"`
	FileNameEncoding   string `toml:"filename_encoding"`
	ContentDedup       bool   `toml:"content_dedup"`
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
	switch c.PasswordHash {
	case "", PasswordHashArgon2id:
	default:
		return fmt.Errorf("crypt: unsupported password_hash %q", c.PasswordHash)
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

// NewRcloneCipherFromConfig builds the rclone-compatible cipher described by
// cfg, deriving the Argon2id pre-stretch with production cost.
func NewRcloneCipherFromConfig(cfg Config) (*RcloneCipher, error) {
	return newRcloneCipherFromConfigWithParams(cfg, DefaultArgon2Params())
}

// newRcloneCipherFromConfigWithParams is NewRcloneCipherFromConfig with explicit
// Argon2id cost, so tests can cover the configuration plumbing cheaply.
func newRcloneCipherFromConfigWithParams(cfg Config, params Argon2Params) (*RcloneCipher, error) {
	cfg = cfg.WithDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	var err error
	if cfg.PasswordObscured {
		cfg.Password, err = RevealRcloneConfigValue(cfg.Password)
		if err != nil {
			return nil, fmt.Errorf("crypt: reveal password: %w", err)
		}
	}
	if cfg.SaltObscured && cfg.Salt != "" {
		cfg.Salt, err = RevealRcloneConfigValue(cfg.Salt)
		if err != nil {
			return nil, fmt.Errorf("crypt: reveal salt: %w", err)
		}
	}
	return newRcloneCipher(cfg.Password, cfg.Salt, params, cfg.FileNameEncoding, cfg.FileNameEncryption, cfg.PasswordHash)
}

// GenerateSalt produces a random 16-byte hex-encoded salt for use in
// new encryption configurations.
func GenerateSalt() (string, error) {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf[:]), nil
}

// ExportRclonePassword returns the password that rclone must use to decrypt
// files encrypted by this Config. When PasswordHash is "argon2id", the
// returned value is the hex-encoded Argon2id-derived key; otherwise it is
// the raw password (unchanged, rclone-compatible as-is). Argon2id always uses
// production cost: the output is an interoperability contract with rclone.
func ExportRclonePassword(cfg Config) (string, error) {
	return exportRclonePasswordWithParams(cfg, DefaultArgon2Params())
}

// exportRclonePasswordWithParams is ExportRclonePassword with explicit Argon2id
// cost, so tests can cover the argument handling without paying for a
// production derivation.
func exportRclonePasswordWithParams(cfg Config, params Argon2Params) (string, error) {
	cfg = cfg.WithDefaults()
	if cfg.Password == "" {
		return "", fmt.Errorf("crypt: password is empty")
	}
	if cfg.PasswordObscured {
		var err error
		cfg.Password, err = RevealRcloneConfigValue(cfg.Password)
		if err != nil {
			return "", fmt.Errorf("crypt: reveal password: %w", err)
		}
	}
	if cfg.PasswordHash == PasswordHashArgon2id {
		return argon2idStretchWith(params, cfg.Password, cfg.Salt), nil
	}
	return cfg.Password, nil
}
