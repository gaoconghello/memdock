package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestPrivateAdminSessionLifecycleAndCSRF(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "admin.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err := migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	a := &app{db: db, mode: "private", approvalKey: []byte(strings.Repeat("k", 32))}
	oldPassword, newPassword := "initial-admin-password", "new-admin-password-2026"
	if err := a.initializeAdmin(context.Background(), oldPassword); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/admin/status", a.adminStatus)
	mux.HandleFunc("POST /api/v1/admin/login", a.adminLogin)
	mux.HandleFunc("POST /api/v1/admin/logout", a.adminLogout)
	mux.HandleFunc("POST /api/v1/admin/password", a.adminChangePassword)
	mux.HandleFunc("POST /write", func(w http.ResponseWriter, r *http.Request) {
		if !a.verifyWriteSignature(w, r) {
			return
		}
		w.WriteHeader(204)
	})
	handler := a.adminSessionMiddleware(mux)
	request := func(method, path, body string, cookie *http.Cookie, csrf string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, "http://memdock.test"+path, strings.NewReader(body))
		r.Header.Set("Origin", "http://memdock.test")
		if body != "" {
			r.Header.Set("Content-Type", "application/json")
		}
		if cookie != nil {
			r.AddCookie(cookie)
		}
		if csrf != "" {
			r.Header.Set("X-MemDock-CSRF", csrf)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	bad := request("POST", "/api/v1/admin/login", `{"password":"wrong"}`, nil, "")
	if bad.Code != 401 {
		t.Fatalf("wrong password status=%d body=%s", bad.Code, bad.Body)
	}
	login := request("POST", "/api/v1/admin/login", `{"password":"`+oldPassword+`"}`, nil, "")
	if login.Code != 200 {
		t.Fatalf("login status=%d body=%s", login.Code, login.Body)
	}
	var sessionResponse struct {
		CSRF string `json:"csrf_token"`
	}
	if err := json.Unmarshal(login.Body.Bytes(), &sessionResponse); err != nil {
		t.Fatal(err)
	}
	var cookie *http.Cookie
	for _, candidate := range login.Result().Cookies() {
		if candidate.Name == adminSessionCookie {
			cookie = candidate
		}
	}
	if cookie == nil || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode {
		t.Fatalf("unsafe session cookie: %#v", cookie)
	}
	if sessionResponse.CSRF == "" {
		t.Fatal("login did not return a CSRF token")
	}
	noCSRF := request("POST", "/write", `{}`, cookie, "")
	if noCSRF.Code != 403 {
		t.Fatalf("write without CSRF status=%d", noCSRF.Code)
	}
	crossOrigin := httptest.NewRequest("POST", "http://memdock.test/write", strings.NewReader(`{}`))
	crossOrigin.AddCookie(cookie)
	crossOrigin.Header.Set("X-MemDock-CSRF", sessionResponse.CSRF)
	crossOrigin.Header.Set("Origin", "http://attacker.test")
	crossResponse := httptest.NewRecorder()
	handler.ServeHTTP(crossResponse, crossOrigin)
	if crossResponse.Code != 403 {
		t.Fatalf("cross-origin write status=%d", crossResponse.Code)
	}
	write := request("POST", "/write", `{}`, cookie, sessionResponse.CSRF)
	if write.Code != 204 {
		t.Fatalf("session write status=%d body=%s", write.Code, write.Body)
	}
	unauthenticated := request("POST", "/write", `{}`, nil, "")
	if unauthenticated.Code != 401 {
		t.Fatalf("write without session/signature status=%d", unauthenticated.Code)
	}
	changed := request("POST", "/api/v1/admin/password", `{"old_password":"`+oldPassword+`","new_password":"`+newPassword+`"}`, cookie, sessionResponse.CSRF)
	if changed.Code != 200 {
		t.Fatalf("change password status=%d body=%s", changed.Code, changed.Body)
	}
	status := request("GET", "/api/v1/admin/status", "", cookie, "")
	var statusResponse map[string]any
	_ = json.Unmarshal(status.Body.Bytes(), &statusResponse)
	if statusResponse["authenticated"] != false {
		t.Fatalf("old session remained active: %s", status.Body)
	}
	oldLogin := request("POST", "/api/v1/admin/login", `{"password":"`+oldPassword+`"}`, nil, "")
	if oldLogin.Code != 401 {
		t.Fatalf("old password status=%d", oldLogin.Code)
	}
	newLogin := request("POST", "/api/v1/admin/login", `{"password":"`+newPassword+`"}`, nil, "")
	if newLogin.Code != 200 {
		t.Fatalf("new password login status=%d body=%s", newLogin.Code, newLogin.Body)
	}
	for _, candidate := range newLogin.Result().Cookies() {
		if candidate.Name == adminSessionCookie {
			cookie = candidate
		}
	}
	var renewed struct {
		CSRF string `json:"csrf_token"`
	}
	_ = json.Unmarshal(newLogin.Body.Bytes(), &renewed)
	logout := request("POST", "/api/v1/admin/logout", `{}`, cookie, renewed.CSRF)
	if logout.Code != 200 {
		t.Fatalf("logout status=%d body=%s", logout.Code, logout.Body)
	}
	status = request("GET", "/api/v1/admin/status", "", cookie, "")
	_ = json.Unmarshal(status.Body.Bytes(), &statusResponse)
	if statusResponse["authenticated"] != false {
		t.Fatalf("logout did not invalidate session: %s", status.Body)
	}
	expiredToken := "expired-session-token"
	if _, err := db.Exec(`INSERT INTO admin_sessions(token_hash,csrf_hash,csrf_token,expires_at) VALUES(?,?,?,0)`, tokenDigest(expiredToken), strings.Repeat("0", 64), "expired-csrf"); err != nil {
		t.Fatal(err)
	}
	expiredCookie := &http.Cookie{Name: adminSessionCookie, Value: expiredToken}
	status = request("GET", "/api/v1/admin/status", "", expiredCookie, "")
	_ = json.Unmarshal(status.Body.Bytes(), &statusResponse)
	if statusResponse["authenticated"] != false {
		t.Fatalf("expired session remained active: %s", status.Body)
	}
}

func TestAdminAuthOperationsSerializeWithPasswordRotation(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "admin-race.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err := migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	a := &app{db: db, mode: "private"}
	oldPassword, rotatedPassword := "initial-admin-password", "rotated-admin-password-2026"
	if err := a.initializeAdmin(context.Background(), oldPassword); err != nil {
		t.Fatal(err)
	}

	// Hold the SQLite writer transaction as a password rotation would, then queue a
	// login. It must read the committed current hash after the rotation releases it.
	rotatedHash, err := passwordHash(rotatedPassword)
	if err != nil {
		t.Fatal(err)
	}
	rotation, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rotation.Exec(`UPDATE admin_credentials SET password_hash=? WHERE id=1`, rotatedHash); err != nil {
		t.Fatal(err)
	}
	beforeWait := db.Stats().WaitCount
	loginDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		r := httptest.NewRequest(http.MethodPost, "http://memdock.test/api/v1/admin/login", strings.NewReader(`{"password":"`+oldPassword+`"}`))
		r.Header.Set("Origin", "http://memdock.test")
		w := httptest.NewRecorder()
		a.adminSessionMiddleware(http.HandlerFunc(a.adminLogin)).ServeHTTP(w, r)
		loginDone <- w
	}()
	waitForDBWaitCount(t, db, beforeWait+1)
	if err := rotation.Commit(); err != nil {
		t.Fatal(err)
	}
	login := <-loginDone
	if login.Code != http.StatusUnauthorized {
		t.Fatalf("stale password login status=%d body=%s", login.Code, login.Body)
	}
	var sessions int
	if err := db.QueryRow(`SELECT count(*) FROM admin_sessions`).Scan(&sessions); err != nil || sessions != 0 {
		t.Fatalf("stale login created sessions=%d err=%v", sessions, err)
	}

	// Model a password-change request whose session was validated before another
	// rotation committed. The handler must re-check the old password under the lock.
	concurrentPassword := "already-rotated-password-2026"
	concurrentHash, err := passwordHash(concurrentPassword)
	if err != nil {
		t.Fatal(err)
	}
	rotation, err = db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rotation.Exec(`UPDATE admin_credentials SET password_hash=? WHERE id=1`, concurrentHash); err != nil {
		t.Fatal(err)
	}
	beforeWait = db.Stats().WaitCount
	changeDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		body := `{"old_password":"` + oldPassword + `","new_password":"attempted-overwrite-password-2026"}`
		r := httptest.NewRequest(http.MethodPost, "http://memdock.test/api/v1/admin/password", strings.NewReader(body))
		r = r.WithContext(context.WithValue(r.Context(), adminSessionContextKey{}, adminSession{tokenHash: "previously-validated-session"}))
		w := httptest.NewRecorder()
		a.adminChangePassword(w, r)
		changeDone <- w
	}()
	waitForDBWaitCount(t, db, beforeWait+1)
	if err := rotation.Commit(); err != nil {
		t.Fatal(err)
	}
	change := <-changeDone
	if change.Code != http.StatusUnauthorized {
		t.Fatalf("stale password change status=%d body=%s", change.Code, change.Body)
	}
	var finalHash string
	if err := db.QueryRow(`SELECT password_hash FROM admin_credentials WHERE id=1`).Scan(&finalHash); err != nil {
		t.Fatal(err)
	}
	if !verifyPassword(concurrentPassword, finalHash) || verifyPassword("attempted-overwrite-password-2026", finalHash) {
		t.Fatal("stale password change overwrote the committed password")
	}
}

func waitForDBWaitCount(t *testing.T, db *sql.DB, target int64) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for db.Stats().WaitCount < target {
		if time.Now().After(deadline) {
			t.Fatalf("database waiter did not reach count %d", target)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestSecureModeDoesNotAcceptPrivateAdminSession(t *testing.T) {
	publicKey, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	a := &app{mode: "secure", approvalPublicKey: publicKey}
	r := httptest.NewRequest(http.MethodPost, "/api/v1/memories", strings.NewReader(`{}`))
	r = r.WithContext(context.WithValue(r.Context(), adminSessionContextKey{}, adminSession{tokenHash: "session"}))
	w := httptest.NewRecorder()
	if a.verifyWriteSignature(w, r) {
		t.Fatal("Secure accepted a Private admin session as write approval")
	}
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d body=%s", w.Code, w.Body)
	}
}

func TestPrivateAdminCookiesAndOriginRequireDirectHTTPS(t *testing.T) {
	httpRequest := httptest.NewRequest(http.MethodGet, "http://memdock.test/", nil)
	httpRequest.Header.Set("X-Forwarded-Proto", "https")
	httpRequest.Header.Set("Origin", "https://memdock.test")
	if sameOrigin(httpRequest) {
		t.Fatal("Private mode accepted a TLS-terminating proxy as direct HTTPS")
	}
	w := httptest.NewRecorder()
	setAdminCookie(w, httpRequest, "plain", time.Now().Add(time.Hour))
	if w.Result().Cookies()[0].Secure {
		t.Fatal("HTTP response unexpectedly set Secure cookie")
	}

	httpsRequest := httptest.NewRequest(http.MethodGet, "https://memdock.test/", nil)
	httpsRequest.Header.Set("Origin", "https://memdock.test")
	if !sameOrigin(httpsRequest) {
		t.Fatal("direct HTTPS origin was rejected")
	}
	w = httptest.NewRecorder()
	setAdminCookie(w, httpsRequest, "secure", time.Now().Add(time.Hour))
	if !w.Result().Cookies()[0].Secure {
		t.Fatal("direct HTTPS response did not set Secure cookie")
	}
}

func TestAdminInitialPasswordIsNotOverwritten(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "admin-init.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	a := &app{db: db}
	if err := a.initializeAdmin(context.Background(), "first-password-123"); err != nil {
		t.Fatal(err)
	}
	if err := a.initializeAdmin(context.Background(), "replacement-password-456"); err != nil {
		t.Fatal(err)
	}
	var hash string
	if err := db.QueryRow(`SELECT password_hash FROM admin_credentials WHERE id=1`).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	if !verifyPassword("first-password-123", hash) || verifyPassword("replacement-password-456", hash) {
		t.Fatal("initial password was unexpectedly replaced")
	}
}
