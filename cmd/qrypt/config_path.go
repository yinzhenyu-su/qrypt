package main

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
	return nil
}

func prepareRuntimeConfig(cmd *cobra.Command, args []string) error {
	if err := prepareConfig(cmd, args); err != nil {
		return err
	}
	configPath, err := commandConfigPath(cmd)
	if err != nil {
		return err
	}
	if configPath != "" {
		cfg, err := config.Load(configPath)
		if err != nil {
			return err
		}
		if err := validateConfig(cfg); err != nil {
			return err
		}
	}
	if err := initLoggerFromConfig(configPath); err != nil {
		return err
	}
	return initTimeFromConfig(commandContext(cmd), configPath)
}

func commandConfigPath(cmd *cobra.Command) (string, error) {
	if cmd.Flag("config") == nil {
		return findConfigPath(), nil
	}
	configPath, err := cmd.Flags().GetString("config")
	if err != nil {
		return "", err
	}
	return osutil.ExpandHome(configPath), nil
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
