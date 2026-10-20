package cli

import (
	"os"

	"github.com/spf13/cobra"
	cliconfig "github.com/yinzhenyu/qrypt/internal/cli/config"
	clicontrol "github.com/yinzhenyu/qrypt/internal/cli/control"
	clidebug "github.com/yinzhenyu/qrypt/internal/cli/debug"
	clidriver "github.com/yinzhenyu/qrypt/internal/cli/driver"
	clifs "github.com/yinzhenyu/qrypt/internal/cli/fs"
	"github.com/yinzhenyu/qrypt/internal/cli/i18n"
	climount "github.com/yinzhenyu/qrypt/internal/cli/mount"
	clioffline "github.com/yinzhenyu/qrypt/internal/cli/offline"
	cliuninstall "github.com/yinzhenyu/qrypt/internal/cli/uninstall"
	cliupgrade "github.com/yinzhenyu/qrypt/internal/cli/upgrade"
	cliversion "github.com/yinzhenyu/qrypt/internal/cli/version"
	buildinfo "github.com/yinzhenyu/qrypt/pkg/buildinfo" // registers all drivers via their init functions
	_ "github.com/yinzhenyu/qrypt/pkg/drivers/all"
)

// NewRootCommand builds the qrypt command tree. When args is nil the command
// tree uses English, which keeps direct programmatic use and tests stable.
// The CLI entry point passes os.Args[1:] so the system locale is honored.
func NewRootCommand(args ...string) *cobra.Command {
	lang := i18n.LangEN
	if args != nil {
		lang = i18n.Detect(args, os.Getenv)
	}
	build := buildinfo.Current()
	cmd := &cobra.Command{
		Use:          "qrypt",
		Short:        "Mounts encrypted cloud drives as a local filesystem",
		SilenceUsage: true,
		Version:      build.Version,
		Long: `qrypt exposes configured cloud drives as one local FUSE mount point.

Each drive appears as a directory under the mount point, with optional
rclone-compatible content and filename encryption.

Use a command's --config flag to point to a TOML config file, then mount to
start the filesystem, or use fs list/cat/get/put for one-shot operations.
When --config is omitted, qrypt searches ./qrypt.toml, ~/.qrypt/qrypt.toml,
then the platform config directory: $XDG_CONFIG_HOME/qrypt/qrypt.toml
(default: ~/.config/qrypt/qrypt.toml) on Unix, or
%AppData%\qrypt\qrypt.toml on Windows.`,
		Example: `  qrypt mount --config ./qrypt.toml
  qrypt fs list /cloud/photos
  qrypt fs get /cloud/photos/cat.jpg ./cat.jpg`,
		Args: commandGroupArgs(nil),
		RunE: func(c *cobra.Command, args []string) error {
			return c.Help()
		},
	}

	cmd.AddCommand(climount.NewCommand(cliRuntime{}))
	cmd.AddCommand(cliconfig.NewCommand(cliRuntime{}))
	cmd.AddCommand(clidriver.NewCommand(cliRuntime{}))
	cmd.AddCommand(clifs.NewCommand(cliRuntime{}))
	cmd.AddCommand(clidebug.NewCommand(cliRuntime{}))
	cmd.AddCommand(clidebug.NewProbeCommand(cliRuntime{}))
	cmd.AddCommand(clicontrol.NewCommand(cliRuntime{}))
	cmd.AddCommand(clioffline.NewCommand(cliRuntime{}))
	cmd.AddCommand(cliupgrade.NewCommand(cliRuntime{}, build))
	cmd.AddCommand(cliuninstall.NewCommand(cliRuntime{}))
	cmd.AddCommand(cliversion.NewCommand(cliRuntime{}, build))
	cmd.AddCommand(NewSchemaCommand(cmd))
	cmd.CompletionOptions.DisableDefaultCmd = true
	cmd.AddCommand(newCompletionCommand())
	cmd.PersistentFlags().String("lang", "auto", "display language: en, zh-CN, or auto")
	// Cobra normally adds these during Execute, after localization has run.
	// Initialize them first so their summaries and help pages are covered by
	// the same catalog and coverage checks as the rest of the tree.
	cmd.InitDefaultHelpCmd()
	for _, child := range cmd.Commands() {
		if child.Name() == "help" {
			child.Example = i18n.New(i18n.LangEN).T("cmd.help.example")
		}
	}
	i18n.InitDefaultFlags(cmd)
	installHelpDefaults(cmd)
	installDeepHelp(cmd)
	i18n.Attach(cmd, lang)
	i18n.LocalizeCommandTree(cmd)
	return cmd
}
