package qryptstorage

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
)

func BenchmarkOpenCommit(b *testing.B) {
	path := filepath.Join(b.TempDir(), "ledger.db")
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(FULL)")
	if err != nil {
		b.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE operations (id TEXT PRIMARY KEY, state TEXT NOT NULL)`); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tx, err := db.Begin()
		if err != nil {
			b.Fatal(err)
		}
		if _, err := tx.Exec(`INSERT INTO operations VALUES (?, 'applied')`, fmt.Sprintf("op-%d", i)); err != nil {
			b.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkOpenReopen(b *testing.B) {
	path := filepath.Join(b.TempDir(), "ledger.db")
	dsn := "file:" + filepath.ToSlash(path) + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(FULL)"
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		db, err := sql.Open("sqlite", dsn)
		if err != nil {
			b.Fatal(err)
		}
		var n int
		if err := db.QueryRow(`SELECT count(*) FROM sqlite_master`).Scan(&n); err != nil {
			b.Fatal(err)
		}
		_ = db.Close()
	}
}
