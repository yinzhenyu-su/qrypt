// Package upgrade implements `qrypt upgrade`, which replaces the running
// executable with a published release.
package upgrade

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"
	cliruntime "github.com/yinzhenyu/qrypt/internal/cli/runtime"
	buildinfo "github.com/yinzhenyu/qrypt/pkg/buildinfo"
	"github.com/yinzhenyu/qrypt/pkg/selfupdate"
)

const (
	exitRuntime = 1
	runTimeout  = 10 * time.Minute
)

// newUpgrader builds the upgrader the command drives. It is a variable so a
// test can inject a client pointed at a local release fixture.
var newUpgrader = selfupdate.NewUpgrader

// NewCommand builds the `qrypt upgrade` command.
func NewCommand(rt cliruntime.ErrorFactory, info buildinfo.Info) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "upgrade",
		Short: "Update qrypt to the newest release",
		Long: `Fetch the newest qrypt release for this platform, verify it against the
published checksum, and replace the running executable in place. The files are
downloaded to a temporary location and only swapped in after verification, so a
failed download or a mismatched checksum leaves the current binary untouched.

Upgrading a development build (a version such as v0.5.3-10-g9d16c54) is refused
because no release can be compared against it; use --force to override. When the
current binary is not writable, the command does not raise privileges itself: it
prints the command to run instead.`,
		Example: `  qrypt upgrade
  qrypt upgrade --check
  qrypt upgrade --to v0.5.0`,
		Args: cliruntime.NoArgs(rt),
		RunE: func(cmd *cobra.Command, _ []string) error {
			check, _ := cmd.Flags().GetBool("check")
			force, _ := cmd.Flags().GetBool("force")
			asJSON, _ := cmd.Flags().GetBool("json")
			to, _ := cmd.Flags().GetString("to")
			if to != "" {
				if _, err := selfupdate.ParseVersion(to); err != nil {
					return rt.UsageError(cmd, "invalid --to value %q: expected a version such as v0.5.0", to)
				}
			}

			ctx, cancel := context.WithTimeout(cmd.Context(), runTimeout)
			defer cancel()

			upgrader := newUpgrader()
			if !asJSON {
				upgrader.Logf = func(format string, args ...any) {
					fmt.Fprintf(cmd.ErrOrStderr(), format+"\n", args...)
				}
			}

			result, err := upgrader.Run(ctx, selfupdate.Options{
				Current: info.Version,
				Target:  to,
				Force:   force,
				Check:   check,
			})
			if err != nil {
				return rt.ExitError(exitRuntime, err)
			}

			if asJSON {
				return cliruntime.WritePrettyJSON(cmd.OutOrStdout(), result)
			}
			fmt.Fprintln(cmd.OutOrStdout(), result.Message)
			return nil
		},
	}
	cmd.Flags().Bool("check", false, "report the newest release without installing it")
	cmd.Flags().String("to", "", "install a specific release version, for example v0.5.0")
	cmd.Flags().Bool("force", false, "upgrade even when the current build is not a release version")
	cmd.Flags().Bool("json", false, "write a JSON result")
	cliruntime.MarkAdvancedFlag(cmd.Flags().Lookup("to"))
	cliruntime.MarkAdvancedFlag(cmd.Flags().Lookup("force"))
	return cmd
}
