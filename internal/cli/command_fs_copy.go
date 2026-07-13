package cli

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/yinzhenyu/qrypt/internal/control"
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
	entry, err := fs.Stat(ctx, args[0])
	if err != nil && recursive {
		return err
	}
	if err == nil && entry.IsDir {
		if !recursive {
			return fmt.Errorf("source %q is a directory (use --recursive to copy directories)", args[0])
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
	if result.Error != "" {
		return fmt.Errorf("%s", result.Error)
	}
	return fmt.Errorf("copy failed")
}
