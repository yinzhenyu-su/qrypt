// Package cliutil holds edits shared by the credential and mount management
// commands: parameter parsing with file/stdin sources, encryption password
// sources, and validate-then-save config mutation.
package cliutil

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	cliruntime "github.com/yinzhenyu/qrypt/internal/cli/runtime"
	"github.com/yinzhenyu/qrypt/pkg/config"
	"github.com/yinzhenyu/qrypt/pkg/util"
)

// ParseParams parses repeated key=value flags. A value may be @path to read a
// file or - to read standard input, so secrets do not have to appear in shell
// history.
func ParseParams(cmd *cobra.Command, flag string) (config.ParamMap, error) {
	values, err := cmd.Flags().GetStringArray(flag)
	if err != nil {
		return nil, err
	}
	params := config.ParamMap{}
	stdinUsed := false
	for _, item := range values {
		key, raw, ok := strings.Cut(item, "=")
		key = strings.TrimSpace(key)
		if !ok || key == "" {
			return nil, fmt.Errorf("invalid parameter %q: expected key=value", item)
		}
		value, err := ParamValue(cmd, raw, &stdinUsed)
		if err != nil {
			return nil, err
		}
		params[key] = value
	}
	return params, nil
}

// ParamValue resolves one parameter value, including @file and stdin sources.
func ParamValue(cmd *cobra.Command, raw string, stdinUsed *bool) (string, error) {
	switch {
	case raw == "-":
		if *stdinUsed {
			return "", fmt.Errorf("standard input can only supply one parameter")
		}
		*stdinUsed = true
		data, err := io.ReadAll(cmd.InOrStdin())
		if err != nil {
			return "", fmt.Errorf("read parameter from standard input: %w", err)
		}
		return TrimLine(string(data)), nil
	case strings.HasPrefix(raw, "@"):
		data, err := os.ReadFile(util.ExpandHome(strings.TrimPrefix(raw, "@")))
		if err != nil {
			return "", fmt.Errorf("read parameter file: %w", err)
		}
		return TrimLine(string(data)), nil
	default:
		return raw, nil
	}
}

// TrimLine removes one trailing newline (LF or CRLF).
func TrimLine(value string) string {
	return strings.TrimSuffix(strings.TrimSuffix(value, "\n"), "\r")
}

// PasswordFromFlags resolves --password, --password-file, and --password-stdin
// into one value. provided reports whether any source was given.
func PasswordFromFlags(cmd *cobra.Command) (password string, provided bool, err error) {
	sources := 0
	for _, name := range []string{"password", "password-file", "password-stdin"} {
		if cmd.Flags().Changed(name) {
			sources++
		}
	}
	if sources > 1 {
		return "", false, fmt.Errorf("use only one of --password, --password-file, or --password-stdin")
	}
	if sources == 0 {
		return "", false, nil
	}
	password, _ = cmd.Flags().GetString("password")
	if file, _ := cmd.Flags().GetString("password-file"); file != "" {
		raw, readErr := os.ReadFile(util.ExpandHome(file))
		if readErr != nil {
			return "", false, fmt.Errorf("read password file: %w", readErr)
		}
		password = TrimLine(string(raw))
	}
	if stdin, _ := cmd.Flags().GetBool("password-stdin"); stdin {
		raw, readErr := io.ReadAll(cmd.InOrStdin())
		if readErr != nil {
			return "", false, fmt.Errorf("read password from standard input: %w", readErr)
		}
		password = TrimLine(string(raw))
	}
	if password == "" {
		return "", false, fmt.Errorf("encryption password is empty")
	}
	return password, true, nil
}

// SaveConfig applies mutate to a shallow copy of the loaded config, validates
// it, and only then writes the file. A rejected update leaves both the file and
// the caller's config untouched.
func SaveConfig(state cliruntime.ConfigState, mutate func(*config.Config) error) error {
	candidate := *state.Cfg
	candidate.Credentials = append([]config.CredentialConfig(nil), state.Cfg.Credentials...)
	candidate.Mounts = append([]config.MountConfig(nil), state.Cfg.Mounts...)
	if err := mutate(&candidate); err != nil {
		return err
	}
	if err := config.Validate(&candidate); err != nil {
		return err
	}
	if err := config.Save(state.Path, &candidate); err != nil {
		return err
	}
	*state.Cfg = candidate
	return nil
}
