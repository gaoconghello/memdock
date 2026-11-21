package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

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
	a := newProjectTestApp(t)
	lookup := httptest.NewRecorder()
	a.lookupProject(lookup, httptest.NewRequest(http.MethodPost, "/api/v1/projects/lookup", strings.NewReader(`{"repository":"https://github.com/uninitialized/app.git"}`)))
	if lookup.Code != http.StatusNotFound {
		t.Fatalf("unresolved lookup status=%d", lookup.Code)
	}
	var count int
	if err := a.db.QueryRow(`SELECT count(*) FROM projects`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("lookup created project: count=%d err=%v", count, err)
	}
	resolve := func() map[string]string {
		r := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/projects/resolve", strings.NewReader(`{"name":"folder"}`))
		addWriteSecret(a, req)
		a.resolveProject(r, req)
		if r.Code != http.StatusCreated {
			t.Fatalf("resolve status=%d body=%s", r.Code, r.Body)
		}
		var result map[string]string
		if err := json.Unmarshal(r.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	first, second := resolve(), resolve()
	if first["project_id"] == "" || second["project_id"] == "" || first["project_id"] == second["project_id"] {
		t.Fatalf("same folder names unexpectedly merged: %v %v", first, second)
	}
}

func TestMemoryReadWriteIsolationAcrossProjects(t *testing.T) {
	a := newProjectTestApp(t)
	resolve := func(repo string) string {
		r := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/projects/resolve", strings.NewReader(`{"repository":"`+repo+`"}`))
		addWriteSecret(a, req)
		a.resolveProject(r, req)
		if r.Code != http.StatusCreated {
			t.Fatalf("resolve project: %d %s", r.Code, r.Body)
		}
		var out struct {
			ID string `json:"project_id"`
		}
		if err := json.Unmarshal(r.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out.ID
	}
	p1 := resolve("https://example.test/team/one.git")
	p2 := resolve("https://example.test/team/two.git")
	write := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/memories", strings.NewReader(`{"project_id":"`+p1+`","title":"isolated","content":"project one secret"}`))
	addWriteSecret(a, request)
	a.createMemory(write, request)
	if write.Code != http.StatusCreated {
		t.Fatalf("save status=%d body=%s", write.Code, write.Body)
	}
	search := func(project, query string) *httptest.ResponseRecorder {
		r := httptest.NewRecorder()
		a.searchMemories(r, httptest.NewRequest(http.MethodPost, "/api/v1/memories/search", strings.NewReader(`{"project_id":"`+project+`","query":"`+query+`"}`)))
		return r
	}
	if got := search(p1, "project one secret"); got.Code != http.StatusOK || !strings.Contains(got.Body.String(), "project one secret") {
		t.Fatalf("own project search failed: %d %s", got.Code, got.Body)
	}
	if got := search(p2, "project one secret"); got.Code != http.StatusOK || strings.Contains(got.Body.String(), "project one secret") {
		t.Fatalf("cross-project memory leaked: %d %s", got.Code, got.Body)
	}
	sharedKB := httptest.NewRecorder()
	sharedKBRequest := httptest.NewRequest(http.MethodPost, "/api/v1/knowledge-bases", strings.NewReader(`{"name":"shared","kind":"shared"}`))
	addWriteSecret(a, sharedKBRequest)
	a.createKnowledgeBase(sharedKB, sharedKBRequest)
	if sharedKB.Code != http.StatusCreated {
		t.Fatalf("create shared library: %d %s", sharedKB.Code, sharedKB.Body)
	}
	var kb struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(sharedKB.Body.Bytes(), &kb); err != nil {
		t.Fatal(err)
	}
	sharedMemory := httptest.NewRecorder()
	sharedMemoryRequest := httptest.NewRequest(http.MethodPost, "/api/v1/memories", strings.NewReader(`{"project_id":"`+p1+`","knowledge_base_id":"`+kb.ID+`","title":"shared note","content":"shared library visibility"}`))
	addWriteSecret(a, sharedMemoryRequest)
	a.createMemory(sharedMemory, sharedMemoryRequest)
	if sharedMemory.Code != http.StatusCreated {
		t.Fatalf("save shared memory: %d %s", sharedMemory.Code, sharedMemory.Body)
	}
	if got := search(p2, "shared library visibility"); got.Code != http.StatusOK || !strings.Contains(got.Body.String(), "shared library visibility") {
		t.Fatalf("shared library should be visible across projects: %d %s", got.Code, got.Body)
	}
}

func newProjectTestApp(t *testing.T) *app {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "projects.db"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if err := migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	return &app{db: db, writeSecret: []byte(strings.Repeat("w", 32))}
}

func addWriteSecret(a *app, r *http.Request) {
	if len(a.writeSecret) < 32 {
		a.writeSecret = []byte(strings.Repeat("w", 32))
	}
	r.Header.Set("X-MemDock-Write-Secret", string(a.writeSecret))
}
