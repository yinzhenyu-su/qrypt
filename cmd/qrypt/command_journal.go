package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/spf13/cobra"
	"github.com/yinzhenyu/qrypt/internal/config"
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

func newJournalCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "journal",
		Short: "Inspect offline upload journal",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if configPath == "" && journalCacheDir == "" {
				return fmt.Errorf("missing --config or --cache")
			}
			targets, err := debugCacheTargets(journalCacheDir, configPath)
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
		},
	}
	cmd.Flags().StringVar(&mountName, "mount", "", "mount name")
	cmd.Flags().StringVar(&journalCacheDir, "cache", "", "cache directory")
	return cmd
}

func debugCacheTargets(cacheDir, configPath string) ([]debugCacheTarget, error) {
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
	baseCacheDir := cfg.CacheDir
	if cacheDir != "" {
		baseCacheDir = cacheDir
	}
	if baseCacheDir == "" {
		baseCacheDir = defaultCacheDir()
	} else {
		baseCacheDir = expandHome(baseCacheDir)
	}
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
		report.Entries, report.DirtyEntries, report.CleanEntries,
		len(report.InvalidEntries), len(report.Pending), len(report.OrphanStaging))
	for _, inv := range report.InvalidEntries {
		fmt.Fprintf(w, "  invalid line %d: %s\n", inv.Line, inv.Err)
	}
	if len(report.Pending) > 0 {
		fmt.Fprintln(w, "pending:")
		for _, item := range report.Pending {
			fmt.Fprintf(w, "  %s size=%d staging_exists=%v staging_size=%d staging_error=%q\n",
				item.Path, item.Size, item.StagingExists, item.StagingSize, item.StagingError)
		}
	}
	if len(report.OrphanStaging) > 0 {
		fmt.Fprintln(w, "orphan staging files:")
		for _, name := range report.OrphanStaging {
			fmt.Fprintf(w, "  %s\n", name)
		}
	}
}

func stagingStatus(item vfs.PendingFile) (string, int64) {
	fi, err := os.Stat(item.LocalPath)
	if err != nil {
		return "missing", 0
	}
	if fi.Size() != item.Size {
		return "size-mismatch", fi.Size()
	}
	return "ok", fi.Size()
}

func formatStagingStatus(status string, size int64) string {
	switch status {
	case "ok":
		return "ok"
	case "missing":
		return "missing"
	case "size-mismatch":
		return fmt.Sprintf("size-mismatch(%d)", size)
	default:
		return status
	}
}

func formatUnixNano(ns int64) string {
	if ns == 0 {
		return "-"
	}
	return time.Unix(0, ns).Format(time.RFC3339)
}
