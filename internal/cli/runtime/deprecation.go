package runtime

import (
	"fmt"
	"sync"

	"github.com/spf13/cobra"

	"github.com/yinzhenyu/qrypt/internal/cli/i18n"
)

// deprecationKey scopes the once-per-process notice to the command tree that
// executed the alias. Tests create a fresh root per execution, so they stay
// deterministic without exposing a reset hook.
type deprecationKey struct {
	root  *cobra.Command
	alias string
}

// deprecatedNotices keeps hidden compatibility aliases from repeating their
// notice within one command tree execution.
var deprecatedNotices sync.Map

// WarnDeprecated prints a one-time stderr notice for a hidden compatibility
// alias. Aliases stay runnable for one minor release; the preferred command is
// their replacement.
func WarnDeprecated(cmd *cobra.Command, alias, replacement string) {
	if cmd == nil {
		return
	}
	key := deprecationKey{root: cmd.Root(), alias: alias}
	if _, loaded := deprecatedNotices.LoadOrStore(key, struct{}{}); loaded {
		return
	}
	fmt.Fprintln(cmd.ErrOrStderr(), i18n.FromCommand(cmd).T("warn.cli.deprecated", alias, replacement))
}
