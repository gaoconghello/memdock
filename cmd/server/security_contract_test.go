package main

import (
	"context"
	"crypto/tls"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestSecureUnifiedCredentialProtectsEveryRouteExceptHealth(t *testing.T) {
	t.Setenv("MEMDOCK_TRUST_PROXY_TLS", "false")
	secret := strings.Repeat("s", 32)
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "secure-contract.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	a := &app{db: db, mode: "secure", writeSecret: []byte(secret)}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("GET /", a.webIndex)
	mux.HandleFunc("GET /api/v1/backup", a.backup)
	mux.HandleFunc("GET /private", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("POST /mcp", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("POST /private", func(w http.ResponseWriter, r *http.Request) {
		if !a.verifyWriteSignature(w, r) {
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	handler := a.authenticate(mux)
	call := func(method, path, credential string, tls bool) int {
		r := httptest.NewRequest(method, path, nil)
		if tls {
			r.TLS = &tlsState
		}
		if credential != "" {
			r.Header.Set("X-MemDock-Write-Secret", credential)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w.Code
	}
	if got := call(http.MethodGet, "/healthz", "", false); got != http.StatusOK {
		t.Fatalf("public health status=%d", got)
	}
	if got := call(http.MethodGet, "/", "", true); got != http.StatusUnauthorized {
		t.Fatalf("unauthenticated Console status=%d", got)
	}
	if got := call(http.MethodGet, "/api/v1/backup", "", true); got != http.StatusUnauthorized {
		t.Fatalf("unauthenticated backup status=%d", got)
	}
	if got := call(http.MethodGet, "/", secret, true); got != http.StatusOK {
		t.Fatalf("authenticated Console status=%d", got)
	}
	if got := call(http.MethodGet, "/api/v1/backup", secret, true); got != http.StatusOK {
		t.Fatalf("authenticated backup status=%d", got)
	}
	for _, tc := range []struct {
		method, path, key string
		want              int
	}{
		{http.MethodGet, "/private", "", http.StatusUnauthorized},
		{http.MethodGet, "/private", strings.Repeat("x", 32), http.StatusUnauthorized},
		{http.MethodGet, "/private", secret, http.StatusNoContent},
		{http.MethodPost, "/private", secret, http.StatusNoContent},
	} {
		if got := call(tc.method, tc.path, tc.key, true); got != tc.want {
			t.Errorf("%s %s status=%d want=%d", tc.method, tc.path, got, tc.want)
		}
	}
	if got := call(http.MethodGet, "/private", secret, false); got != http.StatusBadRequest {
		t.Fatalf("HTTP request status=%d, want HTTPS required", got)
	}
	consoleBasic := httptest.NewRequest(http.MethodGet, "/", nil)
	consoleBasic.TLS = &tlsState
	consoleBasic.SetBasicAuth("memdock", secret)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, consoleBasic)
	if w.Code != http.StatusOK {
		t.Fatalf("Basic-auth Console status=%d", w.Code)
	}
	basic := httptest.NewRequest(http.MethodPost, "/private", nil)
	basic.TLS = &tlsState
	basic.SetBasicAuth("memdock", secret)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, basic)
	if w.Code != http.StatusNoContent {
		t.Fatalf("Basic-auth Console/API write status=%d body=%s", w.Code, w.Body)
	}
	crossSite := httptest.NewRequest(http.MethodPost, "/private", nil)
	crossSite.TLS = &tlsState
	crossSite.SetBasicAuth("memdock", secret)
	crossSite.Header.Set("Origin", "https://attacker.example")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, crossSite)
	if w.Code != http.StatusForbidden {
		t.Fatalf("cross-origin Basic-auth write status=%d body=%s", w.Code, w.Body)
	}
	sameSite := httptest.NewRequest(http.MethodPost, "/private", nil)
	sameSite.TLS = &tlsState
	sameSite.SetBasicAuth("memdock", secret)
	sameSite.Header.Set("Origin", "https://example.com")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, sameSite)
	if w.Code != http.StatusNoContent {
		t.Fatalf("same-origin Basic-auth write status=%d body=%s", w.Code, w.Body)
	}
	crossMCP := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	crossMCP.TLS = &tlsState
	crossMCP.Header.Set("X-MemDock-Write-Secret", secret)
	crossMCP.Header.Set("Origin", "https://attacker.example")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, crossMCP)
	if w.Code != http.StatusForbidden {
		t.Fatalf("cross-origin MCP POST status=%d body=%s", w.Code, w.Body)
	}
}

func TestSecureTrustedProxyRequiresTrustedSourceAndHTTPS(t *testing.T) {
	t.Setenv("MEMDOCK_TRUST_PROXY_TLS", "true")
	networks, err := parseTrustedProxyCIDRs("127.0.0.0/8")
	if err != nil {
		t.Fatal(err)
	}
	a := &app{mode: "secure", writeSecret: []byte(strings.Repeat("p", 32)), trustedProxyCIDRs: networks}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /resource", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	handler := a.authenticate(mux)
	request := func(remote, proto string) int {
		r := httptest.NewRequest(http.MethodGet, "/resource", nil)
		r.RemoteAddr = remote
		r.Header.Set("X-Forwarded-Proto", proto)
		r.Header.Set("X-MemDock-Write-Secret", string(a.writeSecret))
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w.Code
	}
	if got := request("127.0.0.1:8123", "https"); got != http.StatusNoContent {
		t.Fatalf("trusted HTTPS proxy status=%d", got)
	}
	if got := request("192.0.2.10:8123", "https"); got != http.StatusForbidden {
		t.Fatalf("untrusted proxy status=%d", got)
	}
	if got := request("127.0.0.1:8123", "http"); got != http.StatusBadRequest {
		t.Fatalf("proxy HTTP status=%d", got)
	}
}

var tlsState = tls.ConnectionState{}
