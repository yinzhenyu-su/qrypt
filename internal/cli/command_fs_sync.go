package cli

import (
	"context"
	"errors"
	"fmt"

	"github.com/spf13/cobra"
	"github.com/yinzhenyu/qrypt/internal/sync"
)

func newFsSyncCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sync SOURCE DESTINATION",
		Short: "Make DESTINATION match SOURCE (one-way, non-destructive by default)",
		Long: `Synchronize the tree at DESTINATION to match the tree at SOURCE.

SOURCE is authoritative; DESTINATION is modified. Files missing on
DESTINATION are added, changed files are updated, and extra files on
DESTINATION are left alone unless --delete is given. Type conflicts
(file vs directory on the same path) are reported and skipped by
default; use --conflict=source to let SOURCE win.

At least one side must be a virtual path (/MOUNT/dir). Local paths are
compared against the VFS plaintext view (encryption is transparent).

Exit code: 0 on success (also for --dry-run plans), 3 when some files
failed, 2 on usage errors, 1 on overall failures.`,
		Args:              exactNamedArgs("SOURCE", "DESTINATION"),
		RunE:              runFsSync,
		ValidArgsFunction: noFileCompletions,
	}
	cmd.Flags().Bool("dry-run", false, "show the sync plan without changing anything")
	cmd.Flags().Bool("delete", false, "delete files on DESTINATION that are not on SOURCE")
	cmd.Flags().Bool("hash", false, "also compare content hashes (needs backend hash support)")
	cmd.Flags().String("compare", "size-mtime", "comparison strategy: size-mtime, mtime-only, or hash")
	cmd.Flags().String("conflict", "error", "type-conflict policy: error, skip, or source")
	cmd.Flags().Bool("json", false, "write JSON output")
	cmd.Flags().Bool("resume", false, "continue the interrupted sync session for SOURCE and DESTINATION")
	return cmd
}

func runFsSync(cmd *cobra.Command, args []string) error {
	state, err := commandConfig(cmd)
	if err != nil {
		return err
	}
	if state.cfg == nil {
		return configNotFoundError()
	}
	conflictPolicy, _ := cmd.Flags().GetString("conflict")
	switch conflictPolicy {
	case "error", "skip", "source":
	default:
		return commandUsageError(cmd, "--conflict must be error, skip, or source (got %q)", conflictPolicy)
	}
	dryRun, _ := cmd.Flags().GetBool("dry-run")

	req := sync.Request{
		Source:      resolveCheckTarget(state.cfg, args[0]),
		Destination: resolveCheckTarget(state.cfg, args[1]),
		DryRun:      dryRun,
		Delete:      getBoolFlag(cmd, "delete"),
		Hash:        getBoolFlag(cmd, "hash"),
		CompareMode: getStringFlag(cmd, "compare"),
		Conflict:    conflictPolicy,
		Resume:      getBoolFlag(cmd, "resume"),
	}

	ctx, fs, cleanup, err := openFileSystem(cmd)
	if err != nil {
		return err
	}
	defer cleanup()

	waitIdle := func(ctx context.Context, fs any) error {
		return waitFileSystemIdle(ctx, fs, 0)
	}
	result, err := sync.Run(ctx, fs, waitIdle, req)
	if err != nil {
		if errors.Is(err, sync.ErrNoSession) {
			return commandUsageError(cmd, "%v", err)
		}
		return err
	}
	return finishSync(cmd, result, conflictPolicy)
}

// finishSync formats the result and maps failures to the partial exit code.
func finishSync(cmd *cobra.Command, result sync.Result, conflictPolicy string) error {
	asJSON, _ := cmd.Flags().GetBool("json")
	if asJSON {
		if err := writePrettyJSON(cmd.OutOrStdout(), result); err != nil {
			return err
		}
	} else {
		sync.PrintSummary(cmd.OutOrStdout(), result)
	}
	if result.Summary.Failed > 0 {
		return &ExitError{Code: ExitPartial, Err: fmt.Errorf("sync finished with %d failed item(s)", result.Summary.Failed)}
	}
	if result.Summary.Conflict > 0 && conflictPolicy == "error" {
		return &ExitError{Code: ExitPartial, Err: fmt.Errorf("sync finished with %d type conflict(s); use --conflict=skip or --conflict=source", result.Summary.Conflict)}
	}
	return nil
}

func getBoolFlag(cmd *cobra.Command, name string) bool {
	v, _ := cmd.Flags().GetBool(name)
	return v
}

func getStringFlag(cmd *cobra.Command, name string) string {
	v, _ := cmd.Flags().GetString(name)
	return v
}
