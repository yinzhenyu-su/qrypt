package journal

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
	cliruntime "github.com/yinzhenyu/qrypt/internal/cli/runtime"
	"github.com/yinzhenyu/qrypt/pkg/config"
	"github.com/yinzhenyu/qrypt/pkg/core"
	"github.com/yinzhenyu/qrypt/pkg/util"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
	"github.com/yinzhenyu/qrypt/pkg/vfs/upload"
)

type DebugCacheTarget struct {
	Name string `json:"name"`
	Dir  string `json:"dir"`
}

type DebugReport struct {
	Target         DebugCacheTarget `json:"target"`
	Entries        int              `json:"entries"`
	DirtyEntries   int              `json:"dirty_entries"`
	CleanEntries   int              `json:"clean_entries"`
	InvalidEntries []InvalidEntry   `json:"invalid_entries"`
	Pending        []PendingDebug   `json:"pending"`
	OrphanStaging  []string         `json:"orphan_staging"`
}

type InvalidEntry struct {
	Line int    `json:"line"`
	Err  string `json:"err"`
}

type PendingDebug struct {
	vfs.PendingUpload
	StagingExists bool   `json:"staging_exists"`
	StagingSize   int64  `json:"staging_size"`
	StagingError  string `json:"staging_error,omitempty"`
}

type debugJournalEntry struct {
	Op string `json:"op"`
	vfs.PendingUpload
}

func NewCommand(rt cliruntime.Runtime, use string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   use,
		Short: "Inspect and maintain the offline upload journal",
		Args:  cliruntime.NoArgs(rt),
		RunE: func(cmd *cobra.Command, args []string) error {
			targets, err := TargetsFromCmd(rt, cmd)
			if err != nil {
				return err
			}
			asJSON, _ := cmd.Flags().GetBool("json")
			var reports []DebugReport
			for i, target := range targets {
				if !asJSON && i > 0 {
					fmt.Fprintln(cmd.OutOrStdout())
				}
				report := InspectCache(target)
				if asJSON {
					reports = append(reports, report)
				} else {
					PrintReport(cmd.OutOrStdout(), report)
				}
			}
			if asJSON {
				return WriteReportsJSON(rt, cmd.OutOrStdout(), reports)
			}
			return nil
		},
	}
	addJournalTargetFlags(cmd)
	cmd.AddCommand(NewReplayCmd(rt))
	cmd.AddCommand(NewPruneCmd(rt))
	return cmd
}

// journalTargetsFromCmd resolves the journal directories to inspect from the
// config (or explicit --cache-dir/--mount flags).
func TargetsFromCmd(rt cliruntime.Runtime, cmd *cobra.Command) ([]DebugCacheTarget, error) {
	cacheDir := journalFlagValue(cmd, "cache-dir")
	mountName := journalFlagValue(cmd, "mount")
	state, err := rt.CommandConfig(cmd)
	if err != nil {
		return nil, err
	}
	if state.Cfg == nil && cacheDir == "" {
		return nil, fmt.Errorf("%w; alternatively use --cache-dir", rt.ConfigNotFoundError())
	}
	if state.Cfg == nil && mountName != "" {
		return nil, fmt.Errorf("--mount requires a config file")
	}
	return DebugCacheTargets(rt, cacheDir, state.Cfg, mountName)
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

type MaintenanceResult struct {
	Mount    string   `json:"mount"`
	Dir      string   `json:"dir"`
	Replayed int      `json:"replayed,omitempty"`
	Pruned   int      `json:"pruned,omitempty"`
	Entries  []string `json:"entries"`
}

func NewReplayCmd(rt cliruntime.Runtime) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "replay",
		Short: "Reset failed offline uploads so the next mount retries them",
		Args:  cliruntime.NoArgs(rt),
		RunE: func(cmd *cobra.Command, args []string) error {
			targets, err := TargetsFromCmd(rt, cmd)
			if err != nil {
				return err
			}
			asJSON, _ := cmd.Flags().GetBool("json")
			var results []MaintenanceResult
			for _, target := range targets {
				replayed, err := upload.ReplayUploadJournal(target.Dir)
				if err != nil {
					return err
				}
				paths := make([]string, 0, len(replayed))
				for _, pending := range replayed {
					paths = append(paths, pending.Path)
				}
				results = append(results, MaintenanceResult{Mount: target.Name, Dir: target.Dir, Replayed: len(replayed), Entries: paths})
				if !asJSON {
					fmt.Fprintf(cmd.OutOrStdout(), "journal %s: replayed %d failed upload(s)\n", target.Name, len(replayed))
				}
			}
			if asJSON {
				return cliruntime.WritePrettyJSON(cmd.OutOrStdout(), results)
			}
			return nil
		},
	}
	addJournalJSONFlag(cmd)
	return cmd
}

func NewPruneCmd(rt cliruntime.Runtime) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "prune",
		Short: "Drop offline uploads whose staging data is gone",
		Args:  cliruntime.NoArgs(rt),
		RunE: func(cmd *cobra.Command, args []string) error {
			targets, err := TargetsFromCmd(rt, cmd)
			if err != nil {
				return err
			}
			asJSON, _ := cmd.Flags().GetBool("json")
			var results []MaintenanceResult
			for _, target := range targets {
				pruned, err := upload.PruneUploadJournal(target.Dir)
				if err != nil {
					return err
				}
				results = append(results, MaintenanceResult{Mount: target.Name, Dir: target.Dir, Pruned: pruned})
				if !asJSON {
					fmt.Fprintf(cmd.OutOrStdout(), "journal %s: pruned %d stale upload(s)\n", target.Name, pruned)
				}
			}
			if asJSON {
				return cliruntime.WritePrettyJSON(cmd.OutOrStdout(), results)
			}
			return nil
		},
	}
	addJournalJSONFlag(cmd)
	return cmd
}

func WriteReportsJSON(rt cliruntime.Runtime, w io.Writer, reports []DebugReport) error {
	return cliruntime.WritePrettyJSON(w, struct {
		SchemaVersion int           `json:"schema_version"`
		GeneratedAt   time.Time     `json:"generated_at"`
		Reports       []DebugReport `json:"reports"`
	}{
		SchemaVersion: rt.DebugReportSchemaVersion(),
		GeneratedAt:   time.Now(),
		Reports:       reports,
	})
}

func DebugCacheTargets(rt cliruntime.Runtime, cacheDir string, cfg *config.Config, mountName string) ([]DebugCacheTarget, error) {
	if cfg == nil {
		if cacheDir == "" {
			cacheDir = rt.DefaultCacheDir()
		}
		return []DebugCacheTarget{{Name: "default", Dir: util.ExpandHome(cacheDir)}}, nil
	}
	baseStorageDir := core.NewStorageLayout(cfg, core.RuntimeLayout{}).UploadDir
	if cacheDir != "" {
		baseStorageDir = cacheDir
	}
	if baseStorageDir != "" {
		baseStorageDir = util.ExpandHome(baseStorageDir)
	}
	if len(cfg.Mounts) == 0 {
		return []DebugCacheTarget{{Name: "default", Dir: baseStorageDir}}, nil
	}
	var targets []DebugCacheTarget
	for _, mount := range cfg.Mounts {
		if mountName != "" && mount.Name != mountName {
			continue
		}
		dir := filepath.Join(baseStorageDir, mount.Name)
		targets = append(targets, DebugCacheTarget{Name: mount.Name, Dir: dir})
	}
	if len(targets) == 0 {
		return nil, fmt.Errorf("config: no mounts selected")
	}
	return targets, nil
}

func InspectCache(target DebugCacheTarget) DebugReport {
	report := DebugReport{
		Target:         target,
		InvalidEntries: []InvalidEntry{},
		Pending:        []PendingDebug{},
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
				report.InvalidEntries = append(report.InvalidEntries, InvalidEntry{Line: line, Err: err.Error()})
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
				report.InvalidEntries = append(report.InvalidEntries, InvalidEntry{Line: line, Err: "unknown op " + entry.Op})
			}
		}
		if err := scanner.Err(); err != nil {
			report.InvalidEntries = append(report.InvalidEntries, InvalidEntry{Line: line + 1, Err: err.Error()})
		}
		for _, item := range pending {
			debug := PendingDebug{PendingUpload: item}
			status, size := cliruntime.StagingStatus(item)
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
	report.OrphanStaging = OrphanStagingFiles(target.Dir, report.Pending)
	if report.OrphanStaging == nil {
		report.OrphanStaging = []string{}
	}
	return report
}

func OrphanStagingFiles(cacheDir string, pending []PendingDebug) []string {
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

func PrintReport(w io.Writer, report DebugReport) {
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
