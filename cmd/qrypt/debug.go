package main

import (
	"archive/zip"
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
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
	case "bundle":
		return runDebugBundle(ctx, args[1:], debugSocket)
	default:
		return fmt.Errorf("unknown debug command: %s", args[0])
	}
}

func runDebugBundle(ctx context.Context, args []string, debugSocket string) error {
	if debugSocket == "" {
		return fmt.Errorf("usage: qrypt -debug-socket SOCKET debug bundle -out FILE")
	}
	flags := flag.NewFlagSet("debug bundle", flag.ContinueOnError)
	outPath := flags.String("out", "", "debug bundle output zip")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *outPath == "" || len(flags.Args()) != 0 {
		return fmt.Errorf("usage: qrypt -debug-socket SOCKET debug bundle -out FILE")
	}
	client, err := control.NewClient(debugSocket)
	if err != nil {
		return err
	}
	out, err := os.Create(*outPath)
	if err != nil {
		return err
	}
	defer out.Close()
	zw := zip.NewWriter(out)
	endpoints := map[string]string{
		"health.json":    "/v1/health",
		"state.json":     "/v1/state",
		"pending.json":   "/v1/pending",
		"uploads.json":   "/v1/uploads?history=1",
		"events.json":    "/v1/events?level=warn&limit=500",
		"cache.json":     "/v1/cache",
		"staging.json":   "/v1/staging",
		"driver.json":    "/v1/driver",
		"runtime.json":   "/v1/runtime",
		"goroutines.txt": "/v1/goroutines?debug=1",
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
		w, err := zw.Create(name)
		if err != nil {
			return err
		}
		if _, err := w.Write(body); err != nil {
			return err
		}
	}
	return zw.Close()
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
