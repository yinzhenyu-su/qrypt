package main

import (
	"archive/zip"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/yinzhenyu/qrypt/internal/control"
	"github.com/yinzhenyu/qrypt/pkg/osutil"
)

func newDebugCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "debug",
		Short: "Collect AI-oriented diagnostic data",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}
	cmd.AddCommand(withDebugSocketFlag(newDebugBundleCmd()))
	cmd.AddCommand(withDebugSocketFlag(newDebugCollectCmd()))
	cmd.AddCommand(withDebugSocketFlag(newDebugInspectCmd()))
	cmd.AddCommand(withDebugSocketFlag(newDebugWatchCmd()))
	cmd.AddCommand(withConfigFlag(newJournalCmdWithUse("journal")))
	cmd.AddCommand(withDebugSocketFlag(newDebugRawCmd()))
	return cmd
}

type debugSocketContextKey struct{}

func withDebugSocketFlag(cmd *cobra.Command) *cobra.Command {
	cmd.Flags().StringP("socket", "s", "", "debug socket path (required)")
	if err := cmd.MarkFlagRequired("socket"); err != nil {
		panic(err)
	}
	run := cmd.RunE
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		socket, err := cmd.Flags().GetString("socket")
		if err != nil {
			return err
		}
		cmd.SetContext(context.WithValue(commandContext(cmd), debugSocketContextKey{}, socket))
		return run(cmd, args)
	}
	return cmd
}

func debugSocketFromContext(ctx context.Context) string {
	socket, _ := ctx.Value(debugSocketContextKey{}).(string)
	return socket
}

func debugSocketGet(ctx context.Context, endpoint string) ([]byte, error) {
	socket := debugSocketFromContext(ctx)
	if socket == "" {
		return nil, fmt.Errorf("this command requires --socket")
	}
	client, err := control.NewClient(socket)
	if err != nil {
		return nil, err
	}
	return client.Get(ctx, endpoint)
}

func newDebugRawCmd() *cobra.Command {
	return &cobra.Command{
		Use:               "raw ENDPOINT",
		Short:             "Fetch a raw debug socket endpoint",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: noFileCompletions,
		RunE: func(cmd *cobra.Command, args []string) error {
			socket := debugSocketFromContext(cmd.Context())
			endpoint := args[0]
			switch {
			case endpoint == "":
				return fmt.Errorf("endpoint required")
			case strings.HasPrefix(endpoint, "/v1/"):
			case endpoint[0] == '/':
				return fmt.Errorf("debug raw expects a /v1 endpoint, got virtual path %q; use 'qrypt debug inspect %s --socket %s' or 'qrypt debug raw /v1/resolve?path=%s --socket %s'",
					endpoint, endpoint, socket, url.QueryEscape(endpoint), socket)
			case len(endpoint) >= 3 && endpoint[:3] == "v1/":
				endpoint = "/" + endpoint
			default:
				endpoint = "/v1/" + endpoint
			}
			body, err := debugSocketGet(cmd.Context(), endpoint)
			if err != nil {
				return err
			}
			_, err = cmd.OutOrStdout().Write(body)
			return err
		},
	}
}

func newDebugBundleCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "bundle [REMOTE]",
		Short: "Write an AI-oriented debug bundle zip",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			socket := debugSocketFromContext(ctx)
			outPath, _ := cmd.Flags().GetString("out")
			path := ""
			if len(args) == 1 {
				path = args[0]
			}
			eventLimit, _ := cmd.Flags().GetInt("events-limit")
			includeMountHealth, _ := cmd.Flags().GetBool("mount-health")
			watchDuration, _ := cmd.Flags().GetDuration("watch")
			watchInterval, _ := cmd.Flags().GetDuration("watch-interval")
			force, _ := cmd.Flags().GetBool("force")
			if eventLimit < 0 {
				return fmt.Errorf("--events-limit must not be negative")
			}
			if socket == "" {
				return fmt.Errorf("this command requires --socket")
			}
			if outPath == "" {
				return fmt.Errorf("usage: qrypt debug bundle --out FILE (requires --socket)")
			}
			if watchDuration < 0 {
				return fmt.Errorf("--watch must not be negative")
			}
			if watchDuration == 0 && cmd.Flags().Changed("watch-interval") {
				return fmt.Errorf("--watch-interval requires --watch")
			}
			if watchDuration > 0 && watchInterval <= 0 {
				return fmt.Errorf("--watch-interval must be greater than 0")
			}
			if watchDuration > 0 && watchInterval > watchDuration {
				return fmt.Errorf("--watch-interval must not exceed --watch")
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
			out, err := os.CreateTemp(filepath.Dir(outPath), ".qrypt-debug-*.zip")
			if err != nil {
				return err
			}
			tmpPath := out.Name()
			defer os.Remove(tmpPath)
			defer out.Close()
			zw := zip.NewWriter(out)

			cleanPath := cleanDebugPath(path)
			collect := collectDebugAIReport(ctx, "bundle", cleanPath, eventLimit, includeMountHealth)
			diagnostics := collect.Diagnostics
			if err := writeZipJSON(zw, "manifest.json", debugBundleManifest{
				SchemaVersion: debugAIReportSchemaVersion,
				GeneratedAt:   time.Now(),
				Socket:        socket,
				Path:          cleanPath,
				Files:         debugBundleFiles(cleanPath, includeMountHealth, watchDuration > 0),
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
			if err := writeZipJSON(zw, "diagnostics.json", diagnostics); err != nil {
				return err
			}
			if watchDuration > 0 {
				watch := watchDebugAI(ctx, cleanPath, watchDuration, watchInterval, eventLimit)
				if err := writeZipJSON(zw, "watch.json", watch); err != nil {
					return err
				}
			}

			endpoints := map[string]string{
				"raw/health.json":    "/v1/health",
				"raw/state.json":     "/v1/state",
				"raw/pending.json":   "/v1/pending",
				"raw/uploads.json":   "/v1/uploads?history=1",
				"raw/events.json":    "/v1/events?level=warn&limit=500",
				"raw/cache.json":     "/v1/cache",
				"raw/staging.json":   "/v1/staging",
				"raw/driver.json":    "/v1/driver",
				"raw/runtime.json":   "/v1/runtime",
				"raw/goroutines.txt": "/v1/goroutines?debug=1",
			}
			if includeMountHealth {
				endpoints["raw/mount-health.json"] = "/v1/mounts/health"
			}
			if cleanPath != "" {
				escapedPath := url.QueryEscape(cleanPath)
				endpoints["raw/resolve-path.json"] = "/v1/resolve?path=" + escapedPath + "&include_remote_name=1"
				if cleanPath != "/" {
					endpoints["raw/cache-path.json"] = "/v1/cache?path=" + escapedPath
				}
				endpoints["raw/staging-path.json"] = "/v1/staging?path=" + escapedPath
				endpoints["raw/uploads-path.json"] = "/v1/uploads?history=1&path=" + escapedPath
				endpoints["raw/consistency-path.json"] = "/v1/consistency?path=" + escapedPath
				endpoints["raw/events-path.json"] = "/v1/events?level=warn&limit=500&path=" + escapedPath
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
			if err := zw.Close(); err != nil {
				return err
			}
			if err := out.Close(); err != nil {
				return err
			}
			if force {
				if err := replaceLocalFile(tmpPath, outPath); err != nil {
					return err
				}
			} else if err := os.Rename(tmpPath, outPath); err != nil {
				return err
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "Wrote debug bundle to %s\n", outPath)
			return nil
		},
		ValidArgsFunction: noFileCompletions,
	}
	cmd.Flags().StringP("out", "o", "", "debug bundle output zip (required)")
	cmd.Flags().Int("events-limit", 200, "maximum recent warn/error events in collect.json")
	cmd.Flags().Bool("mount-health", false, "include runtime mount health")
	cmd.Flags().BoolP("force", "f", false, "overwrite an existing output file")
	cmd.Flags().Duration("watch", 0, "optional watch duration to include watch.json")
	cmd.Flags().Duration("watch-interval", 2*time.Second, "watch sampling interval")
	if err := cmd.MarkFlagRequired("out"); err != nil {
		panic(err)
	}
	return cmd
}

type debugBundleManifest struct {
	SchemaVersion int       `json:"schema_version"`
	GeneratedAt   time.Time `json:"generated_at"`
	Socket        string    `json:"socket,omitempty"`
	Path          string    `json:"path,omitempty"`
	Files         []string  `json:"files"`
}

func debugBundleFiles(path string, includeMountHealth, includeWatch bool) []string {
	files := []string{
		"manifest.json",
		"collect.json",
		"diagnostics.json",
		"raw/health.json",
		"raw/state.json",
		"raw/pending.json",
		"raw/uploads.json",
		"raw/events.json",
		"raw/cache.json",
		"raw/staging.json",
		"raw/driver.json",
		"raw/runtime.json",
		"raw/goroutines.txt",
	}
	if path != "" {
		files = append(files,
			"inspect.json",
			"raw/resolve-path.json",
			"raw/staging-path.json",
			"raw/uploads-path.json",
			"raw/consistency-path.json",
			"raw/events-path.json",
		)
		if path != "/" {
			files = append(files, "raw/cache-path.json")
		}
	}
	if includeMountHealth {
		files = append(files, "raw/mount-health.json")
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
