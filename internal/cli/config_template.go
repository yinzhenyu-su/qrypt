package cli

import (
	"bytes"
	"fmt"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/yinzhenyu/qrypt/pkg/crypt"
	"github.com/yinzhenyu/qrypt/pkg/drive"
)

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
# content_dedup = false  # true enables deterministic encrypted content for rapid upload/dedup; leaks content equality

{{range .Drivers}}
# ── {{.Name}} ──────────────────────────────────────────────────────
{{.Example}}# [[mounts]]
# name = "{{.Name}}-example"
# type = "{{.Name}}"
# [mounts.params]
{{end}}
`
