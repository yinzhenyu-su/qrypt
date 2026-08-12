package sync

import (
	"testing"
)

func TestPlanMapsDifferences(t *testing.T) {
	snapA := Snapshot{
		"a.txt":    {RelPath: "a.txt", Size: 3, ModTime: 100},
		"same.txt": {RelPath: "same.txt", Size: 5, ModTime: 200},
		"dir/b":    {RelPath: "dir/b", Size: 1, ModTime: 300},
	}
	snapB := Snapshot{
		"a.txt":     {RelPath: "a.txt", Size: 9, ModTime: 100}, // size changed
		"same.txt":  {RelPath: "same.txt", Size: 5, ModTime: 200},
		"extra.txt": {RelPath: "extra.txt", Size: 2, ModTime: 400},
	}
	diffs, err := CompareTrees(t.Context(), snapA, snapB, CompareOptions{AsHash: false})
	if err != nil {
		t.Fatal(err)
	}
	plan := Plan(diffs, snapA, snapB, false, "error", true)
	byPath := map[string]Action{}
	for _, e := range plan {
		byPath[e.Path] = e.Action
	}
	if byPath["a.txt"] != ActionUpdate {
		t.Fatalf("a.txt action = %q, want update", byPath["a.txt"])
	}
	if byPath["dir/b"] != ActionAdd {
		t.Fatalf("dir/b action = %q, want add", byPath["dir/b"])
	}
	if byPath["extra.txt"] != ActionSkip {
		t.Fatalf("extra.txt action = %q, want skip (no --delete)", byPath["extra.txt"])
	}
	// With deleteExtra the extra file becomes a delete.
	plan = Plan(diffs, snapA, snapB, true, "error", true)
	byPath = map[string]Action{}
	for _, e := range plan {
		byPath[e.Path] = e.Action
	}
	if byPath["extra.txt"] != ActionDelete {
		t.Fatalf("extra.txt action with --delete = %q, want delete", byPath["extra.txt"])
	}
}

func TestPlanMtimeOnlySkipsWhenCompareMTimeFalse(t *testing.T) {
	snapA := Snapshot{"f.txt": {RelPath: "f.txt", Size: 5, ModTime: 100}}
	snapB := Snapshot{"f.txt": {RelPath: "f.txt", Size: 5, ModTime: 200}}
	diffs, err := CompareTrees(t.Context(), snapA, snapB, CompareOptions{AsHash: false})
	if err != nil {
		t.Fatal(err)
	}
	if len(diffs) != 1 || diffs[0].Reason != "mtime" {
		t.Fatalf("diffs = %+v, want one mtime diff", diffs)
	}
	// A backend that cannot persist mtime (compareMTime=false) must skip the
	// no-op touch; otherwise every sync would re-upload forever.
	plan := Plan(diffs, snapA, snapB, false, "error", false)
	if len(plan) != 0 {
		t.Fatalf("plan = %+v, want empty (mtime-only diff not convergent)", plan)
	}
	plan = Plan(diffs, snapA, snapB, false, "error", true)
	if len(plan) != 1 || plan[0].Action != ActionUpdate {
		t.Fatalf("plan = %+v, want one update", plan)
	}
}

func TestPlanTypeConflictPolicies(t *testing.T) {
	snapA := Snapshot{"p": {RelPath: "p", IsDir: false, Size: 1}}
	snapB := Snapshot{"p": {RelPath: "p", IsDir: true}}
	diffs, err := CompareTrees(t.Context(), snapA, snapB, CompareOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(diffs) != 1 || diffs[0].Reason != "type" {
		t.Fatalf("diffs = %+v, want type conflict", diffs)
	}
	// error: conflict entry.
	plan := Plan(diffs, snapA, snapB, false, "error", true)
	if len(plan) != 1 || plan[0].Action != ActionConflict {
		t.Fatalf("error policy plan = %+v, want one conflict", plan)
	}
	// source: delete then add.
	plan = Plan(diffs, snapA, snapB, false, "source", true)
	if len(plan) != 2 || plan[0].Action != ActionDelete || plan[1].Action != ActionAdd {
		t.Fatalf("source policy plan = %+v, want delete+add", plan)
	}
}

func TestCompareTreesSortedAndDetailed(t *testing.T) {
	snapA := Snapshot{"z.txt": {RelPath: "z.txt", Size: 1, ModTime: 1}}
	snapB := Snapshot{"a.txt": {RelPath: "a.txt", Size: 2, ModTime: 2}}
	diffs, err := CompareTrees(t.Context(), snapA, snapB, CompareOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(diffs) != 2 {
		t.Fatalf("diffs = %+v, want 2", diffs)
	}
	if diffs[0].Path != "a.txt" || diffs[0].Reason != "extra_in_b" {
		t.Fatalf("diffs[0] = %+v, want extra_in_b a.txt (sorted)", diffs[0])
	}
	if diffs[1].Path != "z.txt" || diffs[1].Reason != "missing_in_b" {
		t.Fatalf("diffs[1] = %+v, want missing_in_b z.txt", diffs[1])
	}
	// Size detail carries both values.
	snapA = Snapshot{"f": {RelPath: "f", Size: 10, ModTime: 1}}
	snapB = Snapshot{"f": {RelPath: "f", Size: 20, ModTime: 1}}
	diffs, err = CompareTrees(t.Context(), snapA, snapB, CompareOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(diffs) != 1 || diffs[0].Reason != "size" || diffs[0].A != "10" || diffs[0].B != "20" {
		t.Fatalf("size diff = %+v, want reason=size A=10 B=20", diffs[0])
	}
}

func TestParseCompareMode(t *testing.T) {
	for _, mode := range []string{"size-mtime", "mtime-only", "hash"} {
		if _, _, err := ParseCompareMode(mode); err != nil {
			t.Fatalf("ParseCompareMode(%q) error: %v", mode, err)
		}
	}
	skip, force, err := ParseCompareMode("mtime-only")
	if err != nil || !skip || force {
		t.Fatalf("mtime-only = (%v, %v, %v), want (true, false, nil)", skip, force, err)
	}
	skip, force, err = ParseCompareMode("hash")
	if err != nil || skip || !force {
		t.Fatalf("hash = (%v, %v, %v), want (false, true, nil)", skip, force, err)
	}
	if _, _, err := ParseCompareMode("bogus"); err == nil {
		t.Fatal("bogus mode must error")
	}
}

func TestCheckContainment(t *testing.T) {
	src := Target{Kind: TargetVFS, VFSPath: "/loc", MountName: "loc", Raw: "/loc"}
	if err := CheckContainment(src, Target{Kind: TargetVFS, VFSPath: "/loc/sub", MountName: "loc"}); err == nil {
		t.Fatal("nested destination must be rejected")
	}
	if err := CheckContainment(src, Target{Kind: TargetVFS, VFSPath: "/other", MountName: "other"}); err != nil {
		t.Fatalf("different mount rejected: %v", err)
	}
	if err := CheckContainment(src, Target{Kind: TargetLocal, LocalPath: "/tmp/x"}); err != nil {
		t.Fatalf("local destination rejected: %v", err)
	}
	if err := CheckContainment(src, Target{Kind: TargetVFS, VFSPath: "/loc", MountName: "loc"}); err == nil {
		t.Fatal("identical paths must be rejected")
	}
}
