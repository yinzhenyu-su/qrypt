// Package core exposes the resident Core lifecycle: serving it in the
// foreground or as a detached process, inspecting it, and stopping it.
package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"
	clii18n "github.com/yinzhenyu/qrypt/internal/cli/i18n"
	cliruntime "github.com/yinzhenyu/qrypt/internal/cli/runtime"
	"github.com/yinzhenyu/qrypt/pkg/buildinfo"
	"github.com/yinzhenyu/qrypt/pkg/control"
	corepkg "github.com/yinzhenyu/qrypt/pkg/core"
	"github.com/yinzhenyu/qrypt/pkg/core/ownerlock"
	"github.com/yinzhenyu/qrypt/pkg/logging"
	"github.com/yinzhenyu/qrypt/pkg/util"
)

const (
	// serveChildEnv marks a detached serve child so it never re-detaches.
	serveChildEnv = "QRYPT_CORE_SERVE_CHILD"
	// readyTimeout bounds how long a detach request waits for the child to
	// accept control requests.
	readyTimeout = 10 * time.Second
	// statusPollInterval is the attach/status poll cadence.
	statusPollInterval = 100 * time.Millisecond
	// stopTimeout bounds how long `core stop` waits for the process to exit.
	stopTimeout = 15 * time.Second
)

// NewCommand exposes the resident Core as a first-class process.
func NewCommand(rt cliruntime.Runtime) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "core",
		Short: "Run and inspect the resident Core",
		Long: `Run the resident Core that owns a state directory, and inspect or stop a
running one. One Core owns a state directory at a time; it holds the same
caches, upload workers and journal that mounts and one-shot fs commands use.`,
		Example: `  qrypt core serve --detach
  qrypt core status
  qrypt core stop`,
		Args: cliruntime.CommandGroupArgs(rt, nil),
		RunE: cliruntime.ShowHelp,
	}
	rt.WithPersistentConfigFlag(cmd)
	cmd.AddCommand(newServeCommand(rt))
	cmd.AddCommand(newStatusCommand(rt))
	cmd.AddCommand(newEndpointCommand(rt))
	cmd.AddCommand(newStopCommand(rt))
	return cmd
}

type serveFlags struct {
	detach      bool
	foreground  bool
	keep        bool
	idleTimeout time.Duration
	logFile     string
}

func newServeCommand(rt cliruntime.Runtime) *cobra.Command {
	flags := serveFlags{}
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the resident Core",
		Long: `Run the resident Core for the configured state directory. The Core holds the
single-writer ownership lock, so no other Core, mount or fs command that writes
state may run against the same directory until this process exits.

By default serve runs in the foreground. --detach starts a background process
and returns once its control endpoint answers. The Core exits on SIGINT/SIGTERM,
on an explicit "qrypt core stop", or after --idle-timeout with no pending work
unless --keep is set.`,
		Example: `  qrypt core serve
  qrypt core serve --detach --idle-timeout 20m
  qrypt core serve --detach --keep --log ./core.log`,
		Args: cliruntime.NoArgs(rt),
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runServe(rt, cmd, flags)
		},
	}
	cmd.Flags().BoolVar(&flags.detach, "detach", false, "start the Core in the background and return once it is ready")
	cmd.Flags().BoolVar(&flags.foreground, "foreground", false, "run in the foreground (the default; used by detached children)")
	cmd.Flags().BoolVar(&flags.keep, "keep", false, "never exit because of the idle timeout")
	cmd.Flags().DurationVar(&flags.idleTimeout, "idle-timeout", 20*time.Minute, "exit after this much idle time (0 disables)")
	cmd.Flags().StringVar(&flags.logFile, "log", "", "write the Core log to this file")
	return cmd
}

func runServe(rt cliruntime.Runtime, cmd *cobra.Command, flags serveFlags) error {
	state, err := rt.CommandConfig(cmd)
	if err != nil {
		return err
	}
	if state.Cfg == nil {
		return rt.ConfigNotFoundError()
	}
	if flags.detach && os.Getenv(serveChildEnv) == "" {
		return detachServe(rt, cmd, state, flags)
	}
	return serveForeground(rt, cmd, state, flags)
}

func serveForeground(rt cliruntime.Runtime, cmd *cobra.Command, state cliruntime.ConfigState, flags serveFlags) error {
	cfg := state.Cfg
	if flags.logFile != "" {
		clone := *cfg
		clone.Logging.LogFile = flags.logFile
		cfg = &clone
	}
	configPath := canonicalConfigPath(state.Path)
	runDir := corepkg.RuntimeRunDir(configPath, cfg)
	runtime := corepkg.RuntimeLayout{RunDir: runDir}
	controlListen := corepkg.ControlSocketPath(cfg, runtime)

	ctx, cancel := context.WithCancel(commandContext(cmd))
	defer cancel()
	instance, err := corepkg.OpenConfig(ctx, cfg, corepkg.Options{
		ConfigPath:    configPath,
		Runtime:       runtime,
		ControlListen: controlListen,
		Owner:         &corepkg.OwnerOptions{ConfigPath: configPath},
		MountHost:     newFuseMountHost(rt, cfg),
		LazyMounts:    true,
	})
	if err != nil {
		return err
	}
	logging.L.Infof("[CORE] serve: pid %d listening on %s", os.Getpid(), controlListen)

	// The runtime logger writes to files, so a foreground serve would otherwise
	// look like it did nothing. Print one line to the terminal for humans.
	logFile := util.ExpandHome(cfg.EffectiveLogFile())
	if logFile == "" {
		logFile = filepath.Join(corepkg.NewStorageLayout(cfg, runtime).LogDir, "qrypt.log")
	}
	fmt.Fprintln(cmd.ErrOrStderr(), clii18n.FromCommand(cmd).T("out.core.serve.ready", os.Getpid(), controlListen, logFile))

	stopCh := make(chan struct{})
	var stopOnce sync.Once
	requestStop := func() {
		stopOnce.Do(func() { close(stopCh) })
	}
	instance.SetShutdownHandler(requestStop)
	defer instance.SetShutdownHandler(nil)

	signalCtx, stopSignals := rt.ShutdownContext(cmd)
	defer stopSignals()
	go func() {
		select {
		case <-signalCtx.Done():
			requestStop()
		case <-stopCh:
		}
	}()

	if !flags.keep && flags.idleTimeout > 0 {
		go watchIdle(instance, flags.idleTimeout, requestStop)
	}

	<-stopCh
	closeCtx, closeCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer closeCancel()
	if err := instance.Close(closeCtx); err != nil {
		return err
	}
	logging.L.Infof("[CORE] serve: stopped")
	return nil
}

func watchIdle(instance *corepkg.Core, timeout time.Duration, requestStop func()) {
	interval := timeout / 4
	if interval > 30*time.Second {
		interval = 30 * time.Second
	}
	if interval < time.Second {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	lastBusy := time.Now()
	for range ticker.C {
		if instance.Busy() {
			lastBusy = time.Now()
			continue
		}
		if time.Since(lastBusy) >= timeout {
			logging.L.Infof("[CORE] serve: idle for %s, exiting", timeout)
			requestStop()
			return
		}
	}
}

func detachServe(rt cliruntime.Runtime, cmd *cobra.Command, state cliruntime.ConfigState, flags serveFlags) error {
	if os.Getenv(serveChildEnv) != "" {
		return fmt.Errorf("core: refusing to detach from an already detached serve process")
	}
	runDir := corepkg.RuntimeRunDir(state.Path, state.Cfg)
	endpoint := corepkg.ControlSocketPath(state.Cfg, corepkg.RuntimeLayout{RunDir: runDir})
	if _, err := fetchStatus(cmd.Context(), endpoint); err == nil {
		return fmt.Errorf("core: a Core is already running for this state directory (stop it before starting another)")
	}
	exe, err := spawnExecutable()
	if err != nil {
		return err
	}
	args := []string{"core", "serve", "--foreground"}
	if state.Path != "" {
		args = append(args, "--config", state.Path)
	}
	if flags.keep {
		args = append(args, "--keep")
	}
	if flags.idleTimeout > 0 {
		args = append(args, "--idle-timeout", flags.idleTimeout.String())
	}
	if flags.logFile != "" {
		args = append(args, "--log", flags.logFile)
	}
	if err := spawnDetached(exe, args, flags.logFile); err != nil {
		return fmt.Errorf("core: start detached serve: %w", err)
	}
	status, err := waitForStatus(cmd.Context(), endpoint, readyTimeout)
	if err != nil {
		return fmt.Errorf("core: detached serve did not become ready: %w", err)
	}
	logging.L.Infof("[CORE] detached serve ready: pid %d", status.PID)
	_, err = fmt.Fprintf(cmd.OutOrStdout(), "core started (pid %d)\n", status.PID)
	return err
}

// spawnExecutable returns the current executable path in a form that can be
// spawned on every platform. On Windows os.Executable can report a path without
// the .exe suffix when the process was started through PATH (for example from
// Git Bash), and exec.Command does not append extensions to absolute paths.
func spawnExecutable() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if runtime.GOOS == "windows" && !strings.HasSuffix(strings.ToLower(exe), ".exe") {
		exe += ".exe"
	}
	return exe, nil
}

// EnsureCore makes sure a resident Core owns the state directory, starting a
// detached one when none is running. It is the single entry point later
// clients (mount attach, fs RPC) use for lazy start.
// looksLikeTestBinary keeps the lazy-start path from re-executing the Go test
// binary (which would recursively run the suite) when a command is exercised
// from an integration test.
func looksLikeTestBinary(exe string) bool {
	base := filepath.Base(exe)
	return strings.HasSuffix(base, ".test") || strings.HasSuffix(base, ".test.exe")
}

func EnsureCore(ctx context.Context, rt cliruntime.Runtime, state cliruntime.ConfigState) error {
	if state.Cfg == nil {
		return fmt.Errorf("core: config required")
	}
	runDir := corepkg.RuntimeRunDir(state.Path, state.Cfg)
	endpoint := corepkg.ControlSocketPath(state.Cfg, corepkg.RuntimeLayout{RunDir: runDir})
	if _, err := fetchStatus(ctx, endpoint); err == nil {
		return nil
	}
	exe, err := spawnExecutable()
	if err != nil {
		return err
	}
	if looksLikeTestBinary(exe) {
		return fmt.Errorf("core: refusing to auto-start a Core from a test binary (%s); start `qrypt core serve` explicitly", filepath.Base(exe))
	}
	args := ensureCoreServeArgs(state)
	if err := spawnDetached(exe, args, ""); err != nil {
		return fmt.Errorf("core: start resident Core: %w", err)
	}
	_, err = waitForStatus(ctx, endpoint, readyTimeout)
	if err != nil {
		return fmt.Errorf("core: resident Core did not become ready: %w", err)
	}
	return nil
}

// EnsureClient ensures a resident Core is running and returns a client after
// checking that both sides speak the same protocol and use the same
// configuration. A stale Core from an older binary or a different config must
// be restarted instead of silently serving diverging state.
func EnsureClient(ctx context.Context, rt cliruntime.Runtime, state cliruntime.ConfigState) (*control.Client, error) {
	if err := EnsureCore(ctx, rt, state); err != nil {
		return nil, err
	}
	if state.Cfg == nil {
		return nil, fmt.Errorf("core: config required")
	}
	runDir := corepkg.RuntimeRunDir(state.Path, state.Cfg)
	endpoint := corepkg.ControlSocketPath(state.Cfg, corepkg.RuntimeLayout{RunDir: runDir})
	client, err := control.NewClient(endpoint)
	if err != nil {
		return nil, err
	}
	status, err := client.RuntimeStatus(ctx)
	if err != nil {
		return nil, fmt.Errorf("core: inspect resident Core: %w", err)
	}
	if err := verifyCoreStatus(status, state); err != nil {
		return nil, err
	}
	return client, nil
}

// verifyCoreStatus rejects a Core that cannot serve this client faithfully.
// canonicalConfigPath resolves a config path to an absolute, symlink-free
// form so a Core started with "./qrypt.toml" and a client resolving the same
// file to an absolute path compare equal.
func canonicalConfigPath(path string) string {
	if path == "" {
		return ""
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved
	}
	return filepath.Clean(abs)
}

func verifyCoreStatus(status control.RuntimeStatus, state cliruntime.ConfigState) error {
	if status.ProtocolVersion != control.ProtocolVersion {
		return fmt.Errorf("core: resident Core speaks protocol v%d but this client speaks v%d; restart it with `qrypt core stop` and retry", status.ProtocolVersion, control.ProtocolVersion)
	}
	if status.Version != "" {
		if want := buildinfo.Current().Version; want != "" && status.Version != want {
			return fmt.Errorf("core: resident Core runs qrypt %s but this client is %s; restart it with `qrypt core stop` and retry", status.Version, want)
		}
	}
	if state.Path == "" {
		return nil
	}
	configPath, err := filepath.Abs(state.Path)
	if err != nil {
		return fmt.Errorf("core: resolve config path: %w", err)
	}
	configPath = canonicalConfigPath(configPath)
	if status.ConfigPath != "" && canonicalConfigPath(status.ConfigPath) != configPath {
		return fmt.Errorf("core: resident Core uses config %q but this command uses %q; stop it with `qrypt core stop` and retry", status.ConfigPath, configPath)
	}
	want, err := ownerlock.FingerprintFile(configPath)
	if err != nil {
		return fmt.Errorf("core: fingerprint config: %w", err)
	}
	if status.Fingerprint != "" && status.Fingerprint != want {
		return fmt.Errorf("core: resident Core was started with a different configuration; stop it with `qrypt core stop` and retry")
	}
	return nil
}

type statusFlags struct {
	json bool
}

func newStatusCommand(rt cliruntime.Runtime) *cobra.Command {
	flags := statusFlags{}
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Report whether a Core is running",
		Long: `Report the resident Core's identity and liveness: pid, version, config
fingerprint, state directory, uptime and whether it still has pending work.
Falls back to the ownership lock when no control endpoint answers.`,
		Example: `  qrypt core status
  qrypt core status --json`,
		Args: cliruntime.NoArgs(rt),
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runStatus(rt, cmd, flags)
		},
	}
	cmd.Flags().BoolVar(&flags.json, "json", false, "print machine-readable JSON")
	return cmd
}

type statusReport struct {
	State  string                 `json:"state"`
	Status *control.RuntimeStatus `json:"status,omitempty"`
	Owner  *ownerlock.Meta        `json:"owner,omitempty"`
	Detail string                 `json:"detail,omitempty"`
}

func runStatus(rt cliruntime.Runtime, cmd *cobra.Command, flags statusFlags) error {
	state, err := rt.CommandConfig(cmd)
	if err != nil {
		return err
	}
	if state.Cfg == nil {
		return rt.ConfigNotFoundError()
	}
	report := statusReport{State: "stopped"}
	runDir := corepkg.RuntimeRunDir(state.Path, state.Cfg)
	endpoint := corepkg.ControlSocketPath(state.Cfg, corepkg.RuntimeLayout{RunDir: runDir})
	if status, err := fetchStatus(cmd.Context(), endpoint); err == nil {
		report.State = "running"
		report.Status = &status
	} else {
		layout := corepkg.NewStorageLayout(state.Cfg, corepkg.RuntimeLayout{})
		if held, meta := ownerlock.Peek(layout.RootDir); held {
			report.State = "unreachable"
			report.Owner = &meta
			report.Detail = "the ownership lock is held but the control endpoint did not answer"
		}
	}
	out := cmd.OutOrStdout()
	if flags.json {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(report)
	}
	fmt.Fprintf(out, "state: %s\n", report.State)
	if report.Status != nil {
		printStatus(out, *report.Status)
	}
	if report.Owner != nil {
		fmt.Fprintf(out, "pid: %d\n", report.Owner.PID)
		if report.Owner.ConfigPath != "" {
			fmt.Fprintf(out, "config: %s\n", report.Owner.ConfigPath)
		}
		if !report.Owner.StartedAt.IsZero() {
			fmt.Fprintf(out, "started_at: %s\n", report.Owner.StartedAt.Format(time.RFC3339))
		}
	}
	if report.Detail != "" {
		fmt.Fprintf(out, "detail: %s\n", report.Detail)
	}
	return nil
}

type endpointFlags struct {
	json bool
}

// endpointReport is the machine-readable `core endpoint --json` payload. The
// endpoint is configuration-derived, so it is reported even when no Core is
// running; Running and the process fields then describe the reachable Core.
type endpointReport struct {
	Endpoint        string `json:"endpoint"`
	Network         string `json:"network"`
	RunDir          string `json:"run_dir,omitempty"`
	Running         bool   `json:"running"`
	PID             int    `json:"pid,omitempty"`
	ProtocolVersion int    `json:"protocol_version,omitempty"`
	Version         string `json:"version,omitempty"`
}

func newEndpointCommand(rt cliruntime.Runtime) *cobra.Command {
	flags := endpointFlags{}
	cmd := &cobra.Command{
		Use:   "endpoint",
		Short: "Print the control endpoint of this configuration",
		Long: `Print the control endpoint that an attached client must dial for this
configuration. The path is derived from the config file and the runtime layout,
so it is printed before a Core starts; "running" then says whether one answers.`,
		Example: `  qrypt core endpoint
  qrypt core endpoint --json`,
		Args: cliruntime.NoArgs(rt),
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runEndpoint(rt, cmd, flags)
		},
	}
	cmd.Flags().BoolVar(&flags.json, "json", false, "print machine-readable JSON")
	return cmd
}

func runEndpoint(rt cliruntime.Runtime, cmd *cobra.Command, flags endpointFlags) error {
	state, err := rt.CommandConfig(cmd)
	if err != nil {
		return err
	}
	if state.Cfg == nil {
		return rt.ConfigNotFoundError()
	}
	runDir := corepkg.RuntimeRunDir(state.Path, state.Cfg)
	endpoint := corepkg.ControlSocketPath(state.Cfg, corepkg.RuntimeLayout{RunDir: runDir})
	report := endpointReport{
		Endpoint: endpoint,
		Network:  endpointNetwork(endpoint),
		RunDir:   runDir,
	}
	if status, err := fetchStatus(cmd.Context(), endpoint); err == nil {
		report.Running = true
		report.PID = status.PID
		report.ProtocolVersion = status.ProtocolVersion
		report.Version = status.Version
	}
	out := cmd.OutOrStdout()
	if flags.json {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(report)
	}
	fmt.Fprintf(out, "endpoint: %s\n", report.Endpoint)
	fmt.Fprintf(out, "network: %s\n", report.Network)
	if report.RunDir != "" {
		fmt.Fprintf(out, "run_dir: %s\n", report.RunDir)
	}
	fmt.Fprintf(out, "running: %t\n", report.Running)
	if report.Running {
		fmt.Fprintf(out, "pid: %d\n", report.PID)
		fmt.Fprintf(out, "protocol_version: %d\n", report.ProtocolVersion)
		if report.Version != "" {
			fmt.Fprintf(out, "version: %s\n", report.Version)
		}
	}
	return nil
}

func endpointNetwork(endpoint string) string {
	switch {
	case strings.HasPrefix(endpoint, "tcp:"),
		strings.HasPrefix(endpoint, "http://"),
		strings.HasPrefix(endpoint, "https://"):
		return "tcp"
	default:
		return "unix"
	}
}

func printStatus(out io.Writer, status control.RuntimeStatus) {
	fmt.Fprintf(out, "pid: %d\n", status.PID)
	if status.Version != "" {
		fmt.Fprintf(out, "version: %s\n", status.Version)
	}
	if status.ConfigPath != "" {
		fmt.Fprintf(out, "config: %s\n", status.ConfigPath)
	}
	if status.WorkDir != "" {
		fmt.Fprintf(out, "work_dir: %s\n", status.WorkDir)
	}
	fmt.Fprintf(out, "started_at: %s\n", status.StartedAt.Format(time.RFC3339))
	fmt.Fprintf(out, "uptime_seconds: %d\n", status.UptimeSeconds)
	fmt.Fprintf(out, "busy: %t\n", status.Busy)
}

type stopFlags struct {
	drain bool
	force bool
}

func newStopCommand(rt cliruntime.Runtime) *cobra.Command {
	flags := stopFlags{}
	cmd := &cobra.Command{
		Use:   "stop",
		Short: "Stop a running Core",
		Long: `Ask the resident Core to shut down and wait until its control endpoint
disappears. --drain waits for pending work first; --force terminates the
owning process when the control endpoint cannot be asked (for example after a
crash left the ownership lock behind).`,
		Example: `  qrypt core stop
  qrypt core stop --drain
  qrypt core stop --force`,
		Args: cliruntime.NoArgs(rt),
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runStop(rt, cmd, flags)
		},
	}
	cmd.Flags().BoolVar(&flags.drain, "drain", false, "wait for pending tasks and uploads before stopping")
	cmd.Flags().BoolVar(&flags.force, "force", false, "terminate the owning process if the control endpoint does not answer")
	return cmd
}

func runStop(rt cliruntime.Runtime, cmd *cobra.Command, flags stopFlags) error {
	state, err := rt.CommandConfig(cmd)
	if err != nil {
		return err
	}
	if state.Cfg == nil {
		return rt.ConfigNotFoundError()
	}
	runDir := corepkg.RuntimeRunDir(state.Path, state.Cfg)
	endpoint := corepkg.ControlSocketPath(state.Cfg, corepkg.RuntimeLayout{RunDir: runDir})
	out := cmd.OutOrStdout()

	status, err := fetchStatus(cmd.Context(), endpoint)
	if err != nil {
		// No control endpoint: report what the lock says and stop there unless
		// --force asks us to clear a crashed owner.
		layout := corepkg.NewStorageLayout(state.Cfg, corepkg.RuntimeLayout{})
		held, meta := ownerlock.Peek(layout.RootDir)
		if !held {
			_, werr := fmt.Fprintln(out, "core is not running")
			return werr
		}
		if !flags.force {
			return fmt.Errorf("core: no control endpoint answered but pid %d still holds the ownership lock; retry with --force", meta.PID)
		}
		if err := terminateProcess(meta.PID); err != nil {
			return fmt.Errorf("core: terminate pid %d: %w", meta.PID, err)
		}
		_, werr := fmt.Fprintf(out, "core stopped (terminated pid %d)\n", meta.PID)
		return werr
	}
	if flags.drain && status.Busy {
		if err := waitUntilIdle(cmd.Context(), endpoint, stopTimeout); err != nil {
			return err
		}
	}
	client, err := control.NewClient(endpoint)
	if err != nil {
		return err
	}
	if _, err := client.PostJSON(cmd.Context(), "/v1/control/shutdown", map[string]bool{"drain": flags.drain}); err != nil {
		var transport *control.TransportError
		if errors.As(err, &transport) && transport.Status == 409 && flags.force {
			if termErr := terminateProcess(status.PID); termErr != nil {
				return fmt.Errorf("core: terminate pid %d: %w", status.PID, termErr)
			}
		} else {
			return err
		}
	}
	if err := waitForStop(cmd.Context(), endpoint, stopTimeout); err != nil {
		return err
	}
	_, err = fmt.Fprintln(out, "core stopped")
	return err
}

func waitUntilIdle(ctx context.Context, endpoint string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		status, err := fetchStatus(ctx, endpoint)
		if err != nil {
			return err
		}
		if !status.Busy {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("core: still busy after %s; use --force to stop anyway", timeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(statusPollInterval):
		}
	}
}

func waitForStop(ctx context.Context, endpoint string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		if _, err := fetchStatus(ctx, endpoint); err != nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("core: did not stop within %s", timeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(statusPollInterval):
		}
	}
}

func waitForStatus(ctx context.Context, endpoint string, timeout time.Duration) (control.RuntimeStatus, error) {
	deadline := time.Now().Add(timeout)
	for {
		status, err := fetchStatus(ctx, endpoint)
		if err == nil {
			return status, nil
		}
		if time.Now().After(deadline) {
			return control.RuntimeStatus{}, err
		}
		select {
		case <-ctx.Done():
			return control.RuntimeStatus{}, ctx.Err()
		case <-time.After(statusPollInterval):
		}
	}
}

func fetchStatus(ctx context.Context, endpoint string) (control.RuntimeStatus, error) {
	var status control.RuntimeStatus
	if endpoint == "" {
		return status, fmt.Errorf("core: no control endpoint for this configuration")
	}
	client, err := control.NewClient(endpoint)
	if err != nil {
		return status, err
	}
	body, err := client.Get(ctx, "/v1/control/status")
	if err != nil {
		return status, err
	}
	if err := json.Unmarshal(body, &status); err != nil {
		return status, err
	}
	return status, nil
}

func commandContext(cmd *cobra.Command) context.Context {
	if cmd.Context() != nil {
		return cmd.Context()
	}
	return context.Background()
}

// ensureCoreServeArgs builds the detached serve arguments used by lazy
// auto-start. It deliberately omits --keep: an auto-started Core must be able
// to idle-exit again (hosted mounts and in-flight work keep it alive).
func ensureCoreServeArgs(state cliruntime.ConfigState) []string {
	args := []string{"core", "serve", "--foreground"}
	if state.Path != "" {
		args = append(args, "--config", state.Path)
	}
	return args
}
