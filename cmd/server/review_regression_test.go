package main

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestHTTPMemoryListWithSingleConnectionAndTags(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "single-connection.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1) // Match the production server configuration.
	if err := migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	a := &app{db: db, writeSecret: []byte(strings.Repeat("s", 32))}
	projectID := createManagementProject(t, a, "single-connection")
	if _, err := db.Exec(`INSERT INTO memories(id,project_id,title,content,created_at,updated_at) VALUES('tagged-memory',?,'title','body','n','n')`, projectID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO memory_tags(memory_id,tag) VALUES('tagged-memory','tested-tag')`); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/memories", a.listMemories)
	request := httptest.NewRequest(http.MethodGet, "/api/v1/memories?project_id="+projectID, nil)
	response := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { mux.ServeHTTP(response, request); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("GET /api/v1/memories blocked with MaxOpenConns(1)")
	}
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"tags":["tested-tag"]`) {
		t.Fatalf("tagged list response: %d %s", response.Code, response.Body)
	}
}

func TestMemoryManagementRequiresMatchingProjectScope(t *testing.T) {
	a := managementApp(t)
	localProject := createManagementProject(t, a, "local")
	foreignProject := createManagementProject(t, a, "foreign")
	if _, err := a.db.Exec(`INSERT INTO memories(id,project_id,title,content,created_at,updated_at) VALUES('foreign-memory',?,'secret title','foreign scoped content','n','n')`, foreignProject); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.Exec(`INSERT INTO memory_revisions(memory_id,title,content,category,status,tags_json,updated_at,revised_at) VALUES('foreign-memory','secret title','foreign scoped content','','active','[]','n','n')`); err != nil {
		t.Fatal(err)
	}

	listWithoutProject := httptest.NewRecorder()
	a.listMemories(listWithoutProject, httptest.NewRequest(http.MethodGet, "/api/v1/memories?knowledge_base_id=missing-kb", nil))
	if listWithoutProject.Code != http.StatusBadRequest {
		t.Fatalf("knowledge-base-only list status=%d body=%s", listWithoutProject.Code, listWithoutProject.Body)
	}
	foreignKBWithLocalProject := httptest.NewRecorder()
	a.listMemories(foreignKBWithLocalProject, httptest.NewRequest(http.MethodGet, "/api/v1/memories?project_id="+localProject+"&knowledge_base_id=missing-kb", nil))
	if foreignKBWithLocalProject.Code != http.StatusNotFound {
		t.Fatalf("missing shared library list status=%d body=%s", foreignKBWithLocalProject.Code, foreignKBWithLocalProject.Body)
	}

	get := callManagement(t, a, http.MethodGet, "/api/v1/memories/foreign-memory?project_id="+localProject, "", a.getMemory, false)
	if get.Code != http.StatusNotFound {
		t.Fatalf("cross-project detail status=%d body=%s", get.Code, get.Body)
	}
	history := callManagement(t, a, http.MethodGet, "/api/v1/memories/foreign-memory/history?project_id="+localProject, "", a.memoryHistory, false)
	if history.Code != http.StatusNotFound {
		t.Fatalf("cross-project history status=%d body=%s", history.Code, history.Body)
	}
	update := callManagement(t, a, http.MethodPatch, "/api/v1/memories/foreign-memory", `{"project_id":"`+localProject+`","title":"attempted overwrite"}`, a.updateMemory, true)
	if update.Code != http.StatusNotFound {
		t.Fatalf("cross-project update status=%d body=%s", update.Code, update.Body)
	}
	delete := callManagement(t, a, http.MethodDelete, "/api/v1/memories/foreign-memory", `{"project_id":"`+localProject+`"}`, a.deleteMemory, true)
	if delete.Code != http.StatusNotFound {
		t.Fatalf("cross-project delete status=%d body=%s", delete.Code, delete.Body)
	}
	var title string
	if err := a.db.QueryRow(`SELECT title FROM memories WHERE id='foreign-memory'`).Scan(&title); err != nil || title != "secret title" {
		t.Fatalf("foreign memory changed: title=%q err=%v", title, err)
	}
}
