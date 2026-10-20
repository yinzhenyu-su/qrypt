package cli

import (
	"io"
	"sort"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/yinzhenyu/qrypt/internal/cli/i18n"
	cliruntime "github.com/yinzhenyu/qrypt/internal/cli/runtime"
)

// commandSchema is the machine-readable form of one command's help. It is
// derived from the command tree, never from the rendered help text, so wording
// changes cannot break consumers.
type commandSchema struct {
	Name        string          `json:"name"`
	Path        string          `json:"path"`
	Summary     string          `json:"summary"`
	Description string          `json:"description,omitempty"`
	Usage       string          `json:"usage"`
	Example     string          `json:"example,omitempty"`
	Aliases     []string        `json:"aliases,omitempty"`
	Commands    []commandSchema `json:"commands,omitempty"`
	Flags       []flagSchema    `json:"flags,omitempty"`
	Advanced    []flagSchema    `json:"advanced_flags,omitempty"`
}

type flagSchema struct {
	Name      string `json:"name"`
	Shorthand string `json:"shorthand,omitempty"`
	Type      string `json:"type"`
	Usage     string `json:"usage"`
	Default   string `json:"default,omitempty"`
}

// NewSchemaCommand exposes a command's help as structured JSON so scripts and
// clients can build help without parsing rendered text.
func NewSchemaCommand(root *cobra.Command) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "schema [COMMAND]",
		Short: "Print a command's help as structured JSON",
		Long: `Print the command tree — subcommands, arguments, and flags — as JSON.

Scripts, editors, and GUIs can read this instead of parsing the rendered help,
so the machine-readable surface stays stable while help wording changes.`,
		Example: `  qrypt schema
  qrypt schema offline fetch`,
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			target := root
			if len(args) > 0 {
				found, _, err := root.Find(args)
				if err != nil || found == nil {
					return cmd.Usage()
				}
				target = found
			}
			return writeCommandSchema(cmd.OutOrStdout(), target)
		},
	}
	cmd.ValidArgsFunction = cliruntime.NoFileCompletions
	return cmd
}

// writeCommandSchema renders one command's structured help.
func writeCommandSchema(out io.Writer, cmd *cobra.Command) error {
	return cliruntime.WritePrettyJSON(out, schemaFor(cmd))
}

func englishText(loc *i18n.Localizer, key, fallback string) string {
	if text := loc.T(key); text != key {
		return text
	}
	return fallback
}

func schemaFor(cmd *cobra.Command) commandSchema {
	english := i18n.New(i18n.LangEN)
	key := i18n.CommandKey(cmd)
	schema := commandSchema{
		Name:        cmd.Name(),
		Path:        cmd.CommandPath(),
		Summary:     englishText(english, "cmd."+key+".short", cmd.Short),
		Description: englishText(english, "cmd."+key+".long", cmd.Long),
		Usage:       cmd.UseLine(),
		Example:     englishText(english, "cmd."+key+".example", cmd.Example),
		Aliases:     append([]string(nil), cmd.Aliases...),
	}
	for _, child := range cmd.Commands() {
		if !child.IsAvailableCommand() {
			continue
		}
		schema.Commands = append(schema.Commands, schemaFor(child))
	}
	sort.Slice(schema.Commands, func(a, b int) bool { return schema.Commands[a].Name < schema.Commands[b].Name })
	cmd.Flags().VisitAll(func(flag *pflag.Flag) {
		entry := flagSchema{
			Name:      flag.Name,
			Shorthand: flag.Shorthand,
			Type:      flag.Value.Type(),
			Usage:     englishText(english, "flag."+key+"."+flag.Name+".usage", flag.Usage),
			Default:   flag.DefValue,
		}
		if cliruntime.IsAdvancedFlag(flag) {
			schema.Advanced = append(schema.Advanced, entry)
			return
		}
		if flag.Hidden {
			return
		}
		schema.Flags = append(schema.Flags, entry)
	})
	sort.Slice(schema.Flags, func(a, b int) bool { return schema.Flags[a].Name < schema.Flags[b].Name })
	sort.Slice(schema.Advanced, func(a, b int) bool { return schema.Advanced[a].Name < schema.Advanced[b].Name })
	return schema
}
