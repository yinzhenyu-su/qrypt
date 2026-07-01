package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"time"

	"github.com/spf13/cobra"
	"github.com/yinzhenyu/qrypt/internal/control"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

func debugDoctor() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor [path]",
		Args:  cobra.MaximumNArgs(1),
		Short: "Show diagnostic overview",
		RunE: func(cmd *cobra.Command, args []string) error {
			c := debugSocketClient{}

			fmt.Println("── Health ──────────────────────────────────")
			body, err := c.get("/v1/health")
			if err != nil {
				fmt.Fprintf(os.Stderr, "  health: %v\n", err)
			} else {
				var h control.HealthResponse
				if json.Unmarshal(body, &h) == nil {
					fmt.Printf("  status:  %s\n", map[bool]string{true: "ok", false: "FAIL"}[h.OK])
					fmt.Printf("  api:     %s\n", h.API)
					fmt.Printf("  time:    %s\n", h.Timestamp.Format(time.RFC3339))
				} else {
					os.Stdout.Write(body)
				}
			}

			fmt.Println("\n── State ───────────────────────────────────")
			body, err = c.get("/v1/state")
			if err != nil {
				fmt.Fprintf(os.Stderr, "  state: %v\n", err)
			} else {
				var snap vfs.DebugSnapshot
				if json.Unmarshal(body, &snap) == nil {
					fmt.Printf("  mounts: %d\n", len(snap.Mounts))
					totalPending := 0
					totalActive := 0
					for _, m := range snap.Mounts {
						totalPending += len(m.Pending)
						for _, u := range m.Uploads {
							if u.State == "uploading" {
								totalActive++
							}
						}
					}
					fmt.Printf("  pending uploads: %d\n", totalPending)
					fmt.Printf("  active uploads:  %d\n", totalActive)
					for _, m := range snap.Mounts {
						fmt.Printf("  - %s", m.Name)
						if m.Encrypted {
							fmt.Print(" [encrypted]")
						}
						fmt.Printf(" (driver: %s)\n", m.DriverName)
					}
				} else {
					os.Stdout.Write(body)
				}
			}

			fmt.Println("\n── Recent Events (warn+) ───────────────────")
			body, err = c.get("/v1/events?level=warn&limit=10")
			if err != nil {
				fmt.Fprintf(os.Stderr, "  events: %v\n", err)
			} else {
				var resp control.EventsResponse
				if json.Unmarshal(body, &resp) == nil {
					if len(resp.Events) == 0 {
						fmt.Println("  (none)")
					}
					for _, e := range resp.Events {
						fmt.Printf("  [%s] %s: %s\n", e.Level, e.Time.Format(time.Stamp), e.Message)
					}
				} else {
					os.Stdout.Write(body)
				}
			}

			fmt.Println("\n── Uploads ──────────────────────────────────")
			body, err = c.get("/v1/uploads")
			if err != nil {
				fmt.Fprintf(os.Stderr, "  uploads: %v\n", err)
			} else {
				var resp control.UploadsResponse
				if json.Unmarshal(body, &resp) == nil {
					if len(resp.Uploads) == 0 {
						fmt.Println("  (none)")
					}
					for _, u := range resp.Uploads {
						pct := ""
						if u.BytesTotal > 0 {
							pct = fmt.Sprintf("%.0f%%", float64(u.BytesUploaded)/float64(u.BytesTotal)*100)
						}
						line := fmt.Sprintf("  %s", u.Path)
						if u.State != "" {
							line += fmt.Sprintf(" [%s]", u.State)
							if pct != "" {
								line += " " + pct
							}
						}
						fmt.Println(line)
					}
				} else {
					os.Stdout.Write(body)
				}
			}

			diagnosticPath := "/"
			if len(args) > 0 {
				diagnosticPath = args[0]
			}

			fmt.Printf("\n── Staging (%s) ─────────────────────────────\n", diagnosticPath)
			body, err = c.get("/v1/staging?path=" + url.QueryEscape(diagnosticPath))
			if err != nil {
				fmt.Fprintf(os.Stderr, "  staging: %v\n", err)
			} else {
				os.Stdout.Write(body)
			}

			fmt.Printf("\n── Cache (%s) ───────────────────────────────\n", diagnosticPath)
			body, err = c.get("/v1/cache?path=" + url.QueryEscape(diagnosticPath))
			if err != nil {
				fmt.Fprintf(os.Stderr, "  cache: %v\n", err)
			} else {
				os.Stdout.Write(body)
			}

			return nil
		},
	}
}
