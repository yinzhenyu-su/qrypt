package credential

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"
	"github.com/yinzhenyu/qrypt/internal/cli/cliutil"
	"github.com/yinzhenyu/qrypt/internal/cli/i18n"
	climount "github.com/yinzhenyu/qrypt/internal/cli/mount"
	cliruntime "github.com/yinzhenyu/qrypt/internal/cli/runtime"
	"github.com/yinzhenyu/qrypt/pkg/config"
)

func loginMountPlan(ctx context.Context, cmd *cobra.Command, state cliruntime.ConfigState, providerType, credentialName string, interactive, mountFlags bool) (config.MountConfig, string, bool, error) {
	if err := ctx.Err(); err != nil {
		return config.MountConfig{}, "", false, loginContextError(err)
	}
	defaultMountPoint := cfgMountPoint(state.Cfg)
	cfg := state.Cfg

	if !mountFlags && !interactive {
		return config.MountConfig{}, "", false, nil
	}

	mountName, _ := cmd.Flags().GetString("mount-name")
	rootPath, _ := cmd.Flags().GetString("root-path")
	mountPoint, _ := cmd.Flags().GetString("mount-point")
	mountName = strings.TrimSpace(mountName)
	rootPath = strings.TrimSpace(rootPath)
	mountPoint = strings.TrimSpace(mountPoint)

	if interactive && !mountFlags {
		if mountName == "" {
			mountName = deriveMountName(credentialName, cfg)
		}
		if rootPath == "" {
			rootPath = "/"
		}
		if mountPoint == "" {
			mountPoint = defaultMountPoint
		}

		reader := bufio.NewReader(cmd.InOrStdin())
		loc := i18n.FromCommand(cmd)
		var err error
		if mountName, err = promptLoginValue(ctx, cmd, reader, loc, "out.credential.login.mount_name", mountName); err != nil {
			return config.MountConfig{}, "", false, err
		}
		if rootPath, err = promptLoginValue(ctx, cmd, reader, loc, "out.credential.login.root_path", rootPath); err != nil {
			return config.MountConfig{}, "", false, err
		}
		if mountPoint, err = promptLoginValue(ctx, cmd, reader, loc, "out.credential.login.mount_point", mountPoint); err != nil {
			return config.MountConfig{}, "", false, err
		}
	} else {
		if mountName == "" {
			mountName = deriveMountName(credentialName, cfg)
		}
		if rootPath == "" {
			rootPath = "/"
		}
		if mountPoint == "" {
			mountPoint = defaultMountPoint
		}
	}

	if err := validateLoginMountName(mountName, cfg); err != nil {
		return config.MountConfig{}, "", false, err
	}
	if rootPath == "" {
		rootPath = "/"
	}
	if !strings.HasPrefix(rootPath, "/") {
		rootPath = "/" + rootPath
	}
	if mountPoint == "" {
		return config.MountConfig{}, "", false, errors.New("mount point is required")
	}

	return config.MountConfig{
		Name:       mountName,
		Type:       providerType,
		Credential: credentialName,
		Params:     config.ParamMap{"root_path": rootPath},
	}, mountPoint, true, nil
}

func promptLoginValue(ctx context.Context, cmd *cobra.Command, reader *bufio.Reader, loc *i18n.Localizer, key, defaultValue string) (string, error) {
	fmt.Fprintf(cmd.ErrOrStderr(), loc.T(key), defaultValue)
	type readResult struct {
		line string
		err  error
	}
	read := make(chan readResult, 1)
	go func() {
		line, err := reader.ReadString('\n')
		read <- readResult{line: line, err: err}
	}()
	select {
	case <-ctx.Done():
		return "", loginContextError(ctx.Err())
	case result := <-read:
		if result.err != nil && !errors.Is(result.err, io.EOF) {
			return "", result.err
		}
		value := strings.TrimSpace(cliutil.TrimLine(result.line))
		if value == "" {
			return defaultValue, nil
		}
		return value, nil
	}
}

func validateLoginMountName(name string, cfg *config.Config) error {
	if name == "" {
		return errors.New("mount name is required")
	}
	if name == "." || name == ".." || strings.ContainsAny(name, `/\`) {
		return fmt.Errorf("mount name %q must be a single path component", name)
	}
	if climount.ReservedMountName(name) {
		return fmt.Errorf("mount name %q is reserved for a subcommand", name)
	}
	if cfg != nil {
		if _, exists := cfg.MountByName(name); exists {
			return fmt.Errorf("mount %q already exists; choose another --mount-name", name)
		}
	}
	return nil
}

func deriveMountName(base string, cfg *config.Config) string {
	return nextAvailableName(base, func(name string) bool {
		return climount.ReservedMountName(name) || mountExists(cfg, name)
	})
}

func mountExists(cfg *config.Config, name string) bool {
	if cfg == nil {
		return false
	}
	_, ok := cfg.MountByName(name)
	return ok
}

func cfgMountPoint(cfg *config.Config) string {
	if cfg != nil {
		if point := cfg.EffectiveMountPoint(); point != "" {
			return point
		}
	}
	return "~/Qrypt"
}

func loginMountFlags(cmd *cobra.Command) bool {
	for _, name := range []string{"mount-name", "root-path", "mount-point"} {
		if cmd.Flags().Changed(name) {
			return true
		}
	}
	return false
}
