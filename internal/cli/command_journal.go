package cli

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
	"github.com/yinzhenyu/qrypt/pkg/core"
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
	vfs.PendingUpload
	StagingExists bool   `json:"staging_exists"`
	StagingSize   int64  `json:"staging_size"`
	StagingError  string `json:"staging_error,omitempty"`
}

type debugJournalEntry struct {
	Op string `json:"op"`
	vfs.PendingUpload
}

func newJournalCmdWithUse(use string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   use,
		Short: "Inspect and maintain the offline upload journal",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			targets, err := journalTargetsFromCmd(cmd)
			if err != nil {
				return err
			}
			asJSON, _ := cmd.Flags().GetBool("json")
			var reports []journalDebugReport
			for i, target := range targets {
				if !asJSON && i > 0 {
					fmt.Fprintln(cmd.OutOrStdout())
				}
				report := inspectJournalCache(target)
				if asJSON {
					reports = append(reports, report)
				} else {
					printJournalReport(cmd.OutOrStdout(), report)
				}
			}
			if asJSON {
				return writeJournalReportsJSON(cmd.OutOrStdout(), reports)
			}
			return nil
		},
	}
	addJournalTargetFlags(cmd)
	cmd.AddCommand(newJournalReplayCmd())
	cmd.AddCommand(newJournalPruneCmd())
	return cmd
}

// journalTargetsFromCmd resolves the journal directories to inspect from the
// config (or explicit --cache-dir/--mount flags).
func journalTargetsFromCmd(cmd *cobra.Command) ([]debugCacheTarget, error) {
	cacheDir := journalFlagValue(cmd, "cache-dir")
	mountName := journalFlagValue(cmd, "mount")
	state, err := commandConfig(cmd)
	if err != nil {
		return nil, err
	}
	if state.cfg != nil {
		// cfg was loaded; only the cache-dir fallback below needs it.
	}
	if state.cfg == nil && cacheDir == "" {
		return nil, fmt.Errorf("%w; alternatively use --cache-dir", configNotFoundError())
	}
	if state.cfg == nil && mountName != "" {
		return nil, fmt.Errorf("--mount requires a config file")
	}
	return debugCacheTargets(cacheDir, state.cfg, mountName)
}

func addJournalTargetFlags(cmd *cobra.Command) {
	cmd.PersistentFlags().String("mount", "", "mount name")
	cmd.PersistentFlags().String("cache-dir", "", "cache directory")
	cmd.Flags().Bool("json", false, "write JSON output")
}

func addJournalJSONFlag(cmd *cobra.Command) {
	cmd.Flags().Bool("json", false, "write JSON output")
}

// journalFlagValue reads a flag that may live on the command or its parents.
func journalFlagValue(cmd *cobra.Command, name string) string {
	if flag := cmd.Flags().Lookup(name); flag != nil {
		return flag.Value.String()
	}
	if flag := cmd.InheritedFlags().Lookup(name); flag != nil {
		return flag.Value.String()
	}
	return ""
}

type journalMaintenanceResult struct {
	Mount    string   `json:"mount"`
	Dir      string   `json:"dir"`
	Replayed int      `json:"replayed,omitempty"`
	Pruned   int      `json:"pruned,omitempty"`
	Entries  []string `json:"entries"`
}

func newJournalReplayCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "replay",
		Short: "Reset failed offline uploads so the next mount retries them",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			targets, err := journalTargetsFromCmd(cmd)
			if err != nil {
				return err
			}
			asJSON, _ := cmd.Flags().GetBool("json")
			var results []journalMaintenanceResult
			for _, target := range targets {
				replayed, err := vfs.ReplayUploadJournal(target.Dir)
				if err != nil {
					return err
				}
				paths := make([]string, 0, len(replayed))
				for _, pending := range replayed {
					paths = append(paths, pending.Path)
				}
				results = append(results, journalMaintenanceResult{Mount: target.Name, Dir: target.Dir, Replayed: len(replayed), Entries: paths})
				if !asJSON {
					fmt.Fprintf(cmd.OutOrStdout(), "journal %s: replayed %d failed upload(s)\n", target.Name, len(replayed))
				}
			}
			if asJSON {
				return writePrettyJSON(cmd.OutOrStdout(), results)
			}
			return nil
		},
	}
	addJournalJSONFlag(cmd)
	return cmd
}

func newJournalPruneCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "prune",
		Short: "Drop offline uploads whose staging data is gone",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			targets, err := journalTargetsFromCmd(cmd)
			if err != nil {
				return err
			}
			asJSON, _ := cmd.Flags().GetBool("json")
			var results []journalMaintenanceResult
			for _, target := range targets {
				pruned, err := vfs.PruneUploadJournal(target.Dir)
				if err != nil {
					return err
				}
				results = append(results, journalMaintenanceResult{Mount: target.Name, Dir: target.Dir, Pruned: pruned})
				if !asJSON {
					fmt.Fprintf(cmd.OutOrStdout(), "journal %s: pruned %d stale upload(s)\n", target.Name, pruned)
				}
			}
			if asJSON {
				return writePrettyJSON(cmd.OutOrStdout(), results)
			}
			return nil
		},
	}
	addJournalJSONFlag(cmd)
	return cmd
}

func writeJournalReportsJSON(w io.Writer, reports []journalDebugReport) error {
	return writePrettyJSON(w, struct {
		SchemaVersion int                  `json:"schema_version"`
		GeneratedAt   time.Time            `json:"generated_at"`
		Reports       []journalDebugReport `json:"reports"`
	}{
		SchemaVersion: debugAIReportSchemaVersion,
		GeneratedAt:   time.Now(),
		Reports:       reports,
	})
}

func debugCacheTargets(cacheDir string, cfg *config.Config, mountName string) ([]debugCacheTarget, error) {
	if cfg == nil {
		if cacheDir == "" {
			cacheDir = defaultCacheDir()
		}
		return []debugCacheTarget{{Name: "default", Dir: osutil.ExpandHome(cacheDir)}}, nil
	}
	baseStorageDir := cfg.Storage.UploadDir
	if cacheDir != "" {
		baseStorageDir = cacheDir
	}
	if baseStorageDir == "" {
		baseStorageDir = core.DefaultUploadDir()
	} else {
		baseStorageDir = osutil.ExpandHome(baseStorageDir)
	}
	if len(cfg.Mounts) == 0 {
		return []debugCacheTarget{{Name: "default", Dir: baseStorageDir}}, nil
	}
	var targets []debugCacheTarget
	for _, mount := range cfg.Mounts {
		if mountName != "" && mount.Name != mountName {
			continue
		}
		dir := filepath.Join(baseStorageDir, mount.Name)
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
		pending := map[string]vfs.PendingUpload{}
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
					pending[entry.Path] = entry.PendingUpload
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
			debug := journalPendingDebug{PendingUpload: item}
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
