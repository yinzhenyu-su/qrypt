package cli

import (
	"context"
	"fmt"
	pathpkg "path"
	"strings"

	"github.com/spf13/cobra"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

// fsFindResult pairs a match with its full virtual path so JSON consumers can
// tell same-named files in different directories apart.
type fsFindResult struct {
	Path  string      `json:"path"`
	Entry drive.Entry `json:"entry"`
}

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
	// Normalize so results always carry canonical /mount/... paths even when
	// the caller passed a relative or dot-prefixed path.
	path = cleanListPath(path)
	ctx, fs, cleanup, err := openFileSystem(cmd)
	if err != nil {
		return err
	}
	defer cleanup()

	needle = strings.ToLower(needle)
	var matches []fsFindResult
	if err := walkFind(ctx, fs, path, needle, &matches); err != nil {
		return err
	}
	asJSON, _ := cmd.Flags().GetBool("json")
	if asJSON {
		if matches == nil {
			matches = []fsFindResult{}
		}
		return writePrettyJSON(cmd.OutOrStdout(), matches)
	}
	for _, match := range matches {
		fmt.Fprintln(cmd.OutOrStdout(), match.Path)
	}
	return nil
}

// walkFind recursively lists path, collecting entries whose name contains the
// (lowercased) needle, case-insensitively.
func walkFind(ctx context.Context, fs vfs.FileSystem, path, needle string, matches *[]fsFindResult) error {
	entries, err := fs.List(ctx, path)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		child := pathpkg.Join(path, entry.Name)
		if strings.Contains(strings.ToLower(entry.Name), needle) {
			*matches = append(*matches, fsFindResult{Path: child, Entry: entry})
		}
		if entry.IsDir {
			if err := walkFind(ctx, fs, child, needle, matches); err != nil {
				return err
			}
		}
	}
	return nil
}
