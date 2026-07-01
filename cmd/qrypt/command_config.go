package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/BurntSushi/toml"
	"github.com/spf13/cobra"
	"github.com/yinzhenyu/qrypt/internal/config"
	"github.com/yinzhenyu/qrypt/pkg/drive"
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
	return cmd
}

func newConfigInitCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Write a starter config",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			force, _ := cmd.Flags().GetBool("force")
			outPath, _ := cmd.Flags().GetString("path")

			if outPath == "" {
				outPath = configPath
			}
			if outPath == "" {
				outPath = "./qrypt.toml"
			}

			if _, err := os.Stat(outPath); err == nil && !force {
				return fmt.Errorf("%s already exists (use --force to overwrite)", outPath)
			}

			content, err := generateConfigTemplate()
			if err != nil {
				return err
			}

			if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(outPath, content, 0o644); err != nil {
				return err
			}

			fmt.Printf("Wrote config to %s\n", outPath)
			return nil
		},
	}
	cmd.Flags().Bool("force", false, "overwrite existing file")
	cmd.Flags().String("path", "", "output path (default: -config value or ./qrypt.toml)")
	return cmd
}

func newConfigShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show",
		Short: "Show config with secrets masked",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if configPath == "" {
				return fmt.Errorf("no config file specified (use -config)")
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

			fmt.Println(strings.Join(masked, "\n"))
			return nil
		},
	}
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

func generateConfigTemplate() ([]byte, error) {
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

	tmpl, err := template.New("config").Parse(configTemplate)
	if err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, map[string]any{
		"Drivers": drivers,
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
cache_dir = "~/.qrypt/qrypt-cache"

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

{{range .Drivers}}
# ── {{.Name}} ──────────────────────────────────────────────────────
{{.Example}}# [[mounts]]
# name = "{{.Name}}-example"
# type = "{{.Name}}"
# [mounts.params]
{{end}}
`
