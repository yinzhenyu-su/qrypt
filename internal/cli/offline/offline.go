// Package offline implements the provider-neutral `qrypt offline` command
// group. Every command talks to a running Core over the local control socket;
// none of them needs a provider name, driver name, socket path, or provider
// task identifier.
package offline

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/yinzhenyu/qrypt/internal/cli/i18n"
	cliruntime "github.com/yinzhenyu/qrypt/internal/cli/runtime"
	"github.com/yinzhenyu/qrypt/pkg/config"
	"github.com/yinzhenyu/qrypt/pkg/control"
	"github.com/yinzhenyu/qrypt/pkg/core"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/task"
	"github.com/yinzhenyu/qrypt/pkg/util"
)

// Exit codes are the same stable set the CLI exposes elsewhere. Interrupts
// surface as 130 through the top-level ExitCode mapping of context.Canceled.
const (
	exitUsage   = 2
	exitFailure = 1
)

// NewCommand builds the `offline` command group.
func NewCommand(rt cliruntime.Runtime) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "offline",
		Short: "Submit and control provider-side offline downloads",
		Long: `Submit a source to a cloud drive's offline download service and control
the resulting provider task. Provider selection is the parse destination mount;
no provider name or task id is ever required.

  qrypt offline add magnet:?xt=urn:btih:... --parse-to /quark/downloads
  qrypt offline fetch ./file.torrent --parse-to /quark/downloads --download-to ./out
  qrypt offline list
  qrypt offline show <task-id>`,
		Example: `  qrypt offline add "magnet:?xt=urn:btih:..." --parse-to /quark/downloads
  qrypt offline fetch https://example.com/file.iso --mount quark --download-to ~/Downloads
  qrypt offline inspect ./movie.torrent --mount quark
  qrypt offline list --all`,
		Args: cliruntime.CommandGroupArgs(rt, nil),
		RunE: cliruntime.ShowHelp,
	}
	rt.WithPersistentConfigFlag(cmd)
	cmd.AddCommand(newAddCommand(rt))
	cmd.AddCommand(newFetchCommand(rt))
	cmd.AddCommand(newInspectCommand(rt))
	cmd.AddCommand(newListCommand(rt))
	cmd.AddCommand(newShowCommand(rt))
	cmd.AddCommand(newCancelCommand(rt))
	cmd.AddCommand(newRetryCommand(rt))
	cmd.AddCommand(newWaitCommand(rt))
	return cmd
}

// sourceArgs validates the SOURCE positional with a hint that explains what a
// source is, instead of a bare "missing SOURCE".
func sourceArgs(rt cliruntime.ErrorFactory, command string) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		switch {
		case len(args) == 0:
			return rt.UsageError(cmd, "missing SOURCE\n\nSOURCE is a magnet link, an HTTP(S)/FTP URL, or a local .torrent file. For example:\n  qrypt offline %s \"magnet:?xt=urn:btih:...\" --parse-to /MOUNT/DIR", command)
		case len(args) > 1:
			return rt.UsageError(cmd, "too many arguments: %s\n\nPass exactly one SOURCE (quote a magnet link or URL to keep its & and ? characters).", strings.Join(args[1:], " "))
		default:
			return nil
		}
	}
}

// taskIDArgs validates the TASK_ID positional with a hint pointing at how to
// find one.
func taskIDArgs(rt cliruntime.ErrorFactory) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		switch {
		case len(args) == 0:
			return rt.UsageError(cmd, "missing TASK_ID\n\nRun 'qrypt offline list' to see task ids, or 'qrypt offline add' / 'qrypt offline fetch' to create one.")
		case len(args) > 1:
			return rt.UsageError(cmd, "too many arguments: %s\n\nPass exactly one TASK_ID.", strings.Join(args[1:], " "))
		default:
			return nil
		}
	}
}

// requireProviderSelection rejects a request that never names the provider
// mount before any Core round trip.
func requireProviderSelection(rt cliruntime.ErrorFactory, cmd *cobra.Command) error {
	parseTo := strings.TrimSpace(cliruntime.FlagStringValue(cmd, "parse-to"))
	mount := strings.TrimSpace(cliruntime.FlagStringValue(cmd, "mount"))
	if parseTo == "" && mount == "" {
		return rt.UsageError(cmd, "--parse-to or --mount is required to select the provider mount.\n\nUse --parse-to /MOUNT/DIR to also choose the staging directory, or --mount NAME to use the provider's default directory. For example:\n  qrypt offline %s SOURCE --parse-to /quark/downloads", cmd.Name())
	}
	return nil
}

type endpointContextKey struct{}

// withOfflineEndpoint resolves the control socket for the selected config and
// injects it into the command context.
func withOfflineEndpoint(rt cliruntime.Runtime, cmd *cobra.Command) *cobra.Command {
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

func offlineClient(cmd *cobra.Command) (*control.Client, error) {
	endpoint, _ := cmd.Context().Value(endpointContextKey{}).(string)
	client, err := control.NewClient(endpoint)
	if err != nil {
		return nil, err
	}
	return client, nil
}

// prepareOfflineSource normalizes a source in the CLI's process before it is
// sent to the Core. A local .torrent path must be read relative to the
// caller's working directory, not the daemon's, and doing the same validation
// here gives add/fetch/inspect one consistent usage error.
func prepareOfflineSource(rt cliruntime.ErrorFactory, cmd *cobra.Command, raw string) (source, kind string, err error) {
	normalized, err := core.NormalizeOfflineSource(raw)
	if err != nil {
		return "", "", rt.UsageError(cmd, "%s", err.Error())
	}
	return normalized.Source, string(normalized.Kind), nil
}

// resolveLocalDestination resolves a relative local download path against the
// caller's working directory before it crosses the control socket. A
// namespace path (leading slash) is left untouched, and a trailing separator
// is preserved so the Core still sees the destination as a directory.
func resolveLocalDestination(raw string) (string, error) {
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

// noCoreError maps a connection failure to a clear runtime error.
func noCoreError(err error) error {
	if err == nil {
		return nil
	}
	var transport *control.TransportError
	if errors.As(err, &transport) {
		return err
	}
	if isConnectionError(err) {
		return fmt.Errorf("offline commands require a running Core (start `qrypt mount`); no Core is listening: %w", err)
	}
	return err
}

func isConnectionError(err error) bool {
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

// finishError converts a command error to the process exit code. Interrupts are
// preserved so the runner reports 130; a usage-classified transport error is 2;
// everything else is a runtime failure (1).
func finishError(rt cliruntime.Runtime, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return err
	}
	var transport *control.TransportError
	if errors.As(err, &transport) {
		if transport.Status == http.StatusBadRequest {
			return rt.ExitError(exitUsage, err)
		}
		return rt.ExitError(exitFailure, err)
	}
	return rt.ExitError(exitFailure, err)
}

// transferTuning resolves the fetch transfer flags: how many download
// connections run at once and how many bytes each request covers. Zero keeps
// the Core default, and the bounds match the contract the Core validates, so a
// bad value is reported as usage before a task exists.
func transferTuning(cmd *cobra.Command) (workers int, chunkSize int64, err error) {
	workers, _ = cmd.Flags().GetInt("workers")
	if workers < 0 || workers > task.MaxOfflineTransferWorkers {
		return 0, 0, fmt.Errorf("--workers must be between 1 and %d (0 uses the default %d)", task.MaxOfflineTransferWorkers, task.DefaultOfflineTransferWorkers)
	}
	raw := strings.TrimSpace(cliruntime.FlagStringValue(cmd, "chunk-size"))
	if raw == "" {
		return workers, 0, nil
	}
	chunkSize, err = config.ParseSize(raw)
	if err != nil {
		return 0, 0, fmt.Errorf("--chunk-size %q: %w", raw, err)
	}
	switch {
	case chunkSize < task.MinOfflineTransferChunk:
		return 0, 0, fmt.Errorf("--chunk-size must be at least %s", task.OfflineByteSize(task.MinOfflineTransferChunk))
	case chunkSize > task.MaxOfflineTransferChunk:
		return 0, 0, fmt.Errorf("--chunk-size must be at most %s", task.OfflineByteSize(task.MaxOfflineTransferChunk))
	}
	return workers, chunkSize, nil
}

// resolveSelection builds the provider-neutral selection from the shared
// selector flags.
func resolveSelection(cmd *cobra.Command) (task.OfflineSelection, error) {
	selectValue := strings.TrimSpace(cliruntime.FlagStringValue(cmd, "select"))
	include, _ := cmd.Flags().GetStringArray("include")
	exclude, _ := cmd.Flags().GetStringArray("exclude")

	if selectValue != "" && (len(include) > 0 || len(exclude) > 0) {
		return task.OfflineSelection{}, fmt.Errorf("--select cannot be combined with --include/--exclude")
	}
	switch {
	case selectValue == "" || selectValue == "all":
		if selectValue == "" && (len(include) > 0 || len(exclude) > 0) {
			if len(include) == 0 {
				return task.OfflineSelection{}, fmt.Errorf("--exclude requires --include")
			}
			return task.OfflineSelection{Mode: task.OfflineSelectionInclude, Include: include, Exclude: exclude}, nil
		}
		if selectValue == "all" && (len(include) > 0 || len(exclude) > 0) {
			return task.OfflineSelection{}, fmt.Errorf("--select all cannot be combined with --include/--exclude")
		}
		return task.OfflineSelection{Mode: task.OfflineSelectionAll}, nil
	case selectValue == "largest":
		return task.OfflineSelection{Mode: task.OfflineSelectionLargest, Largest: 1}, nil
	case strings.HasPrefix(selectValue, "largest:"):
		count, err := strconv.Atoi(strings.TrimPrefix(selectValue, "largest:"))
		if err != nil || count < 1 {
			return task.OfflineSelection{}, fmt.Errorf("--select largest:N requires a positive N")
		}
		return task.OfflineSelection{Mode: task.OfflineSelectionLargest, Largest: count}, nil
	default:
		return task.OfflineSelection{}, fmt.Errorf("unsupported --select value %q (use all or largest[:N])", selectValue)
	}
}

func addSelectionFlags(cmd *cobra.Command) {
	cmd.Flags().String("select", "", "file selection: all or largest[:N]")
	cmd.Flags().StringArray("include", nil, "include file name glob (repeatable)")
	cmd.Flags().StringArray("exclude", nil, "exclude file name glob (repeatable)")
}

func newAddCommand(rt cliruntime.Runtime) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "add SOURCE",
		Short: "Submit a source as a cloud-side offline download",
		Long: `Submit a magnet link, HTTP(S)/FTP URL, or local .torrent file to the
provider's offline download service. The parse destination mount selects the
provider; the cloud-side result stays on the provider.`,
		Example: `  qrypt offline add "magnet:?xt=urn:btih:..." --parse-to /quark/downloads
  qrypt offline add ./movie.torrent --mount quark --mkdir
  qrypt offline add https://example.com/file.iso --parse-to /quark/downloads --select largest:1 --wait`,
		Args: sourceArgs(rt, "add"),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireProviderSelection(rt, cmd); err != nil {
				return err
			}
			source, sourceKind, err := prepareOfflineSource(rt, cmd, args[0])
			if err != nil {
				return err
			}
			selection, err := resolveSelection(cmd)
			if err != nil {
				return rt.UsageError(cmd, "%s", err.Error())
			}
			jsonOut, _ := cmd.Flags().GetBool("json")
			wait, _ := cmd.Flags().GetBool("wait")
			client, err := offlineClient(cmd)
			if err != nil {
				return finishError(rt, err)
			}
			req := control.OfflineCreateRequest{
				Source:         source,
				SourceKind:     sourceKind,
				Title:          cliruntime.FlagStringValue(cmd, "name"),
				Mount:          cliruntime.FlagStringValue(cmd, "mount"),
				ParseTo:        cliruntime.FlagStringValue(cmd, "parse-to"),
				Selection:      selection,
				IdempotencyKey: cliruntime.FlagStringValue(cmd, "idempotency-key"),
			}
			req.Mkdir, _ = cmd.Flags().GetBool("mkdir")
			req.New, _ = cmd.Flags().GetBool("new")
			created, err := client.CreateOfflineTask(cmd.Context(), req)
			if err != nil {
				return finishError(rt, noCoreError(err))
			}
			current, err := awaitFirstObservation(cmd.Context(), client, created, wait)
			if err != nil {
				return finishError(rt, noCoreError(err))
			}
			if err := writeTask(cmd, jsonOut, current); err != nil {
				return err
			}
			if current.State == task.StateFailed || current.State == task.StateCanceled {
				return rt.ExitError(exitFailure, fmt.Errorf("offline task %s ended as %s", current.ID, current.State))
			}
			return nil
		},
	}
	cmd.Flags().String("parse-to", "", "provider-side staging directory (mount-qualified path)")
	cmd.Flags().String("mount", "", "provider mount when --parse-to is omitted (uses the provider default directory)")
	cmd.Flags().Bool("mkdir", false, "create the parse destination if it does not exist")
	cmd.Flags().String("name", "", "provider-side resource title")
	cmd.Flags().Bool("new", false, "bypass active de-duplication and create a new task")
	cmd.Flags().String("idempotency-key", "", "explicit idempotency key")
	cmd.Flags().Bool("wait", false, "wait for the task to reach a terminal state")
	cmd.Flags().Bool("json", false, "output the task snapshot as JSON")
	cliruntime.MarkAdvancedFlag(cmd.Flags().Lookup("idempotency-key"))
	addSelectionFlags(cmd)
	return withOfflineEndpoint(rt, cmd)
}

func newFetchCommand(rt cliruntime.Runtime) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "fetch SOURCE",
		Short: "Stage a source in the cloud, then transfer it to a final destination",
		Long: `Stage a source on the provider, then transfer the result to --download-to.
The destination may be a qrypt namespace path, a path inside the mount tree, or
a plain local path. When it is a directory, each selected file is written under
it at its staged relative path, recreating the provider's folders; --flat writes
by file name instead. By default the command prints the task and returns while
the transfer continues in the Core; --wait blocks until the destination is
written and renders progress.`,
		Example: `  qrypt offline fetch "magnet:?xt=urn:btih:..." --parse-to /quark/downloads --download-to ~/Downloads
  qrypt offline fetch https://example.com/file.iso --parse-to /quark/downloads --download-to ./out/ --keep-cloud
  qrypt offline fetch "magnet:?xt=urn:btih:..." --mount quark --download-to ~/Downloads --wait`,
		Args: sourceArgs(rt, "fetch"),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireProviderSelection(rt, cmd); err != nil {
				return err
			}
			source, sourceKind, err := prepareOfflineSource(rt, cmd, args[0])
			if err != nil {
				return err
			}
			selection, err := resolveSelection(cmd)
			if err != nil {
				return rt.UsageError(cmd, "%s", err.Error())
			}
			jsonOut, _ := cmd.Flags().GetBool("json")
			wait, _ := cmd.Flags().GetBool("wait")
			downloadTo, err := resolveLocalDestination(cliruntime.FlagStringValue(cmd, "download-to"))
			if err != nil {
				return rt.UsageError(cmd, "resolve --download-to: %s", err.Error())
			}
			if strings.TrimSpace(downloadTo) == "" {
				return rt.UsageError(cmd, "--download-to is required.\n\nIt names the final writable destination: a qrypt namespace path, a path inside the mount tree, or a plain local path. For example:\n  qrypt offline fetch SOURCE --parse-to /MOUNT/DIR --download-to ~/Downloads")
			}
			client, err := offlineClient(cmd)
			if err != nil {
				return finishError(rt, err)
			}
			req := control.OfflineCreateRequest{
				Source:         source,
				SourceKind:     sourceKind,
				Title:          cliruntime.FlagStringValue(cmd, "name"),
				Mount:          cliruntime.FlagStringValue(cmd, "mount"),
				ParseTo:        cliruntime.FlagStringValue(cmd, "parse-to"),
				DownloadTo:     downloadTo,
				Selection:      selection,
				IdempotencyKey: cliruntime.FlagStringValue(cmd, "idempotency-key"),
			}
			req.Mkdir, _ = cmd.Flags().GetBool("mkdir")
			req.New, _ = cmd.Flags().GetBool("new")
			req.KeepCloud, _ = cmd.Flags().GetBool("keep-cloud")
			req.Flat, _ = cmd.Flags().GetBool("flat")
			workers, chunkSize, err := transferTuning(cmd)
			if err != nil {
				return rt.UsageError(cmd, "%s", err.Error())
			}
			req.Workers = workers
			req.ChunkSize = chunkSize
			created, err := client.CreateOfflineTask(cmd.Context(), req)
			if err != nil {
				return finishError(rt, noCoreError(err))
			}
			if !wait {
				// By default the command returns once the provider task is
				// observable; the Core task keeps transferring in the
				// background. Use --wait to block until the destination is
				// written.
				current, err := awaitFirstObservation(cmd.Context(), client, created, false)
				if err != nil {
					return finishError(rt, noCoreError(err))
				}
				if err := writeTask(cmd, jsonOut, current); err != nil {
					return err
				}
				fmt.Fprintln(cmd.ErrOrStderr(), i18n.FromCommand(cmd).T("out.offline.background", current.ID, current.ID))
				if current.State == task.StateFailed || current.State == task.StateCanceled {
					return rt.ExitError(exitFailure, fmt.Errorf("offline fetch %s ended as %s", current.ID, current.State))
				}
				return nil
			}
			current, err := waitWithReconnect(cmd, created.ID, 0, newOfflineProgress(cmd.ErrOrStderr(), i18n.FromCommand(cmd)))
			if err != nil {
				// An interrupt detaches the wait without canceling the task.
				return finishError(rt, noCoreError(err))
			}
			if err := writeTask(cmd, jsonOut, current); err != nil {
				return err
			}
			if current.State != task.StateSucceeded {
				return rt.ExitError(exitFailure, fmt.Errorf("offline fetch %s ended as %s", current.ID, current.State))
			}
			return nil
		},
	}
	cmd.Flags().String("parse-to", "", "provider-side staging directory (mount-qualified path)")
	cmd.Flags().String("mount", "", "provider mount when --parse-to is omitted")
	cmd.Flags().String("download-to", "", "final writable destination (namespace, FUSE, or local path)")
	cmd.Flags().Bool("keep-cloud", false, "keep the staged cloud copy after a successful transfer")
	cmd.Flags().Int("workers", 0, fmt.Sprintf("concurrent download connections (1-%d, 0 = default %d)", task.MaxOfflineTransferWorkers, task.DefaultOfflineTransferWorkers))
	cmd.Flags().String("chunk-size", "", fmt.Sprintf("bytes each download request covers, e.g. 4M (%s-%s, empty = default %s)", task.OfflineByteSize(task.MinOfflineTransferChunk), task.OfflineByteSize(task.MaxOfflineTransferChunk), task.OfflineByteSize(task.DefaultOfflineTransferChunk)))
	cmd.Flags().Bool("wait", false, "block until the final destination is written, rendering progress")
	cmd.Flags().Bool("no-wait", false, "deprecated no-op; returning before the transfer finishes is now the default")
	cmd.Flags().Bool("flat", false, "write every file by its own name instead of mirroring the staged folders")
	cmd.Flags().Bool("mkdir", false, "create the parse destination if it does not exist")
	cmd.Flags().String("name", "", "provider-side resource title")
	cmd.Flags().Bool("new", false, "bypass active de-duplication and create a new task")
	cmd.Flags().String("idempotency-key", "", "explicit idempotency key")
	cmd.Flags().Bool("json", false, "output the task snapshot as JSON")
	cliruntime.MarkAdvancedFlag(cmd.Flags().Lookup("workers"))
	cliruntime.MarkAdvancedFlag(cmd.Flags().Lookup("chunk-size"))
	cliruntime.MarkAdvancedFlag(cmd.Flags().Lookup("idempotency-key"))
	addSelectionFlags(cmd)
	return withOfflineEndpoint(rt, cmd)
}

func newInspectCommand(rt cliruntime.Runtime) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "inspect SOURCE",
		Short: "Preview a source without creating a task",
		Long: `Parse a source and report its files, total size, instant-save prediction,
and parse-destination checks without creating a task.`,
		Example: `  qrypt offline inspect "magnet:?xt=urn:btih:..." --parse-to /quark/downloads
  qrypt offline inspect ./movie.torrent --mount quark --select largest:3`,
		Args: sourceArgs(rt, "inspect"),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireProviderSelection(rt, cmd); err != nil {
				return err
			}
			source, sourceKind, err := prepareOfflineSource(rt, cmd, args[0])
			if err != nil {
				return err
			}
			selection, err := resolveSelection(cmd)
			if err != nil {
				return rt.UsageError(cmd, "%s", err.Error())
			}
			jsonOut, _ := cmd.Flags().GetBool("json")
			client, err := offlineClient(cmd)
			if err != nil {
				return finishError(rt, err)
			}
			preview, err := client.InspectOffline(cmd.Context(), control.OfflineInspectRequest{
				Source:     source,
				SourceKind: sourceKind,
				Title:      cliruntime.FlagStringValue(cmd, "name"),
				Mount:      cliruntime.FlagStringValue(cmd, "mount"),
				ParseTo:    cliruntime.FlagStringValue(cmd, "parse-to"),
				Selection:  selection,
			})
			if err != nil {
				return finishError(rt, noCoreError(err))
			}
			if jsonOut {
				return util.WritePrettyJSON(cmd.OutOrStdout(), preview)
			}
			return writePreview(cmd, preview)
		},
	}
	cmd.Flags().String("parse-to", "", "provider-side staging directory to check (optional)")
	cmd.Flags().String("mount", "", "provider mount when --parse-to is omitted")
	cmd.Flags().String("name", "", "provider-side resource title")
	cmd.Flags().Bool("json", false, "output the preview as JSON")
	addSelectionFlags(cmd)
	return withOfflineEndpoint(rt, cmd)
}

func newListCommand(rt cliruntime.Runtime) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List offline tasks (active and failed by default)",
		Long: `List offline tasks. Active and failed tasks are shown by default; use
--all to include succeeded and canceled ones, and --state or --mount to
narrow the result.`,
		Example: `  qrypt offline list
  qrypt offline list --all --state failed --limit 50`,
		Args: cliruntime.NoArgs(rt),
		RunE: func(cmd *cobra.Command, args []string) error {
			jsonOut, _ := cmd.Flags().GetBool("json")
			limit, _ := cmd.Flags().GetInt("limit")
			all, _ := cmd.Flags().GetBool("all")
			client, err := offlineClient(cmd)
			if err != nil {
				return finishError(rt, err)
			}
			tasks, err := client.ListOfflineTasks(cmd.Context(), control.OfflineListRequest{
				All:   all,
				State: cliruntime.FlagStringValue(cmd, "state"),
				Mount: cliruntime.FlagStringValue(cmd, "mount"),
				Limit: limit,
			})
			if err != nil {
				return finishError(rt, noCoreError(err))
			}
			if jsonOut {
				return util.WritePrettyJSON(cmd.OutOrStdout(), tasks)
			}
			return writeTaskList(cmd, tasks)
		},
	}
	cmd.Flags().Bool("all", false, "include succeeded and canceled tasks")
	cmd.Flags().String("state", "", "only tasks in this state")
	cmd.Flags().String("mount", "", "only tasks on this mount")
	cmd.Flags().Int("limit", 20, "maximum number of tasks")
	cmd.Flags().Bool("json", false, "output as JSON")
	return withOfflineEndpoint(rt, cmd)
}

func newShowCommand(rt cliruntime.Runtime) *cobra.Command {
	return taskIDCommand(rt, "show", "Show one offline task", "Show one offline task by id, including its state and, when available, the provider observation.", func(cmd *cobra.Command, client *control.Client, id string) error {
		jsonOut, _ := cmd.Flags().GetBool("json")
		current, err := client.GetOfflineTask(cmd.Context(), id)
		if err != nil {
			return finishError(rt, noCoreError(err))
		}
		return writeTask(cmd, jsonOut, current)
	}, true)
}

func newCancelCommand(rt cliruntime.Runtime) *cobra.Command {
	return taskIDCommand(rt, "cancel", "Cancel an offline task", "Request cancellation of an offline task. Cancellation is cooperative; observe the task to confirm it reaches the canceled state.", func(cmd *cobra.Command, client *control.Client, id string) error {
		if err := client.CancelOfflineTask(cmd.Context(), id); err != nil {
			return finishError(rt, noCoreError(err))
		}
		fmt.Fprintln(cmd.OutOrStdout(), i18n.FromCommand(cmd).T("out.offline.cancel_requested", id))
		return nil
	}, false)
}

func newRetryCommand(rt cliruntime.Runtime) *cobra.Command {
	return taskIDCommand(rt, "retry", "Retry a failed or canceled offline task", "Retry a failed or canceled offline task, starting a fresh attempt under the same task id.", func(cmd *cobra.Command, client *control.Client, id string) error {
		if err := client.RetryOfflineTask(cmd.Context(), id); err != nil {
			return finishError(rt, noCoreError(err))
		}
		fmt.Fprintln(cmd.OutOrStdout(), i18n.FromCommand(cmd).T("out.offline.retry_requested", id))
		return nil
	}, false)
}

func newWaitCommand(rt cliruntime.Runtime) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "wait TASK_ID",
		Short: "Observe a task to a terminal state without canceling it",
		Long: `Observe an existing offline task until it reaches a terminal state. An
interrupt stops only the observation; the Core task keeps running.`,
		Example: `  qrypt offline wait offline-1a2b3c4d
  qrypt offline wait offline-1a2b3c4d --timeout 10m --json`,
		Args: taskIDArgs(rt),
		RunE: func(cmd *cobra.Command, args []string) error {
			jsonOut, _ := cmd.Flags().GetBool("json")
			timeout, _ := cmd.Flags().GetDuration("timeout")
			current, err := waitWithReconnect(cmd, args[0], timeout, newOfflineProgress(cmd.ErrOrStderr(), i18n.FromCommand(cmd)))
			if err != nil {
				return finishError(rt, noCoreError(err))
			}
			if err := writeTask(cmd, jsonOut, current); err != nil {
				return err
			}
			if current.State != task.StateSucceeded {
				return rt.ExitError(exitFailure, fmt.Errorf("offline task %s ended as %s", current.ID, current.State))
			}
			return nil
		},
	}
	cmd.Flags().Duration("timeout", 0, "give up after this duration (0 waits indefinitely)")
	cmd.Flags().Bool("json", false, "output the task snapshot as JSON")
	return withOfflineEndpoint(rt, cmd)
}

func taskIDCommand(rt cliruntime.Runtime, use, short, long string, run func(*cobra.Command, *control.Client, string) error, withJSON bool) *cobra.Command {
	cmd := &cobra.Command{
		Use:     use + " TASK_ID",
		Short:   short,
		Long:    long,
		Example: fmt.Sprintf("  qrypt offline %s offline-1a2b3c4d", use),
		Args:    taskIDArgs(rt),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := offlineClient(cmd)
			if err != nil {
				return finishError(rt, err)
			}
			return run(cmd, client, args[0])
		},
	}
	if withJSON {
		cmd.Flags().Bool("json", false, "output the task snapshot as JSON")
	}
	return withOfflineEndpoint(rt, cmd)
}

// waitWithReconnect observes a task to a terminal state, rendering progress and
// reconnecting to a restarted Core and resuming by task id. The local timeout is
// enforced across reconnects and surfaces as the same typed timeout the Core
// uses.
func waitWithReconnect(cmd *cobra.Command, id string, timeout time.Duration, progress *offlineProgress) (task.Task, error) {
	deadline := time.Time{}
	if timeout > 0 {
		deadline = time.Now().Add(timeout)
	}
	for {
		if err := cmd.Context().Err(); err != nil {
			progress.finish()
			return task.Task{}, err
		}
		remaining, hasRemaining := time.Duration(0), !deadline.IsZero()
		if hasRemaining {
			remaining = time.Until(deadline)
			if remaining <= 0 {
				progress.finish()
				return task.Task{}, drive.NewOfflineError(drive.OfflineErrWaitTimeout, "", fmt.Sprintf("offline task %s did not finish within %s", id, timeout))
			}
		}
		client, err := offlineClient(cmd)
		if err != nil {
			progress.finish()
			return task.Task{}, err
		}
		// Bound each request by the poll interval and by whatever remains of
		// the caller's timeout. A server that accepts the connection and then
		// stalls must not outlive the wait's own deadline.
		requestTimeout := 700 * time.Millisecond
		if hasRemaining && remaining < requestTimeout {
			requestTimeout = remaining
		}
		requestCtx, cancel := context.WithTimeout(cmd.Context(), requestTimeout)
		current, err := client.GetOfflineTask(requestCtx, id)
		cancel()
		if err == nil {
			progress.render(current)
			if offlineTaskDone(current.State) {
				progress.finish()
				return current, nil
			}
		} else {
			if ctxErr := cmd.Context().Err(); ctxErr != nil {
				progress.finish()
				return task.Task{}, ctxErr
			}
			if hasRemaining && !time.Now().Before(deadline) {
				progress.finish()
				return task.Task{}, drive.NewOfflineError(drive.OfflineErrWaitTimeout, "", fmt.Sprintf("offline task %s did not finish within %s", id, timeout))
			}
			if !isConnectionError(err) && !errors.Is(err, context.DeadlineExceeded) {
				progress.finish()
				return task.Task{}, err
			}
		}
		// Poll for progress; after a Core restart (a connection error) back off
		// a little before reconnecting and resuming by task id.
		delay := 700 * time.Millisecond
		if err != nil {
			delay = time.Second
		}
		if hasRemaining {
			left := time.Until(deadline)
			if left <= 0 {
				progress.finish()
				return task.Task{}, drive.NewOfflineError(drive.OfflineErrWaitTimeout, "", fmt.Sprintf("offline task %s did not finish within %s", id, timeout))
			}
			if left < delay {
				delay = left
			}
		}
		select {
		case <-cmd.Context().Done():
			progress.finish()
			return task.Task{}, cmd.Context().Err()
		case <-time.After(delay):
		}
	}
}

// awaitFirstObservation waits until the provider task is observable (a detail
// observation exists) or the task is terminal. When wait is set it waits for a
// terminal state instead.
func awaitFirstObservation(ctx context.Context, client *control.Client, created task.Task, wait bool) (task.Task, error) {
	if wait {
		return client.WaitOfflineTask(ctx, control.OfflineWaitRequest{ID: created.ID})
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		current, err := client.GetOfflineTask(ctx, created.ID)
		if err != nil {
			return task.Task{}, err
		}
		if current.State == task.StateFailed || current.State == task.StateCanceled ||
			current.State == task.StateSucceeded || hasObservation(current) {
			return current, nil
		}
		if time.Now().After(deadline) {
			return current, nil
		}
		select {
		case <-ctx.Done():
			return current, ctx.Err()
		case <-time.After(300 * time.Millisecond):
		}
	}
}

func hasObservation(item task.Task) bool {
	detail, ok := item.Detail[task.OfflineDetailKey].(map[string]any)
	if !ok {
		return false
	}
	observation, _ := detail["observation"].(map[string]any)
	return observation != nil && observation["state"] != ""
}
