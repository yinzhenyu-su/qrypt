package fs

import (
	"context"
	"fmt"
	pathpkg "path"
	"strings"

	"github.com/spf13/cobra"
	cliruntime "github.com/yinzhenyu/qrypt/internal/cli/runtime"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

// FindResult pairs a match with its full virtual path so JSON consumers can
// tell same-named files in different directories apart.
type FindResult struct {
	Path  string      `json:"path"`
	Entry drive.Entry `json:"entry"`
}

func NewFindCmd(rt cliruntime.Runtime) *cobra.Command {
	cmd := &cobra.Command{
		Use:               "find [REMOTE] NAME",
		Short:             "Recursively find entries whose name contains NAME",
		Args:              cliruntime.RangeArgs(rt, 1, 2),
		RunE:              runFind(rt),
		ValidArgsFunction: cliruntime.NoFileCompletions,
	}
	cmd.Flags().Bool("json", false, "write JSON output")
	return cmd
}

func runFind(rt cliruntime.Runtime) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		path := "/"
		needle := args[0]
		if len(args) == 2 {
			path, needle = args[0], args[1]
		}
		if needle == "" {
			return rt.UsageError(cmd, "NAME must not be empty")
		}
		// Normalize so results always carry canonical /mount/... paths even when
		// the caller passed a relative or dot-prefixed path.
		path = cliruntime.CleanListPath(path)
		ctx, fs, cleanup, err := rt.OpenFileSystem(cmd)
		if err != nil {
			return err
		}
		defer cleanup()

		needle = strings.ToLower(needle)
		var matches []FindResult
		if err := walkFind(ctx, fs, path, needle, &matches); err != nil {
			return err
		}
		asJSON, _ := cmd.Flags().GetBool("json")
		if asJSON {
			if matches == nil {
				matches = []FindResult{}
			}
			return cliruntime.WritePrettyJSON(cmd.OutOrStdout(), matches)
		}
		for _, match := range matches {
			fmt.Fprintln(cmd.OutOrStdout(), match.Path)
		}
		return nil
	}
}

// walkFind recursively lists path, collecting entries whose name contains the
// (lowercased) needle, case-insensitively.
func walkFind(ctx context.Context, fs vfs.Reader, path, needle string, matches *[]FindResult) error {
	entries, err := fs.List(ctx, path)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		child := pathpkg.Join(path, entry.Name)
		if strings.Contains(strings.ToLower(entry.Name), needle) {
			*matches = append(*matches, FindResult{Path: child, Entry: entry})
		}
		if entry.IsDir {
			if err := walkFind(ctx, fs, child, needle, matches); err != nil {
				return err
			}
		}
	}
	return nil
}
