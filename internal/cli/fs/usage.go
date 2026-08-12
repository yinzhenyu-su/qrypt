package fs

import (
	"fmt"

	"github.com/spf13/cobra"
	cliruntime "github.com/yinzhenyu/qrypt/internal/cli/runtime"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	syncer "github.com/yinzhenyu/qrypt/pkg/syncer"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

// SpaceEntry is one mount's space usage in the machine-readable df output.
type SpaceEntry struct {
	Name  string `json:"name"`
	Total int64  `json:"total"`
	Free  int64  `json:"free"`
	Used  int64  `json:"used"`
	Error string `json:"error,omitempty"`
}

func NewDfCmd(rt cliruntime.Runtime) *cobra.Command {
	cmd := &cobra.Command{
		Use:               "df [MOUNT]",
		Short:             "Show space usage of the configured drives",
		Args:              cliruntime.MaxArgs(rt, 1),
		RunE:              runDf(rt),
		ValidArgsFunction: cliruntime.NoFileCompletions,
	}
	cmd.Flags().Bool("json", false, "write JSON output")
	cmd.Flags().Bool("bytes", false, "raw byte counts instead of human-readable sizes")
	return cmd
}

func runDf(rt cliruntime.Runtime) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		ctx, fs, cleanup, err := rt.OpenFileSystem(cmd)
		if err != nil {
			return err
		}
		defer cleanup()

		asJSON, _ := cmd.Flags().GetBool("json")
		asBytes, _ := cmd.Flags().GetBool("bytes")
		printBytes := cliruntime.FormatBytes
		if asBytes {
			printBytes = func(n int64) string { return fmt.Sprintf("%d", n) }
		}

		selected := ""
		if len(args) > 0 {
			selected = args[0]
		}

		entryFrom := func(name string, space drive.Space, err error) (SpaceEntry, error) {
			if err != nil {
				return SpaceEntry{}, err
			}
			used := space.Total - space.Free
			if used < 0 {
				used = 0
			}
			return SpaceEntry{Name: name, Total: space.Total, Free: space.Free, Used: used}, nil
		}

		if spacer, ok := fs.(vfs.MountSpaceProvider); ok {
			mounts := spacer.MountSpaces(ctx)
			if selected != "" {
				for _, mount := range mounts {
					if mount.Name == selected {
						entry, err := entryFrom(mount.Name, mount.Space, mount.Err)
						if err != nil {
							return fmt.Errorf("mount %q: %w", selected, err)
						}
						if asJSON {
							return cliruntime.WritePrettyJSON(cmd.OutOrStdout(), entry)
						}
						fmt.Fprintf(cmd.OutOrStdout(), "%s: total %s, free %s, used %s\n", entry.Name, printBytes(entry.Total), printBytes(entry.Free), printBytes(entry.Used))
						return nil
					}
				}
				return fmt.Errorf("mount %q not found", selected)
			}
			if asJSON {
				entries := make([]SpaceEntry, 0, len(mounts))
				for _, mount := range mounts {
					if mount.Err != nil {
						entries = append(entries, SpaceEntry{Name: mount.Name, Error: mount.Err.Error()})
						continue
					}
					entry, err := entryFrom(mount.Name, mount.Space, nil)
					if err != nil {
						return err
					}
					entries = append(entries, entry)
				}
				return cliruntime.WritePrettyJSON(cmd.OutOrStdout(), struct {
					Mounts []SpaceEntry `json:"mounts"`
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

		spacer, ok := fs.(vfs.SpaceProvider)
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
			return cliruntime.WritePrettyJSON(cmd.OutOrStdout(), entry)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "total: %s, free: %s, used: %s\n", printBytes(entry.Total), printBytes(entry.Free), printBytes(entry.Used))
		return nil
	}
}

// DiskUsageResult is the machine-readable output of fs du.
type DiskUsageResult struct {
	Path  string `json:"path"`
	Files int    `json:"files"`
	Bytes int64  `json:"bytes"`
}

func NewDuCmd(rt cliruntime.Runtime) *cobra.Command {
	cmd := &cobra.Command{
		Use:               "du [REMOTE]",
		Short:             "Show disk usage of a directory tree",
		Args:              cliruntime.MaxArgs(rt, 1),
		RunE:              runDu(rt),
		ValidArgsFunction: cliruntime.NoFileCompletions,
	}
	cmd.Flags().Bool("json", false, "write JSON output")
	cmd.Flags().Bool("bytes", false, "raw byte counts instead of human-readable sizes")
	return cmd
}

func runDu(rt cliruntime.Runtime) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		path := "/"
		if len(args) > 0 {
			path = args[0]
		}
		ctx, fs, cleanup, err := rt.OpenFileSystem(cmd)
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
			snap, err := syncer.SnapshotVFS(ctx, fs, path)
			if err != nil {
				return err
			}
			files = snap.FileCount()
			for _, e := range snap {
				if !e.IsDir {
					bytes += e.Size
				}
			}
		} else {
			files = 1
			bytes = entry.Size
		}
		result := DiskUsageResult{Path: path, Files: files, Bytes: bytes}
		asJSON, _ := cmd.Flags().GetBool("json")
		if asJSON {
			return cliruntime.WritePrettyJSON(cmd.OutOrStdout(), result)
		}
		asBytes, _ := cmd.Flags().GetBool("bytes")
		if asBytes {
			fmt.Fprintf(cmd.OutOrStdout(), "%d files, %d bytes\n", files, bytes)
			return nil
		}
		fmt.Fprintf(cmd.OutOrStdout(), "%d files, %s\n", files, cliruntime.FormatBytes(bytes))
		return nil
	}
}
