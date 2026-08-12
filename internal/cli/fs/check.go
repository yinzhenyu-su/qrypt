package fs

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	cliruntime "github.com/yinzhenyu/qrypt/internal/cli/runtime"
	"github.com/yinzhenyu/qrypt/pkg/config"
	syncer "github.com/yinzhenyu/qrypt/pkg/syncer"
)

func NewCheckCmd(rt cliruntime.Runtime) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "check A B",
		Short: "Check that two trees contain the same files",
		Long: `Check that the trees at A and B contain the same files.

A and B can be virtual paths under a configured mount (/MOUNT/dir) or local
paths. Encryption is handled transparently: the mount's [mounts.encryption]
is applied when comparing, so a local plaintext directory can be checked
against an encrypted cloud mount.

Differences are reported relative to the argument order: missing_in_b means
a file exists on A but not B; extra_in_b means it exists on B but not A.

Exit code: 0 when identical, 4 when differences are found, 1 on errors.`,
		Args:              cliruntime.ExactNamedArgs(rt, "A", "B"),
		RunE:              runCheck(rt),
		ValidArgsFunction: cliruntime.NoFileCompletions,
	}
	cmd.Flags().Bool("hash", false, "also compare content hashes (needs backend hash support)")
	cmd.Flags().String("compare", "size-mtime", "comparison strategy: size-mtime, mtime-only, or hash")
	cmd.Flags().Bool("json", false, "write JSON output")
	return cmd
}

// ResolveCheckTarget classifies an argument as a virtual path (first segment
// is a configured mount) or a local path.
func ResolveCheckTarget(cfg *config.Config, arg string) syncer.Target {
	if !strings.HasPrefix(arg, "/") {
		return syncer.Target{Kind: syncer.TargetLocal, Raw: arg, LocalPath: arg}
	}
	first := strings.Split(strings.TrimPrefix(arg, "/"), "/")[0]
	for _, mount := range cfg.Mounts {
		if mount.Name == first {
			return syncer.Target{
				Kind:      syncer.TargetVFS,
				Raw:       arg,
				VFSPath:   arg,
				MountName: mount.Name,
				Encrypted: cfg.EncryptionFor(mount.Name).Password != "",
			}
		}
	}
	return syncer.Target{Kind: syncer.TargetLocal, Raw: arg, LocalPath: arg}
}

func runCheck(rt cliruntime.Runtime) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		state, err := rt.CommandConfig(cmd)
		if err != nil {
			return err
		}
		if state.Cfg == nil {
			return rt.ConfigNotFoundError()
		}
		targetA := ResolveCheckTarget(state.Cfg, args[0])
		targetB := ResolveCheckTarget(state.Cfg, args[1])
		if targetA.Kind == syncer.TargetLocal && targetB.Kind == syncer.TargetLocal {
			return rt.UsageError(cmd, "at least one side must be a virtual path (/MOUNT/...): got %q and %q", args[0], args[1])
		}

		ctx, fs, cleanup, err := rt.OpenFileSystem(cmd)
		if err != nil {
			return err
		}
		defer cleanup()

		asHash, _ := cmd.Flags().GetBool("hash")
		compareMode, _ := cmd.Flags().GetString("compare")
		skipSize, modeHash, err := syncer.ParseCompareMode(compareMode)
		if err != nil {
			return err
		}
		asHash = asHash || modeHash
		snapA, err := syncer.SnapshotTarget(ctx, fs, targetA)
		if err != nil {
			return err
		}
		snapB, err := syncer.SnapshotTarget(ctx, fs, targetB)
		if err != nil {
			return err
		}
		opts := syncer.CompareOptions{AsHash: asHash, SkipSize: skipSize}
		if asHash {
			opts.Hash = func(ctx context.Context, rel string) (bool, string, error) {
				return syncer.CompareHashPair(ctx, fs, targetA, targetB, rel, false)
			}
		}
		differences, err := syncer.CompareTrees(ctx, snapA, snapB, opts)
		if err != nil {
			return err
		}
		filesChecked := snapA.FileCount()

		asJSON, _ := cmd.Flags().GetBool("json")
		if asJSON {
			if err := cliruntime.WritePrettyJSON(cmd.OutOrStdout(), struct {
				OK           bool                `json:"ok"`
				FilesChecked int                 `json:"files_checked"`
				Differences  []syncer.Difference `json:"differences"`
			}{
				OK:           len(differences) == 0,
				FilesChecked: filesChecked,
				Differences:  differences,
			}); err != nil {
				return err
			}
		} else if len(differences) == 0 {
			fmt.Fprintf(cmd.OutOrStdout(), "ok: %d files match\n", filesChecked)
		} else {
			for _, d := range differences {
				fmt.Fprintf(cmd.OutOrStdout(), "[%s] %s", d.Reason, d.Path)
				if d.A != "" || d.B != "" {
					fmt.Fprintf(cmd.OutOrStdout(), " (%s vs %s)", d.A, d.B)
				}
				fmt.Fprintln(cmd.OutOrStdout())
			}
		}
		if len(differences) == 0 {
			return nil
		}
		return rt.ExitError(cliruntime.ExitMismatch, fmt.Errorf("check found %d difference(s)", len(differences)))
	}
}
