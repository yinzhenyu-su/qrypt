package i18n

import (
	"strings"

	"github.com/spf13/cobra"
)

// defaultUsageTemplate mirrors Cobra v1.10.2's default usage template. Cobra
// does not export it, so the localized variant starts from a copy and replaces
// only the section labels.
const defaultUsageTemplate = `Usage:{{if .Runnable}}
  {{.UseLine}}{{end}}{{if .HasAvailableSubCommands}}
  {{.CommandPath}} [command]{{end}}{{if gt (len .Aliases) 0}}

Aliases:
  {{.NameAndAliases}}{{end}}{{if .HasExample}}

Examples:
{{.Example}}{{end}}{{if .HasAvailableSubCommands}}{{$cmds := .Commands}}{{if eq (len .Groups) 0}}

Available Commands:{{range $cmds}}{{if (or .IsAvailableCommand (eq .Name "help"))}}
  {{rpad .Name .NamePadding }} {{.Short}}{{end}}{{end}}{{else}}{{range $group := .Groups}}

{{.Title}}{{range $cmds}}{{if (and (eq .GroupID $group.ID) (or .IsAvailableCommand (eq .Name "help")))}}
  {{rpad .Name .NamePadding }} {{.Short}}{{end}}{{end}}{{end}}{{if not .AllChildCommandsHaveGroup}}

Additional Commands:{{range $cmds}}{{if (and (eq .GroupID "") (or .IsAvailableCommand (eq .Name "help")))}}
  {{rpad .Name .NamePadding }} {{.Short}}{{end}}{{end}}{{end}}{{end}}{{end}}{{if .HasAvailableLocalFlags}}

Flags:
{{.LocalFlags.FlagUsages | trimTrailingWhitespaces}}{{end}}{{if .HasAvailableInheritedFlags}}

Global Flags:
{{.InheritedFlags.FlagUsages | trimTrailingWhitespaces}}{{end}}{{if .HasHelpSubCommands}}

Additional help topics:{{range .Commands}}{{if .IsAdditionalHelpTopicCommand}}
  {{rpad .CommandPath .CommandPathPadding}} {{.Short}}{{end}}{{end}}{{end}}{{if .HasAvailableSubCommands}}

Use "{{.CommandPath}} [command] --help" for more information about a command.{{end}}
`

// LocalizeUsageTemplate installs a localized Cobra usage template for non-English
// languages. English keeps Cobra's default so its rendered help stays unchanged.
func LocalizeUsageTemplate(root *cobra.Command) {
	if root == nil {
		return
	}
	loc := FromCommand(root)
	if loc.Lang() == LangEN {
		return
	}
	root.SetUsageTemplate(localizedUsageTemplate(loc))
}

func localizedUsageTemplate(loc *Localizer) string {
	template := defaultUsageTemplate
	replacements := []struct {
		english string
		key     string
	}{
		{"Global Flags:", "help.global_flags"},
		{"Flags:", "help.flags"},
		{"Available Commands:", "help.available_commands"},
		{"Additional Commands:", "help.additional_commands"},
		{"Additional help topics:", "help.additional_topics"},
		{"Aliases:", "help.aliases"},
		{"Examples:", "help.examples"},
		{"Usage:", "help.usage"},
	}
	for _, replacement := range replacements {
		template = strings.ReplaceAll(template, replacement.english, loc.T(replacement.key))
	}
	template = strings.ReplaceAll(
		template,
		`Use "{{.CommandPath}} [command] --help" for more information about a command.`,
		loc.T("help.more_info", "{{.CommandPath}}"),
	)
	return template
}
