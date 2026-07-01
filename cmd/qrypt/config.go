package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

const configTemplate = `# qrypt configuration

mount_point = "~/Qrypt"
cache_dir = "~/.cache/qrypt"
volume_name = "Qrypt"
no_apple_double = true
no_apple_xattr = true

[logging]
log_level = "info"
log_file = "~/.qrypt/qrypt.log"
error_file = "~/.qrypt/qrypt-error.log"

# Global bandwidth limits (optional). Supports K/M/G/T (B) suffixes.
# [bandwidth]
# download = "50M"
# upload = "10M"

# Global encryption defaults (optional). Applies to all mounts that do not
# specify their own [mounts.encryption].
# [encryption]
# password = "global-secret"
# salt = ""
# filename_encryption = "standard"
# filename_encoding = "base32"

[[mounts]]
name = "local"
type = "localfs"

[mounts.params]
root = "/tmp/qrypt-remote"
`

func newConfigCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Create and inspect configuration",
		Long:  `Manage qrypt TOML configuration files.`,
	}
	cmd.AddCommand(newConfigInitCmd())
	cmd.AddCommand(newConfigShowCmd())
	return cmd
}

func newConfigInitCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Write a sample configuration file",
		RunE: func(cmd *cobra.Command, args []string) error {
			force, _ := cmd.Flags().GetBool("force")
			path, _ := cmd.Flags().GetString("path")
			if path == "" {
				path = "qrypt.toml"
			}
			if _, err := os.Stat(path); err == nil && !force {
				return fmt.Errorf("%s already exists (use --force to overwrite)", path)
			}
			return os.WriteFile(path, []byte(generateConfigTemplate(cmd)), 0o644)
		},
	}
	cmd.Flags().Bool("force", false, "overwrite existing file")
	cmd.Flags().String("path", "", "output path (default qrypt.toml)")
	return cmd
}

func newConfigShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show",
		Short: "Display current configuration with secrets masked",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if configPath == "" {
				return fmt.Errorf("usage: qrypt --config FILE config show")
			}
			data, err := os.ReadFile(configPath)
			if err != nil {
				return err
			}
			lines := strings.Split(string(data), "\n")
			for _, line := range lines {
				fmt.Println(maskLine(line))
			}
			return nil
		},
	}
}

func generateConfigTemplate(cmd *cobra.Command) string {
	drivers := strings.Fields(renderDriverExample())
	_ = drivers
	return configTemplate + "\n" + renderDriverExample()
}

func renderDriverExample() string {
	return `# Other driver examples (uncomment to use):

# [[mounts]]
# name = "aliyun"
# type = "aliyundrive"
# [mounts.params]
# refresh_token = "your-refresh-token"
# drive_id = "your-drive-id"
# root_id = "root"

# [[mounts]]
# name = "baidu"
# type = "baidu_netdisk"
# [mounts.params]
# refresh_token = "your-refresh-token"

# [[mounts]]
# name = "quark"
# type = "quark"
# [mounts.params]
# cookie = "your-cookie"

# [[mounts]]
# name = "yun139"
# type = "yun139"
# [mounts.params]
# authorization = "your-authorization"

# [[mounts]]
# name = "p115"
# type = "115"
# [mounts.params]
# cookie = "your-cookie"

# [[mounts]]
# name = "webdav"
# type = "webdav"
# [mounts.params]
# url = "https://example.com/dav"
# username = "user"
# password = "pass"
`
}

func maskLine(line string) string {
	trimmed := strings.TrimSpace(line)
	if strings.HasPrefix(trimmed, "#") {
		return line
	}
	for _, secretField := range []string{"password", "refresh_token", "cookie", "authorization"} {
		if strings.Contains(trimmed, secretField+" =") || strings.Contains(trimmed, secretField+"=") {
			parts := strings.SplitN(line, "=", 2)
			if len(parts) == 2 {
				return parts[0] + `= "***"`
			}
		}
	}
	return line
}

func mask(s string) string {
	if s == "" {
		return ""
	}
	n := len(s)
	if n <= 4 {
		return strings.Repeat("*", n)
	}
	return s[:2] + strings.Repeat("*", n-4) + s[n-2:]
}
