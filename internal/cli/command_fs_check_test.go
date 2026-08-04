package cli

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
	if err := os.WriteFile(configPath, []byte("[[mounts]]\nname = \"loc\"\ntype = \"localfs\"\n[mounts.params]\nroot_path = \""+remote+"\"\n"), 0o644); err != nil {
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
		Differences  []checkDifference `json:"differences"`
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
	if err := os.WriteFile(filepath.Join(local, "lonly.txt"), []byte("l"), 0o644); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(tmp, "qrypt.toml")
	if err := os.WriteFile(configPath, []byte("[[mounts]]\nname = \"loc\"\ntype = \"localfs\"\n[mounts.params]\nroot_path = \""+remote+"\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// vfs first: vonly missing_in_b, lonly extra_in_b.
	out, _, err := executeCLI(t, "fs", "--config", configPath, "check", "--json", "/loc", local)
	if err == nil {
		t.Fatal("expected differences")
	}
	var result struct {
		Differences []checkDifference `json:"differences"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("json: %v\n%s", err, out)
	}
	got := map[string]string{}
	for _, d := range result.Differences {
		got[d.Path] = d.Reason
	}
	if got["sub/vonly.txt"] != "missing_in_b" || got["lonly.txt"] != "extra_in_b" {
		t.Fatalf("vfs-first directions = %+v, want vonly=missing_in_b lonly=extra_in_b", got)
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
	if got["sub/vonly.txt"] != "extra_in_b" || got["lonly.txt"] != "missing_in_b" {
		t.Fatalf("local-first directions = %+v, want vonly=extra_in_b lonly=missing_in_b", got)
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
		Differences []checkDifference `json:"differences"`
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
		Differences []checkDifference `json:"differences"`
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
