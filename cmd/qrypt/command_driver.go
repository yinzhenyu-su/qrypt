package main

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/spf13/cobra"
	"github.com/yinzhenyu/qrypt/pkg/drive"
)

func newDriverCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "driver",
		Short: "List drivers and show parameter schemas",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}
	cmd.AddCommand(newDriverListCmd())
	cmd.AddCommand(newDriverSchemaCmd())
	return cmd
}

func newDriverListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Args:  cobra.NoArgs,
		Short: "List available drivers",
		RunE: func(cmd *cobra.Command, args []string) error {
			asJSON, _ := cmd.Flags().GetBool("json")
			if asJSON {
				return writeJSON(cmd.OutOrStdout(), drive.Names())
			}
			for _, name := range drive.Names() {
				fmt.Fprintln(cmd.OutOrStdout(), name)
			}
			return nil
		},
	}
	cmd.Flags().Bool("json", false, "write JSON output")
	return cmd
}

func newDriverSchemaCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "schema NAME",
		Args:  cobra.ExactArgs(1),
		Short: "Show driver parameters",
		ValidArgsFunction: cobra.FixedCompletions(
			drive.Names(),
			cobra.ShellCompDirectiveNoFileComp,
		),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			schema := drive.ParamSchema(name)
			if !drive.Registered(name) {
				return fmt.Errorf("unknown driver %q", name)
			}
			asJSON, _ := cmd.Flags().GetBool("json")
			if asJSON {
				if schema == nil {
					schema = []drive.ParamDef{}
				}
				return writeJSON(cmd.OutOrStdout(), struct {
					Name       string           `json:"name"`
					Parameters []drive.ParamDef `json:"parameters"`
				}{Name: name, Parameters: schema})
			}
			if len(schema) == 0 {
				fmt.Fprintf(cmd.OutOrStdout(), "Driver: %s (no parameters required)\n", name)
				return nil
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Driver: %s\n\n", name)
			for _, p := range schema {
				req := ""
				if p.Required {
					req = " (required)"
				}
				secret := ""
				if p.Secret {
					secret = " [secret]"
				}
				fmt.Fprintf(cmd.OutOrStdout(), "  %s%s%s\n", p.Name, req, secret)
				if p.Type != "" {
					fmt.Fprintf(cmd.OutOrStdout(), "    Type: %s\n", p.Type)
				}
				if p.Description != "" {
					fmt.Fprintf(cmd.OutOrStdout(), "    %s\n", p.Description)
				}
				if p.Default != "" {
					fmt.Fprintf(cmd.OutOrStdout(), "    Default: %s\n", p.Default)
				}
				if p.Example != "" {
					fmt.Fprintf(cmd.OutOrStdout(), "    Example: %s\n", p.Example)
				}
				fmt.Fprintln(cmd.OutOrStdout())
			}
			return nil
		},
	}
	cmd.Flags().Bool("json", false, "write JSON output")
	return cmd
}

func writeJSON(w io.Writer, value any) error {
	encoder := json.NewEncoder(w)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}
