package qryptstorage

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// ErrLocked means another owner already holds the state directory.
var ErrLocked = errors.New("localstate: state directory is already in use by another process")

// ErrUnsupported means the directory cannot host the embedded ledger with the
// required durability settings; the caller must not fall back to a weaker mode.
var ErrUnsupported = errors.New("localstate: state directory cannot host the embedded ledger")

const (
	ledgerFile    = "ledger.db"
	ownerLockName = "lock"
)

// Store is an opened, exclusively owned local ledger.
type Store struct {
	db   *sql.DB
	lock *os.File
}

// Open acquires the exclusive owner lock for dir, opens the ledger in WAL mode
// with synchronous=FULL, and runs the capability probe. On any failure it
// releases the lock and returns an error; there is no degraded open.
func Open(dir string) (*Store, error) {
	return openStore(dir, probe)
}

func openStore(dir string, probeFn func(*sql.DB) error) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("localstate: create state directory: %w", err)
	}
	lock, err := os.OpenFile(filepath.Join(dir, ownerLockName), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("localstate: open owner lock: %w", err)
	}
	if err := lockFile(lock); err != nil {
		_ = lock.Close()
		if errors.Is(err, ErrLocked) {
			return nil, fmt.Errorf("%w: %s", ErrLocked, dir)
		}
		return nil, fmt.Errorf("localstate: lock state directory: %w", err)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(filepath.Join(dir, ledgerFile))+
		"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(FULL)&_pragma=foreign_keys(1)")
	if err == nil {
		err = probeFn(db)
		if err != nil {
			err = fmt.Errorf("%w (%s): %v", ErrUnsupported, dir, err)
		}
	}
	if err != nil {
		if db != nil {
			_ = db.Close()
		}
		_ = unlockFile(lock)
		_ = lock.Close()
		return nil, err
	}
	return &Store{db: db, lock: lock}, nil
}

// probe verifies the settings a durable ledger depends on. WAL needs mmap and
// shared memory support; if any check fails the directory must not be used.
func probe(db *sql.DB) error {
	var mode string
	if err := db.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil {
		return fmt.Errorf("read journal_mode: %w", err)
	}
	if mode != "wal" {
		return fmt.Errorf("journal_mode = %q, want wal", mode)
	}
	var sync int
	if err := db.QueryRow(`PRAGMA synchronous`).Scan(&sync); err != nil {
		return fmt.Errorf("read synchronous: %w", err)
	}
	if sync != 2 { // 2 == FULL
		return fmt.Errorf("synchronous = %d, want 2 (FULL)", sync)
	}
	// A real WAL write exercises shared-memory and mmap paths that a plain
	// read would not touch.
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin probe transaction: %w", err)
	}
	if _, err := tx.Exec(`CREATE TABLE IF NOT EXISTS _qrypt_capability_probe (id INTEGER PRIMARY KEY)`); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("create probe table: %w", err)
	}
	if _, err := tx.Exec(`INSERT INTO _qrypt_capability_probe DEFAULT VALUES`); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("write probe row: %w", err)
	}
	if err := tx.Rollback(); err != nil {
		return fmt.Errorf("rollback probe transaction: %w", err)
	}
	return nil
}

// DB exposes the ledger handle for the spike's transaction tests.
func (s *Store) DB() *sql.DB { return s.db }

// Close releases the ledger and the owner lock.
func (s *Store) Close() error {
	var first error
	if s.db != nil {
		if err := s.db.Close(); err != nil {
			first = err
		}
	}
	if s.lock != nil {
		_ = unlockFile(s.lock)
		if err := s.lock.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}
