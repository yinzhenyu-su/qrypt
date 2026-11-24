package qryptstorage

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func open(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(FULL)&_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestTransactionCommitRollbackAndReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ledger.db")
	db := open(t, path)
	if _, err := db.Exec(`CREATE TABLE operations (id TEXT PRIMARY KEY, state TEXT NOT NULL)`); err != nil {
		t.Fatalf("schema: %v", err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if _, err := tx.Exec(`INSERT INTO operations VALUES ('op-1', 'absent')`); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM operations`).Scan(&count); err != nil {
		t.Fatalf("count after rollback: %v", err)
	}
	if count != 0 {
		t.Fatalf("rollback left %d rows", count)
	}

	tx, err = db.Begin()
	if err != nil {
		t.Fatalf("begin 2: %v", err)
	}
	if _, err := tx.Exec(`INSERT INTO operations VALUES ('op-2', 'applied')`); err != nil {
		t.Fatalf("insert 2: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	_ = db.Close()

	db2 := open(t, path)
	var state string
	if err := db2.QueryRow(`SELECT state FROM operations WHERE id = 'op-2'`).Scan(&state); err != nil {
		t.Fatalf("reopen read: %v", err)
	}
	if state != "applied" {
		t.Fatalf("state after reopen = %q", state)
	}
}

func TestPragmaJournalAndSync(t *testing.T) {
	db := open(t, filepath.Join(t.TempDir(), "ledger.db"))
	var mode string
	if err := db.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != "wal" {
		t.Fatalf("journal_mode = %q, want wal", mode)
	}
	var sync int
	if err := db.QueryRow(`PRAGMA synchronous`).Scan(&sync); err != nil {
		t.Fatal(err)
	}
	if sync != 2 { // 2 = FULL
		t.Fatalf("synchronous = %d, want 2 (FULL)", sync)
	}
}

// TestCommitSurvivesKillIsDurable proves a committed transaction is on disk
// even if the process dies immediately after commit (the durability contract the
// operation ledger relies on). The child is terminated with SIGKILL on unix and
// TerminateProcess on windows, so no deferred cleanup or WAL checkpoint runs.
func TestCommitSurvivesKillIsDurable(t *testing.T) {
	var path string
	if os.Getenv("SPIKE_DB") != "" {
		path = os.Getenv("SPIKE_DB")
	} else {
		path = filepath.Join(t.TempDir(), "ledger.db")
	}
	if os.Getenv("SPIKE_KILL_CHILD") == "1" {
		db := open(t, path)
		if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS operations (id TEXT PRIMARY KEY, state TEXT NOT NULL)`); err != nil {
			panic(err)
		}
		tx, err := db.Begin()
		if err != nil {
			panic(err)
		}
		if _, err := tx.Exec(`INSERT INTO operations VALUES ('op-crash', 'applied')`); err != nil {
			panic(err)
		}
		if err := tx.Commit(); err != nil {
			panic(err)
		}
		// Commit returned: synchronous=FULL should have fsync'ed the WAL.
		_ = os.WriteFile(path+".committed", []byte("1"), 0o600)
		killSelf()
	}
	db := open(t, path)
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS operations (id TEXT PRIMARY KEY, state TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	cmd := exec.Command(os.Args[0], "-test.run=^TestCommitSurvivesKillIsDurable$")
	cmd.Env = append(os.Environ(), "SPIKE_KILL_CHILD=1", "SPIKE_DB="+path)
	if err := cmd.Run(); err == nil {
		t.Fatal("child exited without crash")
	}
	if _, err := os.Stat(path + ".committed"); err != nil {
		t.Fatalf("child did not reach commit: %v", err)
	}
	db2 := open(t, path)
	defer db2.Close()
	var state string
	if err := db2.QueryRow(`SELECT state FROM operations WHERE id = 'op-crash'`).Scan(&state); err != nil {
		t.Fatalf("committed row lost after kill: %v", err)
	}
	if state != "applied" {
		t.Fatalf("state = %q", state)
	}
}

// TestSecondProcessIsLockedOut proves the OS-level exclusive lock: while one
// process holds a write transaction, another cannot acquire the write lock.
func TestSecondProcessIsLockedOut(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ledger.db")
	db := open(t, path)
	if _, err := db.Exec(`CREATE TABLE operations (id TEXT PRIMARY KEY, state TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO operations VALUES ('holder', 'in_progress')`); err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback() // keep the write lock while the child tries

	cmd := exec.Command(os.Args[0], "-test.run=^TestSecondProcessIsLockedOutChild$")
	cmd.Env = append(os.Environ(), "SPIKE_CHILD_DB="+path)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("second process acquired the write lock: %s", out)
	}
	t.Logf("second process correctly blocked: %s", out)
}

// TestSecondProcessIsLockedOutChild exits 3 when the write lock is held by
// another process (expected) and 0 when it wrongly succeeds.
func TestSecondProcessIsLockedOutChild(t *testing.T) {
	path := os.Getenv("SPIKE_CHILD_DB")
	if path == "" {
		t.Skip("child only")
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?_pragma=busy_timeout(100)")
	if err != nil {
		fmt.Println("open:", err)
		os.Exit(2)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := db.ExecContext(ctx, `BEGIN IMMEDIATE`); err == nil {
		_, _ = db.ExecContext(ctx, `ROLLBACK`)
		fmt.Println("acquired write lock")
		os.Exit(0)
	}
	fmt.Println("blocked while another process holds the write lock")
	os.Exit(3)
}
