package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newConfigPathCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "path",
		Short: "Print the resolved config file path",
		Long: `Print which config file would be used: an explicit --config PATH wins,
otherwise the first existing file in the search order
(./qrypt.toml, ~/.qrypt/qrypt.toml, platform config dir).`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			configPath, err := commandConfigPath(cmd)
			if err != nil {
				return err
			}
			if configPath == "" {
				return configNotFoundError()
			}
			asJSON, _ := cmd.Flags().GetBool("json")
			if asJSON {
				return writePrettyJSON(cmd.OutOrStdout(), struct {
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
