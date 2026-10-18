package cli

import (
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	cliruntime "github.com/yinzhenyu/qrypt/internal/cli/runtime"
)

// advancedHelpFlag is the hidden persistent flag that reveals advanced flags in
// a command's help. It is a no-op outside help, so it never changes behavior.
const advancedHelpFlag = "advanced"

// installDeepHelp adds the hidden --advanced flag and wraps the root help
// function so the deep-help form reveals advanced flags for that render only.
// Subcommands inherit the wrapped function from the root.
func installDeepHelp(root *cobra.Command) {
	root.PersistentFlags().Bool(advancedHelpFlag, false, "show advanced flags in help")
	_ = root.PersistentFlags().MarkHidden(advancedHelpFlag)
	base := root.HelpFunc()
	root.SetHelpFunc(func(cmd *cobra.Command, args []string) {
		restore := func() {}
		if deep, _ := cmd.Flags().GetBool(advancedHelpFlag); deep {
			restore = revealAdvancedFlags(cmd)
		}
		defer restore()
		base(cmd, args)
	})
}

// revealAdvancedFlags unhides a command's advanced flags and returns a function
// that hides them again, so deep help does not leak into later renders.
func revealAdvancedFlags(cmd *cobra.Command) func() {
	revealed := make([]*pflag.Flag, 0, 4)
	cmd.Flags().VisitAll(func(flag *pflag.Flag) {
		if cliruntime.IsAdvancedFlag(flag) && flag.Hidden {
			flag.Hidden = false
			revealed = append(revealed, flag)
		}
	})
	return func() {
		for _, flag := range revealed {
			flag.Hidden = true
		}
	}
}
