package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/yinzhenyu/qrypt/internal/fileutil"
	"github.com/yinzhenyu/qrypt/pkg/osutil"
)

func newConfigCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Create and inspect configuration",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}
	cmd.AddCommand(newConfigInitCmd())
	cmd.AddCommand(newConfigShowCmd())
	cmd.AddCommand(newConfigValidateCmd())
	cmd.AddCommand(newConfigExportRclonePasswordCmd())
	return cmd
}

func newConfigValidateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "validate",
		Short: "Validate configuration without connecting to remote drives",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			state, err := commandConfig(cmd)
			if err != nil {
				return err
			}
			if state.cfg == nil {
				return configNotFoundError()
			}
			if err := validateConfig(state.cfg); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Config valid: %s\n", state.path)
			return nil
		},
	}
	withConfigFlag(cmd)
	return cmd
}

func newConfigInitCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "init [PATH]",
		Short: "Write a starter config",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			force, _ := cmd.Flags().GetBool("force")
			outPath := "./qrypt.toml"
			if len(args) == 1 {
				outPath = args[0]
			}

			outPath = osutil.ExpandHome(outPath)
			absoluteOutPath, err := filepath.Abs(outPath)
			if err != nil {
				return err
			}
			starterRoot := filepath.Join(filepath.Dir(absoluteOutPath), "qrypt-data")

			if _, err := os.Stat(outPath); err == nil && !force {
				return fmt.Errorf("%s already exists (use --force to overwrite)", outPath)
			}

			content, err := generateConfigTemplate(starterRoot)
			if err != nil {
				return err
			}

			if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
				return err
			}
			if err := os.MkdirAll(starterRoot, 0o755); err != nil {
				return err
			}
			if err := writeConfigFile(outPath, content, force); err != nil {
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

func writeConfigFile(path string, content []byte, force bool) error {
	return fileutil.WriteAtomic(path, ".qrypt-config-*.toml", 0o600, force, func(file *os.File) error {
		_, err := file.Write(content)
		return err
	})
}
