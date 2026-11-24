// Command spike-size is a minimal production-shaped program importing the
// candidate state engine; CI builds it to measure the released binary delta.
package main

import (
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite"
)

func main() {
	db, err := sql.Open("sqlite", "file:ledger.db?_pragma=journal_mode(WAL)&_pragma=synchronous(FULL)")
	if err != nil {
		panic(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS operations (id TEXT PRIMARY KEY, state TEXT NOT NULL)`); err != nil {
		panic(err)
	}
	if _, err := db.Exec(`INSERT OR REPLACE INTO operations VALUES ('smoke', 'applied')`); err != nil {
		panic(err)
	}
	var state string
	if err := db.QueryRow(`SELECT state FROM operations WHERE id = 'smoke'`).Scan(&state); err != nil {
		panic(err)
	}
	fmt.Println(state)
}
