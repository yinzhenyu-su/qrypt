// Package controlctx resolves the local Core control socket for a command and
// maps transport failures onto process exit codes. It exists so the share and
// receive command groups share one provider-neutral connection path without
// duplicating socket discovery.
package controlctx

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	cliruntime "github.com/yinzhenyu/qrypt/internal/cli/runtime"
	"github.com/yinzhenyu/qrypt/pkg/control"
	"github.com/yinzhenyu/qrypt/pkg/core"
	"github.com/yinzhenyu/qrypt/pkg/util"
)

// Exit codes are the stable set the CLI exposes elsewhere.
const (
	ExitUsage   = 2
	ExitFailure = 1
)

type endpointContextKey struct{}

// WithEndpoint resolves the control socket for the selected configuration and
// injects it into the command context.
func WithEndpoint(rt cliruntime.Runtime, cmd *cobra.Command) *cobra.Command {
	run := cmd.RunE
	if run == nil {
		return cmd
	}
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		state, err := rt.CommandConfig(cmd)
		if err != nil {
			return err
		}
		if state.Cfg == nil {
			return rt.ConfigNotFoundError()
		}
		runDir := core.RuntimeRunDir(state.Path, state.Cfg)
		endpoint := core.ControlSocketPath(state.Cfg, core.RuntimeLayout{RunDir: runDir})
		// A signal-aware context lets an interrupt cancel the in-flight wait
		// without canceling the Core task; the exit code becomes 130.
		ctx, stop := rt.ShutdownContext(cmd)
		defer stop()
		cmd.SetContext(context.WithValue(ctx, endpointContextKey{}, endpoint))
		return run(cmd, args)
	}
	return cmd
}

// Client opens a control client for the injected endpoint.
func Client(cmd *cobra.Command) (*control.Client, error) {
	endpoint, _ := cmd.Context().Value(endpointContextKey{}).(string)
	return control.NewClient(endpoint)
}

// ConnectionError maps a connection failure to a clear runtime error.
func ConnectionError(err error, group string) error {
	if err == nil {
		return nil
	}
	var transport *control.TransportError
	if errors.As(err, &transport) {
		return err
	}
	if IsConnectionError(err) {
		return fmt.Errorf("%s commands require a running Core (start `qrypt mount`); no Core is listening: %w", group, err)
	}
	return err
}

// IsConnectionError reports whether err looks like a missing or refused local
// socket rather than a command failure.
func IsConnectionError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	message := strings.ToLower(err.Error())
	for _, token := range []string{
		"connection refused", "no such file or directory", "connect:", "dial unix",
		"the system cannot find the file", "actively refused",
	} {
		if strings.Contains(message, token) {
			return true
		}
	}
	return false
}

// FinishError converts a command error to a process exit code. Interrupts are
// preserved so the runner reports 130; a usage-classified transport error is 2;
// everything else is a runtime failure (1).
func FinishError(rt cliruntime.ErrorFactory, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return err
	}
	var transport *control.TransportError
	if errors.As(err, &transport) {
		if transport.Status == http.StatusBadRequest {
			return rt.ExitError(ExitUsage, err)
		}
		return rt.ExitError(ExitFailure, err)
	}
	return rt.ExitError(ExitFailure, err)
}

// ResolveLocalDestination resolves a relative local download path against the
// caller's working directory before it crosses the control socket. A namespace
// path (leading slash) is left untouched, and a trailing separator is preserved
// so the Core still sees the destination as a directory.
func ResolveLocalDestination(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", nil
	}
	expanded := util.ExpandHome(trimmed)
	if filepath.IsAbs(expanded) || strings.HasPrefix(expanded, "/") || strings.HasPrefix(expanded, `\`) {
		return expanded, nil
	}
	directory := strings.HasSuffix(expanded, "/") || strings.HasSuffix(expanded, `\`)
	absolute, err := filepath.Abs(expanded)
	if err != nil {
		return "", err
	}
	if directory {
		absolute += string(filepath.Separator)
	}
	return absolute, nil
}
