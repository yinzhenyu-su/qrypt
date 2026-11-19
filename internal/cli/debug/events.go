package debug

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	cliruntime "github.com/yinzhenyu/qrypt/internal/cli/runtime"
	"github.com/yinzhenyu/qrypt/pkg/control"
	"github.com/yinzhenyu/qrypt/pkg/logging"
)

// NewEventsCmd lists the mount's recent structured log events. The default
// output is one line per event; --json wraps the raw /v1/events response in
// the stable diagnostics envelope so scripts never parse the human layout.
func NewEventsCmd(rt cliruntime.Runtime) *cobra.Command {
	var level string
	var limit int
	var path string
	var component string
	var mounts []string
	var jsonOutput bool
	cmd := &cobra.Command{
		Use:   "events",
		Short: "List recent structured log events",
		Long: `List recent structured log events from the running mount. The event
buffer keeps the newest entries; --level sets the minimum severity, and
--mount, --path, and --component narrow the result.`,
		Example: `  qrypt debug events --level warn --limit 20
  qrypt debug events --mount cloud --component QUARK
  qrypt debug events --json --limit 100`,
		Args:              cliruntime.NoArgs(rt),
		ValidArgsFunction: cliruntime.NoFileCompletions,
		RunE: func(cmd *cobra.Command, args []string) error {
			endpoint := eventsEndpoint(mounts, path, component, level, limit)
			body, err := debugSocketGet(cmd.Context(), endpoint)
			if err != nil {
				return err
			}
			if jsonOutput {
				envelope := newDiagnosticsEnvelope("debug events", debugSocketFromContext(cmd.Context()), body)
				return writeDiagnosticsEnvelope(cmd.OutOrStdout(), envelope, true)
			}
			var response control.EventsResponse
			if err := json.Unmarshal(body, &response); err != nil {
				_, writeErr := cmd.OutOrStdout().Write(body)
				return writeErr
			}
			return printDebugEvents(cmd.OutOrStdout(), response.Events)
		},
	}
	cmd.Flags().StringVar(&level, "level", "warn", "minimum event level (debug, info, warn, or error)")
	cmd.Flags().IntVar(&limit, "limit", 100, "maximum number of events to return")
	cmd.Flags().StringVar(&path, "path", "", "virtual path substring to filter by")
	cmd.Flags().StringVar(&component, "component", "", "log component to filter by, such as QUARK")
	cmd.Flags().StringArrayVar(&mounts, "mount", nil, "mount name to inspect (repeatable)")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "print stable JSON instead of human-readable lines")
	return cmd
}

func eventsEndpoint(mounts []string, path, component, level string, limit int) string {
	q := url.Values{}
	if level = strings.TrimSpace(level); level != "" {
		q.Set("level", level)
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	if path = strings.TrimSpace(path); path != "" {
		q.Set("path", path)
	}
	if component = strings.TrimSpace(component); component != "" {
		q.Set("component", component)
	}
	endpoint := "/v1/events"
	if encoded := q.Encode(); encoded != "" {
		endpoint += "?" + encoded
	}
	return debugEndpointWithMounts(endpoint, cleanDebugMountNames(mounts))
}

func printDebugEvents(out io.Writer, events []logging.Event) error {
	if len(events) == 0 {
		_, err := fmt.Fprintln(out, "no events")
		return err
	}
	for _, event := range events {
		component := event.Component
		if component == "" {
			component = "-"
		}
		mount := event.Mount
		if mount == "" {
			mount = "-"
		}
		if _, err := fmt.Fprintf(out, "%s %-5s %-10s %-12s %s\n",
			event.Time.Format(time.RFC3339), event.Level, component, mount, event.Message); err != nil {
			return err
		}
	}
	return nil
}
