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

func englishCommandText(english *i18n.Localizer, cmd *cobra.Command, field string) string {
	key := "cmd." + i18n.CommandKey(cmd) + "." + field
	if text := english.T(key); text != key {
		return text
	}
	if snapshot, ok := i18n.EnglishCommandField(cmd, field); ok {
		return snapshot
	}
	switch field {
	case "short":
		return cmd.Short
	case "long":
		return cmd.Long
	case "example":
		return cmd.Example
	default:
		return ""
	}
}

func englishFlagUsage(english *i18n.Localizer, cmd *cobra.Command, flag *pflag.Flag) string {
	key := "flag." + i18n.CommandKey(cmd) + "." + flag.Name + ".usage"
	if text := english.T(key); text != key {
		return text
	}
	if snapshot, ok := i18n.EnglishFlagUsage(flag); ok {
		return snapshot
	}
	return flag.Usage
}

func schemaFor(cmd *cobra.Command) commandSchema {
	english := i18n.New(i18n.LangEN)
	schema := commandSchema{
		Name:        cmd.Name(),
		Path:        cmd.CommandPath(),
		Summary:     englishCommandText(english, cmd, "short"),
		Description: englishCommandText(english, cmd, "long"),
		Usage:       cmd.UseLine(),
		Example:     englishCommandText(english, cmd, "example"),
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
		// Generated flags are an implementation detail of help rendering and
		// were not part of the structured command surface before they became
		// localizable. Keep the machine-readable shape stable.
		if flag.Name == "help" || flag.Name == "version" {
			return
		}
		entry := flagSchema{
			Name:      flag.Name,
			Shorthand: flag.Shorthand,
			Type:      flag.Value.Type(),
			Usage:     englishFlagUsage(english, cmd, flag),
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
