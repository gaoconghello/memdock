package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"database/sql"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestApprovalPromptEscapesTerminalControlsAndBidi(t *testing.T) {
	got := safeTerminalText("line\n\u2028\x1b[31mred\x1b[0m\u202e")
	if strings.ContainsRune(got, '\n') || strings.ContainsRune(got, '\x1b') || strings.ContainsRune(got, '\u202e') {
		t.Fatalf("unsafe prompt text: %q", got)
	}
	if !strings.Contains(got, `\u{000A}`) || !strings.Contains(got, `\u{001B}`) || !strings.Contains(got, `\u{202E}`) || !strings.Contains(got, `\u{2028}`) {
		t.Fatalf("escaped prompt text: %q", got)
	}
}

func testSecureGrants(readToken, writeToken string) map[string]accessGrant {
	return map[string]accessGrant{
		tokenDigest(readToken):  {Role: "read", Projects: []string{"p"}, SharedRead: true},
		tokenDigest(writeToken): {Role: "write", Projects: []string{"p"}, SharedRead: true, SharedWrite: true},
	}
}

func TestSecureMiddlewareEnforcesTLSAndReadWriteRoles(t *testing.T) {
	t.Setenv("MEMDOCK_TRUST_PROXY_TLS", "true")
	cidrs, err := parseTrustedProxyCIDRs("10.10.0.0/24")
	if err != nil {
		t.Fatal(err)
	}
	readToken, writeToken := strings.Repeat("r", 32), strings.Repeat("w", 32)
	a := &app{mode: "secure", projectGrants: testSecureGrants(readToken, writeToken), trustedProxyCIDRs: cidrs}
	h := a.authenticate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	cases := []struct {
		name, method, path, proto, remote, token, body string
		want                                           int
	}{
		{"plain HTTP", "GET", "/api/v1/projects", "http", "10.10.0.2:443", strings.Repeat("r", 32), "", 400},
		{"spoofed proxy header from direct peer", "GET", "/api/v1/projects", "https", "192.0.2.4:8080", strings.Repeat("r", 32), "", 403},
		{"missing credentials", "GET", "/api/v1/projects", "https", "10.10.0.2:443", "", "", 401},
		{"read token can read", "GET", "/api/v1/projects", "https", "10.10.0.2:443", strings.Repeat("r", 32), "", 204},
		{"read token cannot write", "POST", "/api/v1/memories", "https", "10.10.0.2:443", strings.Repeat("r", 32), `{}`, 403},
		{"write token may write", "POST", "/api/v1/memories", "https", "10.10.0.2:443", strings.Repeat("w", 32), `{}`, 204},
		{"read token may search", "POST", "/api/v1/memories/search", "https", "10.10.0.2:443", strings.Repeat("r", 32), `{}`, 204},
		{"read token may submit MCP approval proposal", "POST", "/mcp", "https", "10.10.0.2:443", strings.Repeat("r", 32), `{"jsonrpc":"2.0","method":"tools/call","params":{"name":"memory_save"}}`, 204},
		{"read token may call read MCP tool", "POST", "/mcp", "https", "10.10.0.2:443", strings.Repeat("r", 32), `{"jsonrpc":"2.0","method":"tools/call","params":{"name":"memory_search"}}`, 204},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			r.RemoteAddr = tc.remote
			r.Header.Set("X-Forwarded-Proto", tc.proto)
			if tc.token != "" {
				r.Header.Set("Authorization", "Bearer "+tc.token)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Fatalf("status=%d want=%d: %s", w.Code, tc.want, w.Body)
			}
		})
	}
	t.Run("forwarded header cannot replace direct TLS", func(t *testing.T) {
		t.Setenv("MEMDOCK_TRUST_PROXY_TLS", "false")
		r := httptest.NewRequest("GET", "/api/v1/projects", nil)
		r.Header.Set("X-Forwarded-Proto", "https")
		r.Header.Set("Authorization", "Bearer "+strings.Repeat("r", 32))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 400 {
			t.Fatalf("status=%d want 400", w.Code)
		}
	})
}

func TestSecureHealthzIsAccessibleWithoutCredentials(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "healthz.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	a := &app{db: db, mode: "secure"}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", a.health)
	handler := a.authenticate(a.authorizeProjectAccess(mux))
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("unauthenticated Secure health check status=%d body=%s", w.Code, w.Body)
	}
}

func TestSecureEd25519WriteApprovalIsOneTimeAndBodyBound(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "approval.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err := migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	a := &app{db: db, mode: "secure", approvalPublicKey: pub}
	body := `{"title":"approval"}`
	timestamp, nonce := "1700000000", strings.Repeat("a", 32)
	// Use current timestamp, and include the operation and exact bytes in the approval.
	timestamp = strconv.FormatInt(time.Now().Unix(), 10)
	message := timestamp + "\n" + nonce + "\nPOST\n/api/v1/memories\n" + body
	sig := hex.EncodeToString(ed25519.Sign(priv, []byte(message)))
	header := "approval:" + timestamp + ":" + nonce + ":" + sig
	verify := func(raw, signature string) int {
		r := httptest.NewRequest("POST", "/api/v1/memories", strings.NewReader(raw))
		r.Header.Set("X-MemDock-Write-Signature", signature)
		w := httptest.NewRecorder()
		if a.verifyWriteSignature(w, r) {
			return 200
		}
		return w.Code
	}
	if code := verify(body, header); code != 200 {
		t.Fatalf("valid approval status=%d", code)
	}
	if code := verify(body, header); code != 401 {
		t.Fatalf("replay status=%d", code)
	}
	if code := verify(`{"title":"tampered"}`, "approval:"+timestamp+":"+strings.Repeat("b", 32)+":"+sig); code != 401 {
		t.Fatalf("tampered request status=%d", code)
	}
}

func TestSecureWriteRequiresWriteCredentialAndIndependentApproval(t *testing.T) {
	t.Setenv("MEMDOCK_TRUST_PROXY_TLS", "true")
	networks, err := parseTrustedProxyCIDRs("10.20.0.0/24")
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "secure-write.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err := migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO projects(id,name,created_at) VALUES('p','secure','now')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO knowledge_bases(id,project_id,name,kind,is_default,created_at) VALUES('kb-p','p','project','project',1,'now'),('kb-shared',NULL,'shared','shared',0,'now')`); err != nil {
		t.Fatal(err)
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	readToken, writeToken := strings.Repeat("r", 32), strings.Repeat("w", 32)
	a := &app{db: db, mode: "secure", projectGrants: testSecureGrants(readToken, writeToken), approvalPublicKey: pub, trustedProxyCIDRs: networks}
	h := a.authenticate(http.HandlerFunc(a.createMemory))
	body := `{"project_id":"p","knowledge_base_id":"kb-shared","title":"approved shared","content":"explicitly approved"}`
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	nonce := strings.Repeat("c", 32)
	message := timestamp + "\n" + nonce + "\nPOST\n/api/v1/memories\n" + body
	header := "approval:" + timestamp + ":" + nonce + ":" + hex.EncodeToString(ed25519.Sign(priv, []byte(message)))
	call := func(token, sig, raw string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/api/v1/memories", strings.NewReader(raw))
		r.RemoteAddr = "10.20.0.2:80"
		r.Header.Set("X-Forwarded-Proto", "https")
		r.Header.Set("Authorization", "Bearer "+token)
		if sig != "" {
			r.Header.Set("X-MemDock-Write-Signature", sig)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if got := call(strings.Repeat("r", 32), header, body); got.Code != 403 {
		t.Fatalf("read token write status=%d body=%s", got.Code, got.Body)
	}
	if got := call(strings.Repeat("w", 32), "", body); got.Code != 401 {
		t.Fatalf("missing approval status=%d body=%s", got.Code, got.Body)
	}
	if got := call(strings.Repeat("w", 32), header, body); got.Code != 201 {
		t.Fatalf("approved shared write status=%d body=%s", got.Code, got.Body)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM memories m JOIN knowledge_bases k ON k.id=m.knowledge_base_id WHERE k.kind='shared'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("shared memory count=%d err=%v", count, err)
	}
}

func TestSecureApprovedMemoryMoveThroughHTTPAuthorization(t *testing.T) {
	t.Setenv("MEMDOCK_TRUST_PROXY_TLS", "true")
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "secure-memory-move.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err := migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO projects(id,name,created_at) VALUES('p1','source','now'),('p2','target','now')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO knowledge_bases(id,project_id,name,kind,is_default,created_at) VALUES('kb-p1','p1','source','project',1,'now'),('kb-p2','p2','target','project',1,'now')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO memories(id,knowledge_base_id,title,content,created_at,updated_at) VALUES('move-secure','kb-p1','secure title','secure body','now','now')`); err != nil {
		t.Fatal(err)
	}
	pub, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	writeToken := strings.Repeat("m", 32)
	networks, err := parseTrustedProxyCIDRs("10.30.0.0/24")
	if err != nil {
		t.Fatal(err)
	}
	a := &app{db: db, mode: "secure", approvalPublicKey: pub, trustedProxyCIDRs: networks, projectGrants: map[string]accessGrant{
		tokenDigest(writeToken): {Role: "write", Projects: []string{"p1", "p2"}},
	}}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/memories/{id}/move", a.moveMemory)
	handler := a.authenticate(a.authorizeProjectAccess(mux))
	body := `{"project_id":"p1","target_project_id":"p2","knowledge_base_id":"kb-p2"}`
	timestamp, nonce := strconv.FormatInt(time.Now().Unix(), 10), strings.Repeat("d", 32)
	message := timestamp + "\n" + nonce + "\nPOST\n/api/v1/memories/move-secure/move\n" + body
	signature := "approval:" + timestamp + ":" + nonce + ":" + hex.EncodeToString(ed25519.Sign(privateKey, []byte(message)))
	r := httptest.NewRequest(http.MethodPost, "/api/v1/memories/move-secure/move", strings.NewReader(body))
	r.RemoteAddr = "10.30.0.5:443"
	r.Header.Set("X-Forwarded-Proto", "https")
	r.Header.Set("Authorization", "Bearer "+writeToken)
	r.Header.Set("X-MemDock-Write-Signature", signature)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("approved move status=%d body=%s", w.Code, w.Body)
	}
	var kb string
	if err := db.QueryRow(`SELECT knowledge_base_id FROM memories WHERE id='move-secure'`).Scan(&kb); err != nil || kb != "kb-p2" {
		t.Fatalf("approved move knowledge_base_id=%q err=%v", kb, err)
	}
}

func TestSecureProjectResolveRequiresIndependentApproval(t *testing.T) {
	t.Setenv("MEMDOCK_TRUST_PROXY_TLS", "false")
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "resolve.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err := migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	readToken, writeToken := strings.Repeat("r", 32), strings.Repeat("w", 32)
	a := &app{db: db, mode: "secure", projectGrants: testSecureGrants(readToken, writeToken), approvalPublicKey: pub}
	h := a.authenticate(http.HandlerFunc(a.resolveProject))
	body := `{"name":"resolve-must-be-approved"}`
	call := func(token, signature string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/api/v1/projects/resolve", strings.NewReader(body))
		r.TLS = &tls.ConnectionState{}
		r.Header.Set("Authorization", "Bearer "+token)
		if signature != "" {
			r.Header.Set("X-MemDock-Write-Signature", signature)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if got := call(strings.Repeat("r", 32), ""); got.Code != 403 {
		t.Fatalf("read-token resolve status=%d", got.Code)
	}
	if got := call(strings.Repeat("w", 32), ""); got.Code != 401 {
		t.Fatalf("unsigned write-token resolve status=%d body=%s", got.Code, got.Body)
	}
	var projects int
	if err := db.QueryRow(`SELECT count(*) FROM projects`).Scan(&projects); err != nil || projects != 0 {
		t.Fatalf("unsigned resolve changed projects=%d err=%v", projects, err)
	}
	timestamp, nonce := strconv.FormatInt(time.Now().Unix(), 10), strings.Repeat("d", 32)
	message := timestamp + "\n" + nonce + "\nPOST\n/api/v1/projects/resolve\n" + body
	signature := "approval:" + timestamp + ":" + nonce + ":" + hex.EncodeToString(ed25519.Sign(priv, []byte(message)))
	if got := call(strings.Repeat("w", 32), signature); got.Code != 201 {
		t.Fatalf("approved resolve status=%d body=%s", got.Code, got.Body)
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
