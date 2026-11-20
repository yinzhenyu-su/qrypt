package cli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/config"
	"github.com/yinzhenyu/qrypt/pkg/control"
	corepkg "github.com/yinzhenyu/qrypt/pkg/core"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/drivers/localfs"
	"github.com/yinzhenyu/qrypt/pkg/fsclient"
	"github.com/yinzhenyu/qrypt/pkg/util"
)

// TestResidentCoreBaseline measures the three scenarios issue #31 asks for:
// cold start, warm owner hits, and disk-cache hits after a Core restart. The
// counting driver wraps localfs so every number is a real backend call the
// resident Core issued, not an estimate.
//
// The numbers are written as a markdown table to $QRYPT_PERF_REPORT when set
// (nightly CI appends it to the job summary) and to the test log otherwise.
func TestResidentCoreBaseline(t *testing.T) {
	if testing.Short() {
		t.Skip("resident Core baseline")
	}
	if runtime.GOOS == "windows" {
		t.Skip("baseline is measured on unix hosts")
	}
	if os.Getenv("QRYPT_PERF_BASELINE") != "1" {
		t.Skip("set QRYPT_PERF_BASELINE=1 (scripts/ci/perf-baseline.sh) to run the baseline")
	}
	base, err := os.MkdirTemp("", "qrypt-perf-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	remote := filepath.Join(base, "remote")
	dir := filepath.Join(remote, "dir")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	const dirFiles = 50
	payload := bytes.Repeat([]byte("x"), 4096)
	for i := 0; i < dirFiles; i++ {
		name := filepath.Join(dir, fmt.Sprintf("f%02d.bin", i))
		if err := os.WriteFile(name, payload, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	bigData := bytes.Repeat([]byte("b"), 512*1024)
	if err := os.WriteFile(filepath.Join(remote, "big.bin"), bigData, 0o600); err != nil {
		t.Fatal(err)
	}

	configPath := filepath.Join(base, "qrypt.toml")
	workDir := filepath.Join(base, "work")
	content := fmt.Sprintf(`
mount_point = %s
[storage]
work_dir = %s
[upload]
upload_delay = "10ms"
delete_delay = "10ms"
[[mounts]]
name = "local"
type = "perf-localfs"
[mounts.params]
root_path = %s
`, util.TOMLPath(filepath.Join(base, "mnt")), util.TOMLPath(workDir), util.TOMLPath(remote))
	if err := os.WriteFile(configPath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	counters := &perfCounters{}
	activePerfCounters.Store(counters)
	t.Cleanup(func() { activePerfCounters.Store(nil) })
	t.Setenv("QRYPT_CORE_FS_REQUIRED", "1")

	type coreRun struct {
		done chan error
		out  *bytes.Buffer
	}
	startServe := func() *coreRun {
		root := NewRootCommand()
		out := &bytes.Buffer{}
		root.SetOut(out)
		root.SetErr(out)
		root.SetArgs([]string{"core", "serve", "--foreground", "--keep", "--config", configPath})
		run := &coreRun{done: make(chan error, 1), out: out}
		go func() { run.done <- root.Execute() }()
		return run
	}
	waitRunning := func(t *testing.T, run *coreRun) time.Duration {
		t.Helper()
		started := time.Now()
		for time.Since(started) < 30*time.Second {
			select {
			case err := <-run.done:
				t.Fatalf("core serve exited before it became ready: %v\n%s", err, run.out.String())
			default:
			}
			out, err := executeRoot(t, "core", "status", "--json", "--config", configPath)
			if err == nil && strings.Contains(out, `"state": "running"`) {
				return time.Since(started)
			}
			time.Sleep(25 * time.Millisecond)
		}
		t.Fatalf("resident Core did not become ready within 30s; last status: %s", run.out.String())
		return 0
	}
	stopServe := func(t *testing.T, run *coreRun) {
		t.Helper()
		if out, err := executeRoot(t, "core", "stop", "--config", configPath); err != nil {
			t.Fatalf("core stop: %v (%s)", err, out)
		}
		select {
		case err := <-run.done:
			if err != nil {
				t.Fatalf("core serve exited with error: %v", err)
			}
		case <-time.After(30 * time.Second):
			t.Fatal("core serve did not exit after stop")
		}
	}

	// Scenario 1: cold start. No Core exists yet, so the command has to spawn
	// one, initialize every driver, and resolve the mount root before the first
	// RPC can be served.
	coldRun := startServe()
	coldStart := waitRunning(t, coldRun)
	coldInit := counters.initCalls.Load()

	listStart := time.Now()
	if out, err := executeRoot(t, "fs", "list", "/local", "--config", configPath); err != nil {
		t.Fatalf("first fs list: %v\n%s", err, out)
	}
	firstList := time.Since(listStart)
	firstListChildren := counters.listCalls.Load()
	resolveCalls := counters.resolveCalls.Load()

	rootListBefore := counters.listCalls.Load()
	if out, err := executeRoot(t, "fs", "list", "/", "--config", configPath); err != nil {
		t.Fatalf("namespace root list: %v\n%s", err, out)
	}
	rootListChildren := counters.listCalls.Load() - rootListBefore

	// Scenario 2: warm owner hits. Every command reuses the running Core and
	// only pays an RPC round trip.
	hotBefore := counters.listCalls.Load()
	hotDurations := make([]time.Duration, 0, 20)
	for i := 0; i < 20; i++ {
		started := time.Now()
		if out, err := executeRoot(t, "fs", "list", "/local/dir", "--config", configPath); err != nil {
			t.Fatalf("warm fs list: %v\n%s", err, out)
		}
		hotDurations = append(hotDurations, time.Since(started))
	}
	hotListChildren := counters.listCalls.Load() - hotBefore

	// The in-memory listing cache has a 10s TTL. Wait it out and list once more
	// to measure the miss path that the persistent list cache (#32) is meant to
	// make free after a Core restart.
	time.Sleep(11 * time.Second)
	expiredBefore := counters.listCalls.Load()
	expiredStart := time.Now()
	if out, err := executeRoot(t, "fs", "list", "/local/dir", "--config", configPath); err != nil {
		t.Fatalf("post-TTL fs list: %v\n%s", err, out)
	}
	expiredList := time.Since(expiredStart)
	expiredListChildren := counters.listCalls.Load() - expiredBefore

	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	endpoint := corepkg.ControlSocketPath(cfg, corepkg.RuntimeLayout{RunDir: corepkg.RuntimeRunDir(configPath, cfg)})
	client, err := control.NewClient(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	coreFS := fsclient.New(client)
	readRange := func(t *testing.T) int64 {
		t.Helper()
		before := counters.readCalls.Load()
		rc, err := coreFS.Read(context.Background(), "/local/big.bin", 0, 4096)
		if err != nil {
			t.Fatalf("core read: %v", err)
		}
		if _, err := io.ReadAll(rc); err != nil {
			t.Fatalf("read body: %v", err)
		}
		if err := rc.Close(); err != nil {
			t.Fatalf("close body: %v", err)
		}
		return counters.readCalls.Load() - before
	}
	firstRead := readRange(t)

	stopServe(t, coldRun)

	// Scenario 3: restart. The read cache index is flushed on close and loaded
	// again by the new Core, so the same range is served from disk without
	// touching the driver.
	restartRun := startServe()
	restartStart := waitRunning(t, restartRun)
	restartInit := counters.initCalls.Load() - coldInit
	restartRead := readRange(t)

	rssKB := processRSSKB(t)
	stopServe(t, restartRun)

	hotP50, hotP95 := percentiles(hotDurations)
	report := strings.Builder{}
	report.WriteString("| scenario | metric | value |\n")
	report.WriteString("| --- | --- | --- |\n")
	row := func(scenario, metric string, value any) {
		fmt.Fprintf(&report, "| %s | %s | %v |\n", scenario, metric, value)
	}
	row("cold start", "ready_ms", coldStart.Milliseconds())
	row("cold start", "init_calls", coldInit)
	row("cold start", "resolve_calls", resolveCalls)
	row("cold start", "first_list_ms", firstList.Milliseconds())
	row("cold start", "first_list_children", firstListChildren)
	row("warm owner", "list_runs", len(hotDurations))
	row("warm owner", "list_children_calls", hotListChildren)
	row("warm owner", "list_p50_ms", hotP50.Milliseconds())
	row("warm owner", "list_p95_ms", hotP95.Milliseconds())
	row("after 10s TTL", "list_ms", expiredList.Milliseconds())
	row("after 10s TTL", "list_children_calls", expiredListChildren)
	row("namespace root", "list_children_calls", rootListChildren)
	row("restart", "ready_ms", restartStart.Milliseconds())
	row("restart", "init_calls", restartInit)
	row("restart", "driver_reads_first", firstRead)
	row("restart", "driver_reads_after_restart", restartRead)
	row("process", "rss_kb", rssKB)
	table := report.String()
	t.Logf("resident Core baseline (dir_files=%d):\n%s", dirFiles, table)
	if path := os.Getenv("QRYPT_PERF_REPORT"); path != "" {
		if err := os.WriteFile(path, []byte(table), 0o644); err != nil {
			t.Fatalf("write perf report: %v", err)
		}
	}

	if coldInit != 1 {
		t.Fatalf("cold start should initialize one driver, got %d", coldInit)
	}
	if firstListChildren < 1 {
		t.Fatalf("first list should call the driver, got %d children calls", firstListChildren)
	}
	if rootListChildren != 0 {
		t.Fatalf("namespace root listing must not call ListChildren, got %d", rootListChildren)
	}
	if hotListChildren >= int64(len(hotDurations)) {
		t.Fatalf("warm listings inside the 10s TTL should be served from memory, got %d driver calls for %d runs", hotListChildren, len(hotDurations))
	}
	if expiredListChildren < 1 {
		t.Fatalf("listing after the TTL must reach the driver, got %d calls", expiredListChildren)
	}
	if restartInit != 1 {
		t.Fatalf("restart should initialize one driver, got %d", restartInit)
	}
	if firstRead < 1 {
		t.Fatalf("first read should call the driver, got %d", firstRead)
	}
	if restartRead != 0 {
		t.Fatalf("read cache should serve the restarted range without driver reads, got %d", restartRead)
	}
}

// perfCounters counts backend calls issued by the resident Core under test.
type perfCounters struct {
	initCalls    atomic.Int64
	resolveCalls atomic.Int64
	listCalls    atomic.Int64
	readCalls    atomic.Int64
}

var activePerfCounters atomic.Pointer[perfCounters]

type countingDriver struct {
	drive.Driver
	counters *perfCounters
}

func (d *countingDriver) Init(ctx context.Context) error {
	d.counters.initCalls.Add(1)
	return d.Driver.Init(ctx)
}

func (d *countingDriver) ResolvePath(ctx context.Context, path string) (string, error) {
	d.counters.resolveCalls.Add(1)
	return d.Driver.ResolvePath(ctx, path)
}

func (d *countingDriver) List(ctx context.Context, parentID string) ([]drive.Entry, error) {
	d.counters.listCalls.Add(1)
	return d.Driver.List(ctx, parentID)
}

func (d *countingDriver) Read(ctx context.Context, entry drive.Entry, offset, size int64) (io.ReadCloser, error) {
	d.counters.readCalls.Add(1)
	return d.Driver.Read(ctx, entry, offset, size)
}

func init() {
	drive.Register("perf-localfs", func(params drive.Params) (drive.Driver, error) {
		root := params["root_path"]
		if root == "" {
			return nil, fmt.Errorf("perf-localfs: missing root_path")
		}
		base := localfs.New(root)
		counters := activePerfCounters.Load()
		if counters == nil {
			return base, nil
		}
		return &countingDriver{Driver: base, counters: counters}, nil
	}, drive.ParamDef{Name: "root_path", Required: true, Description: "local directory backing the test mount"})
}

func percentiles(durations []time.Duration) (p50, p95 time.Duration) {
	if len(durations) == 0 {
		return 0, 0
	}
	sorted := append([]time.Duration(nil), durations...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	at := func(p float64) time.Duration {
		idx := int(p * float64(len(sorted)-1))
		return sorted[idx]
	}
	return at(0.50), at(0.95)
}

func processRSSKB(t *testing.T) int64 {
	t.Helper()
	out, err := exec.Command("ps", "-o", "rss=", "-p", strconv.Itoa(os.Getpid())).Output()
	if err != nil {
		t.Logf("rss unavailable: %v", err)
		return 0
	}
	value, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
	if err != nil {
		t.Logf("rss parse: %v", err)
		return 0
	}
	return value
}
