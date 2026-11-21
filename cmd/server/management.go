package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

func (a *app) projectDetail(w http.ResponseWriter, r *http.Request) {
	var p struct{ ID, Name, CreatedAt string }
	if err := a.db.QueryRowContext(r.Context(), `SELECT id,name,created_at FROM projects WHERE id=?`, r.PathValue("id")).Scan(&p.ID, &p.Name, &p.CreatedAt); err != nil {
		writeError(w, 404, "project not found")
		return
	}
	rows, err := a.db.QueryContext(r.Context(), `SELECT canonical FROM repositories WHERE project_id=? ORDER BY canonical`, p.ID)
	if err != nil {
		writeError(w, 500, "database error")
		return
	}
	defer rows.Close()
	repos := []string{}
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			writeError(w, 500, "database error")
			return
		}
		repos = append(repos, v)
	}
	if rows.Err() != nil {
		writeError(w, 500, "database error")
		return
	}
	writeJSON(w, 200, map[string]any{"id": p.ID, "name": p.Name, "created_at": p.CreatedAt, "repositories": repos})
}

func (a *app) bindRepository(w http.ResponseWriter, r *http.Request) {
	if !a.verifyWriteSignature(w, r) {
		return
	}
	var req struct {
		Repository string `json:"repository"`
	}
	if !decode(w, r, &req) {
		return
	}
	repo, err := normalizeRepository(req.Repository)
	if err != nil || repo == "" {
		writeError(w, 400, "valid repository is required")
		return
	}
	id := r.PathValue("id")
	if !a.projectExists(w, r, id) {
		return
	}
	_, err = a.db.ExecContext(r.Context(), `INSERT INTO repositories(canonical,project_id) VALUES(?,?)`, repo, id)
	if err != nil {
		writeError(w, 409, "repository is already bound or could not be bound")
		return
	}
	writeJSON(w, 201, map[string]string{"repository": repo, "project_id": id})
}
func (a *app) unbindRepository(w http.ResponseWriter, r *http.Request) {
	if !a.verifyWriteSignature(w, r) {
		return
	}
	var req struct {
		Repository string `json:"repository"`
	}
	if !decode(w, r, &req) {
		return
	}
	repo, err := normalizeRepository(req.Repository)
	if err != nil || repo == "" {
		writeError(w, 400, "valid repository is required")
		return
	}
	res, err := a.db.ExecContext(r.Context(), `DELETE FROM repositories WHERE canonical=? AND project_id=?`, repo, r.PathValue("id"))
	if err != nil {
		writeError(w, 500, "database error")
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		writeError(w, 404, "repository binding not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func (a *app) deleteProject(w http.ResponseWriter, r *http.Request) {
	if !a.verifyWriteSignature(w, r) {
		return
	}
	var req struct {
		Confirm bool `json:"confirm"`
	}
	if !decode(w, r, &req) {
		return
	}
	if !req.Confirm {
		writeError(w, 400, "confirm must be true to delete project and its data")
		return
	}
	tx, err := a.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeError(w, 500, "database error")
		return
	}
	defer tx.Rollback()
	id := r.PathValue("id")
	var exists int
	if err := tx.QueryRowContext(r.Context(), `SELECT 1 FROM projects WHERE id=?`, id).Scan(&exists); err != nil {
		writeError(w, 404, "project not found")
		return
	}
	if _, err := tx.ExecContext(r.Context(), `DELETE FROM memory_revisions WHERE memory_id IN (SELECT m.id FROM memories m JOIN knowledge_bases k ON k.id=m.knowledge_base_id WHERE k.project_id=?)`, id); err != nil {
		writeError(w, 500, "could not delete project history")
		return
	}
	if _, err := tx.ExecContext(r.Context(), `DELETE FROM memories WHERE knowledge_base_id IN (SELECT id FROM knowledge_bases WHERE project_id=?)`, id); err != nil {
		writeError(w, 500, "could not delete project memories")
		return
	}
	if _, err := tx.ExecContext(r.Context(), `DELETE FROM projects WHERE id=?`, id); err != nil {
		writeError(w, 500, "could not delete project")
		return
	}
	if err := tx.Commit(); err != nil {
		writeError(w, 500, "database error")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func (a *app) mergeProject(w http.ResponseWriter, r *http.Request) {
	if !a.verifyWriteSignature(w, r) {
		return
	}
	var req struct {
		TargetID string `json:"target_project_id"`
		Confirm  bool   `json:"confirm"`
	}
	if !decode(w, r, &req) {
		return
	}
	source := r.PathValue("id")
	if source == req.TargetID || req.TargetID == "" || !req.Confirm {
		writeError(w, 400, "a different target_project_id and confirm=true are required")
		return
	}
	tx, err := a.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeError(w, 500, "database error")
		return
	}
	defer tx.Rollback()
	var n int
	if err = tx.QueryRowContext(r.Context(), `SELECT count(*) FROM projects WHERE id IN (?,?)`, source, req.TargetID).Scan(&n); err != nil || n != 2 {
		writeError(w, 404, "source or target project not found")
		return
	}
	// Keep both projects' memories by converting the source default library to a regular project library before moving it.
	if _, err = tx.ExecContext(r.Context(), `UPDATE knowledge_bases SET is_default=0,name=name || '（合并）' WHERE project_id=? AND is_default=1`, source); err != nil {
		writeError(w, 500, "could not prepare source default library")
		return
	}
	if _, err = tx.ExecContext(r.Context(), `UPDATE knowledge_bases SET project_id=? WHERE project_id=? AND kind='project'`, req.TargetID, source); err != nil {
		writeError(w, 500, "could not move project knowledge bases")
		return
	}
	if _, err = tx.ExecContext(r.Context(), `UPDATE repositories SET project_id=? WHERE project_id=? AND canonical NOT IN (SELECT canonical FROM repositories WHERE project_id=?)`, req.TargetID, source, req.TargetID); err != nil {
		writeError(w, 500, "could not move repository bindings")
		return
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err = tx.ExecContext(r.Context(), `INSERT INTO project_operations(source_project_id,target_project_id,action,created_at) VALUES(?,?,?,?)`, source, req.TargetID, "merge", now); err != nil {
		writeError(w, 500, "could not record merge")
		return
	}
	if _, err = tx.ExecContext(r.Context(), `DELETE FROM projects WHERE id=?`, source); err != nil {
		writeError(w, 500, "could not remove merged project")
		return
	}
	if err = tx.Commit(); err != nil {
		writeError(w, 500, "database error")
		return
	}
	writeJSON(w, 200, map[string]string{"project_id": req.TargetID, "merged_project_id": source})
}

func (a *app) listKnowledgeBases(w http.ResponseWriter, r *http.Request) {
	rows, err := a.db.QueryContext(r.Context(), `SELECT id,coalesce(project_id,''),name,kind,is_default,created_at FROM knowledge_bases ORDER BY kind,name,id`)
	if err != nil {
		writeError(w, 500, "database error")
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id, pid, name, kind, created string
		var def int
		if err := rows.Scan(&id, &pid, &name, &kind, &def, &created); err != nil {
			writeError(w, 500, "database error")
			return
		}
		if grant, ok := grantFromRequest(r); ok {
			if kind == "shared" && !grant.canShared(false) || kind == "project" && !grant.canProject(pid, false) {
				continue
			}
		}
		items = append(items, map[string]any{"id": id, "project_id": pid, "name": name, "kind": kind, "is_default": def == 1, "created_at": created})
	}
	if rows.Err() != nil {
		writeError(w, 500, "database error")
		return
	}
	writeJSON(w, 200, map[string]any{"knowledge_bases": items})
}
func (a *app) updateKnowledgeBase(w http.ResponseWriter, r *http.Request) {
	if !a.verifyWriteSignature(w, r) {
		return
	}
	var req struct {
		Name      string `json:"name"`
		Kind      string `json:"kind"`
		ProjectID string `json:"project_id"`
	}
	if !decode(w, r, &req) {
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	tx, err := a.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeError(w, 500, "database error")
		return
	}
	defer tx.Rollback()
	var oldKind, oldName string
	var oldProject sql.NullString
	var isDefault int
	if err := tx.QueryRowContext(r.Context(), `SELECT kind,project_id,name,is_default FROM knowledge_bases WHERE id=?`, r.PathValue("id")).Scan(&oldKind, &oldProject, &oldName, &isDefault); err != nil {
		writeError(w, 404, "knowledge base not found")
		return
	}
	if req.Name == "" {
		req.Name = oldName
	}
	if len(req.Name) > 200 {
		writeError(w, 400, "knowledge base name must be <= 200 bytes")
		return
	}
	kind := oldKind
	if req.Kind != "" {
		kind = req.Kind
	}
	pid := oldProject.String
	if req.ProjectID != "" {
		pid = req.ProjectID
	} else if kind == "shared" {
		pid = ""
	}
	if kind != "project" && kind != "shared" || (kind == "project" && pid == "") || (kind == "shared" && pid != "") {
		writeError(w, 400, "valid kind/project_id combination required; shared libraries have no project_id")
		return
	}
	if isDefault == 1 && (kind != "project" || pid != oldProject.String) {
		writeError(w, 409, "default project knowledge base cannot change type or project; create a replacement library first")
		return
	}
	if kind == "project" {
		var exists int
		if err := tx.QueryRowContext(r.Context(), `SELECT 1 FROM projects WHERE id=?`, pid).Scan(&exists); errors.Is(err, sql.ErrNoRows) {
			writeError(w, 404, "project not found")
			return
		} else if err != nil {
			writeError(w, 500, "database error")
			return
		}
	}
	defaultValue := isDefault
	if kind == "shared" || pid != oldProject.String {
		defaultValue = 0
	}
	if _, err := tx.ExecContext(r.Context(), `UPDATE knowledge_bases SET name=?,kind=?,project_id=?,is_default=? WHERE id=?`, req.Name, kind, nullString(pid), defaultValue, r.PathValue("id")); err != nil {
		writeError(w, 409, "could not update knowledge base; project default conflicts are not allowed")
		return
	}
	if err := tx.Commit(); err != nil {
		writeError(w, 500, "database error")
		return
	}
	writeJSON(w, 200, map[string]string{"id": r.PathValue("id"), "name": req.Name, "kind": kind, "project_id": pid})
}
func (a *app) deleteKnowledgeBase(w http.ResponseWriter, r *http.Request) {
	if !a.verifyWriteSignature(w, r) {
		return
	}
	var req struct {
		Confirm bool `json:"confirm"`
	}
	if !decode(w, r, &req) {
		return
	}
	if !req.Confirm {
		writeError(w, 400, "confirm must be true to delete knowledge base and its memories")
		return
	}
	var def int
	if err := a.db.QueryRowContext(r.Context(), `SELECT is_default FROM knowledge_bases WHERE id=?`, r.PathValue("id")).Scan(&def); err != nil {
		writeError(w, 404, "knowledge base not found")
		return
	}
	if def == 1 {
		writeError(w, 409, "default project knowledge base cannot be deleted")
		return
	}
	tx, err := a.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeError(w, 500, "database error")
		return
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(r.Context(), `DELETE FROM memory_revisions WHERE memory_id IN (SELECT id FROM memories WHERE knowledge_base_id=?)`, r.PathValue("id")); err != nil {
		writeError(w, 500, "could not delete knowledge base history")
		return
	}
	if _, err := tx.ExecContext(r.Context(), `DELETE FROM memories WHERE knowledge_base_id=?`, r.PathValue("id")); err != nil {
		writeError(w, 500, "could not delete knowledge base memories")
		return
	}
	if _, err := tx.ExecContext(r.Context(), `DELETE FROM knowledge_bases WHERE id=?`, r.PathValue("id")); err != nil {
		writeError(w, 500, "could not delete knowledge base")
		return
	}
	if err := tx.Commit(); err != nil {
		writeError(w, 500, "database error")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *app) listMemories(w http.ResponseWriter, r *http.Request) {
	projectID := r.URL.Query().Get("project_id")
	kbID := r.URL.Query().Get("knowledge_base_id")
	status := r.URL.Query().Get("status")
	tag := strings.TrimSpace(r.URL.Query().Get("tag"))
	if projectID == "" {
		writeError(w, 400, "project_id is required")
		return
	}
	if !a.projectExists(w, r, projectID) {
		return
	}
	if status == "" {
		status = "active"
	}
	if status != "active" && status != "deleted" {
		writeError(w, 400, "status must be active or deleted")
		return
	}
	if kbID != "" {
		var accessible int
		err := a.db.QueryRowContext(r.Context(), `SELECT 1 FROM knowledge_bases WHERE id=? AND (kind='shared' OR project_id=?)`, kbID, projectID).Scan(&accessible)
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, 404, "knowledge base not found for project")
			return
		}
		if err != nil {
			writeError(w, 500, "database error")
			return
		}
	}
	query := `SELECT m.id,m.knowledge_base_id,k.name,k.kind,m.title,m.content,m.category,m.status,m.created_at,m.updated_at,COALESCE((SELECT json_group_array(tag) FROM (SELECT tag FROM memory_tags WHERE memory_id=m.id ORDER BY tag)), '[]') FROM memories m JOIN knowledge_bases k ON k.id=m.knowledge_base_id WHERE m.status=? AND (k.kind='shared' OR k.project_id=?)`
	args := []any{status, projectID}
	if grant, ok := grantFromRequest(r); ok && !grant.canShared(false) && kbID == "" {
		query += ` AND k.kind='project'`
	}
	if kbID != "" {
		query += ` AND k.id=?`
		args = append(args, kbID)
	}
	if tag != "" {
		query += ` AND EXISTS(SELECT 1 FROM memory_tags t WHERE t.memory_id=m.id AND t.tag=?)`
		args = append(args, tag)
	}
	query += ` ORDER BY m.updated_at DESC LIMIT 200`
	rows, err := a.db.QueryContext(r.Context(), query, args...)
	if err != nil {
		writeError(w, 500, "database error")
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id, kb, kbName, kbKind, title, content, category, state, created, updated, tagsJSON string
		if err := rows.Scan(&id, &kb, &kbName, &kbKind, &title, &content, &category, &state, &created, &updated, &tagsJSON); err != nil {
			writeError(w, 500, "database error")
			return
		}
		items = append(items, map[string]any{"id": id, "knowledge_base_id": kb, "knowledge_base_name": kbName, "knowledge_base_type": kbKind, "title": title, "content": content, "category": category, "status": state, "created_at": created, "updated_at": updated, "tags": json.RawMessage(tagsJSON)})
	}
	if rows.Err() != nil {
		writeError(w, 500, "database error")
		return
	}
	writeJSON(w, 200, map[string]any{"memories": items})
}
func (a *app) listProjectMemories(w http.ResponseWriter, r *http.Request) {
	r.URL.RawQuery = "project_id=" + r.PathValue("id") + "&" + r.URL.RawQuery
	a.listMemories(w, r)
}
func (a *app) getMemory(w http.ResponseWriter, r *http.Request) {
	projectID := r.URL.Query().Get("project_id")
	if !a.memoryAccessible(w, r, r.PathValue("id"), projectID) {
		return
	}
	var item map[string]any
	var kb, title, content, category, state, created, updated string
	err := a.db.QueryRowContext(r.Context(), `SELECT knowledge_base_id,title,content,category,status,created_at,updated_at FROM memories WHERE id=?`, r.PathValue("id")).Scan(&kb, &title, &content, &category, &state, &created, &updated)
	if err != nil {
		writeError(w, 404, "memory not found")
		return
	}
	item = map[string]any{"id": r.PathValue("id"), "knowledge_base_id": kb, "title": title, "content": content, "category": category, "status": state, "created_at": created, "updated_at": updated, "tags": a.memoryTags(r, r.PathValue("id"))}
	writeJSON(w, 200, item)
}
func (a *app) memoryAccessible(w http.ResponseWriter, r *http.Request, memoryID, projectID string) bool {
	if projectID == "" {
		writeError(w, 400, "project_id is required")
		return false
	}
	if !a.projectExists(w, r, projectID) {
		return false
	}
	var accessible int
	err := a.db.QueryRowContext(r.Context(), `SELECT 1 FROM memories m JOIN knowledge_bases k ON k.id=m.knowledge_base_id WHERE m.id=? AND (k.kind='shared' OR k.project_id=?)`, memoryID, projectID).Scan(&accessible)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, 404, "memory not found for project")
		return false
	}
	if err != nil {
		writeError(w, 500, "database error")
		return false
	}
	return true
}

func (a *app) memoryTags(r *http.Request, id string) []string {
	rows, err := a.db.QueryContext(r.Context(), `SELECT tag FROM memory_tags WHERE memory_id=? ORDER BY tag`, id)
	if err != nil {
		return []string{}
	}
	defer rows.Close()
	tags := []string{}
	for rows.Next() {
		var tag string
		if rows.Scan(&tag) == nil {
			tags = append(tags, tag)
		}
	}
	return tags
}
func cleanTags(tags []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, tag := range tags {
		tag = strings.TrimSpace(tag)
		if tag != "" && !seen[tag] {
			seen[tag] = true
			out = append(out, tag)
		}
	}
	return out
}
func saveRevision(tx *sql.Tx, id string) error {
	var title, content, category, status, updated string
	if err := tx.QueryRow(`SELECT title,content,category,status,updated_at FROM memories WHERE id=?`, id).Scan(&title, &content, &category, &status, &updated); err != nil {
		return err
	}
	rows, err := tx.Query(`SELECT tag FROM memory_tags WHERE memory_id=? ORDER BY tag`, id)
	if err != nil {
		return err
	}
	tags := []string{}
	for rows.Next() {
		var tag string
		if err := rows.Scan(&tag); err != nil {
			rows.Close()
			return err
		}
		tags = append(tags, tag)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	tagsJSON, err := json.Marshal(tags)
	if err != nil {
		return err
	}
	_, err = tx.Exec(`INSERT INTO memory_revisions(memory_id,title,content,category,status,tags_json,updated_at,revised_at) VALUES(?,?,?,?,?,?,?,?)`, id, title, content, category, status, string(tagsJSON), updated, time.Now().UTC().Format(time.RFC3339Nano))
	return err
}
func (a *app) updateMemory(w http.ResponseWriter, r *http.Request) {
	if !a.verifyWriteSignature(w, r) {
		return
	}
	var req struct {
		ProjectID string    `json:"project_id"`
		Title     *string   `json:"title"`
		Content   *string   `json:"content"`
		Category  *string   `json:"category"`
		Tags      *[]string `json:"tags"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.Title == nil && req.Content == nil && req.Category == nil && req.Tags == nil {
		writeError(w, 400, "at least one field is required")
		return
	}
	if !a.memoryAccessible(w, r, r.PathValue("id"), req.ProjectID) {
		return
	}
	tx, err := a.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeError(w, 500, "database error")
		return
	}
	defer tx.Rollback()
	id := r.PathValue("id")
	if err = saveRevision(tx, id); err != nil {
		writeError(w, 404, "memory not found")
		return
	}
	var title, content, category string
	if err = tx.QueryRowContext(r.Context(), `SELECT title,content,category FROM memories WHERE id=?`, id).Scan(&title, &content, &category); err != nil {
		writeError(w, 404, "memory not found")
		return
	}
	if req.Title != nil {
		title = strings.TrimSpace(*req.Title)
	}
	if req.Content != nil {
		content = strings.TrimSpace(*req.Content)
	}
	if req.Category != nil {
		category = strings.TrimSpace(*req.Category)
	}
	if title == "" || content == "" || len(title) > 300 || len(content) > 20000 {
		writeError(w, 400, "title and content are required (title <= 300, content <= 20000 bytes)")
		return
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err = tx.ExecContext(r.Context(), `UPDATE memories SET title=?,content=?,category=?,updated_at=? WHERE id=?`, title, content, category, now, id); err != nil {
		writeError(w, 500, "could not update memory")
		return
	}
	if req.Tags != nil {
		if _, err = tx.ExecContext(r.Context(), `DELETE FROM memory_tags WHERE memory_id=?`, id); err != nil {
			writeError(w, 500, "database error")
			return
		}
		for _, tag := range cleanTags(*req.Tags) {
			if len(tag) > 100 {
				writeError(w, 400, "tag must be <= 100 bytes")
				return
			}
			if _, err = tx.ExecContext(r.Context(), `INSERT INTO memory_tags(memory_id,tag) VALUES(?,?)`, id, tag); err != nil {
				writeError(w, 500, "could not save tags")
				return
			}
		}
	}
	if err = tx.Commit(); err != nil {
		writeError(w, 500, "database error")
		return
	}
	writeJSON(w, 200, map[string]string{"id": id, "updated_at": now})
}
func (a *app) deleteMemory(w http.ResponseWriter, r *http.Request) {
	a.setMemoryStatus(w, r, "deleted")
}
func (a *app) restoreMemory(w http.ResponseWriter, r *http.Request) {
	a.setMemoryStatus(w, r, "active")
}
func (a *app) setMemoryStatus(w http.ResponseWriter, r *http.Request, status string) {
	if !a.verifyWriteSignature(w, r) {
		return
	}
	var req struct {
		ProjectID string `json:"project_id"`
	}
	if !decode(w, r, &req) {
		return
	}
	if !a.memoryAccessible(w, r, r.PathValue("id"), req.ProjectID) {
		return
	}
	tx, err := a.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeError(w, 500, "database error")
		return
	}
	defer tx.Rollback()
	id := r.PathValue("id")
	if err = saveRevision(tx, id); err != nil {
		writeError(w, 404, "memory not found")
		return
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	res, err := tx.ExecContext(r.Context(), `UPDATE memories SET status=?,updated_at=? WHERE id=?`, status, now, id)
	if err != nil {
		writeError(w, 500, "database error")
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		writeError(w, 404, "memory not found")
		return
	}
	if err = tx.Commit(); err != nil {
		writeError(w, 500, "database error")
		return
	}
	writeJSON(w, 200, map[string]string{"id": id, "status": status})
}
func (a *app) permanentlyDeleteMemory(w http.ResponseWriter, r *http.Request) {
	if !a.verifyWriteSignature(w, r) {
		return
	}
	var req struct {
		ProjectID string `json:"project_id"`
		Confirm   bool   `json:"confirm"`
	}
	if !decode(w, r, &req) {
		return
	}
	if !a.memoryAccessible(w, r, r.PathValue("id"), req.ProjectID) {
		return
	}
	if !req.Confirm {
		writeError(w, 400, "confirm must be true to permanently delete memory")
		return
	}
	var status string
	if err := a.db.QueryRowContext(r.Context(), `SELECT status FROM memories WHERE id=?`, r.PathValue("id")).Scan(&status); err != nil {
		writeError(w, 404, "memory not found")
		return
	}
	if status != "deleted" {
		writeError(w, 409, "memory must be soft-deleted before permanent deletion")
		return
	}
	tx, err := a.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeError(w, 500, "database error")
		return
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(r.Context(), `DELETE FROM memory_revisions WHERE memory_id=?`, r.PathValue("id")); err != nil {
		writeError(w, 500, "could not delete memory history")
		return
	}
	res, err := tx.ExecContext(r.Context(), `DELETE FROM memories WHERE id=?`, r.PathValue("id"))
	if err != nil {
		writeError(w, 500, "database error")
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		writeError(w, 404, "memory not found")
		return
	}
	if err := tx.Commit(); err != nil {
		writeError(w, 500, "database error")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func (a *app) memoryHistory(w http.ResponseWriter, r *http.Request) {
	projectID := r.URL.Query().Get("project_id")
	if !a.memoryAccessible(w, r, r.PathValue("id"), projectID) {
		return
	}
	rows, err := a.db.QueryContext(r.Context(), `SELECT id,title,content,category,status,tags_json,updated_at,revised_at FROM memory_revisions WHERE memory_id=? ORDER BY id DESC`, r.PathValue("id"))
	if err != nil {
		writeError(w, 500, "database error")
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id int64
		var title, content, category, status, tags, updated, revised string
		if err := rows.Scan(&id, &title, &content, &category, &status, &tags, &updated, &revised); err != nil {
			writeError(w, 500, "database error")
			return
		}
		items = append(items, map[string]any{"id": fmt.Sprint(id), "title": title, "content": content, "category": category, "status": status, "tags": json.RawMessage(tags), "updated_at": updated, "revised_at": revised})
	}
	if rows.Err() != nil {
		writeError(w, 500, "database error")
		return
	}
	writeJSON(w, 200, map[string]any{"revisions": items})
}
