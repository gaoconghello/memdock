package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func TestApprovalRequestCLIHelper(t *testing.T) {
	if os.Getenv("MEMDOCK_APPROVAL_E2E_CHILD") != "1" {
		return
	}
	if err := approveRequest([]string{os.Getenv("MEMDOCK_APPROVAL_E2E_REQUEST")}); err != nil {
		t.Fatal(err)
	}
}

func TestSecureMCPProposalToIndependentApproverCLI(t *testing.T) {
	if _, err := exec.LookPath("script"); err != nil {
		t.Skip("script utility required to provide a real pseudo-terminal")
	}
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "approval-e2e.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err := migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO projects(id,name,created_at) VALUES('p1','project','now')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO knowledge_bases(id,project_id,name,kind,is_default,created_at) VALUES('kb-p1','p1','default','project',1,'now')`); err != nil {
		t.Fatal(err)
	}
	readToken, writeToken := strings.Repeat("r", 32), strings.Repeat("w", 32)
	pub, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	app := &app{db: db, mode: "secure", approvalPublicKey: pub, projectGrants: map[string]accessGrant{
		tokenDigest(readToken):  {Role: "read", Projects: []string{"p1"}},
		tokenDigest(writeToken): {Role: "write", Projects: []string{"p1"}},
	}}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/memories", app.createMemory)
	mux.HandleFunc("POST /mcp", app.mcp)
	server := httptest.NewTLSServer(app.authenticate(app.authorizeProjectAccess(mux)))
	defer server.Close()

	mcpRequest := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"memory_save","arguments":{"project_id":"p1","title":"approved through MCP","content":"write only after separate human approval"}}}`
	request, err := http.NewRequest(http.MethodPost, server.URL+"/mcp", strings.NewReader(mcpRequest))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+readToken)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	var mcpResponse struct {
		Result struct {
			Structured json.RawMessage `json:"structuredContent"`
		} `json:"result"`
	}
	if err := json.NewDecoder(response.Body).Decode(&mcpResponse); err != nil {
		response.Body.Close()
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("MCP proposal status=%d", response.StatusCode)
	}
	var prepared struct {
		Request approvalRequest `json:"approval_request"`
	}
	if err := json.Unmarshal(mcpResponse.Result.Structured, &prepared); err != nil || prepared.Request.Method != http.MethodPost || prepared.Request.Path != "/api/v1/memories" {
		t.Fatalf("invalid MCP approval proposal: %s (err=%v)", mcpResponse.Result.Structured, err)
	}
	envelope, err := json.Marshal(prepared.Request)
	if err != nil {
		t.Fatal(err)
	}
	temp := t.TempDir()
	requestPath := filepath.Join(temp, "approval-request.json")
	if err := os.WriteFile(requestPath, envelope, 0600); err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(temp, "approval.key")
	// keygen stores the raw Ed25519 private key as hex.
	keyHex := make([]byte, ed25519.PrivateKeySize*2)
	for i, b := range privateKey {
		const digits = "0123456789abcdef"
		keyHex[2*i], keyHex[2*i+1] = digits[b>>4], digits[b&15]
	}
	if err := os.WriteFile(keyPath, keyHex, 0600); err != nil {
		t.Fatal(err)
	}
	caPath := filepath.Join(temp, "test-ca.pem")
	if err := os.WriteFile(caPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("script", "-q", "-e", "-c", shellQuote(os.Args[0])+" -test.run=^TestApprovalRequestCLIHelper$", "/dev/null")
	cmd.Stdin = strings.NewReader("approve\n")
	cmd.Env = append(os.Environ(),
		"MEMDOCK_APPROVAL_E2E_CHILD=1",
		"MEMDOCK_APPROVAL_E2E_REQUEST="+requestPath,
		"MEMDOCK_URL="+server.URL,
		"MEMDOCK_WRITE_TOKEN="+writeToken,
		"MEMDOCK_APPROVAL_PRIVATE_KEY_FILE="+keyPath,
		"SSL_CERT_FILE="+caPath,
		"MEMDOCK_TRUST_PROXY_TLS=false",
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("independent approval CLI failed: %v\n%s", err, output)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM memories WHERE title='approved through MCP'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("approved request was not persisted: count=%d err=%v; CLI output=%s", count, err, output)
	}
	if !strings.Contains(string(output), "Approve this exact request") || !strings.Contains(string(output), "approved through MCP") {
		t.Fatalf("CLI did not display the request before approval: %s", output)
	}
}

func TestPrepareApprovalScriptWritesPrivateEnvelope(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq required by the Skill request helper")
	}
	_, source, _, _ := runtime.Caller(0)
	helper := filepath.Join(filepath.Dir(source), "..", "..", "scripts", "prepare-approval.sh")
	requestPath := filepath.Join(t.TempDir(), "skill-request.json")
	body := `{"project_id":"p1","title":"skill proposal","content":"prepared only"}`
	cmd := exec.Command("sh", helper, "POST", "/api/v1/memories", body)
	cmd.Env = append(os.Environ(), "MEMDOCK_APPROVAL_REQUEST_FILE="+requestPath)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("prepare-approval helper failed: %v\n%s", err, output)
	}
	info, err := os.Stat(requestPath)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("approval file permissions=%v err=%v", info, err)
	}
	request, parsedBody, err := parseApprovalRequestFile(requestPath)
	if err != nil || request.Method != http.MethodPost || request.Path != "/api/v1/memories" || !strings.Contains(string(parsedBody), "skill proposal") {
		t.Fatalf("prepared request=%+v body=%s err=%v", request, parsedBody, err)
	}
}

func parseApprovalRequestFile(path string) (approvalRequest, []byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return approvalRequest{}, nil, err
	}
	return parseApprovalRequest(data)
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
