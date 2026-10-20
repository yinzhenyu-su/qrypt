package config

import (
	"bytes"
	"fmt"
	"strings"
	"text/template"

	"github.com/yinzhenyu/qrypt/internal/cli/i18n"
	"github.com/yinzhenyu/qrypt/pkg/crypt"
	"github.com/yinzhenyu/qrypt/pkg/drive"
)

func GenerateTemplate(starterRoot string) ([]byte, error) {
	return GenerateTemplateLocalized(starterRoot, i18n.New(i18n.LangEN))
}

func GenerateTemplateLocalized(starterRoot string, loc *i18n.Localizer) ([]byte, error) {
	type driverExample struct {
		Name    string
		Params  []drive.ParamDef
		Example string
	}
	var drivers []driverExample
	for _, name := range drive.Names() {
		schema := drive.ParamSchema(name)
		de := driverExample{Name: name, Params: schema}
		de.Example = renderDriverExample(schema, loc)
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
	}); err != nil {
		return nil, err
	}
	content := localizeConfigSections(buf.String(), loc)
	return []byte(content), nil
}

var configSectionLabels = []struct {
	english string
	key     string
}{
	{"# ── Mount ", "config.section.mount"},
	{"# ── Logging ", "config.section.logging"},
	{"# ── Runtime storage root ", "config.section.storage"},
	{"# ── Time source (NTP first, system clock fallback) ", "config.section.time"},
	{"# ── Read cache settings ", "config.section.read_cache"},
	{"# ── Thumbnail cache settings ", "config.section.thumbnail_cache"},
	{"# ── Upload settings ", "config.section.upload"},
	{"# ── Bandwidth limits (empty = unlimited) ", "config.section.bandwidth"},
	{"# ── Starter local filesystem mount ", "config.section.starter_mount"},
	{"# ── Encryption (uncomment to enable) ", "config.section.encryption"},
}

func localizeConfigSections(content string, loc *i18n.Localizer) string {
	for _, label := range configSectionLabels {
		content = strings.ReplaceAll(content, label.english, "# ── "+loc.T(label.key)+" ")
	}
	return content
}

func renderDriverExample(params []drive.ParamDef, loc *i18n.Localizer) string {
	if len(params) == 0 {
		return "#   " + loc.T("config.section.no_params") + "\n"
	}
	var b strings.Builder
	for _, p := range params {
		secret := ""
		if p.Secret {
			secret = " [secret]"
		}
		fmt.Fprintf(&b, "#   %s%s: %s\n", p.Name, secret, p.DescriptionFor(loc.Lang()))
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
		fmt.Fprintf(&b, "#   %s = %s\n", p.Name, val)
	}
	return b.String()
}

const configTemplate = `#:schema ./schema/qrypt.schema.json
version = "1"

# ── Mount ────────────────────────────────────────────────────────
[mount]
mount_point = "~/Qrypt"
volume_name = "Qrypt"
# read_only = false
attr_timeout = "1s"
entry_timeout = "1s"
negative_timeout = "0s"

[mount.options]
darwin = ["defer_permissions", "auto_xattr", "iosize=4194304"]

# ── Logging ───────────────────────────────────────────────────────
[logging]
log_level = "info"
# log_file  = "~/.qrypt/logs/qrypt.log"
# error_file = "~/.qrypt/logs/qrypt-error.log"

# ── Runtime storage root ─────────────────────────────────────────
[storage]
work_dir = "~/.qrypt"

# ── Time source (NTP first, system clock fallback) ────────────────
[time]
ntp_enabled = true
# ntp_servers = ["ntp1.aliyun.com:123", "ntp2.aliyun.com:123", "ntp1.tencent.com:123", "ntp2.tencent.com:123", "ntp1.ntsc.ac.cn:123", "ntp2.ntsc.ac.cn:123", "ntp1.cstnet.cn:123", "0.cn.pool.ntp.org:123", "time.cloudflare.com:123", "time.google.com:123"]
ntp_timeout = "1500ms"
ntp_poll_interval = "30m"

# ── Read cache settings ───────────────────────────────────────────
[read_cache]
max_size = "2G"

# ── Thumbnail cache settings ─────────────────────────────────────
[thumbnail_cache]
max_size = "256M"

# ── Upload settings ────────────────────────────────────────────
[upload]
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
# content_dedup = false  # true enables deterministic encrypted content for instant upload/dedup; leaks content equality

{{range .Drivers}}
# ── {{.Name}} ──────────────────────────────────────────────────────
{{.Example}}# [[mounts]]
# name = "{{.Name}}-example"
# type = "{{.Name}}"
# [mounts.params]
{{end}}
`
