package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	cliruntime "github.com/yinzhenyu/qrypt/internal/cli/runtime"
	"github.com/yinzhenyu/qrypt/pkg/config"
	"github.com/yinzhenyu/qrypt/pkg/util"
)

func NewCommand(rt cliruntime.Runtime) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Create and inspect configuration",
		Args:  cliruntime.CommandGroupArgs(rt, nil),
		RunE:  cliruntime.ShowHelp,
	}
	cmd.AddCommand(NewInitCmd(rt))
	cmd.AddCommand(NewPathCmd(rt))
	cmd.AddCommand(NewShowCmd(rt))
	cmd.AddCommand(NewValidateCmd(rt))
	cmd.AddCommand(NewExportRclonePasswordCmd(rt))
	return cmd
}

func NewValidateCmd(rt cliruntime.Runtime) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "validate",
		Short: "Validate configuration without connecting to remote drives",
		Args:  cliruntime.NoArgs(rt),
		RunE: func(cmd *cobra.Command, args []string) error {
			state, err := rt.CommandConfig(cmd)
			if err != nil {
				return err
			}
			if state.Cfg == nil {
				return rt.ConfigNotFoundError()
			}
			if err := config.Validate(state.Cfg); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Config valid: %s\n", state.Path)
			return nil
		},
	}
	rt.WithConfigFlag(cmd)
	return cmd
}

func NewInitCmd(rt cliruntime.Runtime) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "init [PATH]",
		Short: "Write a starter config",
		Args:  cliruntime.MaxArgs(rt, 1),
		RunE: func(cmd *cobra.Command, args []string) error {
			force, _ := cmd.Flags().GetBool("force")
			outPath := "./qrypt.toml"
			if len(args) == 1 {
				outPath = args[0]
			}

			outPath = util.ExpandHome(outPath)
			absoluteOutPath, err := filepath.Abs(outPath)
			if err != nil {
				return err
			}
			starterRoot := filepath.Join(filepath.Dir(absoluteOutPath), "qrypt-data")

			if _, err := os.Stat(outPath); err == nil && !force {
				return fmt.Errorf("%s already exists (use --force to overwrite)", outPath)
			}

			content, err := GenerateTemplate(starterRoot)
			if err != nil {
				return err
			}

			if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
				return err
			}
			if err := os.MkdirAll(starterRoot, 0o755); err != nil {
				return err
			}
			if err := WriteConfigFile(outPath, content, force); err != nil {
				return err
			}

			fmt.Fprintf(cmd.ErrOrStderr(), "Wrote config to %s\n", outPath)
			fmt.Fprintf(cmd.ErrOrStderr(), "Created local storage at %s\n", starterRoot)
			return nil
		},
	}
	cmd.Flags().BoolP("force", "f", false, "overwrite existing file")
	return cmd
}

func WriteConfigFile(path string, content []byte, force bool) error {
	return util.WriteAtomic(path, ".qrypt-config-*.toml", 0o600, force, func(file *os.File) error {
		_, err := file.Write(content)
		return err
	})
}
