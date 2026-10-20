// Package uninstall implements `qrypt uninstall`, which reverses an
// installation created by scripts/install/install.sh.
package uninstall

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	cliruntime "github.com/yinzhenyu/qrypt/internal/cli/runtime"
	"github.com/yinzhenyu/qrypt/pkg/install"
)

const exitRuntime = 1

// Seams so tests can supply a temporary home directory.
var userHomeDir = os.UserHomeDir

// NewCommand builds the `qrypt uninstall` command.
func NewCommand(rt cliruntime.ErrorFactory) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "uninstall",
		Short: "Remove an installed qrypt",
		Long: `Remove the qrypt installation created by the install script. On Linux
and macOS that is the ~/.local/bin/qrypt symlink, the binary under ~/.qrypt/bin,
and the installer's PATH block; on Windows it is
%USERPROFILE%\.qrypt\bin\qrypt.exe and its user-scope PATH entry. Your qrypt data
(configuration, cache, logs, and sessions) is kept unless --purge is given.

The command only removes a symlink that points at the managed binary and only
the PATH configuration the installer added, so files it did not create are left
alone. It is safe to run when nothing is installed.`,
		Example: `  qrypt uninstall
  qrypt uninstall --yes
  qrypt uninstall --purge --yes`,
		Args: cliruntime.NoArgs(rt),
		RunE: func(cmd *cobra.Command, _ []string) error {
			confirmed, _ := cmd.Flags().GetBool("yes")
			purge, _ := cmd.Flags().GetBool("purge")

			home, err := userHomeDir()
			if err != nil {
				return rt.ExitError(exitRuntime, fmt.Errorf("locate home directory: %w", err))
			}

			if purge {
				fmt.Fprintf(cmd.ErrOrStderr(), "This also removes qrypt data under %s.\n", install.DataDirPath(home))
			}
			if !confirmed {
				ok, err := confirm(cmd, home)
				if err != nil {
					return rt.ExitError(exitRuntime, err)
				}
				if !ok {
					fmt.Fprintln(cmd.OutOrStdout(), "aborted; nothing was removed")
					return nil
				}
			}

			report, err := install.Uninstall(home, install.UninstallOptions{Purge: purge})
			if err != nil {
				return rt.ExitError(exitRuntime, err)
			}
			printReport(cmd, report)
			return nil
		},
	}
	cmd.Flags().BoolP("yes", "y", false, "do not prompt for confirmation")
	cmd.Flags().Bool("purge", false, "also remove qrypt's data (config, cache, logs)")
	cliruntime.MarkAdvancedFlag(cmd.Flags().Lookup("purge"))
	return cmd
}

func confirm(cmd *cobra.Command, home string) (bool, error) {
	fmt.Fprintf(cmd.ErrOrStderr(), "Remove the qrypt installation under %s? [y/N] ", home)
	line, err := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
	if err != nil && line == "" {
		// No input (for example a closed stdin): treat as "no".
		fmt.Fprintln(cmd.ErrOrStderr())
		return false, nil
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true, nil
	default:
		return false, nil
	}
}

func printReport(cmd *cobra.Command, report install.UninstallReport) {
	if report.Empty() {
		fmt.Fprintln(cmd.OutOrStdout(), "nothing to remove")
		return
	}
	out := cmd.OutOrStdout()
	if report.RemovedLink != "" {
		fmt.Fprintf(out, "removed symlink %s\n", report.RemovedLink)
	}
	if report.RemovedBinary != "" {
		fmt.Fprintf(out, "removed binary %s\n", report.RemovedBinary)
	}
	if report.RemovedPath != "" {
		fmt.Fprintf(out, "removed PATH entry %s\n", report.RemovedPath)
	}
	for _, remnant := range report.RemovedRemnants {
		fmt.Fprintf(out, "removed leftover %s\n", remnant)
	}
	for _, file := range report.EditedFiles {
		fmt.Fprintf(out, "removed PATH block from %s\n", file)
	}
	if report.RemovedData != "" {
		fmt.Fprintf(out, "removed data %s\n", report.RemovedData)
	}
	for _, path := range report.Deferred {
		fmt.Fprintf(out, "could not remove %s (in use; removed by a later run or on reboot)\n", path)
	}
}
