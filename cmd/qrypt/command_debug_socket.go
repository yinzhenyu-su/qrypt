package main

import (
	"context"
	"fmt"
	"net/url"
	"os"

	"github.com/spf13/cobra"
	"github.com/yinzhenyu/qrypt/internal/control"
)

type debugSocketClient struct{}

func (d debugSocketClient) get(endpoint string) ([]byte, error) {
	if debugSocket == "" {
		return nil, fmt.Errorf("this command requires --debug-socket")
	}
	client, err := control.NewClient(debugSocket)
	if err != nil {
		return nil, err
	}
	return client.Get(context.Background(), endpoint)
}

func debugHealth() *cobra.Command {
	return &cobra.Command{
		Use:   "health",
		Short: "Check debug socket health",
		Args:  cobra.NoArgs,
		RunE:  runDebugGet("/v1/health"),
	}
}

func debugState() *cobra.Command {
	return &cobra.Command{
		Use:   "state",
		Short: "Show runtime state",
		Args:  cobra.NoArgs,
		RunE:  runDebugGet("/v1/state"),
	}
}

func debugRuntime() *cobra.Command {
	return &cobra.Command{
		Use:   "runtime",
		Short: "Show Go runtime stats",
		Args:  cobra.NoArgs,
		RunE:  runDebugGet("/v1/runtime"),
	}
}

func debugDriver() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "driver",
		Short: "Show driver state",
		Args:  cobra.NoArgs,
		RunE:  runDebugGet("/v1/driver"),
	}
	cmd.AddCommand(debugDriverHealth())
	cmd.AddCommand(debugDriverTest())
	return cmd
}

func debugDriverHealth() *cobra.Command {
	return &cobra.Command{
		Use:   "health [MOUNT]",
		Short: "Run driver health checks",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c := debugSocketClient{}
			endpoint := "/v1/driver?health=true"
			if len(args) > 0 {
				endpoint += "&mount=" + url.QueryEscape(args[0])
			}
			body, err := c.get(endpoint)
			if err != nil {
				return err
			}
			os.Stdout.Write(body)
			return nil
		},
	}
}

func debugDriverTest() *cobra.Command {
	return &cobra.Command{
		Use:   "test <type> [MOUNT]",
		Short: "Run a driver test",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			c := debugSocketClient{}
			if args[0] != "crud" {
				return fmt.Errorf("unknown test type: %s (only 'crud' is supported)", args[0])
			}
			endpoint := "/v1/driver/test?test=crud"
			if len(args) > 1 {
				endpoint += "&mount=" + url.QueryEscape(args[1])
			}
			body, err := c.get(endpoint)
			if err != nil {
				return err
			}
			os.Stdout.Write(body)
			return nil
		},
	}
}

func debugList() *cobra.Command {
	return &cobra.Command{
		Use:   "list [PATH]",
		Short: "List through live VFS",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c := debugSocketClient{}
			path := "/"
			if len(args) > 0 {
				path = args[0]
			}
			body, err := c.get("/v1/list?path=" + url.QueryEscape(path))
			if err != nil {
				return err
			}
			os.Stdout.Write(body)
			return nil
		},
	}
}

func debugEvents() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "events",
		Short: "Show recent events",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			c := debugSocketClient{}
			v := url.Values{}
			if level, _ := cmd.Flags().GetString("level"); level != "" {
				v.Set("level", level)
			}
			if limit, _ := cmd.Flags().GetInt("limit"); limit > 0 {
				v.Set("limit", fmt.Sprintf("%d", limit))
			}
			if path, _ := cmd.Flags().GetString("path"); path != "" {
				v.Set("path", path)
			}
			if component, _ := cmd.Flags().GetString("component"); component != "" {
				v.Set("component", component)
			}
			endpoint := "/v1/events"
			if encoded := v.Encode(); encoded != "" {
				endpoint += "?" + encoded
			}
			body, err := c.get(endpoint)
			if err != nil {
				return err
			}
			os.Stdout.Write(body)
			return nil
		},
	}
	cmd.Flags().String("level", "", "minimum event level (debug, info, warn, error)")
	cmd.Flags().Int("limit", 100, "max events")
	cmd.Flags().String("path", "", "filter by path prefix")
	cmd.Flags().String("component", "", "filter by component")
	return cmd
}

func debugUploads() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "uploads [PATH]",
		Short: "Show upload state",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c := debugSocketClient{}
			v := url.Values{}
			if history, _ := cmd.Flags().GetBool("history"); history {
				v.Set("history", "1")
			}
			if len(args) > 0 {
				v.Set("path", args[0])
			}
			endpoint := "/v1/uploads"
			if encoded := v.Encode(); encoded != "" {
				endpoint += "?" + encoded
			}
			body, err := c.get(endpoint)
			if err != nil {
				return err
			}
			os.Stdout.Write(body)
			return nil
		},
	}
	cmd.Flags().Bool("history", false, "show upload history")
	return cmd
}

func debugResolve() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "resolve PATH [PATH2...]",
		Short: "Resolve virtual paths",
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			c := debugSocketClient{}
			remoteName, _ := cmd.Flags().GetBool("remote-name")
			remoteID, _ := cmd.Flags().GetString("remote-id")

			v := url.Values{}
			if remoteID != "" {
				if len(args) > 0 || remoteName {
					return fmt.Errorf("--remote-id cannot be combined with paths or --remote-name")
				}
				v.Set("remote_id", remoteID)
			} else {
				if len(args) == 0 {
					return fmt.Errorf("usage: qrypt debug resolve PATH [PATH2...] [--remote-name]")
				}
				for _, p := range args {
					v.Add("path", p)
				}
				if remoteName {
					v.Set("include_remote_name", "1")
				}
			}
			body, err := c.get("/v1/resolve?" + v.Encode())
			if err != nil {
				return err
			}
			os.Stdout.Write(body)
			return nil
		},
	}
	cmd.Flags().Bool("remote-name", false, "include remote name in response")
	cmd.Flags().String("remote-id", "", "resolve by remote ID instead of path")
	return cmd
}

func debugCache() *cobra.Command {
	return &cobra.Command{
		Use:   "cache [PATH]",
		Short: "Show read cache state",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c := debugSocketClient{}
			endpoint := "/v1/cache"
			if len(args) > 0 {
				endpoint += "?path=" + url.QueryEscape(args[0])
			}
			body, err := c.get(endpoint)
			if err != nil {
				return err
			}
			os.Stdout.Write(body)
			return nil
		},
	}
}

func debugStaging() *cobra.Command {
	return &cobra.Command{
		Use:   "staging [PATH]",
		Short: "Show staging state",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c := debugSocketClient{}
			endpoint := "/v1/staging"
			if len(args) > 0 {
				endpoint += "?path=" + url.QueryEscape(args[0])
			}
			body, err := c.get(endpoint)
			if err != nil {
				return err
			}
			os.Stdout.Write(body)
			return nil
		},
	}
}

func debugConsistency() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "consistency",
		Short: "Check pending and remote consistency",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			c := debugSocketClient{}

			dir, _ := cmd.Flags().GetString("dir")
			recursive, _ := cmd.Flags().GetBool("recursive")
			path, _ := cmd.Flags().GetString("path")

			var endpoint string
			switch {
			case dir != "" && path != "":
				return fmt.Errorf("usage: qrypt debug consistency --dir DIR [--recursive] | PATH")
			case dir != "":
				endpoint = "/v1/consistency?dir=" + url.QueryEscape(dir)
				if recursive {
					endpoint += "&recursive=1"
				}
			case path != "":
				endpoint = "/v1/consistency?path=" + url.QueryEscape(path)
			default:
				return fmt.Errorf("usage: qrypt debug consistency --dir DIR [--recursive] | --path PATH")
			}

			body, err := c.get(endpoint)
			if err != nil {
				return err
			}
			os.Stdout.Write(body)
			return nil
		},
	}
	cmd.Flags().String("dir", "", "directory to check")
	cmd.Flags().Bool("recursive", false, "check recursively")
	cmd.Flags().String("path", "", "single path to check")
	return cmd
}

func debugGoroutines() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "goroutines [debug-level]",
		Short: "Dump goroutines",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c := debugSocketClient{}
			endpoint := "/v1/goroutines"
			if len(args) > 0 {
				endpoint += "?debug=" + url.QueryEscape(args[0])
			}
			body, err := c.get(endpoint)
			if err != nil {
				return err
			}
			os.Stdout.Write(body)
			return nil
		},
	}
	return cmd
}

// runDebugGet returns a RunE function that fetches a static endpoint.
func runDebugGet(endpoint string) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		c := debugSocketClient{}
		body, err := c.get(endpoint)
		if err != nil {
			return err
		}
		os.Stdout.Write(body)
		return nil
	}
}
