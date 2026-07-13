package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/spf13/cobra"
	"github.com/yinzhenyu/qrypt/internal/config"
	"github.com/yinzhenyu/qrypt/pkg/drive"
)

func newConfigShowCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show",
		Short: "Show config with secrets masked",
		Args:  noArgs,
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
				trimmed := strings.TrimSpace(line)
				if trimmed == "" || strings.HasPrefix(trimmed, "#") {
					continue
				}
				if isSectionHeader(trimmed) && len(masked) > 0 && masked[len(masked)-1] != "" {
					masked = append(masked, "")
				}
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
	return "******"
}

func isSectionHeader(line string) bool {
	return strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]")
}
