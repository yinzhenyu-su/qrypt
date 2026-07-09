package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/spf13/cobra"
	"github.com/yinzhenyu/qrypt/internal/config"
	"github.com/yinzhenyu/qrypt/pkg/osutil"
)

type commandConfigState struct {
	path string
	cfg  *config.Config
}

type commandConfigContextKey struct{}

func withConfigFlag(cmd *cobra.Command) *cobra.Command {
	cmd.Flags().StringP("config", "c", "", "config file path (auto-discovered when omitted)")
	cmd.PreRunE = prepareConfig
	return cmd
}

func withRuntimeConfigFlag(cmd *cobra.Command) *cobra.Command {
	withConfigFlag(cmd)
	cmd.PreRunE = prepareRuntimeConfig
	return cmd
}

func withPersistentRuntimeConfigFlag(cmd *cobra.Command) *cobra.Command {
	cmd.PersistentFlags().StringP("config", "c", "", "config file path (auto-discovered when omitted)")
	cmd.PersistentPreRunE = prepareRuntimeConfig
	return cmd
}

func commandContext(cmd *cobra.Command) context.Context {
	if ctx := cmd.Context(); ctx != nil {
		return ctx
	}
	return context.Background()
}

func prepareConfig(cmd *cobra.Command, _ []string) error {
	configPath, err := cmd.Flags().GetString("config")
	if err != nil {
		return err
	}
	if configPath == "" {
		configPath = findConfigPath()
	} else {
		configPath = osutil.ExpandHome(configPath)
	}
	if flag := cmd.Flag("config"); flag != nil {
		if err := flag.Value.Set(configPath); err != nil {
			return err
		}
	}
	var cfg *config.Config
	if configPath != "" {
		loaded, err := config.Load(configPath)
		if err != nil {
			return fmt.Errorf("load config %q: %w", configPath, err)
		}
		cfg = loaded
	}
	cmd.SetContext(context.WithValue(commandContext(cmd), commandConfigContextKey{}, commandConfigState{
		path: configPath,
		cfg:  cfg,
	}))
	return nil
}

func prepareRuntimeConfig(cmd *cobra.Command, args []string) error {
	if err := prepareConfig(cmd, args); err != nil {
		return err
	}
	state, err := commandConfig(cmd)
	if err != nil {
		return err
	}
	if state.cfg != nil {
		if err := validateConfig(state.cfg); err != nil {
			return err
		}
	}
	if err := initLogger(state.cfg); err != nil {
		return err
	}
	return initTime(commandContext(cmd), state.cfg)
}

func commandConfigPath(cmd *cobra.Command) (string, error) {
	if state, ok := commandContext(cmd).Value(commandConfigContextKey{}).(commandConfigState); ok {
		return state.path, nil
	}
	if cmd.Flag("config") == nil {
		return findConfigPath(), nil
	}
	configPath, err := cmd.Flags().GetString("config")
	if err != nil {
		return "", err
	}
	configPath = osutil.ExpandHome(configPath)
	if configPath == "" {
		configPath = findConfigPath()
	}
	return configPath, nil
}

func commandConfig(cmd *cobra.Command) (commandConfigState, error) {
	if state, ok := commandContext(cmd).Value(commandConfigContextKey{}).(commandConfigState); ok {
		return state, nil
	}
	configPath, err := commandConfigPath(cmd)
	if err != nil {
		return commandConfigState{}, err
	}
	if configPath == "" {
		return commandConfigState{}, nil
	}
	cfg, err := config.Load(configPath)
	return commandConfigState{path: configPath, cfg: cfg}, err
}

func configNotFoundError() error {
	return fmt.Errorf("config file not found; searched %s (use --config PATH to specify one)",
		strings.Join(configSearchPaths(), ", "))
}

func findConfigPath() string {
	for _, candidate := range configSearchPaths() {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}
	}
	return ""
}

func configSearchPaths() []string {
	candidates := []string{"./qrypt.toml"}
	home, homeErr := os.UserHomeDir()
	if homeErr == nil {
		candidates = append(candidates, filepath.Join(home, ".qrypt", "qrypt.toml"))
	}
	if runtime.GOOS == "windows" {
		if configHome, err := os.UserConfigDir(); err == nil {
			candidates = append(candidates, filepath.Join(configHome, "qrypt", "qrypt.toml"))
		}
	} else if configHome := os.Getenv("XDG_CONFIG_HOME"); filepath.IsAbs(configHome) {
		candidates = append(candidates, filepath.Join(configHome, "qrypt", "qrypt.toml"))
	} else if homeErr == nil {
		candidates = append(candidates, filepath.Join(home, ".config", "qrypt", "qrypt.toml"))
	}
	return candidates
}
