package cli

import (
	"context"
	"encoding/json"
	"errors"
	"io"

	"github.com/yinzhenyu/qrypt/pkg/syncer"

	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

func setupCheckTest(t *testing.T) (configPath, remote, local string) {
	t.Helper()
	tmp := t.TempDir()
	remote = filepath.Join(tmp, "remote")
	local = filepath.Join(tmp, "local")
	if err := os.MkdirAll(filepath.Join(remote, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(local, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(remote, "a.txt"), []byte("aaa"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(remote, "sub", "b.txt"), []byte("bbbbbb"), 0o644); err != nil {
		t.Fatal(err)
	}
	configPath = filepath.Join(tmp, "qrypt.toml")
	if err := os.WriteFile(configPath, []byte("[[mounts]]\nname = \"loc\"\ntype = \"localfs\"\n[mounts.params]\nroot_path = \""+remote+"\"\n[mounts.upload]\nupload_delay = \"10ms\"\ndelete_delay = \"10ms\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return configPath, remote, local
}

func TestFsCheckIdenticalTrees(t *testing.T) {
	configPath, remote, local := setupCheckTest(t)
	copyTree(t, remote, local)

	out, _, err := executeCLI(t, "fs", "--config", configPath, "check", "/loc", local)
	if err != nil {
		t.Fatalf("check identical failed: %v", err)
	}
	if !strings.Contains(out, "ok: 2 files match") {
		t.Fatalf("check output = %q, want ok summary", out)
	}
}

func TestFsCheckSizeMismatchExits4(t *testing.T) {
	configPath, remote, local := setupCheckTest(t)
	copyTree(t, remote, local)
	if err := os.WriteFile(filepath.Join(local, "a.txt"), []byte("aaaaaaa"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, _, err := executeCLI(t, "fs", "--config", configPath, "check", "/loc", local)
	var xe *ExitError
	if err == nil || !errors.As(err, &xe) || xe.Code != ExitMismatch {
		t.Fatalf("check size mismatch err = %v, want ExitMismatch(4)", err)
	}
}

func TestFsCheckMissingAndExtraJSON(t *testing.T) {
	configPath, remote, local := setupCheckTest(t)
	copyTree(t, remote, local)
	// local loses sub/b.txt and gains orphan.txt
	if err := os.Remove(filepath.Join(local, "sub", "b.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(local, "orphan.txt"), []byte("zz"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, _, err := executeCLI(t, "fs", "--config", configPath, "check", "--json", "/loc", local)
	var xe *ExitError
	if err == nil || !errors.As(err, &xe) || xe.Code != ExitMismatch {
		t.Fatalf("check differences err = %v, want ExitMismatch(4)", err)
	}
	var result struct {
		OK           bool              `json:"ok"`
		FilesChecked int               `json:"files_checked"`
		Differences  []sync.Difference `json:"differences"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("check JSON invalid: %v\n%s", err, out)
	}
	if result.OK {
		t.Fatalf("check reported OK with differences: %+v", result)
	}
	reasons := map[string]bool{}
	for _, d := range result.Differences {
		reasons[d.Reason+" "+d.Path] = true
	}
	if !reasons["missing_in_b sub/b.txt"] || !reasons["extra_in_b orphan.txt"] {
		t.Fatalf("differences = %+v, want missing + extra", result.Differences)
	}
}

func TestFsCheckHashUnsupportedByBackend(t *testing.T) {
	configPath, remote, local := setupCheckTest(t)
	copyTree(t, remote, local)
	_, _, err := executeCLI(t, "fs", "--config", configPath, "check", "--hash", "/loc", local)
	if err == nil {
		t.Fatal("--hash on a backend without hash support must error")
	}
	if !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("--hash error = %v, want unsupported", err)
	}
}

func TestFsCheckBothSidesLocalIsUsageError(t *testing.T) {
	configPath, _, local := setupCheckTest(t)
	_, _, err := executeCLI(t, "fs", "--config", configPath, "check", local, filepath.Join(local, ".."))
	var xe *ExitError
	if err == nil || !errors.As(err, &xe) || xe.Code != ExitUsage {
		t.Fatalf("both-local check err = %v, want usage error(2)", err)
	}
}

func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestFsCheckDirectionFlipsWithArgumentOrder(t *testing.T) {
	// A vfs side that is the second argument must report extra/missing in the
	// other direction.
	tmp := t.TempDir()
	remote := filepath.Join(tmp, "remote")
	local := filepath.Join(tmp, "local")
	if err := os.MkdirAll(filepath.Join(remote, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(local, 0o755); err != nil {
		t.Fatal(err)
	}
	// vfs-only file and local-only file
	if err := os.WriteFile(filepath.Join(remote, "sub", "vonly.txt"), []byte("v"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(local, "lonely.txt"), []byte("l"), 0o644); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(tmp, "qrypt.toml")
	if err := os.WriteFile(configPath, []byte("[[mounts]]\nname = \"loc\"\ntype = \"localfs\"\n[mounts.params]\nroot_path = \""+remote+"\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// vfs first: vonly missing_in_b, lonely extra_in_b.
	out, _, err := executeCLI(t, "fs", "--config", configPath, "check", "--json", "/loc", local)
	if err == nil {
		t.Fatal("expected differences")
	}
	var result struct {
		Differences []sync.Difference `json:"differences"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("json: %v\n%s", err, out)
	}
	got := map[string]string{}
	for _, d := range result.Differences {
		got[d.Path] = d.Reason
	}
	if got["sub/vonly.txt"] != "missing_in_b" || got["lonely.txt"] != "extra_in_b" {
		t.Fatalf("vfs-first directions = %+v, want vonly=missing_in_b lonely=extra_in_b", got)
	}

	// local first: directions flip.
	out, _, err = executeCLI(t, "fs", "--config", configPath, "check", "--json", local, "/loc")
	if err == nil {
		t.Fatal("expected differences")
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("json: %v\n%s", err, out)
	}
	got = map[string]string{}
	for _, d := range result.Differences {
		got[d.Path] = d.Reason
	}
	if got["sub/vonly.txt"] != "extra_in_b" || got["lonely.txt"] != "missing_in_b" {
		t.Fatalf("local-first directions = %+v, want vonly=extra_in_b lonely=missing_in_b", got)
	}
}

func TestFsCheckDetectsMtimeDifference(t *testing.T) {
	configPath, remote, local := setupCheckTest(t)
	copyTree(t, remote, local)
	// Same size, different mtime.
	past := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(filepath.Join(local, "a.txt"), past, past); err != nil {
		t.Fatal(err)
	}
	out, _, err := executeCLI(t, "fs", "--config", configPath, "check", "--json", "/loc", local)
	if err == nil {
		t.Fatal("mtime difference must fail the check")
	}
	var result struct {
		Differences []sync.Difference `json:"differences"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("json: %v\n%s", err, out)
	}
	if len(result.Differences) != 1 || result.Differences[0].Reason != "mtime" {
		t.Fatalf("differences = %+v, want one mtime mismatch", result.Differences)
	}
}

func TestFsCheckTwoVFSTreesDetectMtimeAndDirection(t *testing.T) {
	tmp := t.TempDir()
	remoteA := filepath.Join(tmp, "ra")
	remoteB := filepath.Join(tmp, "rb")
	if err := os.MkdirAll(remoteA, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(remoteB, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(remoteA, "same.txt"), []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(remoteB, "same.txt"), []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A-only and B-only files.
	if err := os.WriteFile(filepath.Join(remoteA, "aonly.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(remoteB, "bonly.txt"), []byte("b"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Same size, different mtime on the B copy.
	past := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(filepath.Join(remoteB, "same.txt"), past, past); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(tmp, "qrypt.toml")
	cfg := "[[mounts]]\nname = \"ra\"\ntype = \"localfs\"\n[mounts.params]\nroot_path = \"" + remoteA + "\"\n\n" +
		"[[mounts]]\nname = \"rb\"\ntype = \"localfs\"\n[mounts.params]\nroot_path = \"" + remoteB + "\"\n"
	if err := os.WriteFile(configPath, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}

	out, _, err := executeCLI(t, "fs", "--config", configPath, "check", "--json", "/ra", "/rb")
	if err == nil {
		t.Fatal("two-vfs check with differences must fail")
	}
	var result struct {
		Differences []sync.Difference `json:"differences"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("json: %v\n%s", err, out)
	}
	got := map[string]string{}
	for _, d := range result.Differences {
		got[d.Path] = d.Reason
	}
	if got["aonly.txt"] != "missing_in_b" || got["bonly.txt"] != "extra_in_b" || got["same.txt"] != "mtime" {
		t.Fatalf("two-vfs differences = %+v, want aonly=missing_in_b bonly=extra_in_b same=mtime", got)
	}
}

func TestFsCheckTwoVFSTreesHashUnsupported(t *testing.T) {
	configPath, _, _ := setupCheckTest(t)
	_, _, err := executeCLI(t, "fs", "--config", configPath, "check", "--hash", "/loc", "/loc")
	if err == nil {
		t.Fatal("--hash on backends without hash support must error")
	}
	if !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("--hash error = %v, want unsupported", err)
	}
}

// TestFsCheckDetectsTypeConflict verifies a file/directory mismatch on the
// same relative path is reported as type instead of silently ignored.
func TestFsCheckDetectsTypeConflict(t *testing.T) {
	configPath, remote, _ := setupCheckTest(t)

	// Source: file at sub/item. Destination: directory at sub/item.
	if err := os.Remove(filepath.Join(remote, "sub", "b.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(remote, "sub", "item"), []byte("file-data"), 0o644); err != nil {
		t.Fatal(err)
	}
	local := filepath.Join(t.TempDir(), "local")
	if err := os.MkdirAll(filepath.Join(local, "sub", "item", "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(local, "sub", "item", "nested", "x.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(local, "a.txt"), []byte("aaa"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, _, err := executeCLI(t, "fs", "--config", configPath, "check", "--json", "/loc", local)
	var xe *ExitError
	if err == nil || !errors.As(err, &xe) || xe.Code != ExitMismatch {
		t.Fatalf("type conflict err = %v, want ExitMismatch(4)", err)
	}
	var result struct {
		OK          bool              `json:"ok"`
		Differences []sync.Difference `json:"differences"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("unmarshal: %v out=%s", err, out)
	}
	got := map[string]string{}
	for _, d := range result.Differences {
		got[d.Path] = d.Reason
	}
	if got["sub/item"] != "type" {
		t.Fatalf("sub/item reason = %q, want type; all diffs = %v", got["sub/item"], got)
	}
	// Differences are sorted by path for stable output.
	if result.Differences[0].Path != "sub/item" {
		t.Fatalf("first diff = %s, want sub/item (sorted)", result.Differences[0].Path)
	}
}

// TestCompareVFSHashPairAutoDegrades verifies sync's default comparison
// treats missing hash support as a match (size/mtime already compared)
// instead of failing, while the explicit --hash mode errors.
func TestCompareVFSHashPairAutoDegrades(t *testing.T) {
	ctx := context.Background()
	// A filesystem without RemoteHash support (drive-layer snapshotter).
	type noHashFS struct{ vfs.FileSystem }
	fs := noHashFS{}
	a := sync.Target{Kind: sync.TargetVFS, VFSPath: "/loc", MountName: "loc"}
	b := sync.Target{Kind: sync.TargetLocal, LocalPath: t.TempDir()}

	// autoHash (sync default): degrade to match, no error.
	matched, detail, err := sync.CompareHashPair(ctx, fs, a, b, "f.txt", true)
	if err != nil || !matched || detail != "" {
		t.Fatalf("autoHash = (%v, %q, %v), want (true, \"\", nil)", matched, detail, err)
	}
	// forced (--hash / fs check): error surfaces.
	if _, _, err := sync.CompareHashPair(ctx, fs, a, b, "f.txt", false); !errors.Is(err, drive.ErrUnsupported) {
		t.Fatalf("forced err = %v, want ErrUnsupported", err)
	}
}

// errHashFS simulates a backend whose RemoteHash fails with a network/IO
// error rather than the unsupported-hash signal.
type errHashFS struct{ vfs.FileSystem }

func (errHashFS) RemoteHash(context.Context, string) (drive.HashAlgorithm, string, error) {
	return "", "", io.ErrUnexpectedEOF
}

// TestCompareVFSHashPairNetworkErrorNotDegraded: an autoHash comparison must
// propagate network/data errors instead of reporting a match, so a backend
// outage or permission failure never looks like content equality.
func TestCompareVFSHashPairNetworkErrorNotDegraded(t *testing.T) {
	ctx := context.Background()
	vfsSide := sync.Target{Kind: sync.TargetVFS, VFSPath: "/loc", MountName: "loc"}
	localSide := sync.Target{Kind: sync.TargetLocal, LocalPath: t.TempDir()}

	for name, args := range map[string]struct {
		a, b     sync.Target
		autoHash bool
	}{
		"vfs-local autoHash": {vfsSide, localSide, true},
		"vfs-vfs autoHash":   {vfsSide, vfsSide, true},
		"forced hash":        {vfsSide, localSide, false},
	} {
		_, _, err := sync.CompareHashPair(ctx, errHashFS{}, args.a, args.b, "f.txt", args.autoHash)
		if err == nil {
			t.Fatalf("%s: network error was degraded to a match", name)
		}
		if errors.Is(err, drive.ErrUnsupported) {
			t.Fatalf("%s: error was misclassified as unsupported: %v", name, err)
		}
	}
}

// TestLocalFileHashMD5 computes md5 of a local file.
func TestLocalFileHashMD5(t *testing.T) {
	path := filepath.Join(t.TempDir(), "h.bin")
	if err := os.WriteFile(path, []byte("abc"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := sync.LocalFileHash(path, drive.HashMD5)
	if err != nil {
		t.Fatal(err)
	}
	// md5("abc") = 900150983cd24fb0d6963f7d28e17f72
	if got != "900150983cd24fb0d6963f7d28e17f72" {
		t.Fatalf("md5 = %q, want 900150983cd24fb0d6963f7d28e17f72", got)
	}
}

func TestFsCheckCompareMtimeOnlyIgnoresSizeChange(t *testing.T) {
	configPath, remote, local := setupCheckTest(t)
	copyTree(t, remote, local)
	// Same mtime, different size: size-mtime reports a mismatch, mtime-only
	// treats the pair as identical because mtime matches.
	remoteInfo, err := os.Stat(filepath.Join(remote, "a.txt"))
	if err != nil {
		t.Fatal(err)
	}
	st := remoteInfo.ModTime()
	if err := os.WriteFile(filepath.Join(local, "a.txt"), []byte("local-size-differs"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(filepath.Join(local, "a.txt"), st, st); err != nil {
		t.Fatal(err)
	}

	_, _, err = executeCLI(t, "fs", "--config", configPath, "check", "/loc", local)
	var xe *ExitError
	if err == nil || !errors.As(err, &xe) || xe.Code != ExitMismatch {
		t.Fatalf("default check err = %v, want ExitMismatch(4)", err)
	}

	out, _, err := executeCLI(t, "fs", "--config", configPath, "check", "--compare=mtime-only", "/loc", local)
	if err != nil {
		t.Fatalf("mtime-only check failed: %v", err)
	}
	if !strings.Contains(out, "ok: 2 files match") {
		t.Fatalf("mtime-only check output = %q, want ok summary", out)
	}
}

func TestFsCheckCompareMtimeOnlyDetectsMtimeChange(t *testing.T) {
	configPath, remote, local := setupCheckTest(t)
	copyTree(t, remote, local)
	past := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(filepath.Join(local, "a.txt"), past, past); err != nil {
		t.Fatal(err)
	}

	out, _, err := executeCLI(t, "fs", "--config", configPath, "check", "--compare=mtime-only", "--json", "/loc", local)
	if err == nil {
		t.Fatal("mtime difference must fail the check")
	}
	var result struct {
		Differences []sync.Difference `json:"differences"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("json: %v\n%s", err, out)
	}
	if len(result.Differences) != 1 || result.Differences[0].Reason != "mtime" {
		t.Fatalf("differences = %+v, want one mtime mismatch", result.Differences)
	}
}

func TestFsCheckCompareHashForcesHash(t *testing.T) {
	configPath, remote, local := setupCheckTest(t)
	copyTree(t, remote, local)
	// localfs has no content-hash support, so --compare=hash must behave
	// exactly like --hash: an unsupported error.
	_, _, err := executeCLI(t, "fs", "--config", configPath, "check", "--compare=hash", "/loc", local)
	if err == nil {
		t.Fatal("--compare=hash on a backend without hash support must error")
	}
	if !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("--compare=hash error = %v, want unsupported", err)
	}
}

func TestFsCheckCompareInvalidMode(t *testing.T) {
	configPath, _, local := setupCheckTest(t)
	_, _, err := executeCLI(t, "fs", "--config", configPath, "check", "--compare=bogus", "/loc", local)
	if err == nil || !strings.Contains(err.Error(), "invalid --compare") {
		t.Fatalf("invalid --compare err = %v, want validation error", err)
	}
}
