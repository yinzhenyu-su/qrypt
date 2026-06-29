package main

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/yinzhenyu/qrypt/internal/control"
)

func runDebugLive(ctx context.Context, args []string, debugSocket string) error {
	if debugSocket == "" {
		return fmt.Errorf("usage: qrypt -debug-socket SOCKET debug live health|state|uploads [PATH] [--history]|driver [health [MOUNT]]|events [LEVEL] [LIMIT] [--path PATH] [--component COMPONENT]|list [PATH]|resolve PATH [PATH2 ...] [--remote-name]|resolve --remote-id ID|cache [PATH]|staging [PATH]|consistency PATH")
	}
	if len(args) < 1 {
		return fmt.Errorf("usage: qrypt -debug-socket SOCKET debug live health|state|uploads [PATH] [--history]|driver [health [MOUNT]]|events [LEVEL] [LIMIT] [--path PATH] [--component COMPONENT]|list [PATH]|resolve PATH [PATH2 ...] [--remote-name]|resolve --remote-id ID|cache [PATH]|staging [PATH]|consistency PATH")
	}
	endpoints := map[string]string{
		"health":  "/v1/health",
		"state":   "/v1/state",
		"runtime": "/v1/runtime",
	}
	endpoint, ok := endpoints[args[0]]
	if !ok {
		switch args[0] {
		case "driver":
			endpoint = "/v1/driver"
			if len(args) > 1 {
				switch args[1] {
				case "health":
					if len(args) > 2 {
						endpoint = "/v1/driver?health=true&mount=" + url.QueryEscape(args[2])
					} else {
						endpoint = "/v1/driver?health=true"
					}
				case "test":
					endpoint = "/v1/driver/test"
					if len(args) > 2 && args[2] == "crud" {
						if len(args) > 3 {
							endpoint = "/v1/driver/test?" + url.Values{"mount": {args[3]}, "test": {"crud"}}.Encode()
						} else {
							endpoint = "/v1/driver/test?test=crud"
						}
					} else {
						return fmt.Errorf("usage: qrypt -debug-socket SOCKET debug live driver test crud [MOUNT]")
					}
				default:
					return fmt.Errorf("usage: qrypt -debug-socket SOCKET debug live driver [health [MOUNT]] [test crud [MOUNT]]")
				}
			}
		case "list":
			if len(args) > 2 {
				return fmt.Errorf("usage: qrypt -debug-socket SOCKET debug live list [PATH]")
			}
			path := "/"
			if len(args) == 2 {
				path = args[1]
			}
			endpoint = "/v1/list?path=" + url.QueryEscape(path)
		case "events":
			values := url.Values{}
			positional := 0
			for i := 1; i < len(args); i++ {
				switch args[i] {
				case "--path":
					if i+1 >= len(args) {
						return fmt.Errorf("usage: qrypt -debug-socket SOCKET debug live events [LEVEL] [LIMIT] [--path PATH] [--component COMPONENT]")
					}
					i++
					values.Set("path", args[i])
				case "--component":
					if i+1 >= len(args) {
						return fmt.Errorf("usage: qrypt -debug-socket SOCKET debug live events [LEVEL] [LIMIT] [--path PATH] [--component COMPONENT]")
					}
					i++
					values.Set("component", args[i])
				default:
					positional++
					if positional == 1 {
						values.Set("level", args[i])
					} else if positional == 2 {
						values.Set("limit", args[i])
					} else {
						return fmt.Errorf("usage: qrypt -debug-socket SOCKET debug live events [LEVEL] [LIMIT] [--path PATH] [--component COMPONENT]")
					}
				}
			}
			endpoint = "/v1/events"
			if encoded := values.Encode(); encoded != "" {
				endpoint += "?" + encoded
			}
		case "uploads":
			values := url.Values{}
			for _, arg := range args[1:] {
				if arg == "--history" || arg == "-H" {
					values.Set("history", "1")
					continue
				}
				if values.Get("path") != "" {
					return fmt.Errorf("usage: qrypt -debug-socket SOCKET debug live uploads [PATH] [--history]")
				}
				values.Set("path", arg)
			}
			endpoint = "/v1/uploads"
			if encoded := values.Encode(); encoded != "" {
				endpoint += "?" + encoded
			}
		case "resolve":
			if len(args) < 2 {
				return fmt.Errorf("usage: qrypt -debug-socket SOCKET debug live resolve PATH [PATH2 ...] [--remote-name] | --remote-id ID")
			}
			values := url.Values{}
			includeRemote := false
			positional := []string{}
			remoteIDMode := false
			for _, arg := range args[1:] {
				switch {
				case arg == "--remote-name":
					includeRemote = true
				case arg == "--remote-id" || arg == "--by-id":
					remoteIDMode = true
				case strings.HasPrefix(arg, "--remote-id=") || strings.HasPrefix(arg, "--by-id="):
					id := strings.TrimPrefix(arg, "--remote-id=")
					id = strings.TrimPrefix(id, "--by-id=")
					values.Set("remote_id", id)
				default:
					if remoteIDMode {
						values.Set("remote_id", arg)
						remoteIDMode = false
					} else if values.Get("remote_id") != "" {
						return fmt.Errorf("usage: qrypt -debug-socket SOCKET debug live resolve --remote-id ID")
					} else {
						positional = append(positional, arg)
					}
				}
			}
			if remoteIDMode {
				return fmt.Errorf("usage: qrypt -debug-socket SOCKET debug live resolve --remote-id ID")
			}
			if values.Get("remote_id") != "" {
				if len(positional) > 0 || includeRemote {
					return fmt.Errorf("usage: qrypt -debug-socket SOCKET debug live resolve --remote-id ID (cannot combine with --remote-name or paths)")
				}
			} else {
				if len(positional) == 0 {
					return fmt.Errorf("usage: qrypt -debug-socket SOCKET debug live resolve PATH [PATH2 ...] [--remote-name]")
				}
				for _, p := range positional {
					values.Add("path", p)
				}
			}
			if includeRemote {
				values.Set("include_remote_name", "1")
			}
			endpoint = "/v1/resolve?" + values.Encode()
		case "cache":
			if len(args) > 2 {
				return fmt.Errorf("usage: qrypt -debug-socket SOCKET debug live cache [PATH]")
			}
			endpoint = "/v1/cache"
			if len(args) == 2 {
				endpoint += "?path=" + url.QueryEscape(args[1])
			}
		case "staging":
			if len(args) > 2 {
				return fmt.Errorf("usage: qrypt -debug-socket SOCKET debug live staging [PATH]")
			}
			endpoint = "/v1/staging"
			if len(args) == 2 {
				endpoint += "?path=" + url.QueryEscape(args[1])
			}
		case "consistency":
			if len(args) < 2 || len(args) > 4 {
				return fmt.Errorf("usage: qrypt -debug-socket SOCKET debug live consistency PATH | --dir DIR [--recursive]")
			}
			values := url.Values{}
			if args[1] == "--dir" {
				if len(args) < 3 {
					return fmt.Errorf("usage: qrypt -debug-socket SOCKET debug live consistency --dir DIR [--recursive]")
				}
				values.Set("dir", args[2])
				if len(args) == 4 {
					if args[3] != "--recursive" {
						return fmt.Errorf("usage: qrypt -debug-socket SOCKET debug live consistency --dir DIR [--recursive]")
					}
					values.Set("recursive", "1")
				}
			} else {
				if len(args) != 2 {
					return fmt.Errorf("usage: qrypt -debug-socket SOCKET debug live consistency PATH")
				}
				values.Set("path", args[1])
			}
			endpoint = "/v1/consistency?" + values.Encode()
		case "goroutines":
			if len(args) > 2 {
				return fmt.Errorf("usage: qrypt -debug-socket SOCKET debug live goroutines [DEBUG_LEVEL]")
			}
			endpoint = "/v1/goroutines"
			if len(args) == 2 {
				endpoint += "?debug=" + url.QueryEscape(args[1])
			}
		default:
			return fmt.Errorf("unknown debug live command: %s", args[0])
		}
	} else if len(args) != 1 {
		return fmt.Errorf("usage: qrypt -debug-socket SOCKET debug live %s", args[0])
	}
	client, err := control.NewClient(debugSocket)
	if err != nil {
		return err
	}
	body, err := client.Get(ctx, endpoint)
	if err != nil {
		return err
	}
	_, err = os.Stdout.Write(body)
	return err
}
