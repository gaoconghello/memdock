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
	return &app{db: db, approvalKey: []byte(strings.Repeat("k", 32))}
}

func TestMCPStreamableHTTPToolsAndRead(t *testing.T) {
	a := newMCPTestApp(t)
	initReq := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`))
	initReq.Header.Set("Accept", "application/json, text/event-stream")
	initRec := httptest.NewRecorder()
	a.mcp(initRec, initReq)
	if initRec.Code != 200 || !strings.Contains(initRec.Body.String(), `"protocolVersion":"2025-03-26"`) {
		t.Fatalf("initialize: %d %s", initRec.Code, initRec.Body)
	}
	listReq := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`))
	listReq.Header.Set("Accept", "application/json")
	listRec := httptest.NewRecorder()
	a.mcp(listRec, listReq)
	for _, tool := range []string{`"name":"memory_search"`, `"name":"memory_save"`, `"name":"memory_list"`, `"name":"memory_update"`, `"name":"memory_delete"`, `"name":"project_info"`} {
		if !strings.Contains(listRec.Body.String(), tool) {
			t.Fatalf("missing %s from tools/list: %s", tool, listRec.Body)
		}
	}
	projectRec := httptest.NewRecorder()
	projectRequest := httptest.NewRequest(http.MethodPost, "/api/v1/projects/resolve", strings.NewReader(`{"name":"mcp-test"}`))
	signApproval(a, projectRequest)
	a.resolveProject(projectRec, projectRequest)
	var project struct {
		ID string `json:"project_id"`
	}
	if err := json.Unmarshal(projectRec.Body.Bytes(), &project); err != nil || project.ID == "" {
		t.Fatalf("project setup: %s %v", projectRec.Body, err)
	}
	call := func(name, args string) *httptest.ResponseRecorder {
		body := `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"` + name + `","arguments":` + args + `}}`
		req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
		req.Header.Set("Accept", "application/json")
		rec := httptest.NewRecorder()
		a.mcp(rec, req)
		return rec
	}
	info := call("project_info", `{"project_id":"`+project.ID+`"}`)
	if info.Code != 200 || !strings.Contains(info.Body.String(), project.ID) {
		t.Fatalf("project_info: %d %s", info.Code, info.Body)
	}
	write := call("memory_save", `{"project_id":"`+project.ID+`","title":"no auth","content":"must not write"}`)
	if !strings.Contains(write.Body.String(), `"isError":true`) || !strings.Contains(write.Body.String(), "one-time request signature required") {
		t.Fatalf("unsigned MCP write was not denied: %s", write.Body)
	}
	var count int
	if err := a.db.QueryRow(`SELECT count(*) FROM memories`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("unsigned tool call persisted memory: count=%d err=%v", count, err)
	}
	invalidBody := `{"content":"invalid signature","project_id":"` + project.ID + `","title":"must not persist"}`
	invalidRequest := httptest.NewRequest(http.MethodPost, "/api/v1/memories", strings.NewReader(invalidBody))
	signApproval(a, invalidRequest)
	parts := strings.Split(invalidRequest.Header.Get("X-MemDock-Write-Signature"), ":")
	parts[2] = strings.Repeat("0", 64)
	invalidSignature := strings.Join(parts, ":")
	invalidCall := call("memory_save", `{"project_id":"`+project.ID+`","title":"must not persist","content":"invalid signature","write_signature":"`+invalidSignature+`"}`)
	if !strings.Contains(invalidCall.Body.String(), `"isError":true`) || !strings.Contains(invalidCall.Body.String(), "invalid request signature") {
		t.Fatalf("invalid MCP signature was not rejected: %s", invalidCall.Body)
	}
	if err := a.db.QueryRow(`SELECT count(*) FROM memories`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("invalid signature persisted memory: count=%d err=%v", count, err)
	}
	// The MCP adapter passes the standard one-time signature to the existing REST handler.
	signedBody := `{"content":"mcp signed","project_id":"` + project.ID + `","title":"signed"}`
	signedRequest := httptest.NewRequest(http.MethodPost, "/api/v1/memories", strings.NewReader(signedBody))
	signApproval(a, signedRequest)
	sig := signedRequest.Header.Get("X-MemDock-Write-Signature")
	signedCall := call("memory_save", `{"project_id":"`+project.ID+`","title":"signed","content":"mcp signed","write_signature":"`+sig+`"}`)
	if strings.Contains(signedCall.Body.String(), `"isError":true`) || !strings.Contains(signedCall.Body.String(), `"id":"mem_`) {
		t.Fatalf("signed MCP save failed: %s", signedCall.Body)
	}
	var savedID string
	if err := a.db.QueryRow(`SELECT id FROM memories WHERE title='signed'`).Scan(&savedID); err != nil {
		t.Fatal(err)
	}
	deleteBody := `{"project_id":"` + project.ID + `"}`
	deleteRequest := httptest.NewRequest(http.MethodDelete, "/api/v1/memories/"+savedID, strings.NewReader(deleteBody))
	signApproval(a, deleteRequest)
	deleteSignature := deleteRequest.Header.Get("X-MemDock-Write-Signature")
	for _, args := range []string{
		`{"id":"` + savedID + `","project_id":"` + project.ID + `","write_signature":"` + deleteSignature + `"}`,
		`{"id":"` + savedID + `","project_id":"` + project.ID + `","action":"purge","write_signature":"` + deleteSignature + `"}`,
	} {
		invalidDelete := call("memory_delete", args)
		if !strings.Contains(invalidDelete.Body.String(), `"isError":true`) || !strings.Contains(invalidDelete.Body.String(), "invalid arguments") {
			t.Fatalf("invalid/missing delete action was not rejected before signed mutation: %s", invalidDelete.Body)
		}
	}
	var memoryStatus string
	if err := a.db.QueryRow(`SELECT status FROM memories WHERE id=?`, savedID).Scan(&memoryStatus); err != nil || memoryStatus != "active" {
		t.Fatalf("invalid MCP delete action changed memory status=%q err=%v", memoryStatus, err)
	}
	var kbID string
	if err := a.db.QueryRow(`SELECT id FROM knowledge_bases WHERE project_id=? AND is_default=1`, project.ID).Scan(&kbID); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.Exec(`INSERT INTO memories(id,knowledge_base_id,title,content,created_at,updated_at) VALUES('source-check',?,'visible','library source','now','now')`, kbID); err != nil {
		t.Fatal(err)
	}
	listed := call("memory_list", `{"project_id":"`+project.ID+`"}`)
	if !strings.Contains(listed.Body.String(), `"knowledge_base_name":"默认项目库"`) || !strings.Contains(listed.Body.String(), `"knowledge_base_type":"project"`) {
		t.Fatalf("MCP list omitted source: %s", listed.Body)
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
