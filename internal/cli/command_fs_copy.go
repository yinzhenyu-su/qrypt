package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/yinzhenyu/qrypt/internal/control"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

func newFsCopyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:               "copy SOURCE DESTINATION",
		Aliases:           []string{"cp"},
		Short:             "Copy a remote file directly between mounted backends",
		Args:              exactNamedArgs("SOURCE", "DESTINATION"),
		RunE:              runFsCopy,
		ValidArgsFunction: noFileCompletions,
	}
	cmd.Flags().BoolP("recursive", "r", false, "copy directories recursively")
	cmd.Flags().BoolP("force", "f", false, "overwrite an existing remote destination")
	cmd.Flags().Bool("dry-run", false, "show what would be copied without copying")
	cmd.Flags().Bool("overwrite", false, "deprecated alias for --force")
	_ = cmd.Flags().MarkHidden("overwrite")
	cmd.Flags().Bool("json", false, "write JSON output")
	return cmd
}

func runFsCopy(cmd *cobra.Command, args []string) error {
	ctx, fs, cleanup, err := openFileSystem(cmd)
	if err != nil {
		return err
	}
	defer cleanup()

	source, ok := fs.(control.DriverCopySource)
	if !ok {
		return fmt.Errorf("direct copy requires a filesystem with driver debug resolution")
	}
	force := fsCopyForce(cmd)
	recursive, _ := cmd.Flags().GetBool("recursive")
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	entry, err := fs.Stat(ctx, args[0])
	if err != nil && recursive {
		return err
	}
	if err == nil && entry.IsDir {
		if !recursive {
			return commandUsageError(cmd, "source %q is a directory (use --recursive to copy directories)", args[0])
		}
		if dryRun {
			return runFsCopyDryRun(cmd, ctx, fs, args[0], args[1])
		}
		result := control.RunDirectDriverCopyDir(ctx, fs, source, args[0], args[1], force)
		asJSON, _ := cmd.Flags().GetBool("json")
		if asJSON {
			if err := writePrettyJSON(cmd.OutOrStdout(), result); err != nil {
				return err
			}
			if !result.Pass {
				return fsCopyDirError(result)
			}
			return nil
		}
		if !result.Pass {
			printFsCopyDirSummary(cmd.ErrOrStderr(), result)
			return fsCopyDirError(result)
		}
		printFsCopyDirSummary(cmd.OutOrStdout(), result)
		return nil
	}
	if dryRun {
		return runFsCopyDryRun(cmd, ctx, fs, args[0], args[1])
	}

	result := control.RunDirectDriverCopy(ctx, source, args[0], args[1], force)
	asJSON, _ := cmd.Flags().GetBool("json")
	if asJSON {
		if err := writePrettyJSON(cmd.OutOrStdout(), result); err != nil {
			return err
		}
		if !result.Pass {
			return fsCopyError(result)
		}
		return nil
	}
	if !result.Pass {
		printFsCopySummary(cmd.ErrOrStderr(), result)
		return fsCopyError(result)
	}
	printFsCopySummary(cmd.OutOrStdout(), result)
	return nil
}

func fsCopyForce(cmd *cobra.Command) bool {
	force, _ := cmd.Flags().GetBool("force")
	overwrite, _ := cmd.Flags().GetBool("overwrite")
	return force || overwrite
}

// fsCopyDryRunResult is the machine-readable plan produced by --dry-run.
type fsCopyDryRunResult struct {
	Source      string   `json:"source"`
	Destination string   `json:"destination"`
	DryRun      bool     `json:"dry_run"`
	Files       int      `json:"files"`
	Bytes       int64    `json:"bytes"`
	Entries     []string `json:"entries"`
}

func runFsCopyDryRun(cmd *cobra.Command, ctx context.Context, fs vfs.FileSystem, source, destination string) error {
	result := fsCopyDryRunResult{Source: source, Destination: destination, DryRun: true}
	entry, err := fs.Stat(ctx, source)
	if err != nil {
		return err
	}
	if entry.IsDir {
		if err := walkCopySource(ctx, fs, source, &result.Entries, &result.Files, &result.Bytes); err != nil {
			return err
		}
	} else {
		result.Files = 1
		result.Bytes = entry.Size
		result.Entries = []string{source}
	}
	asJSON, _ := cmd.Flags().GetBool("json")
	if asJSON {
		return writePrettyJSON(cmd.OutOrStdout(), result)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "dry run: would copy %d files (%d bytes) %s -> %s\n", result.Files, result.Bytes, source, destination)
	for _, path := range result.Entries {
		fmt.Fprintf(cmd.OutOrStdout(), "  %s\n", path)
	}
	return nil
}

// walkCopySource recursively enumerates files under path, accumulating a
// dry-run plan without performing any copy.
func walkCopySource(ctx context.Context, fs vfs.FileSystem, path string, entries *[]string, files *int, bytes *int64) error {
	list, err := fs.List(ctx, path)
	if err != nil {
		return err
	}
	for _, entry := range list {
		child := strings.TrimSuffix(path, "/") + "/" + entry.Name
		if entry.IsDir {
			if err := walkCopySource(ctx, fs, child, entries, files, bytes); err != nil {
				return err
			}
			continue
		}
		*files++
		*bytes += entry.Size
		if entries != nil {
			*entries = append(*entries, child)
		}
	}
	return nil
}

func printFsCopySummary(w interface {
	Write([]byte) (int, error)
}, result *control.DriverCopyResult) {
	fmt.Fprintf(w, "copied %s -> %s\n", result.SourcePath, result.DestPath)
	fmt.Fprintf(w, "mounts: %s -> %s\n", result.SourceMount, result.DestMount)
	fmt.Fprintf(w, "bytes: %d\n", result.Bytes)
	fmt.Fprintf(w, "duration: %s\n", result.Duration)
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

func fsCopyError(result *control.DriverCopyResult) error {
	return fmt.Errorf("%s", control.DriverCopyError(result))
}

func printFsCopyDirSummary(w interface {
	Write([]byte) (int, error)
}, result *control.DriverCopyDirResult) {
	fmt.Fprintf(w, "copied directory %s -> %s\n", result.SourcePath, result.DestPath)
	fmt.Fprintf(w, "files copied: %d\n", result.Copied)
	fmt.Fprintf(w, "files skipped: %d\n", result.Skipped)
	fmt.Fprintf(w, "files failed: %d\n", result.Failed)
	fmt.Fprintf(w, "bytes: %d\n", result.Bytes)
	fmt.Fprintf(w, "duration: %s\n", result.Duration)
	if result.Error != "" {
		fmt.Fprintf(w, "error: %s\n", result.Error)
	}
}

func fsCopyDirError(result *control.DriverCopyDirResult) error {
	var err error
	if result.Error != "" {
		err = fmt.Errorf("%s", result.Error)
	} else {
		err = fmt.Errorf("copy failed")
	}
	// Some files copied, some failed: exit 3 so scripts can decide to rerun
	// only the failed subset.
	if result.Copied > 0 && result.Failed > 0 {
		return &ExitError{Code: ExitPartial, Err: err}
	}
	return err
}
