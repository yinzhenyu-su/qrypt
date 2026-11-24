package qryptstorage

import (
	"bufio"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"testing"
	"time"
)

func TestOpenProbesAndLocks(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	var mode string
	if err := store.DB().QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != "wal" {
		t.Fatalf("journal_mode = %q, want wal", mode)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	reopened, err := Open(dir)
	if err != nil {
		t.Fatalf("reopen after close: %v", err)
	}
	_ = reopened.Close()
}

func TestSecondOpenFailsWhileLocked(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = store.Close() }()

	if _, err := Open(dir); !errors.Is(err, ErrLocked) {
		t.Fatalf("second open error = %v, want ErrLocked", err)
	}
}

func TestProbeFailureRejectsWritableOpen(t *testing.T) {
	dir := t.TempDir()
	if _, err := openStore(dir, func(*sql.DB) error { return errors.New("wal unavailable") }); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("probe failure error = %v, want ErrUnsupported", err)
	}
	// The failed open must release the owner lock so a later attempt can probe
	// again and give the user a real recovery path.
	store, err := Open(dir)
	if err != nil {
		t.Fatalf("open after failed probe: %v", err)
	}
	_ = store.Close()
}

// TestStoreLockHelper is the subprocess side of the killed-owner test: it opens
// the store, reports readiness on stdout, and waits to be killed.
func TestStoreLockHelper(t *testing.T) {
	dir := os.Getenv("QRYPT_SPIKE_LOCK_DIR")
	if dir == "" {
		t.Skip("helper process")
	}
	store, err := Open(dir)
	if err != nil {
		fmt.Println("open-error:", err)
		os.Exit(2)
	}
	defer func() { _ = store.Close() }()
	fmt.Println("ready")
	if _, err := bufio.NewReader(os.Stdin).ReadByte(); err != nil {
		return
	}
}

func TestLockReacquiredAfterOwnerKilled(t *testing.T) {
	dir := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestStoreLockHelper$")
	cmd.Env = append(os.Environ(), "QRYPT_SPIKE_LOCK_DIR="+dir)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill() }()
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || line != "ready\n" {
		t.Fatalf("helper did not become ready: line=%q err=%v", line, err)
	}
	if _, err := Open(dir); !errors.Is(err, ErrLocked) {
		t.Fatalf("open while helper holds lock = %v, want ErrLocked", err)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("kill helper: %v", err)
	}
	_ = cmd.Wait()

	deadline := time.Now().Add(10 * time.Second)
	for {
		store, err := Open(dir)
		if err == nil {
			_ = store.Close()
			return
		}
		if !errors.Is(err, ErrLocked) && !errors.Is(err, ErrUnsupported) {
			t.Fatalf("open after kill: %v", err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("lock not released after owner was killed: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestLatencyBudget records open and commit p50/p99 so the platform matrix and
// later Slice 2 work compare against explicit numbers instead of anecdotes.
// The assertions are deliberately loose: they catch order-of-magnitude
// regressions, not CI jitter.
func TestLatencyBudget(t *testing.T) {
	dir := t.TempDir()
	openSamples := make([]time.Duration, 0, 20)
	for i := 0; i < 20; i++ {
		start := time.Now()
		store, err := Open(dir)
		if err != nil {
			t.Fatalf("open %d: %v", i, err)
		}
		if err := store.Close(); err != nil {
			t.Fatalf("close %d: %v", i, err)
		}
		openSamples = append(openSamples, time.Since(start))
	}
	store, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	if _, err := store.DB().Exec(`CREATE TABLE IF NOT EXISTS latency (id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	commitSamples := make([]time.Duration, 0, 200)
	for i := 0; i < 200; i++ {
		start := time.Now()
		tx, err := store.DB().Begin()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(`INSERT OR REPLACE INTO latency (id) VALUES (?)`, i); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		commitSamples = append(commitSamples, time.Since(start))
	}
	openP50, openP99 := percentiles(openSamples)
	commitP50, commitP99 := percentiles(commitSamples)
	t.Logf("latency budget: open p50=%v p99=%v (budget 200ms); commit p50=%v p99=%v (budget 10ms)", openP50, openP99, commitP50, commitP99)
	if openP99 > 200*time.Millisecond {
		t.Fatalf("open p99 = %v, exceeds 200ms budget", openP99)
	}
	if commitP99 > 10*time.Millisecond {
		t.Fatalf("commit p99 = %v, exceeds 10ms budget", commitP99)
	}
}

func percentiles(samples []time.Duration) (p50, p99 time.Duration) {
	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
	return samples[len(samples)/2], samples[(len(samples)*99)/100]
}
