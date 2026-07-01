package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"time"

	"github.com/spf13/cobra"
	"github.com/yinzhenyu/qrypt/internal/control"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

func debugDoctor() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor [path]",
		Short: "Aggregate diagnostic overview of the running qrypt process",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			client := &debugSocketClient{debugSocket}

			path := "/"
			if len(args) > 0 {
				path = args[0]
			}

			// Fetch health
			healthRaw, healthErr := client.get(ctx, "/v1/health")

			// Fetch state
			stateRaw, stateErr := client.get(ctx, "/v1/state")

			// Fetch events
			eventsRaw, eventsErr := client.get(ctx, "/v1/events")

			// Fetch uploads
			uploadsRaw, uploadsErr := client.get(ctx, "/v1/uploads")

			// Fetch staging
			stagingEndpoint := "/v1/staging?path=" + url.QueryEscape(path)
			stagingRaw, stagingErr := client.get(ctx, stagingEndpoint)

			// Fetch cache
			cacheEndpoint := "/v1/cache?path=" + url.QueryEscape(path)
			cacheRaw, cacheErr := client.get(ctx, cacheEndpoint)

			fmt.Println("=== Health ===")
			if healthErr != nil {
				fmt.Printf("  error: %v\n", healthErr)
			} else {
				fmt.Printf("  %s\n", string(healthRaw))
			}

			fmt.Println()
			fmt.Println("=== State ===")
			if stateErr != nil {
				fmt.Printf("  error: %v\n", stateErr)
			} else {
				var snap vfs.DebugSnapshot
				if err := json.Unmarshal(stateRaw, &snap); err != nil {
					fmt.Printf("  parse error: %v\n", err)
				} else {
					fmt.Printf("  Process PID: %d, Started: %s\n", snap.Process.PID, snap.Process.StartedAt.Format(time.RFC3339))
					fmt.Printf("  Mounts: %d\n", len(snap.Mounts))
					for _, m := range snap.Mounts {
						pending := len(m.Pending)
						uploads := len(m.Uploads)
						fmt.Printf("    %s: pending=%d uploads=%d encrypted=%v\n", m.Name, pending, uploads, m.Encrypted)
					}
				}
			}

			fmt.Println()
			fmt.Println("=== Events ===")
			if eventsErr != nil {
				fmt.Printf("  error: %v\n", eventsErr)
			} else {
				var evResp control.EventsResponse
				if err := json.Unmarshal(eventsRaw, &evResp); err != nil {
					fmt.Printf("  parse error: %v\n", err)
				} else {
					byLevel := map[string]int{}
					for _, e := range evResp.Events {
						byLevel[e.Level]++
					}
					for _, lvl := range sortedKeys(byLevel) {
						fmt.Printf("  %s: %d\n", lvl, byLevel[lvl])
					}
				}
			}

			fmt.Println()
			fmt.Println("=== Uploads ===")
			if uploadsErr != nil {
				fmt.Printf("  error: %v\n", uploadsErr)
			} else {
				var upResp control.UploadsResponse
				if err := json.Unmarshal(uploadsRaw, &upResp); err != nil {
					fmt.Printf("  parse error: %v\n", err)
				} else {
					fmt.Printf("  Total uploads: %d\n", len(upResp.Uploads))
					active := 0
					for _, u := range upResp.Uploads {
						if u.BytesUploaded < u.BytesTotal && u.BytesTotal > 0 {
							active++
						}
					}
					fmt.Printf("  Active: %d\n", active)
				}
			}

			fmt.Println()
			fmt.Printf("=== Staging (path=%s) ===\n", path)
			if stagingErr != nil {
				fmt.Printf("  error: %v\n", stagingErr)
			} else {
				var st control.StagingResponse
				if err := json.Unmarshal(stagingRaw, &st); err != nil {
					fmt.Printf("  raw: %s\n", string(stagingRaw))
				} else {
					totalFiles := 0
					for _, m := range st.Mounts {
						totalFiles += len(m.Files)
					}
					fmt.Printf("  Files: %d\n", totalFiles)
				}
			}

			fmt.Println()
			fmt.Printf("=== Cache (path=%s) ===\n", path)
			if cacheErr != nil {
				fmt.Printf("  error: %v\n", cacheErr)
			} else {
				var cr control.CacheResponse
				if err := json.Unmarshal(cacheRaw, &cr); err != nil {
					fmt.Printf("  raw: %s\n", string(cacheRaw))
				} else {
					totalChunks := 0
					for _, m := range cr.Mounts {
						totalChunks += m.Cache.ChunkCount
					}
					fmt.Printf("  Chunks: %d\n", totalChunks)
				}
			}

			return nil
		},
	}
}

func sortedKeys(m map[string]int) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
