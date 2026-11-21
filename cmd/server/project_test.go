package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestNormalizeRepository(t *testing.T) {
	cases := map[string]string{
		"git@GitHub.com:company/app.git":                  "github.com/company/app",
		"https://user:secret@github.com/company/app.git/": "github.com/company/app",
		"ssh://git@github.com/company/app":                "github.com/company/app",
		"ssh://git@example.com:2222/Org/App.git":          "example.com:2222/Org/App",
	}
	for input, want := range cases {
		got, err := normalizeRepository(input)
		if err != nil || got != want {
			t.Errorf("normalizeRepository(%q) = %q, %v; want %q", input, got, err, want)
		}
	}
}

func TestResolveWithoutRepositoryCreatesDistinctProjects(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "projects.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	a := &app{db: db, approvalKey: []byte(strings.Repeat("s", 32))}
	lookup := httptest.NewRecorder()
	a.lookupProject(lookup, httptest.NewRequest(http.MethodPost, "/api/v1/projects/lookup", strings.NewReader(`{"repository":"https://github.com/uninitialized/app.git"}`)))
	if lookup.Code != 404 {
		t.Fatalf("unresolved lookup status = %d, want 404", lookup.Code)
	}
	var projectCount int
	if err := db.QueryRow(`SELECT count(*) FROM projects`).Scan(&projectCount); err != nil {
		t.Fatal(err)
	}
	if projectCount != 0 {
		t.Fatalf("read-only lookup created %d projects", projectCount)
	}
	resolve := func() map[string]string {
		r := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/projects/resolve", strings.NewReader(`{"name":"folder"}`))
		signApproval(a, req)
		a.resolveProject(r, req)
		if r.Code != 201 {
			t.Fatalf("resolve status %d: %s", r.Code, r.Body)
		}
		var result map[string]string
		if err := json.Unmarshal(r.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	first, second := resolve(), resolve()
	if first["project_id"] == "" || second["project_id"] == "" || first["project_id"] == second["project_id"] {
		t.Fatalf("same folder names must not be merged implicitly: first=%v second=%v", first, second)
	}
	selected := httptest.NewRecorder()
	selectedRequest := httptest.NewRequest(http.MethodPost, "/api/v1/projects/resolve", strings.NewReader(`{"project_id":"`+first["project_id"]+`"}`))
	signApproval(a, selectedRequest)
	a.resolveProject(selected, selectedRequest)
	if selected.Code != 200 || !strings.Contains(selected.Body.String(), first["project_id"]) {
		t.Fatalf("explicit local binding failed: %d %s", selected.Code, selected.Body)
	}
	rename := httptest.NewRequest(http.MethodPatch, "/api/v1/projects/"+first["project_id"], strings.NewReader(`{"name":"renamed"}`))
	rename.SetPathValue("id", first["project_id"])
	signApproval(a, rename)
	renamed := httptest.NewRecorder()
	a.renameProject(renamed, rename)
	if renamed.Code != 200 || !strings.Contains(renamed.Body.String(), "renamed") {
		t.Fatalf("project rename failed: %d %s", renamed.Code, renamed.Body)
	}
}

func TestResolveSaveSearch(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "memdock.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	a := &app{db: db, approvalKey: []byte(strings.Repeat("s", 32))}
	resolve := httptest.NewRecorder()
	resolveRequest := httptest.NewRequest(http.MethodPost, "/api/v1/projects/resolve", strings.NewReader(`{"repository":"git@github.com:team/app.git"}`))
	signApproval(a, resolveRequest)
	a.resolveProject(resolve, resolveRequest)
	if resolve.Code != 201 {
		t.Fatalf("resolve status %d: %s", resolve.Code, resolve.Body)
	}
	var project struct {
		ID string `json:"project_id"`
		KB string `json:"knowledge_base_id"`
	}
	if err := json.Unmarshal(resolve.Body.Bytes(), &project); err != nil {
		t.Fatal(err)
	}
	createReq := httptest.NewRequest(http.MethodPost, "/api/v1/memories", strings.NewReader(`{"project_id":"`+project.ID+`","title":"身份认证","content":"使用 JWT 身份认证和 Redis 刷新令牌"}`))
	signApproval(a, createReq)
	created := httptest.NewRecorder()
	a.createMemory(created, createReq)
	if created.Code != 201 {
		t.Fatalf("create status %d: %s", created.Code, created.Body)
	}
	replayReq := httptest.NewRequest(http.MethodPost, "/api/v1/memories", strings.NewReader(`{"project_id":"`+project.ID+`","title":"replay","content":"must not be saved twice"}`))
	signApproval(a, replayReq)
	replayHeader := replayReq.Header.Get("X-MemDock-Write-Signature")
	firstUse := httptest.NewRecorder()
	a.createMemory(firstUse, replayReq)
	if firstUse.Code != 201 {
		t.Fatalf("first one-time approval failed: %d %s", firstUse.Code, firstUse.Body)
	}
	replayed := httptest.NewRequest(http.MethodPost, "/api/v1/memories", strings.NewReader(`{"project_id":"`+project.ID+`","title":"replay","content":"must not be saved twice"}`))
	replayed.Header.Set("X-MemDock-Write-Signature", replayHeader)
	replayDenied := httptest.NewRecorder()
	a.createMemory(replayDenied, replayed)
	if replayDenied.Code != 401 {
		t.Fatalf("replayed approval status = %d", replayDenied.Code)
	}
	signed := httptest.NewRequest(http.MethodPost, "/api/v1/memories", strings.NewReader(`{"project_id":"`+project.ID+`","title":"approved","content":"original content"}`))
	signApproval(a, signed)
	tampered := httptest.NewRequest(http.MethodPost, "/api/v1/memories", strings.NewReader(`{"project_id":"`+project.ID+`","title":"tampered","content":"changed after approval"}`))
	tampered.Header.Set("X-MemDock-Write-Signature", signed.Header.Get("X-MemDock-Write-Signature"))
	tamperDenied := httptest.NewRecorder()
	a.createMemory(tamperDenied, tampered)
	if tamperDenied.Code != 401 {
		t.Fatalf("tampered request status = %d", tamperDenied.Code)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	a.db = db
	if err := db.Ping(); err != nil {
		t.Fatal(err)
	}
	search := httptest.NewRecorder()
	a.searchMemories(search, httptest.NewRequest(http.MethodPost, "/api/v1/memories/search", strings.NewReader(`{"project_id":"`+project.ID+`","query":"身份认证"}`)))
	if search.Code != 200 || !strings.Contains(search.Body.String(), "JWT") {
		t.Fatalf("search status %d: %s", search.Code, search.Body)
	}
	for _, query := range []string{"JWT Redis", "身份认证 Redis", "认证"} {
		response := httptest.NewRecorder()
		a.searchMemories(response, httptest.NewRequest(http.MethodPost, "/api/v1/memories/search", strings.NewReader(`{"project_id":"`+project.ID+`","query":"`+query+`"}`)))
		if response.Code != 200 || !strings.Contains(response.Body.String(), "JWT") {
			t.Errorf("multi/short-term search %q failed: %d %s", query, response.Code, response.Body)
		}
	}
	otherRecorder := httptest.NewRecorder()
	otherResolveRequest := httptest.NewRequest(http.MethodPost, "/api/v1/projects/resolve", strings.NewReader(`{"repository":"https://github.com/other/app.git"}`))
	signApproval(a, otherResolveRequest)
	a.resolveProject(otherRecorder, otherResolveRequest)
	var other struct {
		ID string `json:"project_id"`
		KB string `json:"knowledge_base_id"`
	}
	if err := json.Unmarshal(otherRecorder.Body.Bytes(), &other); err != nil {
		t.Fatal(err)
	}
	isolated := httptest.NewRecorder()
	a.searchMemories(isolated, httptest.NewRequest(http.MethodPost, "/api/v1/memories/search", strings.NewReader(`{"project_id":"`+other.ID+`","query":"身份认证"}`)))
	if isolated.Code != 200 || strings.Contains(isolated.Body.String(), "JWT") {
		t.Fatalf("project isolation failed: %s", isolated.Body)
	}
	crossKBSearch := httptest.NewRecorder()
	a.searchMemories(crossKBSearch, httptest.NewRequest(http.MethodPost, "/api/v1/memories/search", strings.NewReader(`{"project_id":"`+other.ID+`","knowledge_base_id":"`+project.KB+`","query":"身份认证"}`)))
	if crossKBSearch.Code != 200 || strings.Contains(crossKBSearch.Body.String(), "JWT") {
		t.Fatalf("explicit foreign project KB leaked: %s", crossKBSearch.Body)
	}
	crossKBWrite := httptest.NewRequest(http.MethodPost, "/api/v1/memories", strings.NewReader(`{"project_id":"`+other.ID+`","knowledge_base_id":"`+project.KB+`","title":"unauthorized","content":"cross project"}`))
	signApproval(a, crossKBWrite)
	crossKBResponse := httptest.NewRecorder()
	a.createMemory(crossKBResponse, crossKBWrite)
	if crossKBResponse.Code != 404 {
		t.Fatalf("cross-project KB write status = %d, want 404", crossKBResponse.Code)
	}
	sharedReq := httptest.NewRequest(http.MethodPost, "/api/v1/knowledge-bases", strings.NewReader(`{"name":"共享经验","kind":"shared"}`))
	signApproval(a, sharedReq)
	sharedRecorder := httptest.NewRecorder()
	a.createKnowledgeBase(sharedRecorder, sharedReq)
	if sharedRecorder.Code != 201 {
		t.Fatalf("create shared KB status %d: %s", sharedRecorder.Code, sharedRecorder.Body)
	}
	var shared struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(sharedRecorder.Body.Bytes(), &shared); err != nil {
		t.Fatal(err)
	}
	sharedMemory := httptest.NewRequest(http.MethodPost, "/api/v1/memories", strings.NewReader(`{"project_id":"`+project.ID+`","knowledge_base_id":"`+shared.ID+`","title":"共享 JWT","content":"共享 JWT 经验"}`))
	signApproval(a, sharedMemory)
	sharedSaved := httptest.NewRecorder()
	a.createMemory(sharedSaved, sharedMemory)
	if sharedSaved.Code != 201 {
		t.Fatalf("shared save status %d: %s", sharedSaved.Code, sharedSaved.Body)
	}
	sharedSearch := httptest.NewRecorder()
	a.searchMemories(sharedSearch, httptest.NewRequest(http.MethodPost, "/api/v1/memories/search", strings.NewReader(`{"project_id":"`+other.ID+`","query":"共享 JWT"}`)))
	if sharedSearch.Code != 200 || !strings.Contains(sharedSearch.Body.String(), "共享 JWT") {
		t.Fatalf("shared memory not visible: %s", sharedSearch.Body)
	}
	expiredReq := httptest.NewRequest(http.MethodPost, "/api/v1/memories", strings.NewReader(`{"project_id":"`+project.ID+`","title":"expired","content":"must not be saved"}`))
	signApproval(a, expiredReq)
	parts := strings.Split(expiredReq.Header.Get("X-MemDock-Write-Signature"), ":")
	parts[0] = strconv.FormatInt(time.Now().Add(-2*time.Minute).Unix(), 10)
	expiredReq.Header.Set("X-MemDock-Write-Signature", strings.Join(parts, ":"))
	expired := httptest.NewRecorder()
	a.createMemory(expired, expiredReq)
	if expired.Code != 401 {
		t.Fatalf("expired approval status = %d", expired.Code)
	}
	deniedReq := httptest.NewRequest(http.MethodPost, "/api/v1/memories", strings.NewReader(`{"project_id":"`+project.ID+`","title":"x","content":"y"}`))
	denied := httptest.NewRecorder()
	a.createMemory(denied, deniedReq)
	if denied.Code != 401 {
		t.Fatalf("missing one-time approval status = %d", denied.Code)
	}
}

func signApproval(a *app, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		panic(err)
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	var nonceBytes [16]byte
	if _, err := rand.Read(nonceBytes[:]); err != nil {
		panic(err)
	}
	nonce := hex.EncodeToString(nonceBytes[:])
	message := fmt.Sprintf("%s\n%s\n%s\n%s\n%s", timestamp, nonce, r.Method, r.URL.Path, body)
	mac := hmac.New(sha256.New, a.approvalKey)
	_, _ = mac.Write([]byte(message))
	r.Header.Set("X-MemDock-Write-Signature", timestamp+":"+nonce+":"+hex.EncodeToString(mac.Sum(nil)))
}
