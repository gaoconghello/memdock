package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"io"
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
	t.Cleanup(func() { db.Close() })
	if err := migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	return &app{db: db, approvalKey: []byte(strings.Repeat("h", 32)), writeSecret: []byte(strings.Repeat("s", 32))}
}

func TestPrivateStaticWriteSecretAcceptsKeyHeader(t *testing.T) {
	a := newWriteSecretTestApp(t)
	body := `{"title":"static key"}`
	verify := func(raw, writeKey string) (bool, int, string) {
		r := httptest.NewRequest(http.MethodPost, "/api/v1/memories", strings.NewReader(raw))
		if writeKey != "" {
			r.Header.Set("X-MemDock-Write-Key", writeKey)
		}
		w := httptest.NewRecorder()
		ok := a.verifyWriteSignature(w, r)
		rest, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("body unreadable after verify: %v", err)
		}
		return ok, w.Code, string(rest)
	}
	ok, code, rest := verify(body, string(a.writeSecret))
	if !ok || code != 200 || rest != body {
		t.Fatalf("valid static key: ok=%v code=%d body=%q", ok, code, rest)
	}
	if ok, code, _ = verify(body, strings.Repeat("x", 32)); ok || code != 401 {
		t.Fatalf("wrong static key: ok=%v code=%d", ok, code)
	}
	if ok, code, _ = verify(body, ""); ok || code != 401 {
		t.Fatalf("missing static key: ok=%v code=%d", ok, code)
	}
	// The per-request HMAC path keeps working alongside the static key.
	hmacReq := httptest.NewRequest(http.MethodPost, "/api/v1/memories", strings.NewReader(body))
	signApproval(a, hmacReq)
	w := httptest.NewRecorder()
	if !a.verifyWriteSignature(w, hmacReq) {
		t.Fatalf("HMAC path rejected after static key enabled: %s", w.Body)
	}
}

func TestSecureModeIgnoresStaticWriteSecret(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "write-secret-secure.db"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	if err := migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	a := &app{db: db, approvalKey: []byte(strings.Repeat("h", 32)), writeSecret: []byte(strings.Repeat("s", 32)), approvalPublicKey: pub, mode: "secure"}
	r := httptest.NewRequest(http.MethodPost, "/api/v1/memories", strings.NewReader(`{"title":"x"}`))
	r.Header.Set("X-MemDock-Write-Key", string(a.writeSecret))
	w := httptest.NewRecorder()
	if a.verifyWriteSignature(w, r) {
		t.Fatalf("static key must not authorize Secure writes")
	}
	if w.Code != 401 {
		t.Fatalf("Secure rejection status=%d body=%s", w.Code, w.Body)
	}
}

func TestMCPStaticWriteKeyHeaderEnablesSave(t *testing.T) {
	a := newWriteSecretTestApp(t)
	projectRec := httptest.NewRecorder()
	projectRequest := httptest.NewRequest(http.MethodPost, "/api/v1/projects/resolve", strings.NewReader(`{"name":"write-secret"}`))
	signApproval(a, projectRequest)
	a.resolveProject(projectRec, projectRequest)
	var project struct {
		ID string `json:"project_id"`
	}
	if err := json.Unmarshal(projectRec.Body.Bytes(), &project); err != nil || project.ID == "" {
		t.Fatalf("project setup: %s %v", projectRec.Body, err)
	}
	call := func(args string, withHeader bool) *httptest.ResponseRecorder {
		body := `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"memory_save","arguments":` + args + `}}`
		req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
		req.Header.Set("Accept", "application/json")
		if withHeader {
			req.Header.Set("X-MemDock-Write-Key", string(a.writeSecret))
		}
		rec := httptest.NewRecorder()
		a.mcp(rec, req)
		return rec
	}
	args := `{"project_id":"` + project.ID + `","title":"via header","content":"saved with static key"}`
	denied := call(args, false)
	if !strings.Contains(denied.Body.String(), `"isError":true`) || !strings.Contains(denied.Body.String(), "one-time request signature required") {
		t.Fatalf("write without key header was not denied: %s", denied.Body)
	}
	accepted := call(args, true)
	if strings.Contains(accepted.Body.String(), `"isError":true`) || !strings.Contains(accepted.Body.String(), `"id":"mem_`) {
		t.Fatalf("write with key header failed: %s", accepted.Body)
	}
	var count int
	if err := a.db.QueryRow(`SELECT count(*) FROM memories WHERE title='via header'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("memory not persisted via static key header: count=%d err=%v", count, err)
	}
}
