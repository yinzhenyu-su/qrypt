package sync

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

var writeMutations = []string{"PutSource:", "Mkdir:", "Remove:", "Rename:", "Move:"}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestDryRunCausesNoRemoteMutation is the property "a dry run produces no
// remote mutation": the plan is computed and reported, but the destination
// backend must only ever see read traffic (List/Read/ResolvePath).
func TestDryRunCausesNoRemoteMutation(t *testing.T) {
	src := t.TempDir()
	writeTestFile(t, filepath.Join(src, "a.txt"), "aaa")
	writeTestFile(t, filepath.Join(src, "b.txt"), "bb")

	d := drive.NewFakeDriver()
	fs, err := vfs.New(d, vfs.Options{StorageDir: t.TempDir(), CacheMaxBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	req := Request{
		Source:      Target{Kind: TargetLocal, Raw: src, LocalPath: src},
		Destination: Target{Kind: TargetVFS, Raw: "/dest", VFSPath: "/dest"},
		DryRun:      true,
		CompareMode: "size-mtime",
		Conflict:    "error",
	}
	result, err := Run(context.Background(), fs, func(context.Context, any) error { return nil }, req)
	if err != nil {
		t.Fatal(err)
	}
	if !result.DryRun {
		t.Fatal("result.DryRun not propagated")
	}
	if result.Summary.Adds != 2 {
		t.Fatalf("dry-run adds = %d, want 2", result.Summary.Adds)
	}
	for _, call := range d.FakeCalls() {
		for _, prefix := range writeMutations {
			if strings.HasPrefix(call, prefix) {
				t.Fatalf("dry-run mutated the backend: %s", call)
			}
		}
	}
}

// TestNonDryRunMutatesBackend is the positive control: without --dry-run
// the same pair must touch the backend (EnsureRoot mkdirs the destination,
// and the executor copies files into the VFS), proving the dry-run
// assertion above is sensitive rather than vacuously green.
func TestNonDryRunMutatesBackend(t *testing.T) {
	useTestQryptHome(t)
	src := t.TempDir()
	writeTestFile(t, filepath.Join(src, "a.txt"), "aaa")

	d := drive.NewFakeDriver()
	fs, err := vfs.New(d, vfs.Options{StorageDir: t.TempDir(), CacheMaxBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	req := Request{
		Source:      Target{Kind: TargetLocal, Raw: src, LocalPath: src},
		Destination: Target{Kind: TargetVFS, Raw: "/dest", VFSPath: "/dest"},
		CompareMode: "size-mtime",
		Conflict:    "error",
	}
	if _, err := Run(context.Background(), fs, func(context.Context, any) error { return nil }, req); err != nil {
		t.Fatal(err)
	}
	mutated := false
	for _, call := range d.FakeCalls() {
		for _, prefix := range writeMutations {
			if strings.HasPrefix(call, prefix) {
				mutated = true
			}
		}
	}
	if !mutated {
		t.Fatalf("non-dry-run produced no backend mutation (calls: %v)", d.FakeCalls())
	}
}
