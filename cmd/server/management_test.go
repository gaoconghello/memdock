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

func managementApp(t *testing.T) *app {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "management.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	return &app{db: db, writeSecret: []byte(strings.Repeat("s", 32))}
}
func callManagement(t *testing.T, a *app, method, path, body string, handler http.HandlerFunc, write bool) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if write {
		addWriteSecret(a, r)
	}
	// Set the route ID expected by handlers, excluding the query string.
	routePath := r.URL.Path
	if strings.Contains(routePath, "/projects/") {
		parts := strings.Split(strings.Trim(routePath, "/"), "/")
		for i, p := range parts {
			if p == "projects" && i+1 < len(parts) {
				r.SetPathValue("id", parts[i+1])
				break
			}
		}
	}
	if strings.Contains(routePath, "/memories/") {
		parts := strings.Split(strings.Trim(routePath, "/"), "/")
		for i, p := range parts {
			if p == "memories" && i+1 < len(parts) {
				r.SetPathValue("id", parts[i+1])
				break
			}
		}
	}
	if strings.Contains(routePath, "/knowledge-bases/") {
		parts := strings.Split(strings.Trim(routePath, "/"), "/")
		for i, p := range parts {
			if p == "knowledge-bases" && i+1 < len(parts) {
				r.SetPathValue("id", parts[i+1])
				break
			}
		}
	}
	w := httptest.NewRecorder()
	handler(w, r)
	return w
}
func createManagementProject(t *testing.T, a *app, name string) string {
	t.Helper()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects/resolve", strings.NewReader(`{"name":"`+name+`"}`))
	addWriteSecret(a, req)
	a.resolveProject(w, req)
	if w.Code != 201 {
		t.Fatalf("create project: %d %s", w.Code, w.Body)
	}
	var result struct {
		ID string `json:"project_id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	return result.ID
}

func TestMemoryLifecycleTagsHistoryAndSearchIndex(t *testing.T) {
	a := managementApp(t)
	p := createManagementProject(t, a, "project")
	kb := ""
	if err := a.db.QueryRow(`SELECT id FROM knowledge_bases WHERE project_id=? AND is_default=1`, p).Scan(&kb); err != nil {
		t.Fatal(err)
	}
	id := makeID("mem")
	now := "2025-01-01T00:00:00Z"
	if _, err := a.db.Exec(`INSERT INTO memories(id,knowledge_base_id,title,content,created_at,updated_at) VALUES(?,?,?,?,?,?)`, id, kb, "old title", "old unique body", now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.Exec(`INSERT INTO memory_tags(memory_id,tag) VALUES(?,?)`, id, "old-tag"); err != nil {
		t.Fatal(err)
	}
	updated := callManagement(t, a, "PATCH", "/api/v1/memories/"+id, `{"project_id":"`+p+`","title":"new title","content":"new searchable phrase","tags":["one","two","one"]}`, a.updateMemory, true)
	if updated.Code != 200 {
		t.Fatalf("update: %d %s", updated.Code, updated.Body)
	}
	var tags, revisions int
	if err := a.db.QueryRow(`SELECT count(*) FROM memory_tags WHERE memory_id=?`, id).Scan(&tags); err != nil {
		t.Fatal(err)
	}
	if err := a.db.QueryRow(`SELECT count(*) FROM memory_revisions WHERE memory_id=?`, id).Scan(&revisions); err != nil {
		t.Fatal(err)
	}
	if tags != 2 || revisions != 1 {
		t.Fatalf("tags=%d revisions=%d", tags, revisions)
	}
	otherProject := createManagementProject(t, a, "other")
	var otherKB string
	if err := a.db.QueryRow(`SELECT id FROM knowledge_bases WHERE project_id=? AND is_default=1`, otherProject).Scan(&otherKB); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.Exec(`INSERT INTO memories(id,knowledge_base_id,title,content,created_at,updated_at) VALUES('foreign-memory',?,'new title','new searchable phrase','n','n')`, otherKB); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.Exec(`INSERT INTO memory_tags(memory_id,tag) VALUES('foreign-memory','one')`); err != nil {
		t.Fatal(err)
	}
	search := httptest.NewRecorder()
	filterReq := httptest.NewRequest(http.MethodPost, "/api/v1/memories/search", strings.NewReader(`{"project_id":"`+p+`","query":"new searchable phrase","tag":"one"}`))
	a.searchMemories(search, filterReq)
	if search.Code != 200 || !strings.Contains(search.Body.String(), id) || strings.Contains(search.Body.String(), "foreign-memory") {
		t.Fatalf("tag filtering changed project scope or missed memory: %d %s", search.Code, search.Body)
	}
	history := httptest.NewRecorder()
	historyReq := httptest.NewRequest(http.MethodGet, "/api/v1/memories/"+id+"/history?project_id="+p, nil)
	historyReq.SetPathValue("id", id)
	a.memoryHistory(history, historyReq)
	if !strings.Contains(history.Body.String(), `"tags":["old-tag"]`) {
		t.Fatalf("history did not preserve prior tags: %s", history.Body)
	}
	search = httptest.NewRecorder()
	a.searchMemories(search, httptest.NewRequest(http.MethodPost, "/api/v1/memories/search", strings.NewReader(`{"project_id":"`+p+`","query":"new searchable phrase"}`)))
	if search.Code != 200 || !strings.Contains(search.Body.String(), id) {
		t.Fatalf("updated FTS result missing: %d %s", search.Code, search.Body)
	}
	deleted := callManagement(t, a, "DELETE", "/api/v1/memories/"+id, `{"project_id":"`+p+`"}`, a.deleteMemory, true)
	if deleted.Code != 200 {
		t.Fatalf("delete: %d %s", deleted.Code, deleted.Body)
	}
	search = httptest.NewRecorder()
	a.searchMemories(search, httptest.NewRequest(http.MethodPost, "/api/v1/memories/search", strings.NewReader(`{"project_id":"`+p+`","query":"new searchable phrase"}`)))
	if !strings.Contains(search.Body.String(), `"results":[]`) {
		t.Fatalf("soft deleted memory remained searchable: %s", search.Body)
	}
	restored := callManagement(t, a, "POST", "/api/v1/memories/"+id+"/restore", `{"project_id":"`+p+`"}`, a.restoreMemory, true)
	if restored.Code != 200 {
		t.Fatalf("restore: %d %s", restored.Code, restored.Body)
	}
	search = httptest.NewRecorder()
	a.searchMemories(search, httptest.NewRequest(http.MethodPost, "/api/v1/memories/search", strings.NewReader(`{"project_id":"`+p+`","query":"new searchable phrase"}`)))
	if !strings.Contains(search.Body.String(), id) {
		t.Fatalf("restored memory absent from search: %s", search.Body)
	}
	deleted = callManagement(t, a, "DELETE", "/api/v1/memories/"+id, `{"project_id":"`+p+`"}`, a.deleteMemory, true)
	if deleted.Code != 200 {
		t.Fatalf("second delete: %d %s", deleted.Code, deleted.Body)
	}
	purged := callManagement(t, a, "DELETE", "/api/v1/memories/"+id+"/permanent", `{"project_id":"`+p+`","confirm":true}`, a.permanentlyDeleteMemory, true)
	if purged.Code != http.StatusNoContent {
		t.Fatalf("purge: %d %s", purged.Code, purged.Body)
	}
}

func TestMoveMemoryPreservesDataAndHistory(t *testing.T) {
	a := managementApp(t)
	source := createManagementProject(t, a, "move-source")
	target := createManagementProject(t, a, "move-target")
	var sourceKB, targetKB string
	if err := a.db.QueryRow(`SELECT id FROM knowledge_bases WHERE project_id=? AND is_default=1`, source).Scan(&sourceKB); err != nil {
		t.Fatal(err)
	}
	if err := a.db.QueryRow(`SELECT id FROM knowledge_bases WHERE project_id=? AND is_default=1`, target).Scan(&targetKB); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.Exec(`INSERT INTO memories(id,knowledge_base_id,title,content,category,status,created_at,updated_at) VALUES('move-me',?,'title','body','category','active','created','updated')`, sourceKB); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.Exec(`INSERT INTO memory_tags(memory_id,tag) VALUES('move-me','tag')`); err != nil {
		t.Fatal(err)
	}
	moved := callManagement(t, a, http.MethodPost, "/api/v1/memories/move-me/move", `{"project_id":"`+source+`","target_project_id":"`+target+`","knowledge_base_id":"`+targetKB+`"}`, a.moveMemory, true)
	if moved.Code != http.StatusOK {
		t.Fatalf("move status=%d body=%s", moved.Code, moved.Body)
	}
	var gotKB, title, content, category, status string
	var tags, revisions int
	if err := a.db.QueryRow(`SELECT knowledge_base_id,title,content,category,status FROM memories WHERE id='move-me'`).Scan(&gotKB, &title, &content, &category, &status); err != nil {
		t.Fatal(err)
	}
	_ = a.db.QueryRow(`SELECT count(*) FROM memory_tags WHERE memory_id='move-me' AND tag='tag'`).Scan(&tags)
	_ = a.db.QueryRow(`SELECT count(*) FROM memory_revisions WHERE memory_id='move-me'`).Scan(&revisions)
	if gotKB != targetKB || title != "title" || content != "body" || category != "category" || status != "active" || tags != 1 || revisions != 1 {
		t.Fatalf("move lost or changed data: kb=%s title=%s content=%s category=%s status=%s tags=%d revisions=%d", gotKB, title, content, category, status, tags, revisions)
	}
	oldList := httptest.NewRecorder()
	a.listMemories(oldList, httptest.NewRequest(http.MethodGet, "/api/v1/memories?project_id="+source, nil))
	newList := httptest.NewRecorder()
	a.listMemories(newList, httptest.NewRequest(http.MethodGet, "/api/v1/memories?project_id="+target, nil))
	if strings.Contains(oldList.Body.String(), "move-me") || !strings.Contains(newList.Body.String(), "move-me") {
		t.Fatalf("memory list ownership incorrect: source=%s target=%s", oldList.Body, newList.Body)
	}
	searchSource := httptest.NewRecorder()
	a.searchMemories(searchSource, httptest.NewRequest(http.MethodPost, "/api/v1/memories/search", strings.NewReader(`{"project_id":"`+source+`","query":"body"}`)))
	searchTarget := httptest.NewRecorder()
	a.searchMemories(searchTarget, httptest.NewRequest(http.MethodPost, "/api/v1/memories/search", strings.NewReader(`{"project_id":"`+target+`","query":"body"}`)))
	if strings.Contains(searchSource.Body.String(), "move-me") || !strings.Contains(searchTarget.Body.String(), "move-me") {
		t.Fatalf("memory search ownership incorrect: source=%s target=%s", searchSource.Body, searchTarget.Body)
	}
	wrongSource := callManagement(t, a, http.MethodPost, "/api/v1/memories/move-me/move", `{"project_id":"`+source+`","target_project_id":"`+target+`","knowledge_base_id":"`+targetKB+`"}`, a.moveMemory, true)
	if wrongSource.Code != http.StatusNotFound {
		t.Fatalf("wrong source project status=%d body=%s", wrongSource.Code, wrongSource.Body)
	}
	invalid := callManagement(t, a, http.MethodPost, "/api/v1/memories/move-me/move", `{"project_id":"`+target+`","target_project_id":"`+source+`","knowledge_base_id":"`+targetKB+`"}`, a.moveMemory, true)
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("foreign target KB status=%d body=%s", invalid.Code, invalid.Body)
	}
}

func TestProjectRepositoryAndKnowledgeBaseManagement(t *testing.T) {
	a := managementApp(t)
	project := createManagementProject(t, a, "unbound")
	bound := callManagement(t, a, http.MethodPost, "/api/v1/projects/"+project+"/repositories", `{"repository":"git@github.com:team/repo.git"}`, a.bindRepository, true)
	if bound.Code != http.StatusCreated {
		t.Fatalf("bind repository: %d %s", bound.Code, bound.Body)
	}
	detail := httptest.NewRecorder()
	detailReq := httptest.NewRequest(http.MethodGet, "/api/v1/projects/"+project, nil)
	detailReq.SetPathValue("id", project)
	a.projectDetail(detail, detailReq)
	if detail.Code != 200 || !strings.Contains(detail.Body.String(), "github.com/team/repo") {
		t.Fatalf("project detail omitted repository: %d %s", detail.Code, detail.Body)
	}
	unbound := callManagement(t, a, http.MethodDelete, "/api/v1/projects/"+project+"/repositories", `{"repository":"https://github.com/team/repo.git"}`, a.unbindRepository, true)
	if unbound.Code != http.StatusNoContent {
		t.Fatalf("unbind repository: %d %s", unbound.Code, unbound.Body)
	}
	var kb string
	if err := a.db.QueryRow(`SELECT id FROM knowledge_bases WHERE project_id=? AND is_default=1`, project).Scan(&kb); err != nil {
		t.Fatal(err)
	}
	target := createManagementProject(t, a, "target")
	for _, body := range []string{`{"kind":"shared"}`, `{"project_id":"` + target + `"}`} {
		changed := callManagement(t, a, http.MethodPatch, "/api/v1/knowledge-bases/"+kb, body, a.updateKnowledgeBase, true)
		if changed.Code != http.StatusConflict {
			t.Fatalf("default knowledge base scope change should be rejected: %d %s", changed.Code, changed.Body)
		}
	}
	var defaults int
	if err := a.db.QueryRow(`SELECT count(*) FROM knowledge_bases WHERE project_id IN (?,?) AND kind='project' AND is_default=1`, project, target).Scan(&defaults); err != nil || defaults != 2 {
		t.Fatalf("default libraries lost after rejected move: count=%d err=%v", defaults, err)
	}
	createdKB := httptest.NewRecorder()
	createReq := httptest.NewRequest(http.MethodPost, "/api/v1/knowledge-bases", strings.NewReader(`{"project_id":"`+project+`","name":"extra","kind":"project"}`))
	addWriteSecret(a, createReq)
	a.createKnowledgeBase(createdKB, createReq)
	if createdKB.Code != http.StatusCreated {
		t.Fatalf("create non-default knowledge base: %d %s", createdKB.Code, createdKB.Body)
	}
	var extraKB string
	var extra struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(createdKB.Body.Bytes(), &extra); err != nil {
		t.Fatal(err)
	}
	extraKB = extra.ID
	sharedKBRequest := httptest.NewRequest(http.MethodPost, "/api/v1/knowledge-bases", strings.NewReader(`{"project_id":"`+project+`","name":"to-shared","kind":"project"}`))
	addWriteSecret(a, sharedKBRequest)
	sharedKBCreated := httptest.NewRecorder()
	a.createKnowledgeBase(sharedKBCreated, sharedKBRequest)
	var sharedKB struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(sharedKBCreated.Body.Bytes(), &sharedKB); err != nil {
		t.Fatal(err)
	}
	toShared := callManagement(t, a, http.MethodPatch, "/api/v1/knowledge-bases/"+sharedKB.ID, `{"kind":"shared"}`, a.updateKnowledgeBase, true)
	if toShared.Code != http.StatusOK {
		t.Fatalf("convert non-default library to shared: %d %s", toShared.Code, toShared.Body)
	}
	var sharedKind string
	var sharedProject sql.NullString
	var sharedDefault int
	if err := a.db.QueryRow(`SELECT kind,project_id,is_default FROM knowledge_bases WHERE id=?`, sharedKB.ID).Scan(&sharedKind, &sharedProject, &sharedDefault); err != nil {
		t.Fatal(err)
	}
	if sharedKind != "shared" || sharedProject.Valid || sharedDefault != 0 {
		t.Fatalf("shared library state: kind=%s project=%v default=%d", sharedKind, sharedProject, sharedDefault)
	}
	moved := callManagement(t, a, http.MethodPatch, "/api/v1/knowledge-bases/"+extraKB, `{"project_id":"`+target+`"}`, a.updateKnowledgeBase, true)
	if moved.Code != http.StatusOK {
		t.Fatalf("move non-default knowledge base: %d %s", moved.Code, moved.Body)
	}
	var actualProject string
	var isDefault int
	if err := a.db.QueryRow(`SELECT project_id,is_default FROM knowledge_bases WHERE id=?`, extraKB).Scan(&actualProject, &isDefault); err != nil {
		t.Fatal(err)
	}
	if actualProject != target || isDefault != 0 {
		t.Fatalf("moved library state: project=%s default=%d", actualProject, isDefault)
	}
	if err := a.db.QueryRow(`SELECT count(*) FROM knowledge_bases WHERE project_id IN (?,?) AND kind='project' AND is_default=1`, project, target).Scan(&defaults); err != nil || defaults != 2 {
		t.Fatalf("default libraries lost after regular library move: count=%d err=%v", defaults, err)
	}
}

func TestProjectDeletionRequiresConfirmationAndRemovesProjectData(t *testing.T) {
	a := managementApp(t)
	project := createManagementProject(t, a, "delete-me")
	var kb string
	if err := a.db.QueryRow(`SELECT id FROM knowledge_bases WHERE project_id=? AND is_default=1`, project).Scan(&kb); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.Exec(`INSERT INTO memories(id,knowledge_base_id,title,content,created_at,updated_at) VALUES('delete-me-memory',?,'title','indexed project data','n','n')`, kb); err != nil {
		t.Fatal(err)
	}
	denied := callManagement(t, a, http.MethodDelete, "/api/v1/projects/"+project, `{}`, a.deleteProject, true)
	if denied.Code != http.StatusBadRequest {
		t.Fatalf("unconfirmed project delete status = %d", denied.Code)
	}
	deleted := callManagement(t, a, http.MethodDelete, "/api/v1/projects/"+project, `{"confirm":true}`, a.deleteProject, true)
	if deleted.Code != http.StatusNoContent {
		t.Fatalf("confirmed project delete: %d %s", deleted.Code, deleted.Body)
	}
	var count int
	if err := a.db.QueryRow(`SELECT count(*) FROM memories WHERE id='delete-me-memory'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("project memory remains after delete: count=%d err=%v", count, err)
	}
	if err := a.db.QueryRow(`SELECT count(*) FROM memories_fts WHERE memories_fts MATCH 'indexed project data'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("deleted project memory remains indexed: count=%d err=%v", count, err)
	}
}

func TestProjectMergePreservesMemoriesAndRepositoryConflictRule(t *testing.T) {
	a := managementApp(t)
	source := createManagementProject(t, a, "source")
	target := createManagementProject(t, a, "target")
	var sourceKB, targetKB string
	_ = a.db.QueryRow(`SELECT id FROM knowledge_bases WHERE project_id=? AND is_default=1`, source).Scan(&sourceKB)
	_ = a.db.QueryRow(`SELECT id FROM knowledge_bases WHERE project_id=? AND is_default=1`, target).Scan(&targetKB)
	_, _ = a.db.Exec(`INSERT INTO memories(id,knowledge_base_id,title,content,created_at,updated_at) VALUES('kept',?,'kept','merge searchable content','n','n')`, sourceKB)
	_, _ = a.db.Exec(`INSERT INTO repositories(canonical,project_id) VALUES('github.com/same/repo',?)`, target)
	_, _ = a.db.Exec(`INSERT INTO repositories(canonical,project_id) VALUES('github.com/same/repo-source',?)`, source)
	merged := callManagement(t, a, "POST", "/api/v1/projects/"+source+"/merge", `{"target_project_id":"`+target+`","confirm":true}`, a.mergeProject, true)
	if merged.Code != 200 {
		t.Fatalf("merge: %d %s", merged.Code, merged.Body)
	}
	var count int
	if err := a.db.QueryRow(`SELECT count(*) FROM memories m JOIN knowledge_bases k ON k.id=m.knowledge_base_id WHERE m.id='kept' AND k.project_id=?`, target).Scan(&count); err != nil || count != 1 {
		t.Fatalf("merged memory lost: count=%d err=%v", count, err)
	}
	if err := a.db.QueryRow(`SELECT count(*) FROM project_operations WHERE source_project_id=? AND target_project_id=?`, source, target).Scan(&count); err != nil || count != 1 {
		t.Fatalf("merge operation missing: %d %v", count, err)
	}
}
