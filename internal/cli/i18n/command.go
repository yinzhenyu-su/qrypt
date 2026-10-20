package i18n

import (
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// LocalizeCommandTree replaces human-facing command metadata with catalog
// entries derived from the command path and flag names.
func LocalizeCommandTree(root *cobra.Command) {
	localizeCommandTree(root, FromCommand(root))
	LocalizeUsageTemplate(root)
}

// InitDefaultFlags adds Cobra's generated help flag to every command and the
// root version flag before localization runs. Cobra otherwise adds them during
// Execute, after command metadata has already been replaced.
func InitDefaultFlags(root *cobra.Command) {
	if root == nil {
		return
	}
	var walk func(*cobra.Command)
	walk = func(cmd *cobra.Command) {
		cmd.InitDefaultHelpFlag()
		for _, child := range cmd.Commands() {
			walk(child)
		}
	}
	walk(root)
	root.InitDefaultVersionFlag()
}

func localizeCommandTree(cmd *cobra.Command, loc *Localizer) {
	key := CommandKey(cmd)
	localizeField := func(field string, target *string) {
		if *target == "" {
			return
		}
		snapshotCommandField(cmd, field, *target)
		catalogKey := "cmd." + key + "." + field
		if text := loc.T(catalogKey); text != catalogKey {
			*target = text
		}
	}
	localizeField("short", &cmd.Short)
	localizeField("long", &cmd.Long)
	localizeField("example", &cmd.Example)

	cmd.LocalFlags().VisitAll(func(flag *pflag.Flag) {
		if flag.Usage == "" {
			return
		}
		snapshotFlagUsage(flag)
		catalogKey := flagUsageKey(key, flag.Name)
		// Generated flags keep Cobra's English wording for English output;
		// their catalog entries exist for other languages and coverage checks.
		if loc.Lang() == LangEN && isGeneratedUsageFlag(flag.Name) {
			return
		}
		if text := loc.T(catalogKey); text != catalogKey {
			flag.Usage = text
		}
	})

	for _, child := range cmd.Commands() {
		localizeCommandTree(child, loc)
	}
}

func flagUsageKey(commandKey, flagName string) string {
	switch flagName {
	case "help", "version":
		return "flag." + flagName + ".usage"
	default:
		return "flag." + commandKey + "." + flagName + ".usage"
	}
}

func isGeneratedUsageFlag(flagName string) bool {
	return flagName == "help" || flagName == "version"
}

// CommandKey returns the catalog key segment for a command path.
func CommandKey(cmd *cobra.Command) string {
	if cmd == nil {
		return "root"
	}
	path := strings.Fields(cmd.CommandPath())
	if len(path) <= 1 {
		return "root"
	}
	return strings.Join(path[1:], ".")
}
