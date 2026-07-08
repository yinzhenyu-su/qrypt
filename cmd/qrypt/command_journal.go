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
	"github.com/yinzhenyu/qrypt/pkg/osutil"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

type debugCacheTarget struct {
	Name string `json:"name"`
	Dir  string `json:"dir"`
}

type journalDebugReport struct {
	Target         debugCacheTarget      `json:"target"`
	Entries        int                   `json:"entries"`
	DirtyEntries   int                   `json:"dirty_entries"`
	CleanEntries   int                   `json:"clean_entries"`
	InvalidEntries []journalInvalidEntry `json:"invalid_entries"`
	Pending        []journalPendingDebug `json:"pending"`
	OrphanStaging  []string              `json:"orphan_staging"`
}

type journalInvalidEntry struct {
	Line int    `json:"line"`
	Err  string `json:"err"`
}

type journalPendingDebug struct {
	vfs.PendingFile
	StagingExists bool   `json:"staging_exists"`
	StagingSize   int64  `json:"staging_size"`
	StagingError  string `json:"staging_error,omitempty"`
}

type debugJournalEntry struct {
	Op string `json:"op"`
	vfs.PendingFile
}

func newJournalCmdWithUse(use string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   use,
		Short: "Inspect offline upload journal",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cacheDir, _ := cmd.Flags().GetString("cache-dir")
			mountName, _ := cmd.Flags().GetString("mount")
			if configPath == "" && cacheDir == "" {
				return fmt.Errorf("missing --config or --cache-dir")
			}
			targets, err := debugCacheTargets(cacheDir, configPath, mountName)
			if err != nil {
				return err
			}
			asJSON, _ := cmd.Flags().GetBool("json")
			var reports []journalDebugReport
			for i, target := range targets {
				if !asJSON && i > 0 {
					fmt.Println()
				}
				report := inspectJournalCache(target)
				if asJSON {
					reports = append(reports, report)
				} else {
					printJournalReport(os.Stdout, report)
				}
			}
			if asJSON {
				return writeJournalReportsJSON(os.Stdout, reports)
			}
			return nil
		},
	}
	cmd.Flags().String("mount", "", "mount name")
	cmd.Flags().String("cache-dir", "", "cache directory")
	cmd.Flags().Bool("json", false, "write JSON output")
	return cmd
}

func writeJournalReportsJSON(w io.Writer, reports []journalDebugReport) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	return enc.Encode(struct {
		SchemaVersion int                  `json:"schema_version"`
		GeneratedAt   time.Time            `json:"generated_at"`
		Reports       []journalDebugReport `json:"reports"`
	}{
		SchemaVersion: debugAIReportSchemaVersion,
		GeneratedAt:   time.Now(),
		Reports:       reports,
	})
}

func debugCacheTargets(cacheDir, configPath, mountName string) ([]debugCacheTarget, error) {
	if configPath == "" {
		if cacheDir == "" {
			cacheDir = defaultCacheDir()
		}
		return []debugCacheTarget{{Name: "default", Dir: osutil.ExpandHome(cacheDir)}}, nil
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
		baseCacheDir = osutil.ExpandHome(baseCacheDir)
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
			dir = osutil.ExpandHome(dir)
		}
		targets = append(targets, debugCacheTarget{Name: mount.Name, Dir: dir})
	}
	if len(targets) == 0 {
		return nil, fmt.Errorf("config: no mounts selected")
	}
	return targets, nil
}

func inspectJournalCache(target debugCacheTarget) journalDebugReport {
	report := journalDebugReport{
		Target:         target,
		InvalidEntries: []journalInvalidEntry{},
		Pending:        []journalPendingDebug{},
		OrphanStaging:  []string{},
	}
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
	if report.OrphanStaging == nil {
		report.OrphanStaging = []string{}
	}
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
