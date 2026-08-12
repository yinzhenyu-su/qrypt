package config

import (
	"fmt"

	"github.com/spf13/cobra"
	cliruntime "github.com/yinzhenyu/qrypt/internal/cli/runtime"
)

func NewPathCmd(rt cliruntime.Runtime) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "path",
		Short: "Print the resolved config file path",
		Long: `Print which config file would be used: an explicit --config PATH wins,
otherwise the first existing file in the search order
(./qrypt.toml, ~/.qrypt/qrypt.toml, platform config dir).`,
		Args: cliruntime.NoArgs(rt),
		RunE: func(cmd *cobra.Command, args []string) error {
			configPath, err := rt.CommandConfigPath(cmd)
			if err != nil {
				return err
			}
			if configPath == "" {
				return rt.ConfigNotFoundError()
			}
			asJSON, _ := cmd.Flags().GetBool("json")
			if asJSON {
				return cliruntime.WritePrettyJSON(cmd.OutOrStdout(), struct {
					Path string `json:"path"`
				}{Path: configPath})
			}
			fmt.Fprintln(cmd.OutOrStdout(), configPath)
			return nil
		},
	}
	cmd.Flags().StringP("config", "c", "", "config file path (auto-discovered when omitted)")
	cmd.Flags().Bool("json", false, "write JSON output")
	return cmd
}
