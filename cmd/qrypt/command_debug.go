package main

import (
	"archive/zip"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/yinzhenyu/qrypt/internal/control"
)

var debugSocket string

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
	cmd.AddCommand(newJournalCmdWithUse("journal"))
	cmd.AddCommand(withDebugSocketFlag(newDebugRawCmd()))
	return cmd
}

type debugSocketClient struct{}

func withDebugSocketFlag(cmd *cobra.Command) *cobra.Command {
	cmd.Flags().StringVar(&debugSocket, "socket", "", "debug socket path")
	return cmd
}

func (d debugSocketClient) get(endpoint string) ([]byte, error) {
	if debugSocket == "" {
		return nil, fmt.Errorf("this command requires --socket")
	}
	client, err := control.NewClient(debugSocket)
	if err != nil {
		return nil, err
	}
	return client.Get(context.Background(), endpoint)
}

func newDebugRawCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "raw ENDPOINT",
		Short: "Fetch a raw debug socket endpoint",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			endpoint := args[0]
			switch {
			case endpoint == "":
				return fmt.Errorf("endpoint required")
			case strings.HasPrefix(endpoint, "/v1/"):
			case endpoint[0] == '/':
				return fmt.Errorf("debug raw expects a /v1 endpoint, got virtual path %q; use 'qrypt debug inspect %s --socket %s' or 'qrypt debug raw /v1/resolve?path=%s --socket %s'",
					endpoint, endpoint, debugSocket, url.QueryEscape(endpoint), debugSocket)
			case len(endpoint) >= 3 && endpoint[:3] == "v1/":
				endpoint = "/" + endpoint
			default:
				endpoint = "/v1/" + endpoint
			}
			c := debugSocketClient{}
			body, err := c.get(endpoint)
			if err != nil {
				return err
			}
			_, err = os.Stdout.Write(body)
			return err
		},
	}
}

func newDebugBundleCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "bundle",
		Short: "Write an AI-oriented debug bundle zip",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			outPath, _ := cmd.Flags().GetString("out")
			path, _ := cmd.Flags().GetString("path")
			eventLimit, _ := cmd.Flags().GetInt("events-limit")
			includeDriverHealth, _ := cmd.Flags().GetBool("driver-health")
			watchDuration, _ := cmd.Flags().GetDuration("watch")
			watchInterval, _ := cmd.Flags().GetDuration("watch-interval")
			if debugSocket == "" {
				return fmt.Errorf("this command requires --socket")
			}
			if outPath == "" {
				return fmt.Errorf("usage: qrypt debug bundle --out FILE (requires --socket)")
			}
			client, err := control.NewClient(debugSocket)
			if err != nil {
				return err
			}
			out, err := os.Create(outPath)
			if err != nil {
				return err
			}
			defer out.Close()
			zw := zip.NewWriter(out)
			defer zw.Close()

			cleanPath := cleanDebugPath(path)
			collect := collectDebugAIReport(ctx, "bundle", cleanPath, eventLimit, includeDriverHealth)
			diagnostics := collect.Diagnostics
			if err := writeZipJSON(zw, "manifest.json", debugBundleManifest{
				SchemaVersion: debugAIReportSchemaVersion,
				GeneratedAt:   time.Now(),
				Socket:        debugSocket,
				Path:          cleanPath,
				Files:         debugBundleFiles(cleanPath, includeDriverHealth, watchDuration > 0),
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
				if watchInterval <= 0 {
					return fmt.Errorf("--watch-interval must be greater than 0")
				}
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
			if includeDriverHealth {
				endpoints["raw/driver-health.json"] = "/v1/driver?health=true"
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
			return nil
		},
	}
	cmd.Flags().String("out", "", "debug bundle output zip")
	cmd.Flags().String("path", "", "optional path to inspect")
	cmd.Flags().Int("events-limit", 200, "maximum recent warn/error events in collect.json")
	cmd.Flags().Bool("driver-health", false, "run live driver health checks")
	cmd.Flags().Duration("watch", 0, "optional watch duration to include watch.json")
	cmd.Flags().Duration("watch-interval", 2*time.Second, "watch sampling interval")
	return cmd
}

type debugBundleManifest struct {
	SchemaVersion int       `json:"schema_version"`
	GeneratedAt   time.Time `json:"generated_at"`
	Socket        string    `json:"socket,omitempty"`
	Path          string    `json:"path,omitempty"`
	Files         []string  `json:"files"`
}

func debugBundleFiles(path string, includeDriverHealth, includeWatch bool) []string {
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
	if includeDriverHealth {
		files = append(files, "raw/driver-health.json")
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
