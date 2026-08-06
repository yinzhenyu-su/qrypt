package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/yinzhenyu/qrypt/internal/config"
	"github.com/yinzhenyu/qrypt/internal/sync"
)

func newFsCheckCmd() *cobra.Command {
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
		Args:              exactNamedArgs("A", "B"),
		RunE:              runCheck,
		ValidArgsFunction: noFileCompletions,
	}
	cmd.Flags().Bool("hash", false, "also compare content hashes (needs backend hash support)")
	cmd.Flags().String("compare", "size-mtime", "comparison strategy: size-mtime, mtime-only, or hash")
	cmd.Flags().Bool("json", false, "write JSON output")
	return cmd
}

// resolveCheckTarget classifies an argument as a virtual path (first segment
// is a configured mount) or a local path.
func resolveCheckTarget(cfg *config.Config, arg string) sync.Target {
	if !strings.HasPrefix(arg, "/") {
		return sync.Target{Kind: sync.TargetLocal, Raw: arg, LocalPath: arg}
	}
	first := strings.Split(strings.TrimPrefix(arg, "/"), "/")[0]
	for _, mount := range cfg.Mounts {
		if mount.Name == first {
			return sync.Target{
				Kind:      sync.TargetVFS,
				Raw:       arg,
				VFSPath:   arg,
				MountName: mount.Name,
				Encrypted: cfg.EncryptionFor(mount.Name).Password != "",
			}
		}
	}
	return sync.Target{Kind: sync.TargetLocal, Raw: arg, LocalPath: arg}
}

func runCheck(cmd *cobra.Command, args []string) error {
	state, err := commandConfig(cmd)
	if err != nil {
		return err
	}
	if state.cfg == nil {
		return configNotFoundError()
	}
	targetA := resolveCheckTarget(state.cfg, args[0])
	targetB := resolveCheckTarget(state.cfg, args[1])
	if targetA.Kind == sync.TargetLocal && targetB.Kind == sync.TargetLocal {
		return commandUsageError(cmd, "at least one side must be a virtual path (/MOUNT/...): got %q and %q", args[0], args[1])
	}

	ctx, fs, cleanup, err := openFileSystem(cmd)
	if err != nil {
		return err
	}
	defer cleanup()

	asHash, _ := cmd.Flags().GetBool("hash")
	compareMode, _ := cmd.Flags().GetString("compare")
	skipSize, modeHash, err := sync.ParseCompareMode(compareMode)
	if err != nil {
		return err
	}
	asHash = asHash || modeHash
	snapA, err := sync.SnapshotTarget(ctx, fs, targetA)
	if err != nil {
		return err
	}
	snapB, err := sync.SnapshotTarget(ctx, fs, targetB)
	if err != nil {
		return err
	}
	opts := sync.CompareOptions{AsHash: asHash, SkipSize: skipSize}
	if asHash {
		opts.Hash = func(ctx context.Context, rel string) (bool, string, error) {
			return sync.CompareHashPair(ctx, fs, targetA, targetB, rel, false)
		}
	}
	differences, err := sync.CompareTrees(ctx, snapA, snapB, opts)
	if err != nil {
		return err
	}
	filesChecked := snapA.FileCount()

	asJSON, _ := cmd.Flags().GetBool("json")
	if asJSON {
		if err := writePrettyJSON(cmd.OutOrStdout(), struct {
			OK           bool              `json:"ok"`
			FilesChecked int               `json:"files_checked"`
			Differences  []sync.Difference `json:"differences"`
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
	return &ExitError{Code: ExitMismatch, Err: fmt.Errorf("check found %d difference(s)", len(differences))}
}
