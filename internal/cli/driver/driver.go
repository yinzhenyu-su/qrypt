package driver

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/yinzhenyu/qrypt/internal/cli/i18n"
	cliruntime "github.com/yinzhenyu/qrypt/internal/cli/runtime"
	"github.com/yinzhenyu/qrypt/pkg/drive"
)

func NewCommand(rt cliruntime.ErrorFactory) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "driver",
		Short: "List drivers and show parameter schemas",
		Long: `Inspect the storage drivers built into this binary and the parameters each
one accepts, without reading a config file or contacting a remote.`,
		Example: `  qrypt driver list
  qrypt driver schema quark`,
		Args: cliruntime.CommandGroupArgs(rt, nil),
		RunE: cliruntime.ShowHelp,
	}
	cmd.AddCommand(NewListCmd(rt))
	cmd.AddCommand(NewSchemaCmd(rt))
	return cmd
}

func NewListCmd(rt cliruntime.ErrorFactory) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Args:  cliruntime.NoArgs(rt),
		Short: "List available drivers",
		Long: `List the names of every storage driver built into this binary, one per
line, or as JSON with --json.`,
		Example: `  qrypt driver list
  qrypt driver list --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			asJSON, _ := cmd.Flags().GetBool("json")
			if asJSON {
				return cliruntime.WritePrettyJSON(cmd.OutOrStdout(), drive.Names())
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

func NewSchemaCmd(rt cliruntime.ErrorFactory) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "schema NAME",
		Args:  cliruntime.ExactNamedArgs(rt, "NAME"),
		Short: "Show driver parameters",
		Long: `Show the parameters a driver accepts, marking required and secret ones,
or emit the same schema as JSON with --json.`,
		Example: `  qrypt driver schema quark
  qrypt driver schema s3 --json`,
		ValidArgsFunction: cobra.FixedCompletions(
			drive.Names(),
			cobra.ShellCompDirectiveNoFileComp,
		),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if !drive.Registered(name) {
				return fmt.Errorf("unknown driver %q (run 'qrypt driver list' to see available drivers)", name)
			}
			spec := drive.ParamSpecFor(name)
			schema := spec.Params
			asJSON, _ := cmd.Flags().GetBool("json")
			if asJSON {
				if schema == nil {
					schema = []drive.ParamDef{}
				}
				return cliruntime.WritePrettyJSON(cmd.OutOrStdout(), struct {
					Name       string           `json:"name"`
					Parameters []drive.ParamDef `json:"parameters"`
					Required   []string         `json:"required,omitempty"`
					AnyOf      [][]string       `json:"any_of,omitempty"`
				}{Name: name, Parameters: schema, Required: spec.Required, AnyOf: spec.AnyOf})
			}
			if len(schema) == 0 {
				fmt.Fprintf(cmd.OutOrStdout(), "Driver: %s (no parameters required)\n", name)
				return nil
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Driver: %s\n\n", name)
			lang := i18n.FromCommand(cmd).Lang()
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
				if description := p.DescriptionFor(lang); description != "" {
					fmt.Fprintf(cmd.OutOrStdout(), "    %s\n", description)
				}
				if p.Default != "" {
					fmt.Fprintf(cmd.OutOrStdout(), "    Default: %s\n", p.Default)
				}
				if p.Example != "" {
					fmt.Fprintf(cmd.OutOrStdout(), "    Example: %s\n", p.Example)
				}
				fmt.Fprintln(cmd.OutOrStdout())
			}
			if len(spec.AnyOf) > 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "Alternative requirements (satisfy one group):")
				for _, group := range spec.AnyOf {
					fmt.Fprintf(cmd.OutOrStdout(), "  %s\n", strings.Join(group, " + "))
				}
			}
			return nil
		},
	}
	cmd.Flags().Bool("json", false, "write JSON output")
	return cmd
}
