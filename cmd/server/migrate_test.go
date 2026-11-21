package main

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestMigrateV1ToV3AddsManagementTables(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "legacy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`PRAGMA user_version=1`); err != nil {
		t.Fatal(err)
	}
	if err := migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 3 {
		t.Fatalf("schema version = %d, want 3", version)
	}
	var table string
	for _, expected := range []string{"used_write_authorizations", "memory_tags", "memory_revisions", "project_operations"} {
		if err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, expected).Scan(&table); err != nil {
			t.Fatalf("missing table %s: %v", expected, err)
		}
	}
}
