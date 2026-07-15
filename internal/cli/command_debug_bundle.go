package cli

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/spf13/cobra"
	"github.com/yinzhenyu/qrypt/internal/control"
	"github.com/yinzhenyu/qrypt/internal/fileutil"
	"github.com/yinzhenyu/qrypt/pkg/osutil"
)

func newDebugBundleCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "bundle [REMOTE]",
		Short: "Write an AI-oriented debug bundle zip",
		Args:  maxArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			socket := debugSocketFromContext(ctx)
			outPath, _ := cmd.Flags().GetString("out")
			path := ""
			if len(args) == 1 {
				path = args[0]
			}
			eventLimit, err := nonNegativeIntFlag(cmd, "events-limit")
			if err != nil {
				return err
			}
			includeGoroutines, _ := cmd.Flags().GetBool("goroutines")
			watchDuration, _ := cmd.Flags().GetDuration("watch")
			watchInterval, _ := cmd.Flags().GetDuration("watch-interval")
			force, _ := cmd.Flags().GetBool("force")
			destPath, _ := cmd.Flags().GetString("dest")
			if outPath == "" {
				return commandUsageError(cmd, "missing --out FILE")
			}
			mounts, allMounts, err := debugMountScopeFromFlags(cmd)
			if err != nil {
				return err
			}
			if watchDuration < 0 {
				return fmt.Errorf("--watch must not be negative")
			}
			if watchDuration == 0 && cmd.Flags().Changed("watch-interval") {
				return fmt.Errorf("--watch-interval requires --watch\n\nExample:\n  qrypt debug bundle --socket /tmp/qrypt.sock --out /tmp/qrypt-debug.zip --watch 30s --watch-interval 2s")
			}
			if watchDuration > 0 {
				if err := validateSamplingWindow(watchDuration, watchInterval, "watch", "watch-interval"); err != nil {
					return err
				}
			}
			outPath = osutil.ExpandHome(outPath)
			if info, err := os.Stat(outPath); err == nil {
				if info.IsDir() {
					return fmt.Errorf("output %q is a directory", outPath)
				}
				if !force {
					return fmt.Errorf("output %q already exists (use --force to overwrite)", outPath)
				}
			} else if !os.IsNotExist(err) {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
				return err
			}
			client, err := control.NewClient(socket)
			if err != nil {
				return err
			}
			err = fileutil.WriteAtomic(outPath, ".qrypt-debug-*.zip", 0o600, force, func(out *os.File) error {
				zw := zip.NewWriter(out)

				cleanPath := cleanDebugPath(path)
				cleanDestPath := cleanDebugPath(destPath)
				collect := collectDebugAIReport(ctx, "bundle", cleanPath, cleanDestPath, eventLimit, mounts, allMounts)
				diagnostics := collect.Diagnostics
				if err := writeZipJSON(zw, "manifest.json", debugBundleManifest{
					SchemaVersion:   debugAIReportSchemaVersion,
					GeneratedAt:     time.Now(),
					Socket:          socket,
					MountNames:      mounts,
					AllMounts:       allMounts,
					Path:            cleanPath,
					DestinationPath: cleanDestPath,
					Files:           debugBundleFiles(cleanPath, cleanDestPath, includeGoroutines, watchDuration > 0),
				}); err != nil {
					return err
				}
				if err := writeZipJSON(zw, "collect.json", collect); err != nil {
					return err
				}
				if collect.Inspect != nil {
					if err := writeZipJSON(zw, "inspect.json", collect.Inspect); err != nil {
						return err
					}
				}
				if collect.Destination != nil {
					if err := writeZipJSON(zw, "destination.json", collect.Destination); err != nil {
						return err
					}
				}
				if err := writeZipJSON(zw, "diagnostics.json", diagnostics); err != nil {
					return err
				}
				if watchDuration > 0 {
					watch := watchDebugAI(ctx, cleanPath, watchDuration, watchInterval, eventLimit, mounts, allMounts)
					if err := writeZipJSON(zw, "watch.json", watch); err != nil {
						return err
					}
				}

				endpoints := map[string]string{
					"raw/health.json":       "/v1/health",
					"raw/state.json":        debugEndpointWithMounts("/v1/state", mounts),
					"raw/pending.json":      debugEndpointWithMounts("/v1/pending", mounts),
					"raw/uploads.json":      debugEndpointWithMounts("/v1/uploads?history=1", mounts),
					"raw/reads.json":        debugEndpointWithMounts("/v1/reads", mounts),
					"raw/events.json":       "/v1/events?level=warn&limit=500",
					"raw/cache.json":        debugEndpointWithMounts("/v1/cache", mounts),
					"raw/staging.json":      debugEndpointWithMounts("/v1/staging", mounts),
					"raw/driver.json":       debugEndpointWithMounts("/v1/driver", mounts),
					"raw/runtime.json":      "/v1/runtime",
					"raw/mount-health.json": debugEndpointWithMounts("/v1/mounts/health", mounts),
				}
				if includeGoroutines {
					endpoints["raw/goroutines.txt"] = "/v1/goroutines?debug=1"
				}
				if cleanPath != "" {
					escapedPath := url.QueryEscape(cleanPath)
					endpoints["raw/resolve-path.json"] = debugEndpointWithMounts("/v1/resolve?path="+escapedPath+"&include_remote_name=1", mounts)
					if cleanPath != "/" {
						endpoints["raw/cache-path.json"] = debugEndpointWithMounts("/v1/cache?path="+escapedPath, mounts)
					}
					endpoints["raw/staging-path.json"] = debugEndpointWithMounts("/v1/staging?path="+escapedPath, mounts)
					endpoints["raw/uploads-path.json"] = debugEndpointWithMounts("/v1/uploads?history=1&path="+escapedPath, mounts)
					endpoints["raw/reads-path.json"] = debugEndpointWithMounts("/v1/reads?path="+escapedPath, mounts)
					endpoints["raw/consistency-path.json"] = "/v1/consistency?path=" + escapedPath
					endpoints["raw/events-path.json"] = "/v1/events?level=warn&limit=500&path=" + escapedPath
				}
				if cleanDestPath != "" {
					escapedDest := url.QueryEscape(cleanDestPath)
					endpoints["raw/resolve-destination.json"] = debugEndpointWithMounts("/v1/resolve?path="+escapedDest+"&include_remote_name=1", mounts)
					endpoints["raw/staging-destination.json"] = debugEndpointWithMounts("/v1/staging?path="+escapedDest, mounts)
					endpoints["raw/uploads-destination.json"] = debugEndpointWithMounts("/v1/uploads?history=1&path="+escapedDest, mounts)
					endpoints["raw/reads-destination.json"] = debugEndpointWithMounts("/v1/reads?path="+escapedDest, mounts)
					endpoints["raw/consistency-destination.json"] = "/v1/consistency?path=" + escapedDest
					endpoints["raw/events-destination.json"] = "/v1/events?level=warn&limit=500&path=" + escapedDest
					if cleanDestPath != "/" {
						endpoints["raw/cache-destination.json"] = debugEndpointWithMounts("/v1/cache?path="+escapedDest, mounts)
					}
					if cleanPath != "" {
						endpoints["raw/transfer-context.json"] = transferContextEndpoint(cleanPath, cleanDestPath)
					}
				}
				names := make([]string, 0, len(endpoints))
				for name := range endpoints {
					names = append(names, name)
				}
				sort.Strings(names)
				for _, name := range names {
					body, err := client.Get(ctx, endpoints[name])
					if err != nil {
						body = []byte(err.Error() + "\n")
					}
					if err := writeZipBytes(zw, name, body); err != nil {
						return err
					}
				}
				return zw.Close()
			})
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "Wrote debug bundle to %s\n", outPath)
			return nil
		},
		ValidArgsFunction: noFileCompletions,
	}
	cmd.Flags().StringP("out", "o", "", "debug bundle output zip (required)")
	cmd.Flags().Int("events-limit", 200, "maximum recent warn/error events in collect.json")
	cmd.Flags().Bool("goroutines", false, "include goroutine dump")
	cmd.Flags().BoolP("force", "f", false, "overwrite an existing output file")
	cmd.Flags().Duration("watch", 0, "optional watch duration to include watch.json")
	cmd.Flags().Duration("watch-interval", 2*time.Second, "watch sampling interval")
	cmd.Flags().String("dest", "", "optional destination path for transfer diagnostics")
	addDebugMountScopeFlags(cmd)
	return cmd
}

type debugBundleManifest struct {
	SchemaVersion   int       `json:"schema_version"`
	GeneratedAt     time.Time `json:"generated_at"`
	Socket          string    `json:"socket,omitempty"`
	MountNames      []string  `json:"mount_names,omitempty"`
	AllMounts       bool      `json:"all_mounts,omitempty"`
	Path            string    `json:"path,omitempty"`
	DestinationPath string    `json:"destination_path,omitempty"`
	Files           []string  `json:"files"`
}

func debugBundleFiles(path, destinationPath string, includeGoroutines, includeWatch bool) []string {
	files := []string{
		"manifest.json",
		"collect.json",
		"diagnostics.json",
		"raw/health.json",
		"raw/state.json",
		"raw/pending.json",
		"raw/uploads.json",
		"raw/reads.json",
		"raw/events.json",
		"raw/cache.json",
		"raw/staging.json",
		"raw/driver.json",
		"raw/runtime.json",
		"raw/mount-health.json",
	}
	if path != "" {
		files = append(files,
			"inspect.json",
			"raw/resolve-path.json",
			"raw/staging-path.json",
			"raw/uploads-path.json",
			"raw/reads-path.json",
			"raw/consistency-path.json",
			"raw/events-path.json",
		)
		if path != "/" {
			files = append(files, "raw/cache-path.json")
		}
	}
	if destinationPath != "" {
		files = append(files,
			"destination.json",
			"raw/resolve-destination.json",
			"raw/staging-destination.json",
			"raw/uploads-destination.json",
			"raw/reads-destination.json",
			"raw/consistency-destination.json",
			"raw/events-destination.json",
		)
		if destinationPath != "/" {
			files = append(files, "raw/cache-destination.json")
		}
		if path != "" {
			files = append(files, "raw/transfer-context.json")
		}
	}
	if includeGoroutines {
		files = append(files, "raw/goroutines.txt")
	}
	if includeWatch {
		files = append(files, "watch.json")
	}
	sort.Strings(files)
	return files
}

func writeZipJSON(zw *zip.Writer, name string, value any) error {
	body, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	body = append(body, '\n')
	return writeZipBytes(zw, name, body)
}

func writeZipBytes(zw *zip.Writer, name string, body []byte) error {
	w, err := zw.Create(name)
	if err != nil {
		return err
	}
	_, err = w.Write(body)
	return err
}
