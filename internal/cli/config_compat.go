package cli

import (
	"github.com/spf13/cobra"
	cliconfig "github.com/yinzhenyu/qrypt/internal/cli/config"
)

func newConfigCmd() *cobra.Command {
	return cliconfig.NewCommand(cliRuntime{})
}

func newConfigInitCmd() *cobra.Command {
	return cliconfig.NewInitCmd(cliRuntime{})
}

func newConfigShowCmd() *cobra.Command {
	return cliconfig.NewShowCmd(cliRuntime{})
}

func newConfigExportRclonePasswordCmd() *cobra.Command {
	return cliconfig.NewExportRclonePasswordCmd(cliRuntime{})
}

func generateConfigTemplate(starterRoot string) ([]byte, error) {
	return cliconfig.GenerateTemplate(starterRoot)
}

func maskLine(line string, secrets map[string]bool) string {
	return cliconfig.MaskLine(line, secrets)
}

func mask(s string) string {
	return cliconfig.Mask(s)
}

func isSectionHeader(line string) bool {
	return cliconfig.IsSectionHeader(line)
}

func directPasswordFromFlags(cmd *cobra.Command) (string, bool, error) {
	return cliconfig.DirectPasswordFromFlags(cmd)
}

func trimPasswordLine(raw []byte) string {
	return cliconfig.TrimPasswordLine(raw)
}

func exportDirect(password, salt, passwordHash string) (string, error) {
	return cliconfig.ExportDirect(password, salt, passwordHash)
}
