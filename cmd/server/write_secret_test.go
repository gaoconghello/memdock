package main

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func newWriteSecretTestApp(t *testing.T) *app {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "write-secret.db"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if err := migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	return &app{db: db, writeSecret: []byte(strings.Repeat("s", 32)), mode: "private"}
}

func TestPrivateAPIWriteRequiresUnifiedSecret(t *testing.T) {
	a := newWriteSecretTestApp(t)
	check := func(secret string, want int) {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, "/api/v1/projects/resolve", strings.NewReader(`{"name":"write-key"}`))
		if secret != "" {
			r.Header.Set("X-MemDock-Write-Secret", secret)
		}
		w := httptest.NewRecorder()
		a.resolveProject(w, r)
		if w.Code != want {
			t.Fatalf("secret %q status=%d want=%d body=%s", secret, w.Code, want, w.Body)
		}
	}
	check("", http.StatusUnauthorized)
	check(strings.Repeat("x", 32), http.StatusUnauthorized)
	check(strings.Repeat("s", 32), http.StatusCreated)
	legacy := httptest.NewRequest(http.MethodPost, "/api/v1/projects/resolve", strings.NewReader(`{"name":"legacy"}`))
	legacy.Header.Set("X-MemDock-Write-Signature", "legacy")
	w := httptest.NewRecorder()
	a.resolveProject(w, legacy)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("legacy signature status=%d body=%s", w.Code, w.Body)
	}
}
