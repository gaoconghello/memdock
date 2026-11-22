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

func createSharedKnowledgeBase(t *testing.T, a *app, name string) string {
	t.Helper()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/knowledge-bases", strings.NewReader(`{"name":"`+name+`","kind":"shared"}`))
	addWriteSecret(a, req)
	a.createKnowledgeBase(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("create shared library: %d %s", w.Code, w.Body)
	}
	var result struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	return result.ID
}

func TestMemoryLifecycleTagsHistoryAndSearchIndex(t *testing.T) {
	a := managementApp(t)
	p := createManagementProject(t, a, "project")
	id := makeID("mem")
	now := "2025-01-01T00:00:00Z"
	if _, err := a.db.Exec(`INSERT INTO memories(id,project_id,title,content,created_at,updated_at) VALUES(?,?,?,?,?,?)`, id, p, "old title", "old unique body", now, now); err != nil {
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
	if _, err := a.db.Exec(`INSERT INTO memories(id,project_id,title,content,created_at,updated_at) VALUES('foreign-memory',?,'new title','new searchable phrase','n','n')`, otherProject); err != nil {
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
	if _, err := a.db.Exec(`INSERT INTO memories(id,project_id,title,content,category,status,created_at,updated_at) VALUES('move-me',?,'title','body','category','active','created','updated')`, source); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.Exec(`INSERT INTO memory_tags(memory_id,tag) VALUES('move-me','tag')`); err != nil {
		t.Fatal(err)
	}
	moved := callManagement(t, a, http.MethodPost, "/api/v1/memories/move-me/move", `{"project_id":"`+source+`","target_project_id":"`+target+`"}`, a.moveMemory, true)
	if moved.Code != http.StatusOK {
		t.Fatalf("move status=%d body=%s", moved.Code, moved.Body)
	}
	var gotProject, title, content, category, status string
	var gotKB sql.NullString
	var tags, revisions int
	if err := a.db.QueryRow(`SELECT project_id,knowledge_base_id,title,content,category,status FROM memories WHERE id='move-me'`).Scan(&gotProject, &gotKB, &title, &content, &category, &status); err != nil {
		t.Fatal(err)
	}
	_ = a.db.QueryRow(`SELECT count(*) FROM memory_tags WHERE memory_id='move-me' AND tag='tag'`).Scan(&tags)
	_ = a.db.QueryRow(`SELECT count(*) FROM memory_revisions WHERE memory_id='move-me'`).Scan(&revisions)
	if gotProject != target || gotKB.Valid || title != "title" || content != "body" || category != "category" || status != "active" || tags != 1 || revisions != 1 {
		t.Fatalf("move lost or changed data: project=%s kb=%v title=%s content=%s category=%s status=%s tags=%d revisions=%d", gotProject, gotKB, title, content, category, status, tags, revisions)
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
	wrongSource := callManagement(t, a, http.MethodPost, "/api/v1/memories/move-me/move", `{"project_id":"`+source+`","target_project_id":"`+target+`"}`, a.moveMemory, true)
	if wrongSource.Code != http.StatusNotFound {
		t.Fatalf("wrong source project status=%d body=%s", wrongSource.Code, wrongSource.Body)
	}
	shared := createSharedKnowledgeBase(t, a, "move-shared")
	toShared := callManagement(t, a, http.MethodPost, "/api/v1/memories/move-me/move", `{"project_id":"`+target+`","knowledge_base_id":"`+shared+`"}`, a.moveMemory, true)
	if toShared.Code != http.StatusOK {
		t.Fatalf("move to shared status=%d body=%s", toShared.Code, toShared.Body)
	}
	var sharedProject sql.NullString
	var sharedKB string
	if err := a.db.QueryRow(`SELECT project_id,knowledge_base_id FROM memories WHERE id='move-me'`).Scan(&sharedProject, &sharedKB); err != nil {
		t.Fatal(err)
	}
	if sharedProject.Valid || sharedKB != shared {
		t.Fatalf("shared ownership incorrect: project=%v kb=%s", sharedProject, sharedKB)
	}
	searchSource = httptest.NewRecorder()
	a.searchMemories(searchSource, httptest.NewRequest(http.MethodPost, "/api/v1/memories/search", strings.NewReader(`{"project_id":"`+source+`","query":"body"}`)))
	if !strings.Contains(searchSource.Body.String(), "move-me") {
		t.Fatalf("shared memory not visible from another project: %s", searchSource.Body)
	}
	invalid := callManagement(t, a, http.MethodPost, "/api/v1/memories/move-me/move", `{"project_id":"`+source+`","target_project_id":"`+target+`","knowledge_base_id":"missing"}`, a.moveMemory, true)
	if invalid.Code != http.StatusNotFound {
		t.Fatalf("missing target library status=%d body=%s", invalid.Code, invalid.Body)
	}
}

func TestSharedKnowledgeBaseManagement(t *testing.T) {
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
	for _, body := range []string{`{"name":"project library","kind":"project"}`, `{"name":"owned library","kind":"shared","project_id":"` + project + `"}`} {
		rejected := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/knowledge-bases", strings.NewReader(body))
		addWriteSecret(a, req)
		a.createKnowledgeBase(rejected, req)
		if rejected.Code != http.StatusBadRequest {
			t.Fatalf("project-owned library should be rejected: %d %s", rejected.Code, rejected.Body)
		}
	}
	shared := createSharedKnowledgeBase(t, a, "shared")
	list := httptest.NewRecorder()
	a.listKnowledgeBases(list, httptest.NewRequest(http.MethodGet, "/api/v1/knowledge-bases", nil))
	if list.Code != 200 || !strings.Contains(list.Body.String(), shared) || !strings.Contains(list.Body.String(), `"kind":"shared"`) {
		t.Fatalf("shared library list: %d %s", list.Code, list.Body)
	}
	renamed := callManagement(t, a, http.MethodPatch, "/api/v1/knowledge-bases/"+shared, `{"name":"renamed"}`, a.updateKnowledgeBase, true)
	if renamed.Code != http.StatusOK {
		t.Fatalf("rename shared library: %d %s", renamed.Code, renamed.Body)
	}
	for _, body := range []string{`{"kind":"project"}`, `{"project_id":"` + project + `"}`} {
		rejected := callManagement(t, a, http.MethodPatch, "/api/v1/knowledge-bases/"+shared, body, a.updateKnowledgeBase, true)
		if rejected.Code != http.StatusBadRequest {
			t.Fatalf("shared library scope change should be rejected: %d %s", rejected.Code, rejected.Body)
		}
	}
	if _, err := a.db.Exec(`INSERT INTO memories(id,knowledge_base_id,title,content,created_at,updated_at) VALUES('shared-memory',?,'shared','shared body','n','n')`, shared); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.Exec(`INSERT INTO memory_revisions(memory_id,title,content,category,status,tags_json,updated_at,revised_at) VALUES('shared-memory','shared','shared body','','active','[]','n','n')`); err != nil {
		t.Fatal(err)
	}
	deleted := callManagement(t, a, http.MethodDelete, "/api/v1/knowledge-bases/"+shared, `{"confirm":true}`, a.deleteKnowledgeBase, true)
	if deleted.Code != http.StatusNoContent {
		t.Fatalf("delete shared library: %d %s", deleted.Code, deleted.Body)
	}
	var memories, revisions int
	_ = a.db.QueryRow(`SELECT count(*) FROM memories WHERE knowledge_base_id=?`, shared).Scan(&memories)
	_ = a.db.QueryRow(`SELECT count(*) FROM memory_revisions WHERE memory_id='shared-memory'`).Scan(&revisions)
	if memories != 0 || revisions != 0 {
		t.Fatalf("shared library deletion left data: memories=%d revisions=%d", memories, revisions)
	}
}

func TestProjectDeletionRequiresConfirmationAndRemovesProjectData(t *testing.T) {
	a := managementApp(t)
	project := createManagementProject(t, a, "delete-me")
	if _, err := a.db.Exec(`INSERT INTO memories(id,project_id,title,content,created_at,updated_at) VALUES('delete-me-memory',?,'title','indexed project data','n','n')`, project); err != nil {
		t.Fatal(err)
	}
	shared := createSharedKnowledgeBase(t, a, "keep-shared")
	if _, err := a.db.Exec(`INSERT INTO memories(id,knowledge_base_id,title,content,created_at,updated_at) VALUES('keep-shared-memory',?,'shared','shared survives project deletion','n','n')`, shared); err != nil {
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
	if err := a.db.QueryRow(`SELECT count(*) FROM memories WHERE id='keep-shared-memory'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("shared memory removed with project: count=%d err=%v", count, err)
	}
}

func TestProjectMergePreservesMemoriesAndRepositoryConflictRule(t *testing.T) {
	a := managementApp(t)
	source := createManagementProject(t, a, "source")
	target := createManagementProject(t, a, "target")
	shared := createSharedKnowledgeBase(t, a, "merge-shared")
	_, _ = a.db.Exec(`INSERT INTO memories(id,project_id,title,content,created_at,updated_at) VALUES('kept',?,'kept','merge searchable content','n','n')`, source)
	_, _ = a.db.Exec(`INSERT INTO memories(id,knowledge_base_id,title,content,created_at,updated_at) VALUES('shared-kept',?,'shared','shared merge content','n','n')`, shared)
	_, _ = a.db.Exec(`INSERT INTO repositories(canonical,project_id) VALUES('github.com/same/repo',?)`, target)
	_, _ = a.db.Exec(`INSERT INTO repositories(canonical,project_id) VALUES('github.com/same/repo-source',?)`, source)
	merged := callManagement(t, a, "POST", "/api/v1/projects/"+source+"/merge", `{"target_project_id":"`+target+`","confirm":true}`, a.mergeProject, true)
	if merged.Code != 200 {
		t.Fatalf("merge: %d %s", merged.Code, merged.Body)
	}
	var count int
	if err := a.db.QueryRow(`SELECT count(*) FROM memories WHERE id='kept' AND project_id=?`, target).Scan(&count); err != nil || count != 1 {
		t.Fatalf("merged memory lost: count=%d err=%v", count, err)
	}
	if err := a.db.QueryRow(`SELECT count(*) FROM memories WHERE id='shared-kept' AND knowledge_base_id=?`, shared).Scan(&count); err != nil || count != 1 {
		t.Fatalf("shared memory lost during merge: count=%d err=%v", count, err)
	}
	if err := a.db.QueryRow(`SELECT count(*) FROM project_operations WHERE source_project_id=? AND target_project_id=?`, source, target).Scan(&count); err != nil || count != 1 {
		t.Fatalf("merge operation missing: %d %v", count, err)
	}
}

func TestProjectAndSharedMemoryScopes(t *testing.T) {
	a := managementApp(t)
	local := createManagementProject(t, a, "scope-local")
	foreign := createManagementProject(t, a, "scope-foreign")
	shared := createSharedKnowledgeBase(t, a, "scope-shared")
	for _, memory := range []struct {
		id, project, kb, title, content string
	}{
		{"local-memory", local, "", "local", "scope local content"},
		{"foreign-memory", foreign, "", "foreign", "scope foreign content"},
		{"shared-memory", "", shared, "shared", "scope shared content"},
	} {
		var projectValue, kbValue any
		if memory.project != "" {
			projectValue = memory.project
		} else {
			kbValue = memory.kb
		}
		if _, err := a.db.Exec(`INSERT INTO memories(id,project_id,knowledge_base_id,title,content,created_at,updated_at) VALUES(?,?,?,?,?,'n','n')`, memory.id, projectValue, kbValue, memory.title, memory.content); err != nil {
			t.Fatal(err)
		}
	}
	list := func(project, kb string) string {
		t.Helper()
		path := "/api/v1/memories?project_id=" + project
		if kb != "" {
			path += "&knowledge_base_id=" + kb
		}
		rec := httptest.NewRecorder()
		a.listMemories(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("list %s: %d %s", path, rec.Code, rec.Body)
		}
		return rec.Body.String()
	}
	search := func(project, kb string) string {
		t.Helper()
		body := `{"project_id":"` + project + `","query":"scope content"`
		if kb != "" {
			body += `,"knowledge_base_id":"` + kb + `"`
		}
		body += `}`
		rec := httptest.NewRecorder()
		a.searchMemories(rec, httptest.NewRequest(http.MethodPost, "/api/v1/memories/search", strings.NewReader(body)))
		if rec.Code != http.StatusOK {
			t.Fatalf("search %s: %d %s", body, rec.Code, rec.Body)
		}
		return rec.Body.String()
	}
	localList, foreignList := list(local, ""), list(foreign, "")
	if !strings.Contains(localList, "local-memory") || !strings.Contains(localList, "shared-memory") || strings.Contains(localList, "foreign-memory") {
		t.Fatalf("local list scope incorrect: %s", localList)
	}
	if !strings.Contains(foreignList, "foreign-memory") || !strings.Contains(foreignList, "shared-memory") || strings.Contains(foreignList, "local-memory") {
		t.Fatalf("foreign list scope incorrect: %s", foreignList)
	}
	if filtered := list(local, shared); strings.Contains(filtered, "local-memory") || !strings.Contains(filtered, "shared-memory") {
		t.Fatalf("shared library filter returned project memories: %s", filtered)
	}
	projectList := httptest.NewRecorder()
	a.listMemories(projectList, httptest.NewRequest(http.MethodGet, "/api/v1/memories?project_id="+local+"&project_only=true", nil))
	if projectList.Code != http.StatusOK || !strings.Contains(projectList.Body.String(), "local-memory") || strings.Contains(projectList.Body.String(), "shared-memory") || strings.Contains(projectList.Body.String(), "foreign-memory") {
		t.Fatalf("project-only list scope incorrect: %d %s", projectList.Code, projectList.Body)
	}
	projectSearch := httptest.NewRecorder()
	a.searchMemories(projectSearch, httptest.NewRequest(http.MethodPost, "/api/v1/memories/search", strings.NewReader(`{"project_id":"`+local+`","query":"scope content","project_only":true}`)))
	if projectSearch.Code != http.StatusOK || !strings.Contains(projectSearch.Body.String(), "local-memory") || strings.Contains(projectSearch.Body.String(), "shared-memory") || strings.Contains(projectSearch.Body.String(), "foreign-memory") {
		t.Fatalf("project-only search scope incorrect: %d %s", projectSearch.Code, projectSearch.Body)
	}
	conflictList := httptest.NewRecorder()
	a.listMemories(conflictList, httptest.NewRequest(http.MethodGet, "/api/v1/memories?project_id="+local+"&project_only=true&knowledge_base_id="+shared, nil))
	if conflictList.Code != http.StatusBadRequest {
		t.Fatalf("project-only list conflict status=%d", conflictList.Code)
	}
	conflictSearch := httptest.NewRecorder()
	a.searchMemories(conflictSearch, httptest.NewRequest(http.MethodPost, "/api/v1/memories/search", strings.NewReader(`{"project_id":"`+local+`","query":"scope","project_only":true,"knowledge_base_id":"`+shared+`"}`)))
	if conflictSearch.Code != http.StatusBadRequest {
		t.Fatalf("project-only search conflict status=%d", conflictSearch.Code)
	}
	if results := search(local, ""); !strings.Contains(results, "local-memory") || !strings.Contains(results, "shared-memory") || strings.Contains(results, "foreign-memory") {
		t.Fatalf("default search scope incorrect: %s", results)
	}
	if results := search(local, shared); strings.Contains(results, "local-memory") || !strings.Contains(results, "shared-memory") {
		t.Fatalf("shared library search returned project memories: %s", results)
	}
	get := func(project, id string) int {
		t.Helper()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/memories/"+id+"?project_id="+project, nil)
		req.SetPathValue("id", id)
		a.getMemory(rec, req)
		return rec.Code
	}
	if code := get(local, "foreign-memory"); code != http.StatusNotFound {
		t.Fatalf("foreign project memory detail status=%d", code)
	}
	if code := get(local, "shared-memory"); code != http.StatusOK {
		t.Fatalf("shared memory detail status=%d", code)
	}
}

func TestMemoryOwnershipIsMutuallyExclusive(t *testing.T) {
	a := managementApp(t)
	project := createManagementProject(t, a, "ownership")
	shared := createSharedKnowledgeBase(t, a, "ownership-shared")
	for _, tc := range []struct {
		name, project, kb any
	}{
		{"both", project, shared},
		{"neither", nil, nil},
	} {
		_, err := a.db.Exec(`INSERT INTO memories(id,project_id,knowledge_base_id,title,content,created_at,updated_at) VALUES(?,?,?,'title','body','n','n')`, tc.name, tc.project, tc.kb)
		if err == nil {
			t.Fatalf("memory ownership %q should violate the constraint", tc.name)
		}
	}
}
