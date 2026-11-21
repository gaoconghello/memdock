package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func TestSecureProjectGrantsAcrossRESTMCPAndBackup(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "acl.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err := migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"p1", "p2"} {
		if _, err := db.Exec(`INSERT INTO projects(id,name,created_at) VALUES(?,?,?)`, p, p, "now"); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO knowledge_bases(id,project_id,name,kind,is_default,created_at) VALUES(?,?,?,'project',1,'now')`, "kb-"+p, p, p); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO memories(id,knowledge_base_id,title,content,created_at,updated_at) VALUES(?,?,?,?,?,?)`, "mem-"+p, "kb-"+p, "private "+p, "unique "+p+" searchable", "now", "now"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO knowledge_bases(id,name,kind,is_default,created_at) VALUES('kb-shared','shared','shared',0,'now')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO memories(id,knowledge_base_id,title,content,created_at,updated_at) VALUES('mem-shared','kb-shared','shared result','shared searchable text','now','now')`); err != nil {
		t.Fatal(err)
	}
	readA, readB, writeA, sharedReader, sharedWriter, admin := strings.Repeat("a", 32), strings.Repeat("b", 32), strings.Repeat("w", 32), strings.Repeat("s", 32), strings.Repeat("t", 32), strings.Repeat("z", 32)
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	a := &app{db: db, mode: "secure", approvalPublicKey: pub, projectGrants: map[string]accessGrant{
		tokenDigest(readA):        {Role: "read", Projects: []string{"p1"}},
		tokenDigest(readB):        {Role: "read", Projects: []string{"p2"}},
		tokenDigest(writeA):       {Role: "write", Projects: []string{"p1"}},
		tokenDigest(sharedReader): {Role: "read", Projects: []string{"p1"}, SharedRead: true},
		tokenDigest(sharedWriter): {Role: "write", Projects: []string{"p1"}, SharedWrite: true},
		tokenDigest(admin):        {Role: "admin"},
	}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/projects", a.listProjects)
	mux.HandleFunc("POST /api/v1/projects/lookup", a.lookupProject)
	mux.HandleFunc("POST /api/v1/projects/resolve", a.resolveProject)
	mux.HandleFunc("GET /api/v1/projects/{id}", a.projectDetail)
	mux.HandleFunc("GET /api/v1/projects/{id}/memories", a.listProjectMemories)
	mux.HandleFunc("PATCH /api/v1/projects/{id}", a.renameProject)
	mux.HandleFunc("GET /api/v1/knowledge-bases", a.listKnowledgeBases)
	mux.HandleFunc("PATCH /api/v1/knowledge-bases/{id}", a.updateKnowledgeBase)
	mux.HandleFunc("POST /api/v1/memories/search", a.searchMemories)
	mux.HandleFunc("POST /api/v1/memories", a.createMemory)
	mux.HandleFunc("GET /api/v1/memories/{id}", a.getMemory)
	mux.HandleFunc("POST /api/v1/memories/{id}/move", a.moveMemory)
	mux.HandleFunc("POST /api/v1/knowledge-bases", a.createKnowledgeBase)
	mux.HandleFunc("GET /api/v1/backup", a.backup)
	mux.HandleFunc("GET /api/v1/status", a.systemStatus)
	mux.HandleFunc("POST /mcp", a.mcp)
	server := httptest.NewTLSServer(a.authenticate(a.authorizeProjectAccess(mux)))
	defer server.Close()
	client := server.Client()
	request := func(method, path, token, body string) (*http.Response, []byte) {
		t.Helper()
		req, err := http.NewRequest(method, server.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		if method == http.MethodPost && path == "/mcp" {
			req.Header.Set("Accept", "application/json")
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		payload, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		return resp, payload
	}

	basicReq, err := http.NewRequest(http.MethodGet, server.URL+"/api/v1/projects/p2", nil)
	if err != nil {
		t.Fatal(err)
	}
	basicReq.SetBasicAuth("memdock", readA)
	basicResponse, err := client.Do(basicReq)
	if err != nil {
		t.Fatal(err)
	}
	basicResponse.Body.Close()
	if basicResponse.StatusCode != http.StatusForbidden {
		t.Fatalf("Web Basic credential crossed project boundary: %d", basicResponse.StatusCode)
	}

	for _, tc := range []struct {
		name, path string
		want       int
	}{
		{"own project read", "/api/v1/projects/p1", 200},
		{"other identity project denied", "/api/v1/projects/p2", 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp, _ := request(http.MethodGet, tc.path, readA, "")
			if resp.StatusCode != tc.want {
				t.Fatalf("status=%d want=%d", resp.StatusCode, tc.want)
			}
		})
	}
	resp, payload := request(http.MethodGet, "/api/v1/projects", readA, "")
	if resp.StatusCode != 200 {
		t.Fatalf("project listing status=%d: %s", resp.StatusCode, payload)
	}
	var projectList struct {
		Projects []map[string]string `json:"projects"`
	}
	if err := json.Unmarshal(payload, &projectList); err != nil || len(projectList.Projects) != 1 || projectList.Projects[0]["id"] != "p1" {
		t.Fatalf("project list leaked another identity's project: %s (err=%v)", payload, err)
	}
	search := func(projectID string) string {
		return fmt.Sprintf(`{"project_id":%q,"query":"searchable"}`, projectID)
	}
	resp, _ = request(http.MethodGet, "/api/v1/projects/p1/memories?knowledge_base_id=kb-shared", readA, "")
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("project memory route exposed shared data without shared-read grant: %d", resp.StatusCode)
	}
	resp, _ = request(http.MethodGet, "/api/v1/projects/p1/memories?knowledge_base_id=kb-shared", sharedReader, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("shared-reader project memory route status=%d", resp.StatusCode)
	}
	resp, payload = request(http.MethodGet, "/api/v1/knowledge-bases", readA, "")
	if resp.StatusCode != 200 {
		t.Fatalf("knowledge-base listing status=%d", resp.StatusCode)
	}
	var kbList struct {
		KnowledgeBases []map[string]any `json:"knowledge_bases"`
	}
	if err := json.Unmarshal(payload, &kbList); err != nil || len(kbList.KnowledgeBases) != 1 || kbList.KnowledgeBases[0]["id"] != "kb-p1" {
		t.Fatalf("knowledge-base list leaked foreign/shared library: %s (err=%v)", payload, err)
	}
	var searchResultBody struct {
		Results []searchResult `json:"results"`
	}
	resp, payload = request(http.MethodPost, "/api/v1/memories/search", sharedReader, search("p1"))
	if resp.StatusCode != 200 {
		t.Fatalf("shared-read search status=%d", resp.StatusCode)
	}
	if err := json.Unmarshal(payload, &searchResultBody); err != nil || len(searchResultBody.Results) != 2 {
		t.Fatalf("shared-read search did not include project plus shared results: %s (err=%v)", payload, err)
	}
	resp, _ = request(http.MethodGet, "/api/v1/memories/mem-p2?project_id=p2", readA, "")
	if resp.StatusCode != 403 {
		t.Fatalf("cross-identity memory detail status=%d", resp.StatusCode)
	}
	resp, _ = request(http.MethodPost, "/api/v1/memories/mem-p1/move", writeA, `{"project_id":"p1","target_project_id":"p2","knowledge_base_id":"kb-p2"}`)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("move into unauthorized target project status=%d", resp.StatusCode)
	}
	resp, _ = request(http.MethodPost, "/api/v1/memories/mem-p2/move", writeA, `{"project_id":"p2","target_project_id":"p1","knowledge_base_id":"kb-p1"}`)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("move from unauthorized source project status=%d", resp.StatusCode)
	}
	resp, _ = request(http.MethodPost, "/api/v1/memories/search", readA, search("p2"))
	if resp.StatusCode != 403 {
		t.Fatalf("cross-identity REST search status=%d", resp.StatusCode)
	}
	resp, payload = request(http.MethodPost, "/api/v1/memories/search", readA, search("p1"))
	if resp.StatusCode != 200 {
		t.Fatalf("own-project search status=%d: %s", resp.StatusCode, payload)
	}
	if err := json.Unmarshal(payload, &searchResultBody); err != nil || len(searchResultBody.Results) != 1 || searchResultBody.Results[0].ID != "mem-p1" {
		t.Fatalf("project search leaked shared/foreign data: %s (err=%v)", payload, err)
	}
	resp, _ = request(http.MethodPost, "/api/v1/memories", readA, `{"project_id":"p1","title":"x","content":"y"}`)
	if resp.StatusCode != 403 {
		t.Fatalf("reader unexpectedly wrote: %d", resp.StatusCode)
	}
	resp, _ = request(http.MethodPost, "/api/v1/memories", writeA, `{"project_id":"p2","title":"x","content":"y"}`)
	if resp.StatusCode != 403 {
		t.Fatalf("writer crossed project boundary: %d", resp.StatusCode)
	}
	resp, _ = request(http.MethodPatch, "/api/v1/projects/p2", writeA, `{"name":"hijacked"}`)
	if resp.StatusCode != 403 {
		t.Fatalf("writer renamed another identity's project: %d", resp.StatusCode)
	}
	resp, _ = request(http.MethodPatch, "/api/v1/knowledge-bases/kb-p1", writeA, `{"project_id":"p2"}`)
	if resp.StatusCode != 403 {
		t.Fatalf("writer moved a library into an unauthorized project: %d", resp.StatusCode)
	}
	resp, _ = request(http.MethodPost, "/api/v1/knowledge-bases", writeA, `{"name":"global","kind":"shared"}`)
	if resp.StatusCode != 403 {
		t.Fatalf("writer created shared library without shared-write grant: %d", resp.StatusCode)
	}
	resp, _ = request(http.MethodPost, "/api/v1/knowledge-bases", sharedWriter, `{"name":"global","kind":"shared"}`)
	if resp.StatusCode != 401 {
		t.Fatalf("shared writer did not reach independent signature check: %d", resp.StatusCode)
	}
	resolve := `{"name":"new-project"}`
	resp, _ = request(http.MethodPost, "/api/v1/projects/lookup", readA, `{"repository":"https://example.com/unbound.git"}`)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unbound project lookup should reach not-found handler: %d", resp.StatusCode)
	}
	resp, _ = request(http.MethodPost, "/api/v1/projects/resolve", writeA, resolve)
	if resp.StatusCode != 403 {
		t.Fatalf("project writer created an unassigned project: %d", resp.StatusCode)
	}
	resp, _ = request(http.MethodPost, "/api/v1/projects/resolve", admin, resolve)
	if resp.StatusCode != 401 {
		t.Fatalf("admin should reach signature check for project creation, got %d", resp.StatusCode)
	}
	resp, _ = request(http.MethodPost, "/api/v1/memories", writeA, `{"project_id":"p1","title":"x","content":"y"}`)
	if resp.StatusCode != 401 {
		t.Fatalf("own project writer should reach independent signature check, got %d", resp.StatusCode)
	}
	resp, payload = request(http.MethodGet, "/api/v1/status", readA, "")
	if resp.StatusCode != 200 {
		t.Fatalf("project reader status endpoint=%d", resp.StatusCode)
	}
	var status struct {
		CountsVisible bool `json:"counts_visible"`
		Projects      int  `json:"projects"`
	}
	if err := json.Unmarshal(payload, &status); err != nil || status.CountsVisible || status.Projects != 0 {
		t.Fatalf("status exposed global project totals: %s (err=%v)", payload, err)
	}
	resp, _ = request(http.MethodGet, "/api/v1/backup", readA, "")
	if resp.StatusCode != 403 {
		t.Fatalf("project reader downloaded global backup: %d", resp.StatusCode)
	}
	resp, _ = request(http.MethodGet, "/api/v1/backup", admin, "")
	if resp.StatusCode != 200 {
		t.Fatalf("admin backup status=%d", resp.StatusCode)
	}

	mcp := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"memory_save","arguments":{"project_id":"p1","title":"proposal","content":"not written until approved"}}}`
	resp, payload = request(http.MethodPost, "/mcp", readA, mcp)
	if resp.StatusCode != 200 {
		t.Fatalf("MCP proposal status=%d: %s", resp.StatusCode, payload)
	}
	var mcpEnvelope struct {
		Result struct {
			Structured struct {
				ApprovalRequired bool `json:"approval_required"`
				Request          struct {
					Method string `json:"method"`
					Path   string `json:"path"`
				} `json:"approval_request"`
			} `json:"structuredContent"`
		} `json:"result"`
	}
	if err := json.Unmarshal(payload, &mcpEnvelope); err != nil || !mcpEnvelope.Result.Structured.ApprovalRequired || mcpEnvelope.Result.Structured.Request.Method != http.MethodPost || mcpEnvelope.Result.Structured.Request.Path != "/api/v1/memories" {
		t.Fatalf("MCP response did not produce actionable approval request: %s (err=%v)", payload, err)
	}
	for _, invalidArgs := range []string{
		`{"project_id":"p1","id":"mem-p1"}`,
		`{"project_id":"p1","id":"mem-p1","action":"purge"}`,
		`{"project_id":"p1","id":"mem-p1","action":true}`,
		`{"project_id":"p1","id":"mem-p1","action":"delete","extra":1}`,
		`{"project_id":"p1","title":42,"content":"bad type"}`,
		`{"project_id":"p1","content":"missing title"}`,
	} {
		invalidCall := `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"memory_delete","arguments":` + invalidArgs + `}}`
		if strings.Contains(invalidArgs, `"title"`) || strings.Contains(invalidArgs, `"content"`) {
			invalidCall = `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"memory_save","arguments":` + invalidArgs + `}}`
		}
		resp, payload = request(http.MethodPost, "/mcp", readA, invalidCall)
		if resp.StatusCode != http.StatusOK || !strings.Contains(string(payload), `"isError":true`) || !strings.Contains(string(payload), "invalid arguments") || strings.Contains(string(payload), `"approval_required":true`) {
			t.Fatalf("Secure MCP accepted invalid arguments or prepared a request: status=%d body=%s", resp.StatusCode, payload)
		}
	}
	resp, _ = request(http.MethodPost, "/mcp", readA, strings.Replace(mcp, `"p1"`, `"p2"`, 1))
	if resp.StatusCode != 403 {
		t.Fatalf("MCP proposal crossed project boundary: %d", resp.StatusCode)
	}
}

func TestLoadAccessGrantsRequiresExplicitBaseTokenScopes(t *testing.T) {
	readToken, writeToken := strings.Repeat("r", 32), strings.Repeat("w", 32)
	path := filepath.Join(t.TempDir(), "acl.json")
	data, err := json.Marshal(accessFile{Credentials: []accessGrant{
		{TokenSHA256: tokenDigest(readToken), Role: "read", Projects: []string{"p1"}},
		{TokenSHA256: tokenDigest(writeToken), Role: "write", Projects: []string{"p1"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	grants, err := loadAccessGrants(path, readToken, writeToken)
	if err != nil || len(grants) != 2 {
		t.Fatalf("valid ACL rejected: grants=%d err=%v", len(grants), err)
	}
	if _, err := loadAccessGrants("", readToken, writeToken); err == nil {
		t.Fatal("missing ACL should fail closed")
	}
	bad := accessFile{Credentials: []accessGrant{{TokenSHA256: tokenDigest(readToken), Role: "read"}}}
	data, _ = json.Marshal(bad)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadAccessGrants(path, readToken, writeToken); err == nil {
		t.Fatal("ACL without configured write token grant should fail closed")
	}
}

func TestParseApprovalRequestEnvelope(t *testing.T) {
	request, body, err := parseApprovalRequest([]byte(`{"method":"POST","path":"/api/v1/memories","body":{"project_id":"p1","title":"reviewed","content":"saved"}}`))
	if err != nil || request.Method != http.MethodPost || request.Path != "/api/v1/memories" || string(body) != `{"project_id":"p1","title":"reviewed","content":"saved"}` {
		t.Fatalf("parsed envelope=%+v body=%s err=%v", request, body, err)
	}
	for _, raw := range []string{
		`{"method":"GET","path":"/api/v1/memories","body":{}}`,
		`{"method":"DELETE","path":"https://evil.invalid/api/v1/memories","body":{}}`,
		`{"method":"POST","path":"/api/v1/memories?all=true","body":{}}`,
		`{"method":"POST","path":"/api/v1/memories","body":{},"extra":true}`,
	} {
		if _, _, err := parseApprovalRequest([]byte(raw)); err == nil {
			t.Errorf("invalid envelope accepted: %s", raw)
		}
	}
}
