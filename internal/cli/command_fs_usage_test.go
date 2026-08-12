package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	clifs "github.com/yinzhenyu/qrypt/internal/cli/fs"
)

func setupUsageTest(t *testing.T) string {
	t.Helper()
	remote := filepath.Join(t.TempDir(), "src")
	if err := os.MkdirAll(filepath.Join(remote, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(remote, "a.txt"), []byte("aaa"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(remote, "sub", "b.txt"), []byte("bbbbbbb"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(remote, "zebra.txt"), []byte("zz"), 0o644); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(t.TempDir(), "qrypt.toml")
	if err := os.WriteFile(configPath, []byte(`
[[mounts]]
name = "loc"
type = "localfs"
[mounts.params]
root_path = "`+remote+`"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	return configPath
}

func executeCLI(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	var out, errBuf bytes.Buffer
	root := NewRootCommand()
	root.SetOut(&out)
	root.SetErr(&errBuf)
	root.SetArgs(args)
	err := root.Execute()
	return out.String(), errBuf.String(), err
}

func TestFsDfJSON(t *testing.T) {
	configPath := setupUsageTest(t)
	out, _, err := executeCLI(t, "fs", "--config", configPath, "df", "--json")
	if err != nil {
		t.Fatalf("df failed: %v", err)
	}
	var result struct {
		Mounts []clifs.SpaceEntry `json:"mounts"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("df JSON invalid: %v\n%s", err, out)
	}
	if len(result.Mounts) != 1 || result.Mounts[0].Name != "loc" {
		t.Fatalf("df mounts = %+v, want one loc mount", result.Mounts)
	}
	entry := result.Mounts[0]
	if entry.Total <= 0 || entry.Used > entry.Total || entry.Used < 0 {
		t.Fatalf("df entry implausible: %+v", entry)
	}
}

func TestFsDfSingleMountByArgument(t *testing.T) {
	configPath := setupUsageTest(t)
	out, _, err := executeCLI(t, "fs", "--config", configPath, "df", "loc")
	if err != nil {
		t.Fatalf("df loc failed: %v", err)
	}
	if !strings.Contains(out, "loc: total") {
		t.Fatalf("df single-mount output missing mount line:\n%s", out)
	}
	if strings.Contains(out, "mounts") {
		t.Fatalf("df single-mount output looks aggregated:\n%s", out)
	}
	_, _, err = executeCLI(t, "fs", "--config", configPath, "df", "nope")
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("df with unknown mount = %v, want not found error", err)
	}
}

func TestFsDfBytesFlag(t *testing.T) {
	configPath := setupUsageTest(t)
	out, _, err := executeCLI(t, "fs", "--config", configPath, "df", "--bytes")
	if err != nil {
		t.Fatalf("df --bytes failed: %v", err)
	}
	if strings.Contains(out, " G ") || strings.Contains(out, " M ") {
		t.Fatalf("df --bytes output still human-readable:\n%s", out)
	}
}

func TestFsDuJSON(t *testing.T) {
	configPath := setupUsageTest(t)
	out, _, err := executeCLI(t, "fs", "--config", configPath, "du", "--json", "/loc")
	if err != nil {
		t.Fatalf("du failed: %v", err)
	}
	var result clifs.DiskUsageResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("du JSON invalid: %v\n%s", err, out)
	}
	if result.Files != 3 || result.Bytes != 12 {
		t.Fatalf("du result = %+v, want 3 files, 12 bytes", result)
	}
}

func TestFsFindMatchesSubstring(t *testing.T) {
	configPath := setupUsageTest(t)
	out, _, err := executeCLI(t, "fs", "--config", configPath, "find", "/loc", "zeb")
	if err != nil {
		t.Fatalf("find failed: %v", err)
	}
	if !strings.Contains(out, "/loc/zebra.txt") {
		t.Fatalf("find output missing match:\n%s", out)
	}
	if strings.Contains(out, "/loc/a.txt") || strings.Contains(out, "/loc/sub/b.txt") {
		t.Fatalf("find matched unrelated entry:\n%s", out)
	}
}

func TestFsListHumanSizes(t *testing.T) {
	configPath := setupUsageTest(t)
	out, _, err := executeCLI(t, "fs", "--config", configPath, "list", "--human", "/loc")
	if err != nil {
		t.Fatalf("list --human failed: %v", err)
	}
	if !strings.Contains(out, "3 B a.txt") {
		t.Fatalf("list --human output missing human size:\n%s", out)
	}
}

func TestFsRmAndMvJSON(t *testing.T) {
	configPath := setupUsageTest(t)
	out, _, err := executeCLI(t, "fs", "--config", configPath, "rm", "--json", "/loc/zebra.txt")
	if err != nil {
		t.Fatalf("rm --json failed: %v", err)
	}
	if !strings.Contains(out, `"removed": true`) {
		t.Fatalf("rm --json output invalid:\n%s", out)
	}
	out, _, err = executeCLI(t, "fs", "--config", configPath, "mv", "--json", "/loc/a.txt", "/loc/renamed.txt")
	if err != nil {
		t.Fatalf("mv --json failed: %v", err)
	}
	var mv struct {
		Renamed bool `json:"renamed"`
	}
	if err := json.Unmarshal([]byte(out), &mv); err != nil || !mv.Renamed {
		t.Fatalf("mv --json output invalid: %v\n%s", err, out)
	}
}

func TestBandwidthOverrideFromFlags(t *testing.T) {
	// both
	fsCmd := newFsCmd()
	if err := fsCmd.ParseFlags([]string{"--bwlimit", "10M"}); err != nil {
		t.Fatal(err)
	}
	limits, err := bandwidthOverrideFromFlags(fsCmd)
	if err != nil {
		t.Fatal(err)
	}
	if limits.DownloadBytesPerSecond != 10<<20 || limits.UploadBytesPerSecond != 10<<20 {
		t.Fatalf("both limits = %+v, want 10M each", limits)
	}

	// download only must not clobber upload set by --bwlimit
	fsCmd = newFsCmd()
	if err := fsCmd.ParseFlags([]string{"--bwlimit", "10M", "--bwlimit-download", "5M"}); err != nil {
		t.Fatal(err)
	}
	limits, err = bandwidthOverrideFromFlags(fsCmd)
	if err != nil {
		t.Fatal(err)
	}
	if limits.DownloadBytesPerSecond != 5<<20 || limits.UploadBytesPerSecond != 10<<20 {
		t.Fatalf("mixed limits = %+v, want 5M/10M", limits)
	}

	// invalid size
	fsCmd = newFsCmd()
	if err := fsCmd.ParseFlags([]string{"--bwlimit", "abc"}); err != nil {
		t.Fatal(err)
	}
	if _, err := bandwidthOverrideFromFlags(fsCmd); err == nil {
		t.Fatal("invalid --bwlimit must error")
	}

	// no flags -> nil override
	fsCmd = newFsCmd()
	if err := fsCmd.ParseFlags([]string{}); err != nil {
		t.Fatal(err)
	}
	limits, err = bandwidthOverrideFromFlags(fsCmd)
	if err != nil || limits != nil {
		t.Fatalf("no flags: limits = %v err = %v, want nil", limits, err)
	}
}

func TestFsDfJSONIncludesUnsupportedMountError(t *testing.T) {
	// s3 backends do not support space queries; the JSON output must keep the
	// failing mount with an error field instead of silently dropping it.
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK) // satisfies HeadBucket during Init
	}))
	defer api.Close()

	tmp := t.TempDir()
	remote := filepath.Join(tmp, "remote")
	if err := os.MkdirAll(remote, 0o755); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(tmp, "qrypt.toml")
	cfg := `[[mounts]]
name = "loc"
type = "localfs"
[mounts.params]
root_path = "` + remote + `"

[[mounts]]
name = "s3m"
type = "s3"
[mounts.params]
bucket = "b"
endpoint = "` + api.URL + `"
region = "us-east-1"
access_key_id = "x"
secret_access_key = "y"
`
	if err := os.WriteFile(configPath, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}

	out, _, err := executeCLI(t, "fs", "--config", configPath, "df", "--json")
	if err != nil {
		t.Fatalf("df --json failed: %v", err)
	}
	var result struct {
		Mounts []clifs.SpaceEntry `json:"mounts"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("df JSON invalid: %v\n%s", err, out)
	}
	if len(result.Mounts) != 2 {
		t.Fatalf("df mounts = %d, want 2 (failing mount must not be dropped)", len(result.Mounts))
	}
	byName := map[string]clifs.SpaceEntry{}
	for _, m := range result.Mounts {
		byName[m.Name] = m
	}
	if byName["loc"].Error != "" || byName["loc"].Total <= 0 {
		t.Fatalf("loc mount malformed: %+v", byName["loc"])
	}
	if byName["s3m"].Error == "" || !strings.Contains(byName["s3m"].Error, "space query") {
		t.Fatalf("s3 mount missing error field: %+v", byName["s3m"])
	}
}

func TestFsDuSingleFile(t *testing.T) {
	configPath := setupUsageTest(t)
	out, _, err := executeCLI(t, "fs", "--config", configPath, "du", "--json", "/loc/a.txt")
	if err != nil {
		t.Fatalf("du on a file failed: %v", err)
	}
	var result clifs.DiskUsageResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("du JSON invalid: %v\n%s", err, out)
	}
	if result.Files != 1 || result.Bytes != 3 {
		t.Fatalf("du file = %+v, want 1 file, 3 bytes", result)
	}
}

func TestFsFindNormalizesRelativePath(t *testing.T) {
	configPath := setupUsageTest(t)
	// No leading slash: results must still be canonical /loc/... paths.
	out, _, err := executeCLI(t, "fs", "--config", configPath, "find", "loc", "zeb")
	if err != nil {
		t.Fatalf("find with relative path failed: %v", err)
	}
	if !strings.Contains(out, "/loc/zebra.txt") {
		t.Fatalf("find output not canonical:\n%s", out)
	}
	if strings.Contains(out, "\nloc/") {
		t.Fatalf("find output leaked non-canonical path:\n%s", out)
	}
}

func TestFsFindJSONIncludesFullPath(t *testing.T) {
	configPath := setupUsageTest(t)
	out, _, err := executeCLI(t, "fs", "--config", configPath, "find", "--json", "/loc", "a.txt")
	if err != nil {
		t.Fatalf("find --json failed: %v", err)
	}
	var results []clifs.FindResult
	if err := json.Unmarshal([]byte(out), &results); err != nil {
		t.Fatalf("find JSON invalid: %v\n%s", err, out)
	}
	if len(results) != 2 {
		t.Fatalf("find results = %d, want 2", len(results))
	}
	paths := map[string]bool{}
	for _, r := range results {
		if r.Path == "" || r.Entry.Name == "" {
			t.Fatalf("find result missing path or entry: %+v", r)
		}
		paths[r.Path] = true
	}
	if !paths["/loc/a.txt"] || !paths["/loc/zebra.txt"] {
		t.Fatalf("find paths = %v, want /loc/a.txt and /loc/zebra.txt", paths)
	}
}
