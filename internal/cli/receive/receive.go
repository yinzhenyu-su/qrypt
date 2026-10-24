// Package receive implements the provider-neutral `qrypt receive` command
// group. A link is the only source selector: its own provider is inferred by
// the Core, so no command needs a provider name, driver name, or socket path.
package receive

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/yinzhenyu/qrypt/internal/cli/controlctx"
	"github.com/yinzhenyu/qrypt/internal/cli/i18n"
	cliruntime "github.com/yinzhenyu/qrypt/internal/cli/runtime"
	"github.com/yinzhenyu/qrypt/pkg/control"
	shareapi "github.com/yinzhenyu/qrypt/pkg/share"
	"github.com/yinzhenyu/qrypt/pkg/task"
)

// ExitPartial marks a receive that completed with some entries failed.
const ExitPartial = 3

// NewCommand builds the `receive` command group.
func NewCommand(rt cliruntime.Runtime) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "receive",
		Short: "Inspect, save, or download content behind a share link",
		Long: `Inspect a share link, copy its content into your own drive provider-side
(转存), or download it locally. The link's provider is inferred from the link,
so no provider name is ever required.

  qrypt receive inspect https://pan.quark.cn/s/<id>
  qrypt receive save <link> --to /quark/inbox
  qrypt receive fetch <link> --download-to ./out`,
		Example: `  qrypt receive inspect https://pan.quark.cn/s/abc123
  qrypt receive save https://pan.quark.cn/s/abc123 --to /quark/inbox --mkdir
  qrypt receive fetch https://pan.quark.cn/s/abc123 --download-to ./out --keep-cloud
  qrypt receive list --all`,
		Args: cliruntime.CommandGroupArgs(rt, nil),
		RunE: cliruntime.ShowHelp,
	}
	rt.WithPersistentConfigFlag(cmd)
	cmd.AddCommand(newInspectCommand(rt))
	cmd.AddCommand(newSaveCommand(rt))
	cmd.AddCommand(newFetchCommand(rt))
	cmd.AddCommand(newListCommand(rt))
	cmd.AddCommand(newShowCommand(rt))
	cmd.AddCommand(newCancelCommand(rt))
	cmd.AddCommand(newRetryCommand(rt))
	cmd.AddCommand(newWaitCommand(rt))
	return cmd
}

func linkArgs(rt cliruntime.ErrorFactory, command string) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		switch {
		case len(args) == 0:
			return rt.UsageError(cmd, "missing LINK\n\nLINK is a provider share link or its bare id. For example:\n  qrypt receive %s https://pan.quark.cn/s/<id>", command)
		case len(args) > 1:
			return rt.UsageError(cmd, "too many arguments: %s\n\nPass exactly one LINK (quote a URL to keep its & and ? characters).", strings.Join(args[1:], " "))
		default:
			return nil
		}
	}
}

func taskIDArgs(rt cliruntime.ErrorFactory) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		switch {
		case len(args) == 0:
			return rt.UsageError(cmd, "missing TASK_ID\n\nRun 'qrypt receive list' to see task ids, or 'qrypt receive save' / 'qrypt receive fetch' to create one.")
		case len(args) > 1:
			return rt.UsageError(cmd, "too many arguments: %s\n\nPass exactly one TASK_ID.", strings.Join(args[1:], " "))
		default:
			return nil
		}
	}
}

func newInspectCommand(rt cliruntime.Runtime) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "inspect LINK",
		Short: "Browse a share link without saving anything",
		Long: `Open a share link and list one directory. Inspection is read-only: it
creates no task, saves nothing, and persists no session token.

A passcode comes from the link's ?pwd= query, from --passcode CODE, or from one
stdin line when --passcode is given without a value.`,
		Example: `  qrypt receive inspect https://pan.quark.cn/s/abc123
  qrypt receive inspect https://pan.quark.cn/s/abc123 --passcode
  qrypt receive inspect https://pan.quark.cn/s/abc123 --dir 1234 --json`,
		Args: linkArgs(rt, "inspect"),
		RunE: func(cmd *cobra.Command, args []string) error {
			jsonOut, _ := cmd.Flags().GetBool("json")
			client, err := controlctx.Client(cmd)
			if err != nil {
				return controlctx.FinishError(rt, err)
			}
			mount, err := resolveReceiveMount(rt, cmd, client, args[0], false)
			if err != nil {
				return err
			}
			passcode, err := resolvePasscode(rt, cmd)
			if err != nil {
				return err
			}
			inspect := func(passcode string) (shareapi.InspectResult, error) {
				return client.InspectShareLink(cmd.Context(), shareapi.InspectRequest{
					Link:     args[0],
					Passcode: passcode,
					Mount:    mount,
					Dir:      cliruntime.FlagStringValue(cmd, "dir"),
					Cursor:   cliruntime.FlagStringValue(cmd, "cursor"),
				})
			}
			preview, err := inspect(passcode)
			if err != nil && passcode == "" && passcodeRequired(err) && !jsonOut && stdinIsTerminal(cmd) {
				entered, promptErr := promptPasscode(rt, cmd)
				if promptErr == nil {
					preview, err = inspect(entered)
				}
			}
			if err != nil {
				return finishReceiveCall(rt, cmd, err)
			}
			if jsonOut {
				preview.Share = shareapi.RedactLink(preview.Share, false)
				if preview.Link.Passcode != "" {
					preview.Link.Passcode = shareapi.RedactedPasscode
				}
				return writeJSON(cmd, preview)
			}
			return writeInspect(cmd, preview)
		},
	}
	cmd.Flags().String("mount", "", "acting account when the provider has several mounts")
	cmd.Flags().String("dir", "", "share-relative directory id (empty = share root)")
	cmd.Flags().String("cursor", "", "continue from a previous page")
	cmd.Flags().Bool("json", false, "output the preview as JSON")
	passcodeFlag(cmd)
	return controlctx.WithEndpoint(rt, cmd)
}

func newSaveCommand(rt cliruntime.Runtime) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "save LINK",
		Short: "Copy a share's content into your own drive (转存)",
		Long: `Copy share content into your own drive provider-side. The destination
must belong to the link's provider; a cross-provider destination is rejected
because a native save never passes bytes through qrypt.

The command blocks until the save is verified at the destination. --no-wait
returns once the task is admitted.`,
		Example: `  qrypt receive save https://pan.quark.cn/s/abc123 --to /quark/inbox
  qrypt receive save https://pan.quark.cn/s/abc123 --mount quark --mkdir --select largest:2
  qrypt receive save https://pan.quark.cn/s/abc123 --to /quark/inbox --passcode --no-wait`,
		Args: linkArgs(rt, "save"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return createReceive(rt, cmd, args[0], false)
		},
	}
	cmd.Flags().String("to", "", "cloud destination directory (mount-qualified path)")
	cmd.Flags().String("mount", "", "acting account and destination mount root")
	cmd.Flags().Bool("mkdir", false, "create the destination directory if it does not exist")
	cmd.Flags().Bool("new", false, "bypass active de-duplication and create a new task")
	cmd.Flags().String("idempotency-key", "", "explicit idempotency key")
	cmd.Flags().Bool("no-wait", false, "return once the task is admitted instead of waiting for completion")
	cmd.Flags().Bool("json", false, "output the task snapshot as JSON")
	cmd.Flags().Bool("show-secrets", false, "print the passcode instead of masking it")
	cliruntime.MarkAdvancedFlag(cmd.Flags().Lookup("idempotency-key"))
	passcodeFlag(cmd)
	addSelectionFlags(cmd)
	return controlctx.WithEndpoint(rt, cmd)
}

func newFetchCommand(rt cliruntime.Runtime) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "fetch LINK",
		Short: "Download a share's content, with or without saving it first",
		Long: `Download share content to --download-to. With --to, qrypt first saves
into your drive and downloads that copy, removing the copy it created unless
--keep-cloud is given. Without --to, it downloads the share directly and leaves
the provider unchanged.

The destination may be a qrypt namespace path, a path inside the mount tree, or
a plain local path.`,
		Example: `  qrypt receive fetch https://pan.quark.cn/s/abc123 --download-to ./out
  qrypt receive fetch https://pan.quark.cn/s/abc123 --to /quark/inbox --download-to ./out --keep-cloud
  qrypt receive fetch https://pan.quark.cn/s/abc123 --download-to ~/Downloads --select largest:1 --wait`,
		Args: linkArgs(rt, "fetch"),
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(cliruntime.FlagStringValue(cmd, "download-to")) == "" {
				return rt.UsageError(cmd, "--download-to is required.\n\nIt names the final writable destination: a qrypt namespace path, a path inside the mount tree, or a plain local path. For example:\n  qrypt receive fetch LINK --download-to ~/Downloads")
			}
			return createReceive(rt, cmd, args[0], true)
		},
	}
	cmd.Flags().String("download-to", "", "final writable destination (namespace, FUSE, or local path)")
	cmd.Flags().String("to", "", "optional cloud destination to save into before downloading")
	cmd.Flags().String("mount", "", "acting account when the provider has several mounts")
	cmd.Flags().Bool("mkdir", false, "create the cloud destination if it does not exist")
	cmd.Flags().Bool("keep-cloud", false, "keep the saved cloud copy after a successful download")
	cmd.Flags().Bool("new", false, "bypass active de-duplication and create a new task")
	cmd.Flags().String("idempotency-key", "", "explicit idempotency key")
	cmd.Flags().Bool("no-wait", false, "return once the task is admitted instead of waiting for completion")
	cmd.Flags().Bool("json", false, "output the task snapshot as JSON")
	cmd.Flags().Bool("show-secrets", false, "print the passcode instead of masking it")
	cliruntime.MarkAdvancedFlag(cmd.Flags().Lookup("idempotency-key"))
	passcodeFlag(cmd)
	addSelectionFlags(cmd)
	addTransferFlags(cmd)
	return controlctx.WithEndpoint(rt, cmd)
}

// createReceive builds the shared save/fetch request and optionally waits for
// the task to reach a terminal state.
func createReceive(rt cliruntime.Runtime, cmd *cobra.Command, link string, fetch bool) error {
	jsonOut, _ := cmd.Flags().GetBool("json")
	reveal, _ := cmd.Flags().GetBool("show-secrets")
	noWait, _ := cmd.Flags().GetBool("no-wait")
	selection, err := resolveSelection(cmd)
	if err != nil {
		return rt.UsageError(cmd, "%s", err.Error())
	}
	client, err := controlctx.Client(cmd)
	if err != nil {
		return controlctx.FinishError(rt, err)
	}
	// A save without --to treats the chosen mount as the destination root, so
	// the account choice must be explicit; inspect and direct fetch can use the
	// only candidate automatically.
	mount, err := resolveReceiveMount(rt, cmd, client, link, !fetch)
	if err != nil {
		return err
	}
	passcode, err := resolvePasscode(rt, cmd)
	if err != nil {
		return err
	}
	req := shareapi.ReceiveRequest{
		Link:           link,
		Passcode:       passcode,
		Mount:          mount,
		To:             cliruntime.FlagStringValue(cmd, "to"),
		Selection:      selection,
		IdempotencyKey: cliruntime.FlagStringValue(cmd, "idempotency-key"),
	}
	req.Mkdir, _ = cmd.Flags().GetBool("mkdir")
	req.New, _ = cmd.Flags().GetBool("new")
	if fetch {
		downloadTo, err := controlctx.ResolveLocalDestination(cliruntime.FlagStringValue(cmd, "download-to"))
		if err != nil {
			return rt.UsageError(cmd, "resolve --download-to: %s", err.Error())
		}
		workers, chunkSize, err := transferTuning(cmd)
		if err != nil {
			return rt.UsageError(cmd, "%s", err.Error())
		}
		req.DownloadTo = downloadTo
		req.KeepCloud, _ = cmd.Flags().GetBool("keep-cloud")
		req.Workers = workers
		req.ChunkSize = chunkSize
	}
	created, err := client.CreateShareTask(cmd.Context(), req)
	if err != nil && req.Passcode == "" && passcodeRequired(err) && !jsonOut && stdinIsTerminal(cmd) {
		entered, promptErr := promptPasscode(rt, cmd)
		if promptErr == nil {
			req.Passcode = entered
			created, err = client.CreateShareTask(cmd.Context(), req)
		}
	}
	if err != nil {
		return finishReceiveCall(rt, cmd, err)
	}
	if noWait {
		current, err := client.GetShareTask(cmd.Context(), created.ID)
		if err != nil {
			return controlctx.FinishError(rt, controlctx.ConnectionError(err, "receive"))
		}
		if err := writeTask(cmd, jsonOut, reveal, current); err != nil {
			return err
		}
		fmt.Fprintln(cmd.ErrOrStderr(), i18n.FromCommand(cmd).T("out.receive.background", current.ID, current.ID))
		return nil
	}
	current, err := client.WaitShareTask(cmd.Context(), shareapi.WaitRequest{ID: created.ID})
	if err != nil {
		return controlctx.FinishError(rt, controlctx.ConnectionError(err, "receive"))
	}
	if err := writeTask(cmd, jsonOut, reveal, current); err != nil {
		return err
	}
	return taskOutcome(rt, cmd, current)
}

// taskOutcome maps a terminal receive task onto the process exit code.
func taskOutcome(rt cliruntime.Runtime, cmd *cobra.Command, current task.Task) error {
	switch current.State {
	case task.StateSucceeded:
		return nil
	case task.StatePartialFailed:
		return rt.ExitError(ExitPartial, fmt.Errorf("receive task %s ended as %s", current.ID, current.State))
	case task.StateFailed, task.StateCanceled:
		return rt.ExitError(controlctx.ExitFailure, fmt.Errorf("receive task %s ended as %s", current.ID, current.State))
	default:
		return nil
	}
}

func newListCommand(rt cliruntime.Runtime) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List receive tasks (active and failed by default)",
		Long: `List receive tasks. Active and failed tasks are shown by default; use
--all to include succeeded and canceled ones, and --state or --mount to narrow
the result.`,
		Example: `  qrypt receive list
  qrypt receive list --all --state failed --limit 50`,
		Args: cliruntime.NoArgs(rt),
		RunE: func(cmd *cobra.Command, args []string) error {
			jsonOut, _ := cmd.Flags().GetBool("json")
			reveal, _ := cmd.Flags().GetBool("show-secrets")
			limit, _ := cmd.Flags().GetInt("limit")
			all, _ := cmd.Flags().GetBool("all")
			client, err := controlctx.Client(cmd)
			if err != nil {
				return controlctx.FinishError(rt, err)
			}
			tasks, err := client.ListShareTasks(cmd.Context(), shareapi.ReceiveListRequest{
				All:   all,
				State: cliruntime.FlagStringValue(cmd, "state"),
				Mount: cliruntime.FlagStringValue(cmd, "mount"),
				Limit: limit,
			})
			if err != nil {
				return controlctx.FinishError(rt, controlctx.ConnectionError(err, "receive"))
			}
			if jsonOut {
				redacted := make([]task.Task, len(tasks))
				for i, item := range tasks {
					redacted[i] = shareapi.RedactTask(item, reveal)
				}
				return writeJSON(cmd, redacted)
			}
			return writeTaskList(cmd, tasks)
		},
	}
	cmd.Flags().Bool("all", false, "include succeeded and canceled tasks")
	cmd.Flags().String("state", "", "only tasks in this state")
	cmd.Flags().String("mount", "", "only tasks on this mount")
	cmd.Flags().Int("limit", 20, "maximum number of tasks")
	cmd.Flags().Bool("json", false, "output as JSON")
	cmd.Flags().Bool("show-secrets", false, "print passcodes instead of masking them")
	return controlctx.WithEndpoint(rt, cmd)
}

func newShowCommand(rt cliruntime.Runtime) *cobra.Command {
	return taskIDCommand(rt, "show", "Show one receive task", "Show one receive task by id, including its phase, destinations, and per-entry results.", func(cmd *cobra.Command, client *control.Client, id string) error {
		jsonOut, _ := cmd.Flags().GetBool("json")
		reveal, _ := cmd.Flags().GetBool("show-secrets")
		current, err := client.GetShareTask(cmd.Context(), id)
		if err != nil {
			return controlctx.FinishError(rt, controlctx.ConnectionError(err, "receive"))
		}
		return writeTask(cmd, jsonOut, reveal, current)
	}, true)
}

func newCancelCommand(rt cliruntime.Runtime) *cobra.Command {
	return taskIDCommand(rt, "cancel", "Cancel a receive task", "Request cancellation of a receive task. Cancellation stops observation; a provider-side copy that already started is left in place.", func(cmd *cobra.Command, client *control.Client, id string) error {
		if err := client.CancelShareTask(cmd.Context(), id); err != nil {
			return controlctx.FinishError(rt, controlctx.ConnectionError(err, "receive"))
		}
		fmt.Fprintln(cmd.OutOrStdout(), i18n.FromCommand(cmd).T("out.receive.cancel_requested", id))
		return nil
	}, false)
}

func newRetryCommand(rt cliruntime.Runtime) *cobra.Command {
	return taskIDCommand(rt, "retry", "Retry a failed or canceled receive task", "Retry a failed or canceled receive task from its persisted detail. A completed save is not repeated.", func(cmd *cobra.Command, client *control.Client, id string) error {
		if err := client.RetryShareTask(cmd.Context(), id); err != nil {
			return controlctx.FinishError(rt, controlctx.ConnectionError(err, "receive"))
		}
		fmt.Fprintln(cmd.OutOrStdout(), i18n.FromCommand(cmd).T("out.receive.retry_requested", id))
		return nil
	}, false)
}

func newWaitCommand(rt cliruntime.Runtime) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "wait TASK_ID",
		Short: "Observe a receive task without canceling it",
		Long: `Observe an existing receive task until it reaches a terminal state. An
interrupt stops only the observation; the Core task keeps running.`,
		Example: `  qrypt receive wait share-1a2b3c4d
  qrypt receive wait share-1a2b3c4d --timeout 10m --json`,
		Args: taskIDArgs(rt),
		RunE: func(cmd *cobra.Command, args []string) error {
			jsonOut, _ := cmd.Flags().GetBool("json")
			reveal, _ := cmd.Flags().GetBool("show-secrets")
			timeout, _ := cmd.Flags().GetDuration("timeout")
			client, err := controlctx.Client(cmd)
			if err != nil {
				return controlctx.FinishError(rt, err)
			}
			current, err := client.WaitShareTask(cmd.Context(), shareapi.WaitRequest{
				ID:            args[0],
				TimeoutMillis: timeout.Milliseconds(),
			})
			if err != nil {
				return controlctx.FinishError(rt, controlctx.ConnectionError(err, "receive"))
			}
			if err := writeTask(cmd, jsonOut, reveal, current); err != nil {
				return err
			}
			return taskOutcome(rt, cmd, current)
		},
	}
	cmd.Flags().Duration("timeout", 0, "give up after this duration (0 waits indefinitely)")
	cmd.Flags().Bool("json", false, "output the task snapshot as JSON")
	cmd.Flags().Bool("show-secrets", false, "print the passcode instead of masking it")
	return controlctx.WithEndpoint(rt, cmd)
}

func taskIDCommand(rt cliruntime.Runtime, use, short, long string, run func(*cobra.Command, *control.Client, string) error, withJSON bool) *cobra.Command {
	cmd := &cobra.Command{
		Use:     use + " TASK_ID",
		Short:   short,
		Long:    long,
		Example: fmt.Sprintf("  qrypt receive %s share-1a2b3c4d", use),
		Args:    taskIDArgs(rt),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := controlctx.Client(cmd)
			if err != nil {
				return controlctx.FinishError(rt, err)
			}
			return run(cmd, client, args[0])
		},
	}
	if withJSON {
		cmd.Flags().Bool("json", false, "output the task snapshot as JSON")
		cmd.Flags().Bool("show-secrets", false, "print the passcode instead of masking it")
	}
	return controlctx.WithEndpoint(rt, cmd)
}
