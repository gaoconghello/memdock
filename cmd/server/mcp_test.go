package main

import (
	"context"
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

func newMCPTestApp(t *testing.T) *app {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "mcp.db"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	if err := migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	return &app{db: db, writeSecret: []byte(strings.Repeat("k", 32))}
}

func TestSecureMCPProjectCreateAndSaveWithUnifiedSecret(t *testing.T) {
	a := newMCPTestApp(t)
	a.mode = "secure"
	a.writeSecret = []byte(strings.Repeat("s", 32))
	call := func(name, args string) (map[string]json.RawMessage, bool) {
		t.Helper()
		body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"` + name + `","arguments":` + args + `}}`
		req := httptest.NewRequest("POST", "/mcp", strings.NewReader(body))
		req.Header.Set("Accept", "application/json")
		req.Header.Set("X-MemDock-Write-Secret", string(a.writeSecret))
		req.TLS = &tlsState
		rec := httptest.NewRecorder()
		a.authenticate(http.HandlerFunc(a.mcp)).ServeHTTP(rec, req)
		var response struct {
			Result struct {
				Structured map[string]json.RawMessage `json:"structuredContent"`
				IsError    bool                       `json:"isError"`
			} `json:"result"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		return response.Result.Structured, response.Result.IsError
	}
	project, failed := call("project_resolve", `{"name":"raspberry-pi-gateway"}`)
	var id string
	if failed || json.Unmarshal(project["project_id"], &id) != nil || !strings.HasPrefix(id, "prj_") {
		t.Fatalf("create project: %s", project)
	}
	_, failed = call("memory_save", `{"project_id":"`+id+`","title":"test","content":"saved"}`)
	if failed {
		t.Fatal("save failed")
	}
	_, err := a.db.Exec(`INSERT INTO knowledge_bases(id,name,created_at) VALUES('kb_shared','shared','2026-01-01')`)
	if err != nil {
		t.Fatal(err)
	}
	libraries, failed := call("knowledge_base_list", `{}`)
	var items []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if failed || json.Unmarshal(libraries["knowledge_bases"], &items) != nil || len(items) != 1 || items[0].Name != "shared" || items[0].ID != "kb_shared" {
		t.Fatalf("list shared libraries: %v", libraries)
	}
	_, failed = call("memory_save", `{"project_id":"`+id+`","knowledge_base_id":"`+items[0].ID+`","title":"test","content":"saved"}`)
	if failed {
		t.Fatal("shared save failed")
	}
	var sharedCount int
	if err := a.db.QueryRow(`SELECT count(*) FROM memories WHERE knowledge_base_id='kb_shared' AND project_id IS NULL AND title='test'`).Scan(&sharedCount); err != nil || sharedCount != 1 {
		t.Fatalf("shared memory count %d: %v", sharedCount, err)
	}
	var count int
	if err := a.db.QueryRow(`SELECT count(*) FROM memories WHERE title='test' AND project_id=? AND knowledge_base_id IS NULL`, id).Scan(&count); err != nil || count != 1 {
		t.Fatalf("memory count %d: %v", count, err)
	}
	search, failed := call("memory_search", `{"project_id":"`+id+`","query":"saved"}`)
	if failed || !strings.Contains(string(search["results"]), "saved") {
		t.Fatalf("authenticated MCP search failed: %s", search)
	}
	_, failed = call("project_resolve", `{"project_id":"missing","name":"new"}`)
	if !failed {
		t.Fatal("unknown existing project ID must be rejected")
	}
}

func TestMCPOverHTTP(t *testing.T) {
	a := newMCPTestApp(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /mcp", a.mcp)
	server := httptest.NewServer(mux)
	defer server.Close()
	request, err := http.NewRequest(http.MethodPost, server.URL+"/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":"http-init","method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"http-test","version":"1"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || response.Header.Get("MCP-Protocol-Version") != mcpProtocolVersion || !strings.Contains(string(body), `"serverInfo":{"name":"memdock"`) {
		t.Fatalf("HTTP MCP initialize: status=%d headers=%v body=%s", response.StatusCode, response.Header, body)
	}
}

func TestWebIndexAndStatus(t *testing.T) {
	a := newMCPTestApp(t)
	web := httptest.NewRecorder()
	a.webIndex(web, httptest.NewRequest(http.MethodGet, "/", nil))
	if web.Code != 200 || !strings.Contains(web.Body.String(), "MemDock") {
		t.Fatalf("web index: %d", web.Code)
	}
	status := httptest.NewRecorder()
	a.systemStatus(status, httptest.NewRequest(http.MethodGet, "/api/v1/status", nil))
	if status.Code != 200 || !strings.Contains(status.Body.String(), `"database":"ok"`) || !strings.Contains(status.Body.String(), `"mode":"private"`) {
		t.Fatalf("status: %d %s", status.Code, status.Body)
	}
}
