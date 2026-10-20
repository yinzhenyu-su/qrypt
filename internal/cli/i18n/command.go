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
}

func localizeCommandTree(cmd *cobra.Command, loc *Localizer) {
	key := CommandKey(cmd)
	localizeField := func(field string, target *string) {
		if *target == "" {
			return
		}
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
		catalogKey := "flag." + key + "." + flag.Name + ".usage"
		if text := loc.T(catalogKey); text != catalogKey {
			flag.Usage = text
		}
	})

	for _, child := range cmd.Commands() {
		localizeCommandTree(child, loc)
	}
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
