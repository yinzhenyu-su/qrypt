package cli

import (
	"context"
	"fmt"
	"math"

	"github.com/spf13/cobra"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

// fsSpaceEntry is one mount's space usage in the machine-readable df output.
type fsSpaceEntry struct {
	Name  string `json:"name"`
	Total int64  `json:"total"`
	Free  int64  `json:"free"`
	Used  int64  `json:"used"`
	Error string `json:"error,omitempty"`
}

func newFsDfCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:               "df [MOUNT]",
		Short:             "Show space usage of the configured drives",
		Args:              maxArgs(1),
		RunE:              runDf,
		ValidArgsFunction: noFileCompletions,
	}
	cmd.Flags().Bool("json", false, "write JSON output")
	cmd.Flags().Bool("bytes", false, "raw byte counts instead of human-readable sizes")
	return cmd
}

func runDf(cmd *cobra.Command, args []string) error {
	ctx, fs, cleanup, err := openFileSystem(cmd)
	if err != nil {
		return err
	}
	defer cleanup()

	asJSON, _ := cmd.Flags().GetBool("json")
	asBytes, _ := cmd.Flags().GetBool("bytes")
	printBytes := formatBytes
	if asBytes {
		printBytes = func(n int64) string { return fmt.Sprintf("%d", n) }
	}

	selected := ""
	if len(args) > 0 {
		selected = args[0]
	}

	entryFrom := func(name string, space drive.Space, err error) (fsSpaceEntry, error) {
		if err != nil {
			return fsSpaceEntry{}, err
		}
		used := space.Total - space.Free
		if used < 0 {
			used = 0
		}
		return fsSpaceEntry{Name: name, Total: space.Total, Free: space.Free, Used: used}, nil
	}

	// Prefer per-mount breakdown when the filesystem exposes one.
	if spacer, ok := fs.(interface {
		MountSpaces(ctx context.Context) []vfs.MountSpace
	}); ok {
		mounts := spacer.MountSpaces(ctx)
		if selected != "" {
			for _, mount := range mounts {
				if mount.Name == selected {
					entry, err := entryFrom(mount.Name, mount.Space, mount.Err)
					if err != nil {
						return fmt.Errorf("mount %q: %w", selected, err)
					}
					if asJSON {
						return writePrettyJSON(cmd.OutOrStdout(), entry)
					}
					fmt.Fprintf(cmd.OutOrStdout(), "%s: total %s, free %s, used %s\n", entry.Name, printBytes(entry.Total), printBytes(entry.Free), printBytes(entry.Used))
					return nil
				}
			}
			return fmt.Errorf("mount %q not found", selected)
		}
		if asJSON {
			entries := make([]fsSpaceEntry, 0, len(mounts))
			for _, mount := range mounts {
				if mount.Err != nil {
					entries = append(entries, fsSpaceEntry{Name: mount.Name, Error: mount.Err.Error()})
					continue
				}
				entry, err := entryFrom(mount.Name, mount.Space, nil)
				if err != nil {
					return err
				}
				entries = append(entries, entry)
			}
			return writePrettyJSON(cmd.OutOrStdout(), struct {
				Mounts []fsSpaceEntry `json:"mounts"`
			}{Mounts: entries})
		}
		for _, mount := range mounts {
			if mount.Err != nil {
				fmt.Fprintf(cmd.OutOrStdout(), "%s: %s\n", mount.Name, mount.Err)
				continue
			}
			entry, _ := entryFrom(mount.Name, mount.Space, nil)
			fmt.Fprintf(cmd.OutOrStdout(), "%s: total %s, free %s, used %s\n", entry.Name, printBytes(entry.Total), printBytes(entry.Free), printBytes(entry.Used))
		}
		return nil
	}

	// Fallback: single aggregate space (filesystem without per-mount query).
	spacer, ok := fs.(interface {
		Space(ctx context.Context) (drive.Space, error)
	})
	if !ok {
		return fmt.Errorf("this filesystem does not report space usage")
	}
	space, err := spacer.Space(ctx)
	if err != nil {
		return fmt.Errorf("space query: %w", err)
	}
	entry, err := entryFrom("total", space, nil)
	if err != nil {
		return err
	}
	if asJSON {
		return writePrettyJSON(cmd.OutOrStdout(), entry)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "total: %s, free: %s, used: %s\n", printBytes(entry.Total), printBytes(entry.Free), printBytes(entry.Used))
	return nil
}

// fsDiskUsageResult is the machine-readable output of fs du.
type fsDiskUsageResult struct {
	Path  string `json:"path"`
	Files int    `json:"files"`
	Bytes int64  `json:"bytes"`
}

func newFsDuCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:               "du [REMOTE]",
		Short:             "Show disk usage of a directory tree",
		Args:              maxArgs(1),
		RunE:              runDu,
		ValidArgsFunction: noFileCompletions,
	}
	cmd.Flags().Bool("json", false, "write JSON output")
	cmd.Flags().Bool("bytes", false, "raw byte counts instead of human-readable sizes")
	return cmd
}

func runDu(cmd *cobra.Command, args []string) error {
	path := "/"
	if len(args) > 0 {
		path = args[0]
	}
	ctx, fs, cleanup, err := openFileSystem(cmd)
	if err != nil {
		return err
	}
	defer cleanup()
	entry, err := fs.Stat(ctx, path)
	if err != nil {
		return err
	}
	var files int
	var bytes int64
	if entry.IsDir {
		snap, err := snapshotVFS(ctx, fs, path)
		if err != nil {
			return err
		}
		files = snap.fileCount()
		for _, e := range snap {
			if !e.IsDir {
				bytes += e.Size
			}
		}
	} else {
		// du on a single file reports just that file.
		files = 1
		bytes = entry.Size
	}
	result := fsDiskUsageResult{Path: path, Files: files, Bytes: bytes}
	asJSON, _ := cmd.Flags().GetBool("json")
	if asJSON {
		return writePrettyJSON(cmd.OutOrStdout(), result)
	}
	asBytes, _ := cmd.Flags().GetBool("bytes")
	if asBytes {
		fmt.Fprintf(cmd.OutOrStdout(), "%d files, %d bytes\n", files, bytes)
		return nil
	}
	fmt.Fprintf(cmd.OutOrStdout(), "%d files, %s\n", files, formatBytes(bytes))
	return nil
}

// formatBytes renders a byte count in a compact human-readable form.
func formatBytes(n int64) string {
	if n < 0 {
		return "-"
	}
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	units := []string{"K", "M", "G", "T", "P"}
	value := float64(n)
	unit := "B"
	for _, u := range units {
		value /= 1024
		if value < 1024 {
			unit = u
			break
		}
	}
	if value >= 1024 {
		return fmt.Sprintf("%.0f P", value/1024)
	}
	if value >= 100 {
		return fmt.Sprintf("%.0f %s", value, unit)
	}
	return fmt.Sprintf("%.1f %s", math.Round(value*10)/10, unit)
}
