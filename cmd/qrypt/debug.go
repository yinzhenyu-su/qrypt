package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"text/tabwriter"
	"time"

	"github.com/yinzhenyu/qrypt/internal/config"
	"github.com/yinzhenyu/qrypt/internal/control"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

type debugCacheTarget struct {
	Name string
	Dir  string
}

type journalDebugReport struct {
	Target         debugCacheTarget
	Entries        int
	DirtyEntries   int
	CleanEntries   int
	InvalidEntries []journalInvalidEntry
	Pending        []journalPendingDebug
	OrphanStaging  []string
}

type journalInvalidEntry struct {
	Line int
	Err  string
}

type journalPendingDebug struct {
	vfs.PendingFile
	StagingExists bool
	StagingSize   int64
	StagingError  string
}

type debugJournalEntry struct {
	Op string `json:"op"`
	vfs.PendingFile
}

func runDebugCommand(ctx context.Context, flags *flag.FlagSet, args []string, cacheDir, configPath, mountName, debugSocket string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: qrypt [flags] debug journal|live ...")
	}
	switch args[0] {
	case "journal":
		if len(args) != 1 {
			return fmt.Errorf("usage: qrypt [flags] debug journal")
		}
		targets, err := debugCacheTargets(flags, cacheDir, configPath, mountName)
		if err != nil {
			return err
		}
		for i, target := range targets {
			if i > 0 {
				fmt.Println()
			}
			report := inspectJournalCache(target)
			printJournalReport(os.Stdout, report)
		}
		return nil
	case "live":
		return runDebugLive(ctx, args[1:], debugSocket)
	default:
		return fmt.Errorf("unknown debug command: %s", args[0])
	}
}

func runDebugLive(ctx context.Context, args []string, debugSocket string) error {
	if debugSocket == "" {
		return fmt.Errorf("usage: qrypt -debug-socket SOCKET debug live health|state|pending|uploads [PATH] [--history]|driver|events [LEVEL] [LIMIT]|list [PATH]|resolve PATH [--remote-name]|cache|tasks|consistency PATH")
	}
	if len(args) < 1 {
		return fmt.Errorf("usage: qrypt -debug-socket SOCKET debug live health|state|pending|uploads [PATH] [--history]|driver|events [LEVEL] [LIMIT]|list [PATH]|resolve PATH [--remote-name]|cache|tasks|consistency PATH")
	}
	endpoints := map[string]string{
		"health":  "/v1/health",
		"state":   "/v1/state",
		"pending": "/v1/pending",
		"driver":  "/v1/driver",
		"cache":   "/v1/cache",
		"tasks":   "/v1/tasks",
	}
	endpoint, ok := endpoints[args[0]]
	if !ok {
		switch args[0] {
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
			if len(args) > 3 {
				return fmt.Errorf("usage: qrypt -debug-socket SOCKET debug live events [LEVEL] [LIMIT]")
			}
			values := url.Values{}
			if len(args) >= 2 {
				values.Set("level", args[1])
			}
			if len(args) == 3 {
				values.Set("limit", args[2])
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
			if len(args) < 2 || len(args) > 3 {
				return fmt.Errorf("usage: qrypt -debug-socket SOCKET debug live resolve PATH [--remote-name]")
			}
			values := url.Values{}
			values.Set("path", args[1])
			if len(args) == 3 {
				if args[2] != "--remote-name" {
					return fmt.Errorf("usage: qrypt -debug-socket SOCKET debug live resolve PATH [--remote-name]")
				}
				values.Set("include_remote_name", "1")
			}
			endpoint = "/v1/resolve?" + values.Encode()
		case "consistency":
			if len(args) != 2 {
				return fmt.Errorf("usage: qrypt -debug-socket SOCKET debug live consistency PATH")
			}
			endpoint = "/v1/consistency?path=" + url.QueryEscape(args[1])
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

func parsePendingArgs(args []string) (bool, error) {
	verbose := false
	for _, arg := range args {
		switch arg {
		case "-v", "--verbose":
			verbose = true
		default:
			return false, fmt.Errorf("usage: qrypt [flags] pending [-v|--verbose]")
		}
	}
	return verbose, nil
}

func printPendingVerbose(w io.Writer, pending []vfs.PendingFile) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "PATH\tSIZE\tLOCAL\tSTAGING\tRETRY\tLAST_ATTEMPT\tNEXT_ATTEMPT\tLAST_ERROR")
	for _, item := range pending {
		status, size := stagingStatus(item)
		fmt.Fprintf(tw, "%s\t%d\t%s\t%s\t%d\t%s\t%s\t%s\n",
			item.Path,
			item.Size,
			item.LocalPath,
			formatStagingStatus(status, size),
			item.RetryCount,
			formatUnixNano(item.LastAttemptAt),
			formatUnixNano(item.NextAttemptAt),
			item.LastError,
		)
	}
	_ = tw.Flush()
}

func debugCacheTargets(flags *flag.FlagSet, cacheDir, configPath, mountName string) ([]debugCacheTarget, error) {
	if configPath == "" {
		if cacheDir == "" {
			cacheDir = defaultCacheDir()
		}
		return []debugCacheTarget{{Name: "default", Dir: expandHome(cacheDir)}}, nil
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		return nil, err
	}
	baseCacheDir := effectiveCacheDir(flags, cacheDir, cfg)
	if len(cfg.Mounts) == 0 {
		return []debugCacheTarget{{Name: "default", Dir: baseCacheDir}}, nil
	}
	var targets []debugCacheTarget
	for _, mount := range cfg.Mounts {
		if mountName != "" && mount.Name != mountName {
			continue
		}
		cache := cfg.CacheFor(mount.Name)
		dir := cache.Dir
		if dir == "" {
			dir = filepath.Join(baseCacheDir, mount.Name)
		} else {
			dir = expandHome(dir)
		}
		targets = append(targets, debugCacheTarget{Name: mount.Name, Dir: dir})
	}
	if len(targets) == 0 {
		return nil, fmt.Errorf("config: no mounts selected")
	}
	return targets, nil
}

func inspectJournalCache(target debugCacheTarget) journalDebugReport {
	report := journalDebugReport{Target: target}
	journalPath := filepath.Join(target.Dir, "pending.jsonl")
	file, err := os.Open(journalPath)
	if err == nil {
		defer file.Close()
		pending := map[string]vfs.PendingFile{}
		scanner := bufio.NewScanner(file)
		line := 0
		for scanner.Scan() {
			line++
			var entry debugJournalEntry
			if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
				report.InvalidEntries = append(report.InvalidEntries, journalInvalidEntry{Line: line, Err: err.Error()})
				continue
			}
			report.Entries++
			switch entry.Op {
			case "dirty":
				report.DirtyEntries++
				if entry.LocalPath != "" {
					pending[entry.Path] = entry.PendingFile
				}
			case "clean":
				report.CleanEntries++
				delete(pending, entry.Path)
			default:
				report.InvalidEntries = append(report.InvalidEntries, journalInvalidEntry{Line: line, Err: "unknown op " + entry.Op})
			}
		}
		if err := scanner.Err(); err != nil {
			report.InvalidEntries = append(report.InvalidEntries, journalInvalidEntry{Line: line + 1, Err: err.Error()})
		}
		for _, item := range pending {
			debug := journalPendingDebug{PendingFile: item}
			status, size := stagingStatus(item)
			debug.StagingExists = status == "ok" || status == "size-mismatch"
			debug.StagingSize = size
			if status == "missing" {
				debug.StagingError = "missing"
			}
			report.Pending = append(report.Pending, debug)
		}
	}
	sort.Slice(report.Pending, func(i, j int) bool {
		return report.Pending[i].Path < report.Pending[j].Path
	})
	report.OrphanStaging = orphanStagingFiles(target.Dir, report.Pending)
	return report
}

func orphanStagingFiles(cacheDir string, pending []journalPendingDebug) []string {
	known := map[string]bool{}
	for _, item := range pending {
		if item.LocalPath != "" {
			known[item.LocalPath] = true
		}
	}
	files, err := filepath.Glob(filepath.Join(cacheDir, "staging", "*.staging"))
	if err != nil {
		return nil
	}
	var orphans []string
	for _, file := range files {
		if !known[file] {
			orphans = append(orphans, file)
		}
	}
	sort.Strings(orphans)
	return orphans
}

func printJournalReport(w io.Writer, report journalDebugReport) {
	fmt.Fprintf(w, "cache %s %s\n", report.Target.Name, report.Target.Dir)
	fmt.Fprintf(w, "journal entries=%d dirty=%d clean=%d invalid=%d pending=%d orphan_staging=%d\n",
		report.Entries,
		report.DirtyEntries,
		report.CleanEntries,
		len(report.InvalidEntries),
		len(report.Pending),
		len(report.OrphanStaging),
	)
	for _, invalid := range report.InvalidEntries {
		fmt.Fprintf(w, "invalid line=%d err=%s\n", invalid.Line, invalid.Err)
	}
	for _, item := range report.Pending {
		status, size := stagingStatus(item.PendingFile)
		fmt.Fprintf(w, "pending path=%s size=%d local=%s staging=%s retry=%d last_attempt=%s next_attempt=%s last_error=%q\n",
			item.Path,
			item.Size,
			item.LocalPath,
			formatStagingStatus(status, size),
			item.RetryCount,
			formatUnixNano(item.LastAttemptAt),
			formatUnixNano(item.NextAttemptAt),
			item.LastError,
		)
	}
	for _, orphan := range report.OrphanStaging {
		fmt.Fprintf(w, "orphan_staging local=%s\n", orphan)
	}
}

func stagingStatus(item vfs.PendingFile) (string, int64) {
	if item.LocalPath == "" {
		return "missing", 0
	}
	info, err := os.Stat(item.LocalPath)
	if err != nil {
		return "missing", 0
	}
	if info.Size() != item.Size {
		return "size-mismatch", info.Size()
	}
	return "ok", info.Size()
}

func formatStagingStatus(status string, size int64) string {
	if status == "ok" {
		return "ok"
	}
	if status == "size-mismatch" {
		return fmt.Sprintf("size-mismatch(%d)", size)
	}
	return status
}

func formatUnixNano(value int64) string {
	if value == 0 {
		return "-"
	}
	return time.Unix(0, value).Format(time.RFC3339)
}
