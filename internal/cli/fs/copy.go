package fs

import (
	"context"
	"fmt"
	"sort"

	"github.com/spf13/cobra"
	"github.com/yinzhenyu/qrypt/internal/cli/i18n"
	cliruntime "github.com/yinzhenyu/qrypt/internal/cli/runtime"
	syncer "github.com/yinzhenyu/qrypt/pkg/syncer"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
	"github.com/yinzhenyu/qrypt/pkg/vfs/diagnostics"
	"github.com/yinzhenyu/qrypt/pkg/vfs/drivecopy"
)

func NewCopyCmd(rt cliruntime.Runtime) *cobra.Command {
	cmd := &cobra.Command{
		Use:               "copy SOURCE DESTINATION",
		Aliases:           []string{"cp"},
		Short:             "Copy a remote file directly between mounted backends",
		Long:              `Copy SOURCE to DESTINATION directly between mounted backends, without downloading through the local machine. Use --recursive for directories.`,
		Example:           `  qrypt fs copy /cloud/photos/cat.jpg /backup/photos/cat.jpg`,
		Args:              cliruntime.ExactNamedArgs(rt, "SOURCE", "DESTINATION"),
		RunE:              RunCopy(rt),
		ValidArgsFunction: cliruntime.NoFileCompletions,
	}
	cmd.Flags().BoolP("recursive", "r", false, "copy directories recursively")
	cmd.Flags().BoolP("force", "f", false, "overwrite an existing remote destination")
	cmd.Flags().Bool("dry-run", false, "show what would be copied without copying")
	cmd.Flags().Bool("overwrite", false, "deprecated alias for --force")
	_ = cmd.Flags().MarkHidden("overwrite")
	cmd.Flags().Bool("json", false, "write JSON output")
	return cmd
}

func RunCopy(rt cliruntime.Runtime) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		ctx, fs, cleanup, err := rt.OpenFileSystem(cmd)
		if err != nil {
			return err
		}
		defer cleanup()

		source, ok := fs.(diagnostics.DriverCopySource)
		if !ok {
			return fmt.Errorf("direct copy requires a filesystem with driver debug resolution")
		}
		force := copyForce(cmd)
		recursive, _ := cmd.Flags().GetBool("recursive")
		dryRun, _ := cmd.Flags().GetBool("dry-run")
		entry, err := fs.Stat(ctx, args[0])
		if err != nil && recursive {
			return err
		}
		if err == nil && entry.IsDir {
			if !recursive {
				return rt.UsageError(cmd, "source %q is a directory (use --recursive to copy directories)", args[0])
			}
			if dryRun {
				return RunCopyDryRun(cmd, ctx, fs, args[0], args[1])
			}
			result := drivecopy.RunDirectDriverCopyDir(ctx, fs, source, args[0], args[1], force)
			asJSON, _ := cmd.Flags().GetBool("json")
			if asJSON {
				if err := cliruntime.WritePrettyJSON(cmd.OutOrStdout(), result); err != nil {
					return err
				}
				if !result.Pass {
					return CopyDirError(rt, result)
				}
				return nil
			}
			if !result.Pass {
				PrintCopyDirSummary(cmd.ErrOrStderr(), i18n.FromCommand(cmd), result)
				return CopyDirError(rt, result)
			}
			PrintCopyDirSummary(cmd.OutOrStdout(), i18n.FromCommand(cmd), result)
			return nil
		}
		if dryRun {
			return RunCopyDryRun(cmd, ctx, fs, args[0], args[1])
		}

		result := drivecopy.RunDirectDriverCopy(ctx, source, args[0], args[1], force)
		asJSON, _ := cmd.Flags().GetBool("json")
		if asJSON {
			if err := cliruntime.WritePrettyJSON(cmd.OutOrStdout(), result); err != nil {
				return err
			}
			if !result.Pass {
				return CopyError(result)
			}
			return nil
		}
		if !result.Pass {
			PrintCopySummary(cmd.ErrOrStderr(), i18n.FromCommand(cmd), result)
			return CopyError(result)
		}
		PrintCopySummary(cmd.OutOrStdout(), i18n.FromCommand(cmd), result)
		return nil
	}
}

func copyForce(cmd *cobra.Command) bool {
	force, _ := cmd.Flags().GetBool("force")
	overwrite, _ := cmd.Flags().GetBool("overwrite")
	return force || overwrite
}

// CopyDryRunResult is the machine-readable plan produced by --dry-run.
type CopyDryRunResult struct {
	Source      string   `json:"source"`
	Destination string   `json:"destination"`
	DryRun      bool     `json:"dry_run"`
	Files       int      `json:"files"`
	Bytes       int64    `json:"bytes"`
	Entries     []string `json:"entries"`
}

func RunCopyDryRun(cmd *cobra.Command, ctx context.Context, fs vfs.Reader, source, destination string) error {
	result := CopyDryRunResult{Source: source, Destination: destination, DryRun: true}
	entry, err := fs.Stat(ctx, source)
	if err != nil {
		return err
	}
	if entry.IsDir {
		snap, err := syncer.SnapshotVFS(ctx, fs, source)
		if err != nil {
			return err
		}
		result.Files = snap.FileCount()
		for _, e := range snap {
			if !e.IsDir {
				result.Bytes += e.Size
				result.Entries = append(result.Entries, syncer.JoinVFS(source, e.RelPath))
			}
		}
		sort.Strings(result.Entries)
	} else {
		result.Files = 1
		result.Bytes = entry.Size
		result.Entries = []string{source}
	}
	asJSON, _ := cmd.Flags().GetBool("json")
	if asJSON {
		return cliruntime.WritePrettyJSON(cmd.OutOrStdout(), result)
	}
	fmt.Fprintln(cmd.OutOrStdout(), i18n.FromCommand(cmd).T("out.fs.copy.dry_run", result.Files, result.Bytes, source, destination))
	for _, path := range result.Entries {
		fmt.Fprintf(cmd.OutOrStdout(), "  %s\n", path)
	}
	return nil
}

func PrintCopySummary(w interface {
	Write([]byte) (int, error)
}, loc *i18n.Localizer, result *drivecopy.DriverCopyResult) {
	fmt.Fprintln(w, loc.T("out.fs.copy.copied", result.SourcePath, result.DestPath))
	fmt.Fprintln(w, loc.T("out.fs.copy.mounts", result.SourceMount, result.DestMount))
	fmt.Fprintln(w, loc.T("out.fs.copy.bytes", result.Bytes))
	fmt.Fprintln(w, loc.T("out.fs.copy.duration", result.Duration))
	for _, event := range result.Timeline {
		if event.Phase != "read_source_to_temp" && event.Phase != "driver_put_source" {
			continue
		}
		fmt.Fprintf(w, "%s: %s", event.Phase, event.Duration)
		if event.Throughput > 0 {
			fmt.Fprintf(w, " (%d B/s)", event.Throughput)
		}
		fmt.Fprintln(w)
	}
}

func CopyError(result *drivecopy.DriverCopyResult) error {
	return fmt.Errorf("%s", drivecopy.DriverCopyError(result))
}

func PrintCopyDirSummary(w interface {
	Write([]byte) (int, error)
}, loc *i18n.Localizer, result *drivecopy.DriverCopyDirResult) {
	fmt.Fprintln(w, loc.T("out.fs.copy.dir_copied", result.SourcePath, result.DestPath))
	fmt.Fprintln(w, loc.T("out.fs.copy.files_copied", result.Copied))
	fmt.Fprintln(w, loc.T("out.fs.copy.files_skipped", result.Skipped))
	fmt.Fprintln(w, loc.T("out.fs.copy.files_failed", result.Failed))
	fmt.Fprintln(w, loc.T("out.fs.copy.bytes", result.Bytes))
	fmt.Fprintln(w, loc.T("out.fs.copy.duration", result.Duration))
	if result.Error != "" {
		fmt.Fprintln(w, loc.T("out.fs.copy.error", result.Error))
	}
}

func CopyDirError(rt cliruntime.Runtime, result *drivecopy.DriverCopyDirResult) error {
	var err error
	if result.Error != "" {
		err = fmt.Errorf("%s", result.Error)
	} else {
		err = fmt.Errorf("copy failed")
	}
	// Some files copied, some failed: exit 3 so scripts can decide to rerun
	// only the failed subset.
	if result.Copied > 0 && result.Failed > 0 {
		return rt.ExitError(cliruntime.ExitPartial, err)
	}
	return err
}
