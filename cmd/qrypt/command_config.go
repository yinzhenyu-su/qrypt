package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/BurntSushi/toml"
	"github.com/spf13/cobra"
	"github.com/yinzhenyu/qrypt/internal/config"
	"github.com/yinzhenyu/qrypt/pkg/crypt"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/osutil"
)

func newConfigCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Create and inspect configuration",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}
	cmd.AddCommand(newConfigInitCmd())
	cmd.AddCommand(newConfigShowCmd())
	cmd.AddCommand(newConfigValidateCmd())
	cmd.AddCommand(newConfigExportRclonePasswordCmd())
	return cmd
}

func newConfigValidateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "validate",
		Short: "Validate configuration without connecting to remote drives",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			configPath, err := commandConfigPath(cmd)
			if err != nil {
				return err
			}
			if configPath == "" {
				return configNotFoundError()
			}
			cfg, err := config.Load(configPath)
			if err != nil {
				return fmt.Errorf("load config %q: %w", configPath, err)
			}
			if err := validateConfig(cfg); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Config valid: %s\n", configPath)
			return nil
		},
	}
	withConfigFlag(cmd)
	return cmd
}

func newConfigInitCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "init [PATH]",
		Short: "Write a starter config",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			force, _ := cmd.Flags().GetBool("force")
			outPath := "./qrypt.toml"
			if len(args) == 1 {
				outPath = args[0]
			}

			outPath = osutil.ExpandHome(outPath)
			absoluteOutPath, err := filepath.Abs(outPath)
			if err != nil {
				return err
			}
			starterRoot := filepath.Join(filepath.Dir(absoluteOutPath), "qrypt-data")

			if _, err := os.Stat(outPath); err == nil && !force {
				return fmt.Errorf("%s already exists (use --force to overwrite)", outPath)
			}

			content, err := generateConfigTemplate(starterRoot)
			if err != nil {
				return err
			}

			if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
				return err
			}
			if err := os.MkdirAll(starterRoot, 0o755); err != nil {
				return err
			}
			if err := writeConfigFile(outPath, content, force); err != nil {
				return err
			}

			fmt.Fprintf(cmd.ErrOrStderr(), "Wrote config to %s\n", outPath)
			fmt.Fprintf(cmd.ErrOrStderr(), "Created local storage at %s\n", starterRoot)
			return nil
		},
	}
	cmd.Flags().BoolP("force", "f", false, "overwrite existing file")
	return cmd
}

func writeConfigFile(path string, content []byte, force bool) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".qrypt-config-*.toml")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if force {
		return replaceLocalFile(tmpPath, path)
	}
	return os.Rename(tmpPath, path)
}

func newConfigExportRclonePasswordCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "export-rclone-password [MOUNT_NAME]",
		Short: "Print the rclone-compatible password for a mount",
		Long: `Print the password rclone needs to decrypt files encrypted by this config.

If password_hash is "argon2id", the Argon2id-derived key is printed as a hex string.
Otherwise, the raw password is printed unchanged.

Use with a config file (reads encryption settings from the named mount):
  rclone config update myremote password=$(qrypt config export-rclone-password mymount)

Or compute directly from raw inputs (no config file needed):
  qrypt config export-rclone-password --password-file ./password.txt --salt "mysalt"`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rawPassword, direct, err := directPasswordFromFlags(cmd)
			if err != nil {
				return err
			}
			rawSalt, _ := cmd.Flags().GetString("salt")
			passwordHash, _ := cmd.Flags().GetString("password-hash")

			if direct {
				if len(args) != 0 {
					return fmt.Errorf("MOUNT_NAME cannot be used with a direct password source")
				}
				if cmd.Flags().Changed("config") {
					return fmt.Errorf("--config cannot be used with a direct password source")
				}
				switch passwordHash {
				case "argon2id":
				case "none":
					passwordHash = ""
				default:
					return fmt.Errorf("--password-hash must be argon2id or none")
				}
				pw, err := exportDirect(rawPassword, rawSalt, passwordHash)
				if err != nil {
					return err
				}
				fmt.Fprintln(cmd.OutOrStdout(), pw)
				return nil
			}

			configPath, err := commandConfigPath(cmd)
			if err != nil {
				return err
			}
			if configPath == "" {
				return fmt.Errorf("%w; alternatively use --password-file or --password-stdin", configNotFoundError())
			}
			cfg, err := config.Load(configPath)
			if err != nil {
				return err
			}
			if len(args) == 0 && len(cfg.Mounts) > 1 {
				return fmt.Errorf("MOUNT_NAME is required when the config contains multiple mounts")
			}
			mountName := ""
			if len(args) == 1 {
				mountName = args[0]
				found := false
				for _, mount := range cfg.Mounts {
					if mount.Name == mountName {
						found = true
						break
					}
				}
				if !found {
					return fmt.Errorf("mount %q not found in config", mountName)
				}
			}
			enc := cfg.EncryptionFor(mountName)
			pw, err := crypt.ExportRclonePassword(enc)
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), pw)
			return nil
		},
	}
	withConfigFlag(cmd)
	cmd.Flags().String("password", "", "raw password (visible in shell history; overrides config)")
	cmd.Flags().String("password-file", "", "read raw password from a file")
	cmd.Flags().Bool("password-stdin", false, "read raw password from standard input")
	cmd.Flags().String("salt", "", "salt for key derivation (used with a direct password source)")
	cmd.Flags().String("password-hash", crypt.PasswordHashArgon2id, "password hash mode: argon2id or none")
	return cmd
}

func directPasswordFromFlags(cmd *cobra.Command) (string, bool, error) {
	password, _ := cmd.Flags().GetString("password")
	passwordFile, _ := cmd.Flags().GetString("password-file")
	passwordStdin, _ := cmd.Flags().GetBool("password-stdin")

	sources := 0
	for _, name := range []string{"password", "password-file", "password-stdin"} {
		if cmd.Flags().Changed(name) {
			sources++
		}
	}
	if sources > 1 {
		return "", false, fmt.Errorf("use only one of --password, --password-file, or --password-stdin")
	}
	if sources == 0 {
		if cmd.Flags().Changed("salt") || cmd.Flags().Changed("password-hash") {
			return "", false, fmt.Errorf("--salt and --password-hash require a direct password source")
		}
		return "", false, nil
	}
	if passwordFile != "" {
		raw, err := os.ReadFile(osutil.ExpandHome(passwordFile))
		if err != nil {
			return "", false, fmt.Errorf("read password file: %w", err)
		}
		password = trimPasswordLine(raw)
	}
	if passwordStdin {
		raw, err := io.ReadAll(cmd.InOrStdin())
		if err != nil {
			return "", false, fmt.Errorf("read password from stdin: %w", err)
		}
		password = trimPasswordLine(raw)
	}
	return password, true, nil
}

func trimPasswordLine(raw []byte) string {
	return strings.TrimSuffix(strings.TrimSuffix(string(raw), "\n"), "\r")
}

func exportDirect(password, salt, passwordHash string) (string, error) {
	cfg := crypt.Config{
		Password:     password,
		Salt:         salt,
		PasswordHash: passwordHash,
	}
	cfg = cfg.WithDefaults()
	if err := cfg.Validate(); err != nil {
		return "", err
	}
	pw, err := crypt.ExportRclonePassword(cfg)
	if err != nil {
		return "", err
	}
	return pw, nil
}

func newConfigShowCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show",
		Short: "Show config with secrets masked",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			configPath, err := commandConfigPath(cmd)
			if err != nil {
				return err
			}
			if configPath == "" {
				return configNotFoundError()
			}
			raw, err := os.ReadFile(configPath)
			if err != nil {
				return fmt.Errorf("read config: %w", err)
			}

			var cfg config.Config
			if err := toml.Unmarshal(raw, &cfg); err != nil {
				return fmt.Errorf("parse config: %w", err)
			}

			knownSecrets := map[string]bool{"password": true, "salt": true}
			for _, name := range drive.Names() {
				for _, p := range drive.ParamSchema(name) {
					if p.Secret {
						knownSecrets[p.Name] = true
					}
				}
			}

			lines := strings.Split(string(raw), "\n")
			masked := make([]string, 0, len(lines))
			for _, line := range lines {
				masked = append(masked, maskLine(line, knownSecrets))
			}

			fmt.Fprintln(cmd.OutOrStdout(), strings.Join(masked, "\n"))
			return nil
		},
	}
	withConfigFlag(cmd)
	return cmd
}

func maskLine(line string, secrets map[string]bool) string {
	line = strings.TrimRight(line, " \t\r\n")
	before, after, ok := strings.Cut(line, "=")
	if !ok {
		return line
	}
	key := strings.TrimSpace(before)
	if !secrets[key] {
		return line
	}
	val := strings.TrimSpace(after)
	val = strings.Trim(val, `"'`)
	if val == "" {
		return key + ` = ""`
	}
	return key + ` = "` + mask(val) + `"`
}

func mask(s string) string {
	if len(s) <= 4 {
		return strings.Repeat("*", len(s))
	}
	return s[:2] + strings.Repeat("*", len(s)-4) + s[len(s)-2:]
}

func generateConfigTemplate(starterRoot string) ([]byte, error) {
	type driverExample struct {
		Name    string
		Params  []drive.ParamDef
		Example string
	}
	var drivers []driverExample
	for _, name := range drive.Names() {
		schema := drive.ParamSchema(name)
		de := driverExample{Name: name, Params: schema}
		de.Example = renderDriverExample(name, schema)
		drivers = append(drivers, de)
	}

	encryptionSalt, err := crypt.GenerateSalt()
	if err != nil {
		return nil, fmt.Errorf("generate salt: %w", err)
	}

	tmpl, err := template.New("config").Parse(configTemplate)
	if err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, map[string]any{
		"Drivers":        drivers,
		"EncryptionSalt": encryptionSalt,
		"StarterRoot":    starterRoot,
		"StarterCache":   filepath.Join(filepath.Dir(starterRoot), "qrypt-cache"),
	}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func renderDriverExample(name string, params []drive.ParamDef) string {
	if len(params) == 0 {
		return "#   no parameters required\n"
	}
	var b strings.Builder
	for _, p := range params {
		secret := ""
		if p.Secret {
			secret = " [secret]"
		}
		b.WriteString(fmt.Sprintf("#   %s%s: %s\n", p.Name, secret, p.Description))
	}
	b.WriteString("#   [mounts.params]\n")
	for _, p := range params {
		val := p.Example
		if val == "" {
			val = p.Default
		}
		if val == "" {
			val = `""`
		} else if !strings.HasPrefix(val, `"`) {
			val = fmt.Sprintf("%q", val)
		}
		if p.Secret && val != `""` {
			if len(val) > 4 {
				val = val[:3] + strings.Repeat("*", len(val)-5) + val[len(val)-2:]
			}
		}
		b.WriteString(fmt.Sprintf("#   %s = %s\n", p.Name, val))
	}
	return b.String()
}

const configTemplate = `#:schema ./qrypt.schema.json
version = "1"

# FUSE mount point (expanded with ~)
mount_point = "~/Qrypt"

# Global cache directory
cache_dir = {{printf "%q" .StarterCache}}

# Volume name shown in the OS file manager
volume_name = "Qrypt"

# ── FUSE mount options ────────────────────────────────────────────
# read_only       = false
# allow_other     = false
no_apple_double  = true
no_apple_xattr   = true
attr_timeout     = "1s"
entry_timeout    = "1s"
negative_timeout = "0s"

# ── Logging ───────────────────────────────────────────────────────
[logging]
log_level = "info"
# log_file  = "~/.qrypt/qrypt.log"
# error_file = "~/.qrypt/qrypt-error.log"

# ── Time source (NTP first, system clock fallback) ────────────────
[time]
ntp_enabled = true
# ntp_servers = ["ntp1.aliyun.com:123", "ntp2.aliyun.com:123", "ntp1.tencent.com:123", "ntp2.tencent.com:123", "ntp1.ntsc.ac.cn:123", "ntp2.ntsc.ac.cn:123", "ntp1.cstnet.cn:123", "0.cn.pool.ntp.org:123", "time.cloudflare.com:123", "time.google.com:123"]
ntp_timeout = "1500ms"
ntp_poll_interval = "30m"

# ── Default cache settings (applied to all mounts) ────────────────
[defaults.cache]
max_size       = "2G"
upload_workers = 8
upload_delay   = "3s"
delete_delay   = "2s"

# ── Bandwidth limits (empty = unlimited) ─────────────────────────
[bandwidth]
download = ""
upload   = ""

# ── Starter local filesystem mount ───────────────────────────────
[[mounts]]
name = "local"
type = "localfs"

[mounts.params]
root_path = {{printf "%q" .StarterRoot}}

# ── Encryption (uncomment to enable) ────────────────────────────
# [encryption]
# password = "your-password"
# salt = "{{.EncryptionSalt}}"
# password_hash = "argon2id"
# filename_encryption = "standard"
# filename_encoding = "base32"

{{range .Drivers}}
# ── {{.Name}} ──────────────────────────────────────────────────────
{{.Example}}# [[mounts]]
# name = "{{.Name}}-example"
# type = "{{.Name}}"
# [mounts.params]
{{end}}
`
