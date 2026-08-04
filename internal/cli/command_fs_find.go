package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

func newFsFindCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:               "find [REMOTE] NAME",
		Short:             "Recursively find entries whose name contains NAME",
		Args:              rangeArgs(1, 2),
		RunE:              runFind,
		ValidArgsFunction: noFileCompletions,
	}
	cmd.Flags().Bool("json", false, "write JSON output")
	return cmd
}

func runFind(cmd *cobra.Command, args []string) error {
	path := "/"
	needle := args[0]
	if len(args) == 2 {
		path, needle = args[0], args[1]
	}
	if needle == "" {
		return commandUsageError(cmd, "NAME must not be empty")
	}
	ctx, fs, cleanup, err := openFileSystem(cmd)
	if err != nil {
		return err
	}
	defer cleanup()

	needle = strings.ToLower(needle)
	var matches []drive.Entry
	var matchPaths []string
	if err := walkFind(ctx, fs, path, needle, &matches, &matchPaths); err != nil {
		return err
	}
	asJSON, _ := cmd.Flags().GetBool("json")
	if asJSON {
		if matches == nil {
			matches = []drive.Entry{}
		}
		return writePrettyJSON(cmd.OutOrStdout(), matches)
	}
	for _, path := range matchPaths {
		fmt.Fprintln(cmd.OutOrStdout(), path)
	}
	return nil
}

// walkFind recursively lists path, collecting entries whose name contains
// the (lowercased) needle, case-insensitively.
func walkFind(ctx context.Context, fs vfs.FileSystem, path, needle string, matches *[]drive.Entry, matchPaths *[]string) error {
	entries, err := fs.List(ctx, path)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		child := strings.TrimSuffix(path, "/") + "/" + entry.Name
		if strings.Contains(strings.ToLower(entry.Name), needle) {
			*matches = append(*matches, entry)
			*matchPaths = append(*matchPaths, child)
		}
		if entry.IsDir {
			if err := walkFind(ctx, fs, child, needle, matches, matchPaths); err != nil {
				return err
			}
		}
	}
	return nil
}
