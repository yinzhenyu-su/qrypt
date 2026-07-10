package cli

import (
	"context"
	"fmt"
	pathpkg "path"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/yinzhenyu/qrypt/internal/control"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

func newFsCopyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:               "copy SOURCE DESTINATION",
		Aliases:           []string{"cp"},
		Short:             "Copy a remote file directly between mounted backends",
		Args:              cobra.ExactArgs(2),
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
		result := runFsCopyDir(ctx, fs, source, args[0], args[1], force)
		asJSON, _ := cmd.Flags().GetBool("json")
		if asJSON {
			if err := writePrettyJSON(cmd.OutOrStdout(), result); err != nil {
				return err
			}
			if !result.Pass {
				return result.err()
			}
			return nil
		}
		if !result.Pass {
			printFsCopyDirSummary(cmd.ErrOrStderr(), result)
			return result.err()
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
	for _, step := range result.Steps {
		if !step.OK && step.Error != "" {
			return fmt.Errorf("%s: %s", step.Phase, step.Error)
		}
	}
	return fmt.Errorf("copy failed")
}

type fsCopyDirResult struct {
	SourcePath string    `json:"source_path"`
	DestPath   string    `json:"dest_path"`
	Pass       bool      `json:"pass"`
	Copied     int       `json:"copied"`
	Skipped    int       `json:"skipped"`
	Bytes      int64     `json:"bytes"`
	Started    time.Time `json:"started_at"`
	Finished   time.Time `json:"finished_at"`
	Duration   string    `json:"duration"`
	Error      string    `json:"error,omitempty"`
}

func runFsCopyDir(ctx context.Context, fs vfs.FileSystem, source control.DriverCopySource, srcPath, dstParentPath string, overwrite bool) *fsCopyDirResult {
	started := time.Now()
	result := &fsCopyDirResult{
		SourcePath: cleanRemotePath(srcPath),
		DestPath:   pathpkg.Join(cleanRemotePath(dstParentPath), pathpkg.Base(cleanRemotePath(srcPath))),
		Started:    started,
	}
	defer func() {
		result.Finished = time.Now()
		result.Duration = result.Finished.Sub(result.Started).String()
		result.Pass = result.Error == ""
	}()
	if err := copyDirRecursive(ctx, fs, source, result.SourcePath, result.DestPath, overwrite, result); err != nil {
		result.Error = err.Error()
	}
	return result
}

func copyDirRecursive(ctx context.Context, fs vfs.FileSystem, source control.DriverCopySource, srcPath, dstPath string, overwrite bool, result *fsCopyDirResult) error {
	if err := mkdirAllRemote(ctx, fs, dstPath); err != nil {
		return err
	}
	entries, err := fs.List(ctx, srcPath)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		childSrc := pathpkg.Join(srcPath, entry.Name)
		childDst := pathpkg.Join(dstPath, entry.Name)
		if entry.IsDir {
			if err := copyDirRecursive(ctx, fs, source, childSrc, childDst, overwrite, result); err != nil {
				return err
			}
			continue
		}
		if !overwrite {
			if _, err := fs.Stat(ctx, childDst); err == nil {
				result.Skipped++
				continue
			} else if !isRemoteNotFound(err) {
				return err
			}
		}
		copyResult := control.RunDirectDriverCopy(ctx, source, childSrc, childDst, overwrite)
		if !copyResult.Pass {
			return fsCopyError(copyResult)
		}
		result.Copied++
		result.Bytes += copyResult.Bytes
	}
	return nil
}

func mkdirAllRemote(ctx context.Context, fs vfs.FileSystem, dir string) error {
	dir = cleanRemotePath(dir)
	if dir == "/" {
		return nil
	}
	current := "/"
	for _, part := range strings.Split(strings.Trim(dir, "/"), "/") {
		current = pathpkg.Join(current, part)
		entry, err := fs.Stat(ctx, current)
		if err == nil {
			if !entry.IsDir {
				return fmt.Errorf("remote destination %q exists and is not a directory", current)
			}
			continue
		}
		if !isRemoteNotFound(err) {
			return err
		}
		if _, err := fs.Mkdir(ctx, current); err != nil {
			return err
		}
	}
	return nil
}

func printFsCopyDirSummary(w interface {
	Write([]byte) (int, error)
}, result *fsCopyDirResult) {
	fmt.Fprintf(w, "copied directory %s -> %s\n", result.SourcePath, result.DestPath)
	fmt.Fprintf(w, "files copied: %d\n", result.Copied)
	fmt.Fprintf(w, "files skipped: %d\n", result.Skipped)
	fmt.Fprintf(w, "bytes: %d\n", result.Bytes)
	fmt.Fprintf(w, "duration: %s\n", result.Duration)
	if result.Error != "" {
		fmt.Fprintf(w, "error: %s\n", result.Error)
	}
}

func (r *fsCopyDirResult) err() error {
	if r.Error != "" {
		return fmt.Errorf("%s", r.Error)
	}
	return fmt.Errorf("copy failed")
}

func cleanRemotePath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return "/"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return pathpkg.Clean(path)
}

func isRemoteNotFound(err error) bool {
	return strings.Contains(strings.ToLower(err.Error()), "not found")
}
