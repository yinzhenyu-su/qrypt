package debug

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/spf13/cobra"
	cliruntime "github.com/yinzhenyu/qrypt/internal/cli/runtime"
	"github.com/yinzhenyu/qrypt/pkg/contracttest"
	"github.com/yinzhenyu/qrypt/pkg/vfs/vfstypes"
)

// NewCacheCmd owns cache maintenance that must run inside the mount process.
// Clearing there drops both the durable chunks and the in-memory fast paths;
// deleting the cache directory from another process would leave the running
// index pointing at missing files.
func NewCacheCmd(rt cliruntime.Runtime) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cache",
		Short: "Clear mounted read-cache state",
		Long: `Clear qrypt's read cache inside a running mount. The mount keeps running;
later reads start cold, which is what scripted playback and benchmark runs
need when they measure cache-miss load time.`,
		Example: `  qrypt probe cache clear --mount cloud`,
		Args:    cliruntime.CommandGroupArgs(rt, nil),
		RunE:    cliruntime.ShowHelp,
	}
	cmd.AddCommand(withProbeSocketFlag(rt, newProbeCacheClearCmd(rt)))
	cmd.AddCommand(withProbeSocketFlag(rt, newProbeCacheInvalidateCmd(rt)))
	return cmd
}

func newProbeCacheClearCmd(rt cliruntime.Runtime) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "clear",
		Short: "Clear one mount's read cache",
		Long: `Clear one mount's durable read-cache chunks and in-memory hot chunks,
range-hit state, and sequential-read state. In-flight reads finish normally;
subsequent reads fetch from the provider again.`,
		Example:           `  qrypt probe cache clear --mount cloud`,
		Args:              cliruntime.NoArgs(rt),
		ValidArgsFunction: cliruntime.NoFileCompletions,
		RunE: func(cmd *cobra.Command, args []string) error {
			mount, _ := cmd.Flags().GetString("mount")
			mount = contracttest.NormalizeMountName(mount)
			if mount == "" {
				return fmt.Errorf("cache clear requires --mount")
			}
			endpoint := "/v1/debug/cache/clear?mount=" + url.QueryEscape(mount)
			body, err := debugSocketPostJSON(cmd.Context(), endpoint, map[string]string{})
			if err != nil {
				if detail, routeMissing := debugSocket404Detail(err); detail != "" {
					return fmt.Errorf("cache clear failed: %s", detail)
				} else if routeMissing {
					return fmt.Errorf("cache clear endpoint is not available on this socket; restart the qrypt mount process with the current binary")
				}
				return err
			}
			_, err = cmd.OutOrStdout().Write(body)
			return err
		},
	}
	cmd.Flags().String("mount", "", "mount name whose read cache to clear")
	return cmd
}

func newProbeCacheInvalidateCmd(rt cliruntime.Runtime) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "invalidate",
		Short: "Drop one file's read cache",
		Long: `Drop one file's durable read-cache chunks, hot chunks, range-hit state,
and sequential-read state. Other files keep their cache; subsequent reads of
this file fetch from the provider again.`,
		Example:           `  qrypt probe cache invalidate --path /cloud/movie.mp4`,
		Args:              cliruntime.NoArgs(rt),
		ValidArgsFunction: cliruntime.NoFileCompletions,
		RunE: func(cmd *cobra.Command, args []string) error {
			path, _ := cmd.Flags().GetString("path")
			path = strings.TrimSpace(path)
			if path == "" {
				return fmt.Errorf("cache invalidate requires --path")
			}
			path = vfstypes.CleanVirtualPath(path)
			endpoint := "/v1/debug/cache/invalidate?path=" + url.QueryEscape(path)
			body, err := debugSocketPostJSON(cmd.Context(), endpoint, map[string]string{})
			if err != nil {
				if detail, routeMissing := debugSocket404Detail(err); detail != "" {
					return fmt.Errorf("cache invalidate failed: %s", detail)
				} else if routeMissing {
					return fmt.Errorf("cache invalidate endpoint is not available on this socket; restart the qrypt mount process with the current binary")
				}
				return err
			}
			_, err = cmd.OutOrStdout().Write(body)
			return err
		},
	}
	cmd.Flags().String("path", "", "virtual path of the file whose read cache should be dropped")
	return cmd
}
