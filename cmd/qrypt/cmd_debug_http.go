package main

import (
	"context"
	"fmt"
	"net/url"
	"os"

	"github.com/spf13/cobra"
	"github.com/yinzhenyu/qrypt/internal/control"
)

// debugSocketClient wraps the control client for HTTP debug commands.
type debugSocketClient struct {
	socket string
}

func (c *debugSocketClient) get(ctx context.Context, endpoint string) ([]byte, error) {
	if c.socket == "" {
		return nil, fmt.Errorf("--debug-socket is required")
	}
	client, err := control.NewClient(c.socket)
	if err != nil {
		return nil, err
	}
	return client.Get(ctx, endpoint)
}

func debugHealth() *cobra.Command {
	return &cobra.Command{
		Use:   "health",
		Short: "Check debug socket health",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			body, err := (&debugSocketClient{debugSocket}).get(context.Background(), "/v1/health")
			if err != nil {
				return err
			}
			_, err = os.Stdout.Write(body)
			return err
		},
	}
}

func debugState() *cobra.Command {
	return &cobra.Command{
		Use:   "state",
		Short: "Show filesystem debug snapshot",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			body, err := (&debugSocketClient{debugSocket}).get(context.Background(), "/v1/state")
			if err != nil {
				return err
			}
			_, err = os.Stdout.Write(body)
			return err
		},
	}
}

func debugRuntime() *cobra.Command {
	return &cobra.Command{
		Use:   "runtime",
		Short: "Show runtime statistics",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			body, err := (&debugSocketClient{debugSocket}).get(context.Background(), "/v1/runtime")
			if err != nil {
				return err
			}
			_, err = os.Stdout.Write(body)
			return err
		},
	}
}

func debugDriverCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "driver",
		Short: "Inspect driver state and run health checks",
	}
	cmd.AddCommand(debugDriverHealth())
	cmd.AddCommand(debugDriverTest())
	return cmd
}

func debugDriverHealth() *cobra.Command {
	return &cobra.Command{
		Use:   "health [MOUNT]",
		Short: "Check driver health for one or all mounts",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client := &debugSocketClient{debugSocket}
			endpoint := "/v1/driver?health=true"
			if len(args) > 0 {
				endpoint = "/v1/driver?health=true&mount=" + url.QueryEscape(args[0])
			}
			body, err := client.get(context.Background(), endpoint)
			if err != nil {
				return err
			}
			_, err = os.Stdout.Write(body)
			return err
		},
	}
}

func debugDriverTest() *cobra.Command {
	return &cobra.Command{
		Use:   "test <type> [MOUNT]",
		Short: "Run a driver test (only 'crud' is supported)",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if args[0] != "crud" {
				return fmt.Errorf("usage: qrypt debug driver test crud [MOUNT]")
			}
			client := &debugSocketClient{debugSocket}
			v := url.Values{"test": {"crud"}}
			if len(args) > 1 {
				v.Set("mount", args[1])
			}
			endpoint := "/v1/driver/test?" + v.Encode()
			body, err := client.get(context.Background(), endpoint)
			if err != nil {
				return err
			}
			_, err = os.Stdout.Write(body)
			return err
		},
	}
}

func debugList() *cobra.Command {
	return &cobra.Command{
		Use:   "list [PATH]",
		Short: "List filesystem entries via debug socket",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client := &debugSocketClient{debugSocket}
			path := "/"
			if len(args) > 0 {
				path = args[0]
			}
			body, err := client.get(context.Background(), "/v1/list?path="+url.QueryEscape(path))
			if err != nil {
				return err
			}
			_, err = os.Stdout.Write(body)
			return err
		},
	}
}

func debugEvents() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "events",
		Short: "Query recent log events",
		RunE: func(cmd *cobra.Command, args []string) error {
			client := &debugSocketClient{debugSocket}
			v := url.Values{}
			level, _ := cmd.Flags().GetString("level")
			limit, _ := cmd.Flags().GetInt("limit")
			eventPath, _ := cmd.Flags().GetString("path")
			component, _ := cmd.Flags().GetString("component")
			if level != "" {
				v.Set("level", level)
			}
			if limit > 0 {
				v.Set("limit", fmt.Sprintf("%d", limit))
			}
			if eventPath != "" {
				v.Set("path", eventPath)
			}
			if component != "" {
				v.Set("component", component)
			}
			endpoint := "/v1/events"
			if encoded := v.Encode(); encoded != "" {
				endpoint += "?" + encoded
			}
			body, err := client.get(context.Background(), endpoint)
			if err != nil {
				return err
			}
			_, err = os.Stdout.Write(body)
			return err
		},
	}
	cmd.Flags().String("level", "", "minimum event level (debug, info, warn, error)")
	cmd.Flags().Int("limit", 0, "max events to return")
	cmd.Flags().String("path", "", "filter by path prefix")
	cmd.Flags().String("component", "", "filter by component")
	return cmd
}

func debugUploads() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "uploads [PATH]",
		Short: "Show upload status for one or all paths",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client := &debugSocketClient{debugSocket}
			v := url.Values{}
			history, _ := cmd.Flags().GetBool("history")
			if history {
				v.Set("history", "1")
			}
			if len(args) > 0 {
				v.Set("path", args[0])
			}
			endpoint := "/v1/uploads"
			if encoded := v.Encode(); encoded != "" {
				endpoint += "?" + encoded
			}
			body, err := client.get(context.Background(), endpoint)
			if err != nil {
				return err
			}
			_, err = os.Stdout.Write(body)
			return err
		},
	}
	cmd.Flags().Bool("history", false, "include upload history")
	return cmd
}

func debugResolve() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "resolve [PATH...]",
		Short: "Resolve virtual paths to remote IDs and names",
		RunE: func(cmd *cobra.Command, args []string) error {
			client := &debugSocketClient{debugSocket}
			v := url.Values{}
			remoteID, _ := cmd.Flags().GetString("remote-id")
			remoteName, _ := cmd.Flags().GetBool("remote-name")
			if remoteID != "" {
				if len(args) > 0 {
					return fmt.Errorf("usage: qrypt debug resolve --remote-id ID (cannot combine with paths)")
				}
				v.Set("remote_id", remoteID)
			} else {
				if len(args) == 0 {
					return fmt.Errorf("usage: qrypt debug resolve PATH [PATH2 ...] [--remote-name]")
				}
				for _, p := range args {
					v.Add("path", p)
				}
			}
			if remoteName {
				v.Set("include_remote_name", "1")
			}
			body, err := client.get(context.Background(), "/v1/resolve?"+v.Encode())
			if err != nil {
				return err
			}
			_, err = os.Stdout.Write(body)
			return err
		},
	}
	cmd.Flags().String("remote-id", "", "resolve by remote ID instead of path")
	cmd.Flags().Bool("remote-name", false, "include remote-side name in response")
	return cmd
}

func debugCache() *cobra.Command {
	return &cobra.Command{
		Use:   "cache [PATH]",
		Short: "Show read cache state for one or all paths",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client := &debugSocketClient{debugSocket}
			endpoint := "/v1/cache"
			if len(args) > 0 {
				endpoint += "?path=" + url.QueryEscape(args[0])
			}
			body, err := client.get(context.Background(), endpoint)
			if err != nil {
				return err
			}
			_, err = os.Stdout.Write(body)
			return err
		},
	}
}

func debugStaging() *cobra.Command {
	return &cobra.Command{
		Use:   "staging [PATH]",
		Short: "List staging files for pending uploads",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client := &debugSocketClient{debugSocket}
			endpoint := "/v1/staging"
			if len(args) > 0 {
				endpoint += "?path=" + url.QueryEscape(args[0])
			}
			body, err := client.get(context.Background(), endpoint)
			if err != nil {
				return err
			}
			_, err = os.Stdout.Write(body)
			return err
		},
	}
}

func debugConsistency() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "consistency [PATH]",
		Short: "Check path consistency between cache and remote",
		RunE: func(cmd *cobra.Command, args []string) error {
			client := &debugSocketClient{debugSocket}
			v := url.Values{}
			dir, _ := cmd.Flags().GetString("dir")
			recursive, _ := cmd.Flags().GetBool("recursive")
			if dir != "" {
				v.Set("dir", dir)
				if recursive {
					v.Set("recursive", "1")
				}
			} else if len(args) > 0 {
				v.Set("path", args[0])
			} else {
				return fmt.Errorf("usage: qrypt debug consistency PATH | --dir DIR [--recursive]")
			}
			body, err := client.get(context.Background(), "/v1/consistency?"+v.Encode())
			if err != nil {
				return err
			}
			_, err = os.Stdout.Write(body)
			return err
		},
	}
	cmd.Flags().String("dir", "", "check all entries under a directory")
	cmd.Flags().Bool("recursive", false, "recurse into subdirectories")
	return cmd
}

func debugGoroutines() *cobra.Command {
	return &cobra.Command{
		Use:   "goroutines [debug-level]",
		Short: "Dump goroutine stack traces",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client := &debugSocketClient{debugSocket}
			endpoint := "/v1/goroutines"
			if len(args) > 0 {
				endpoint += "?debug=" + url.QueryEscape(args[0])
			}
			body, err := client.get(context.Background(), endpoint)
			if err != nil {
				return err
			}
			_, err = os.Stdout.Write(body)
			return err
		},
	}
}
