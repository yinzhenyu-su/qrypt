package debug

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	cliruntime "github.com/yinzhenyu/qrypt/internal/cli/runtime"
	"github.com/yinzhenyu/qrypt/pkg/contracttest"
	"github.com/yinzhenyu/qrypt/pkg/control"
	"github.com/yinzhenyu/qrypt/pkg/vfs/vfstypes"
)

// NewCacheCmd owns cache maintenance that must run inside the mount process.
// Clearing there drops both the durable chunks and the in-memory fast paths;
// deleting the cache directory from another process would leave the running
// index pointing at missing files.
func NewCacheCmd(rt cliruntime.Runtime) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cache",
		Short: "Clear or warm mounted read-cache state",
		Long: `Clear or warm qrypt's read cache inside a running mount. The mount keeps
running; later reads start cold, which is what scripted playback and benchmark
runs need when they measure cache-miss load time.`,
		Example: `  qrypt probe cache clear --mount cloud
  qrypt probe cache fill --mount cloud --path /cloud/movie.mp4 --bytes 64m`,
		Args: cliruntime.CommandGroupArgs(rt, nil),
		RunE: cliruntime.ShowHelp,
	}
	cmd.AddCommand(withProbeSocketFlag(rt, newProbeCacheClearCmd(rt)))
	cmd.AddCommand(withProbeSocketFlag(rt, newProbeCacheInvalidateCmd(rt)))
	cmd.AddCommand(withProbeSocketFlag(rt, newProbeCacheFillCmd(rt)))
	return cmd
}

func newProbeCacheClearCmd(rt cliruntime.Runtime) *cobra.Command {
	var jsonOutput bool
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
				return cacheCommandError("clear", err)
			}
			return writeCacheResponse(cmd, "probe cache clear", body, jsonOutput)
		},
	}
	cmd.Flags().String("mount", "", "mount name whose read cache to clear")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "print stable JSON instead of the raw response body")
	return cmd
}

func newProbeCacheInvalidateCmd(rt cliruntime.Runtime) *cobra.Command {
	var jsonOutput bool
	cmd := &cobra.Command{
		Use:   "invalidate",
		Short: "Drop one file's read cache",
		Long: `Drop one file's durable read-cache chunks, hot chunks, range-hit state,
and sequential-read state. Other files keep their cache; subsequent reads of
this file fetch from the provider again. Pass --mount to confirm the path
resolves to that mount.`,
		Example: `  qrypt probe cache invalidate --path /cloud/movie.mp4`,
		// Drop just one file's chunks; other files keep their cache.

		Args:              cliruntime.NoArgs(rt),
		ValidArgsFunction: cliruntime.NoFileCompletions,
		RunE: func(cmd *cobra.Command, args []string) error {
			path, _ := cmd.Flags().GetString("path")
			path = strings.TrimSpace(path)
			if path == "" {
				return fmt.Errorf("cache invalidate requires --path")
			}
			path = vfstypes.CleanVirtualPath(path)
			mount, _ := cmd.Flags().GetString("mount")
			mount = contracttest.NormalizeMountName(mount)
			endpoint := "/v1/debug/cache/invalidate?path=" + url.QueryEscape(path)
			body, err := debugSocketPostJSON(cmd.Context(), endpoint, map[string]string{})
			if err != nil {
				return cacheCommandError("invalidate", err)
			}
			if mount != "" {
				var resolved struct {
					Mount string `json:"mount"`
				}
				if json.Unmarshal(body, &resolved) == nil && resolved.Mount != "" && !strings.EqualFold(resolved.Mount, mount) {
					return fmt.Errorf("cache invalidate resolved mount %q, want %q; check that --path belongs to --mount", resolved.Mount, mount)
				}
			}
			return writeCacheResponse(cmd, "probe cache invalidate", body, jsonOutput)
		},
	}
	cmd.Flags().String("path", "", "virtual path of the file whose read cache should be dropped")
	cmd.Flags().String("mount", "", "optional mount name to confirm the path resolves to that mount")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "print stable JSON instead of the raw response body")
	return cmd
}

// newProbeCacheFillCmd warms one file's read cache through the mount, so a
// later measurement can start warm without reading the file in a separate
// process (which would bypass the mount's cache).
func newProbeCacheFillCmd(rt cliruntime.Runtime) *cobra.Command {
	var jsonOutput bool
	cmd := &cobra.Command{
		Use:   "fill",
		Short: "Warm one file's read cache through the mount",
		Long: `Read one file through the mounted VFS to warm qrypt's read cache without
touching the provider from outside the mount process. --bytes limits how much
of the file is read (default 64 MiB, capped at the file size and the server's
per-request limit).`,
		Example:           `  qrypt probe cache fill --mount cloud --path /cloud/movie.mp4 --bytes 64m`,
		Args:              cliruntime.NoArgs(rt),
		ValidArgsFunction: cliruntime.NoFileCompletions,
		RunE: func(cmd *cobra.Command, args []string) error {
			mount, _ := cmd.Flags().GetString("mount")
			mount = contracttest.NormalizeMountName(mount)
			if mount == "" {
				return fmt.Errorf("cache fill requires --mount")
			}
			path, _ := cmd.Flags().GetString("path")
			path = strings.TrimSpace(path)
			if path == "" {
				return fmt.Errorf("cache fill requires --path")
			}
			path = vfstypes.CleanVirtualPath(path)
			bytesValue, _ := cmd.Flags().GetString("bytes")
			bytesSize := contracttest.ParseXferSize(bytesValue)
			if strings.TrimSpace(bytesValue) != "" && bytesSize < 0 {
				return fmt.Errorf("cache fill --bytes must not be negative")
			}
			blockValue, _ := cmd.Flags().GetString("block-size")
			blockSize := contracttest.ParseXferSize(blockValue)
			if blockSize <= 0 {
				return fmt.Errorf("cache fill --block-size must be a positive byte count")
			}
			endpoint := "/v1/debug/cache/fill?mount=" + url.QueryEscape(mount) +
				"&path=" + url.QueryEscape(path) +
				"&bytes=" + strconv.FormatInt(bytesSize, 10) +
				"&block_size=" + strconv.FormatInt(blockSize, 10)
			body, err := debugSocketPostJSON(cmd.Context(), endpoint, map[string]string{})
			if err != nil {
				return cacheCommandError("fill", err)
			}
			if jsonOutput {
				return writeCacheResponse(cmd, "probe cache fill", body, true)
			}
			var response control.DebugCacheFillResponse
			if err := json.Unmarshal(body, &response); err != nil {
				_, writeErr := cmd.OutOrStdout().Write(body)
				return writeErr
			}
			return printCacheFill(cmd, response)
		},
	}
	cmd.Flags().String("mount", "", "mount name whose read cache to warm")
	cmd.Flags().String("path", "", "virtual path of the file to read through the mount")
	cmd.Flags().String("bytes", "64m", "how much of the file to read; 0 reads up to the server limit")
	cmd.Flags().String("block-size", "1m", "read block size used while warming")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "print stable JSON instead of human-readable output")
	return cmd
}

func cacheCommandError(action string, err error) error {
	if detail, routeMissing := debugSocket404Detail(err); detail != "" {
		return fmt.Errorf("cache %s failed: %s", action, detail)
	} else if routeMissing {
		return fmt.Errorf("cache %s endpoint is not available on this socket; restart the qrypt mount process with the current binary", action)
	}
	return err
}

func writeCacheResponse(cmd *cobra.Command, command string, body []byte, jsonOutput bool) error {
	if !jsonOutput {
		_, err := cmd.OutOrStdout().Write(body)
		return err
	}
	envelope := newDiagnosticsEnvelope(command, debugSocketFromContext(cmd.Context()), body)
	return writeDiagnosticsEnvelope(cmd.OutOrStdout(), envelope, true)
}

func printCacheFill(cmd *cobra.Command, response control.DebugCacheFillResponse) error {
	out := cmd.OutOrStdout()
	fileSize := cliruntime.FormatBytes(response.FileSize)
	read := cliruntime.FormatBytes(response.Bytes)
	line := fmt.Sprintf("filled %s of %s %s (file %s) with %d read calls in %dms",
		read, response.Mount, response.Path, fileSize, response.ReadCalls, response.DurationMS)
	if response.CacheBytesAfter > 0 || response.CacheBytesBefore > 0 {
		line += fmt.Sprintf("; read cache %s -> %s",
			cliruntime.FormatBytes(response.CacheBytesBefore), cliruntime.FormatBytes(response.CacheBytesAfter))
	}
	_, err := fmt.Fprintln(out, line)
	return err
}
