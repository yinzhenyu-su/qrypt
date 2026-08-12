package sync

import "time"

// Action is one step of a sync plan.
type Action string

const (
	ActionAdd      Action = "add"
	ActionUpdate   Action = "update"
	ActionDelete   Action = "delete"
	ActionSkip     Action = "skip"
	ActionConflict Action = "conflict"
)

// PlanEntry is one planned or executed sync step.
type PlanEntry struct {
	Path       string `json:"path"`
	Action     Action `json:"action"`
	Reason     string `json:"reason,omitempty"`
	IsDir      bool   `json:"is_dir,omitempty"`
	SourceSize int64  `json:"source_size,omitempty"`
	DestSize   int64  `json:"dest_size,omitempty"`
	Bytes      int64  `json:"bytes,omitempty"`
	// SourceModTime carries the authoritative mtime so the destination can
	// be set to it after transfer; otherwise the backend stamps the upload
	// time and every subsequent sync sees an mtime difference.
	SourceModTime int64 `json:"source_mod_time,omitempty"`
}

// Summary aggregates a plan or run by action.
type Summary struct {
	Adds     int   `json:"add"`
	Update   int   `json:"update"`
	Delete   int   `json:"delete"`
	Skip     int   `json:"skip"`
	Conflict int   `json:"conflict"`
	Failed   int   `json:"failed"`
	Bytes    int64 `json:"bytes"`
}

// Result is the machine-readable output of a sync run.
type Result struct {
	OK          bool        `json:"ok"`
	Source      string      `json:"source"`
	Destination string      `json:"destination"`
	DryRun      bool        `json:"dry_run"`
	Summary     Summary     `json:"summary"`
	Entries     []PlanEntry `json:"entries"`
}

// Add folds one planned or executed entry into the summary.
func (s *Summary) Add(entry PlanEntry) {
	switch entry.Action {
	case ActionAdd:
		s.Adds++
		s.Bytes += entry.Bytes
	case ActionUpdate:
		s.Update++
		s.Bytes += entry.Bytes
	case ActionDelete:
		s.Delete++
	case ActionSkip:
		s.Skip++
	case ActionConflict:
		s.Conflict++
	}
}

// Plan maps tree differences to sync actions. Source is authoritative;
// destination extras become skips unless deleteExtra is set.
func Plan(diffs []Difference, snapA, snapB Snapshot, deleteExtra bool, conflictPolicy string, compareMTime bool) []PlanEntry {
	var plan []PlanEntry
	conflictAsAdd := conflictPolicy == "source"
	for _, d := range diffs {
		entryA := snapA[d.Path]
		entryB := snapB[d.Path]
		switch d.Reason {
		case "missing_in_b":
			plan = append(plan, PlanEntry{
				Path: d.Path, Action: ActionAdd, Reason: "missing",
				IsDir: d.IsDir, SourceSize: entryA.Size, Bytes: entryA.Size,
				SourceModTime: entryA.ModTime,
			})
		case "size", "mtime", "hash":
			if d.Reason == "mtime" && !compareMTime {
				// The destination backend cannot persist mtime (it stamps the
				// upload time), so an mtime difference would never converge:
				// skip the no-op touch and rely on size/hash comparison.
				continue
			}
			plan = append(plan, PlanEntry{
				Path: d.Path, Action: ActionUpdate, Reason: d.Reason,
				IsDir: d.IsDir, SourceSize: entryA.Size, DestSize: entryB.Size,
				Bytes:         diffBytes(entryA.Size, entryB.Size),
				SourceModTime: entryA.ModTime,
			})
		case "extra_in_b":
			if deleteExtra {
				plan = append(plan, PlanEntry{Path: d.Path, Action: ActionDelete, Reason: "extra", IsDir: d.IsDir})
			} else {
				plan = append(plan, PlanEntry{Path: d.Path, Action: ActionSkip, Reason: "extra", IsDir: d.IsDir, DestSize: entryB.Size})
			}
		case "type":
			if conflictAsAdd {
				// SOURCE wins: delete the destination entry, then add.
				plan = append(plan, PlanEntry{Path: d.Path, Action: ActionDelete, Reason: "type", IsDir: entryB.IsDir, DestSize: entryB.Size})
				plan = append(plan, PlanEntry{Path: d.Path, Action: ActionAdd, Reason: "type", IsDir: entryA.IsDir, SourceSize: entryA.Size, Bytes: entryA.Size})
			} else {
				plan = append(plan, PlanEntry{Path: d.Path, Action: ActionConflict, Reason: "type", IsDir: entryA.IsDir})
			}
		}
	}
	return plan
}

func diffBytes(a, b int64) int64 {
	if a > b {
		return a - b
	}
	return b - a
}

// UnixModTime converts a stored Unix-seconds mtime to a time.Time, zero for
// absent values (so the backend keeps its own stamp).
func UnixModTime(sec int64) time.Time {
	if sec <= 0 {
		return time.Time{}
	}
	return time.Unix(sec, 0)
}
