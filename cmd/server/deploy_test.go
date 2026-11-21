package main

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestSecureHealthzIsAccessibleWithoutCredentials(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "healthz.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	a := &app{db: db, mode: "secure"}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", a.health)
	handler := a.authenticate(mux)
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("unauthenticated Secure health check status=%d body=%s", w.Code, w.Body)
	}
}

func TestWALCrashHelper(t *testing.T) {
	if os.Getenv("MEMDOCK_WAL_CRASH_HELPER") != "1" {
		return
	}
	path := os.Getenv("MEMDOCK_WAL_CRASH_DB")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		os.Exit(10)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA journal_mode=WAL`); err != nil {
		os.Exit(11)
	}
	if _, err := db.Exec(`PRAGMA wal_autocheckpoint=0`); err != nil {
		os.Exit(12)
	}
	if err := migrate(context.Background(), db); err != nil {
		os.Exit(13)
	}
	name := os.Getenv("MEMDOCK_WAL_CRASH_NAME")
	if name == "" {
		name = "old committed WAL"
	}
	if _, err := db.Exec(`INSERT INTO projects(id,name,created_at) VALUES('old-wal',?,'now')`, name); err != nil {
		os.Exit(14)
	}
	os.Exit(0) // Intentionally emulate abrupt process termination without SQLite close/checkpoint.
}

func TestReplaceDatabaseFilesFailureRestoresMainAndSidecars(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "db.sqlite")
	files := map[string][]byte{target: []byte("original-main"), target + "-wal": []byte("original-wal"), target + "-shm": []byte("original-shm")}
	for path, content := range files {
		if err := os.WriteFile(path, content, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := replaceDatabaseFiles(filepath.Join(dir, "missing-stage"), target); err == nil {
		t.Fatal("expected staged rename failure")
	}
	for path, expected := range files {
		got, err := os.ReadFile(path)
		if err != nil || string(got) != string(expected) {
			t.Fatalf("rollback %s: got=%q err=%v", path, got, err)
		}
	}
}

func TestRestoreReplacesCrashLeftWALAndPreservesFilesOnInvalidBackup(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.sqlite")
	cmd := exec.Command(os.Args[0], "-test.run=^TestWALCrashHelper$")
	cmd.Env = append(os.Environ(), "MEMDOCK_WAL_CRASH_HELPER=1", "MEMDOCK_WAL_CRASH_DB="+target)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("WAL helper: %v %s", err, output)
	}
	walPath, shmPath := target+"-wal", target+"-shm"
	mainBefore, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	walBefore, err := os.ReadFile(walPath)
	if err != nil || len(walBefore) == 0 {
		t.Fatalf("expected non-empty residual WAL: bytes=%d err=%v", len(walBefore), err)
	}
	shmBefore, err := os.ReadFile(shmPath)
	if err != nil || len(shmBefore) == 0 {
		t.Fatalf("expected residual SHM: bytes=%d err=%v", len(shmBefore), err)
	}
	fake := filepath.Join(dir, "wrong-schema.sqlite")
	db, err := sql.Open("sqlite", fake)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`PRAGMA user_version=3`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if err := restoreDatabase(fake, target); err == nil {
		t.Fatal("SQLite file with user_version only accepted")
	}
	mainAfter, err := os.ReadFile(target)
	if err != nil || string(mainAfter) != string(mainBefore) {
		t.Fatalf("failed restore changed main database: err=%v", err)
	}
	walAfter, err := os.ReadFile(walPath)
	if err != nil || string(walAfter) != string(walBefore) {
		t.Fatalf("failed restore changed WAL: err=%v", err)
	}
	shmAfter, err := os.ReadFile(shmPath)
	if err != nil || string(shmAfter) != string(shmBefore) {
		t.Fatalf("failed restore changed SHM: err=%v", err)
	}
	backup := filepath.Join(dir, "valid.sqlite")
	sourceCmd := exec.Command(os.Args[0], "-test.run=^TestWALCrashHelper$")
	sourceCmd.Env = append(os.Environ(), "MEMDOCK_WAL_CRASH_HELPER=1", "MEMDOCK_WAL_CRASH_DB="+backup, "MEMDOCK_WAL_CRASH_NAME=new restored data")
	if output, err := sourceCmd.CombinedOutput(); err != nil {
		t.Fatalf("WAL source helper: %v %s", err, output)
	}
	if sourceWAL, err := os.Stat(backup + "-wal"); err != nil || sourceWAL.Size() == 0 {
		t.Fatalf("expected source WAL: %v", err)
	}
	if err := restoreDatabase(backup, target); err != nil {
		t.Fatal(err)
	}
	for _, sidecar := range []string{walPath, shmPath} {
		if _, err := os.Stat(sidecar); !os.IsNotExist(err) {
			t.Fatalf("stale sidecar remains at target path %s: %v", sidecar, err)
		}
	}
	check, err := sql.Open("sqlite", target)
	if err != nil {
		t.Fatal(err)
	}
	defer check.Close()
	if err := validateMemDockDatabase(check); err != nil {
		t.Fatalf("restored database validation: %v", err)
	}
	var count int
	if err := check.QueryRow(`SELECT count(*) FROM projects WHERE name='old committed WAL'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("old committed WAL transaction appeared in restored database")
	}
	if err := check.QueryRow(`SELECT count(*) FROM projects WHERE name='new restored data'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatal("restored data missing")
	}
}
