package main

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/yinzhenyu/qrypt/pkg/drive"
)

func newDriverCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "driver",
		Short: "List drivers and show parameter schemas",
		Long:  `Inspect available storage drivers and their parameter schemas.`,
	}
	cmd.AddCommand(newDriverListCmd())
	cmd.AddCommand(newDriverSchemaCmd())
	return cmd
}

func newDriverListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List registered storage drivers",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			for _, name := range drive.Names() {
				fmt.Println(name)
			}
			return nil
		},
	}
}

func newDriverSchemaCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "schema <name>",
		Short: "Show parameter schema for a driver",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			schema := drive.ParamSchema(name)
			if schema == nil {
				return fmt.Errorf("unknown driver: %s", name)
			}
			fmt.Printf("Driver: %s\n", name)
			fmt.Println()
			if len(schema) == 0 {
				fmt.Println("  No parameters required.")
				return nil
			}
			for _, p := range schema {
				req := ""
				if p.Required {
					req = " (required)"
				}
				secret := ""
				if p.Secret {
					secret = " [secret]"
				}
				fmt.Printf("  %s%s%s\n", p.Name, req, secret)
				if p.Type != "" {
					fmt.Printf("    Type: %s\n", p.Type)
				}
				if p.Description != "" {
					fmt.Printf("    %s\n", p.Description)
				}
				if p.Default != "" {
					fmt.Printf("    Default: %s\n", p.Default)
				}
				if p.Example != "" {
					fmt.Printf("    Example: %s\n", p.Example)
				}
				fmt.Println()
			}
			return nil
		},
	}
}
