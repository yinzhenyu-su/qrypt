package cli

import (
	"github.com/spf13/cobra"
	"github.com/yinzhenyu/qrypt/internal/cli/i18n"
)

// newCompletionCommand replaces Cobra's generated completion command. The
// generated command captures the root output stream when it is created, which
// breaks command output redirection; building it here also lets the catalog
// localize the built-in help pages.
func newCompletionCommand() *cobra.Command {
	english := i18n.New(i18n.LangEN)
	cmd := &cobra.Command{
		Use:     "completion",
		Short:   english.T("cmd.completion.short"),
		Long:    english.T("cmd.completion.long"),
		Example: english.T("cmd.completion.example"),
		Args:    cobra.NoArgs,
	}
	for _, shell := range []string{"bash", "zsh", "fish", "powershell"} {
		cmd.AddCommand(newCompletionShellCommand(shell, english))
	}
	return cmd
}

func newCompletionShellCommand(shell string, english *i18n.Localizer) *cobra.Command {
	cmd := &cobra.Command{
		Use:                   shell,
		Short:                 english.T("cmd.completion." + shell + ".short"),
		Long:                  english.T("cmd.completion." + shell + ".long"),
		Example:               english.T("cmd.completion." + shell + ".example"),
		Args:                  cobra.NoArgs,
		DisableFlagsInUseLine: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			noDescriptions, _ := cmd.Flags().GetBool("no-descriptions")
			out := cmd.OutOrStdout()
			switch shell {
			case "bash":
				return cmd.Root().GenBashCompletionV2(out, !noDescriptions)
			case "zsh":
				if noDescriptions {
					return cmd.Root().GenZshCompletionNoDesc(out)
				}
				return cmd.Root().GenZshCompletion(out)
			case "fish":
				return cmd.Root().GenFishCompletion(out, !noDescriptions)
			case "powershell":
				if noDescriptions {
					return cmd.Root().GenPowerShellCompletion(out)
				}
				return cmd.Root().GenPowerShellCompletionWithDesc(out)
			default:
				return nil
			}
		},
	}
	cmd.Flags().Bool("no-descriptions", false, english.T("flag.completion."+shell+".no-descriptions.usage"))
	return cmd
}
