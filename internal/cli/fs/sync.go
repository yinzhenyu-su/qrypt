package fs

import (
	"context"
	"errors"
	"fmt"

	"github.com/spf13/cobra"
	cliruntime "github.com/yinzhenyu/qrypt/internal/cli/runtime"
	"github.com/yinzhenyu/qrypt/pkg/core"
	syncer "github.com/yinzhenyu/qrypt/pkg/syncer"
)

func NewSyncCmd(rt cliruntime.Runtime) *cobra.Command {
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
		Args:              cliruntime.ExactNamedArgs(rt, "SOURCE", "DESTINATION"),
		RunE:              runSync(rt),
		ValidArgsFunction: cliruntime.NoFileCompletions,
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

func runSync(rt cliruntime.Runtime) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		state, err := rt.CommandConfig(cmd)
		if err != nil {
			return err
		}
		if state.Cfg == nil {
			return rt.ConfigNotFoundError()
		}
		conflictPolicy, _ := cmd.Flags().GetString("conflict")
		switch conflictPolicy {
		case "error", "skip", "source":
		default:
			return rt.UsageError(cmd, "--conflict must be error, skip, or source (got %q)", conflictPolicy)
		}
		dryRun, _ := cmd.Flags().GetBool("dry-run")

		req := syncer.Request{
			Source:      ResolveCheckTarget(state.Cfg, args[0]),
			Destination: ResolveCheckTarget(state.Cfg, args[1]),
			WorkDir:     core.NewStorageLayout(state.Cfg, core.RuntimeLayout{}).RootDir,
			DryRun:      dryRun,
			Delete:      getBoolFlag(cmd, "delete"),
			Hash:        getBoolFlag(cmd, "hash"),
			CompareMode: getStringFlag(cmd, "compare"),
			Conflict:    conflictPolicy,
			Resume:      getBoolFlag(cmd, "resume"),
		}

		ctx, fs, cleanup, err := rt.OpenFileSystem(cmd)
		if err != nil {
			return err
		}
		defer cleanup()

		result, err := syncer.Run(ctx, fs, func(context.Context, any) error {
			// The sync engine passes us the filesystem untyped; we already
			// hold the started OpenedFileSystem, so wait on that, typed.
			return rt.WaitFileSystemIdle(ctx, fs, 0)
		}, req)
		if err != nil {
			if errors.Is(err, syncer.ErrNoSession) {
				return rt.UsageError(cmd, "%v", err)
			}
			return err
		}
		return FinishSync(rt, cmd, result, conflictPolicy)
	}
}

// FinishSync formats the result and maps failures to the partial exit code.
func FinishSync(rt cliruntime.Runtime, cmd *cobra.Command, result syncer.Result, conflictPolicy string) error {
	asJSON, _ := cmd.Flags().GetBool("json")
	if asJSON {
		if err := cliruntime.WritePrettyJSON(cmd.OutOrStdout(), result); err != nil {
			return err
		}
	} else {
		syncer.PrintSummary(cmd.OutOrStdout(), result)
	}
	if result.Summary.Failed > 0 {
		return rt.ExitError(cliruntime.ExitPartial, fmt.Errorf("sync finished with %d failed item(s)", result.Summary.Failed))
	}
	if result.Summary.Conflict > 0 && conflictPolicy == "error" {
		return rt.ExitError(cliruntime.ExitPartial, fmt.Errorf("sync finished with %d type conflict(s); use --conflict=skip or --conflict=source", result.Summary.Conflict))
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
