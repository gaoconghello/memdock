package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type app struct {
	db                *sql.DB
	approvalKey       []byte
	approvalPublicKey ed25519.PublicKey
	trustedProxyCIDRs []*net.IPNet
	projectGrants     map[string]accessGrant
	mode              string
}

type apiError struct {
	Error string `json:"error"`
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "keygen" {
		if err := generateApprovalKey(os.Args[2:]); err != nil {
			log.Fatal(err)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "approve" {
		if err := approveWrite(os.Args[2:]); err != nil {
			log.Fatal(err)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "approve-request" {
		if err := approveRequest(os.Args[2:]); err != nil {
			log.Fatal(err)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		db, err := sql.Open("sqlite", env("MEMDOCK_DB_PATH", "memdock.db"))
		if err != nil {
			log.Fatal(err)
		}
		defer db.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := db.PingContext(ctx); err != nil {
			log.Fatal(err)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "restore" {
		if len(os.Args) != 4 {
			log.Fatal("usage: memdock-server restore BACKUP.sqlite DATABASE.sqlite")
		}
		if err := restoreDatabase(os.Args[2], os.Args[3]); err != nil {
			log.Fatal(err)
		}
		return
	}
	addr := env("MEMDOCK_ADDR", ":8080")
	dbPath := env("MEMDOCK_DB_PATH", "memdock.db")
	mode := env("MEMDOCK_MODE", "private")
	readToken, writeToken := os.Getenv("MEMDOCK_READ_TOKEN"), os.Getenv("MEMDOCK_WRITE_TOKEN")
	publicKey, keyErr := hex.DecodeString(os.Getenv("MEMDOCK_APPROVAL_PUBLIC_KEY"))
	var projectGrants map[string]accessGrant
	tlsCert, tlsKey := os.Getenv("MEMDOCK_TLS_CERT"), os.Getenv("MEMDOCK_TLS_KEY")
	proxyTLS := os.Getenv("MEMDOCK_TRUST_PROXY_TLS") == "true"
	var trustedProxyCIDRs []*net.IPNet
	if proxyTLS {
		var proxyErr error
		trustedProxyCIDRs, proxyErr = parseTrustedProxyCIDRs(os.Getenv("MEMDOCK_TRUST_PROXY_CIDRS"))
		if proxyErr != nil {
			log.Fatal(proxyErr)
		}
	} else if os.Getenv("MEMDOCK_TRUST_PROXY_CIDRS") != "" {
		log.Fatal("MEMDOCK_TRUST_PROXY_CIDRS requires MEMDOCK_TRUST_PROXY_TLS=true")
	}
	if mode != "private" && mode != "secure" {
		log.Fatal("MEMDOCK_MODE must be private or secure")
	}
	if mode == "secure" && (len(readToken) < 32 || len(writeToken) < 32 || readToken == writeToken || keyErr != nil || len(publicKey) != ed25519.PublicKeySize) {
		log.Fatal("secure mode requires distinct 32+ byte MEMDOCK_READ_TOKEN/MEMDOCK_WRITE_TOKEN and a 32-byte hex MEMDOCK_APPROVAL_PUBLIC_KEY")
	}
	if mode == "secure" {
		var aclErr error
		projectGrants, aclErr = loadAccessGrants(os.Getenv("MEMDOCK_PROJECT_ACL_FILE"), readToken, writeToken)
		if aclErr != nil {
			log.Fatal(aclErr)
		}
	}
	if (tlsCert == "") != (tlsKey == "") || (mode == "secure" && tlsCert == "" && !proxyTLS) {
		log.Fatal("secure mode requires both MEMDOCK_TLS_CERT/MEMDOCK_TLS_KEY or MEMDOCK_TRUST_PROXY_TLS=true")
	}
	if mode == "private" && proxyTLS {
		log.Fatal("MEMDOCK_TRUST_PROXY_TLS is only valid in secure mode")
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		log.Fatal(err)
	}
	for _, statement := range []string{"PRAGMA foreign_keys=ON", "PRAGMA journal_mode=WAL", "PRAGMA busy_timeout=5000"} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			log.Fatal(err)
		}
	}
	if err := migrate(ctx, db); err != nil {
		log.Fatal(err)
	}
	a := &app{db: db, approvalKey: []byte(os.Getenv("MEMDOCK_APPROVAL_KEY")), approvalPublicKey: ed25519.PublicKey(publicKey), trustedProxyCIDRs: trustedProxyCIDRs, projectGrants: projectGrants, mode: mode}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", a.health)
	mux.HandleFunc("GET /api/v1/backup", a.backup)
	mux.HandleFunc("GET /", a.webIndex)
	mux.HandleFunc("GET /api/v1/status", a.systemStatus)
	mux.HandleFunc("POST /mcp", a.mcp)
	mux.HandleFunc("GET /api/v1/projects", a.listProjects)
	mux.HandleFunc("PATCH /api/v1/projects/{id}", a.renameProject)
	mux.HandleFunc("POST /api/v1/projects/lookup", a.lookupProject)
	mux.HandleFunc("POST /api/v1/projects/resolve", a.resolveProject)
	mux.HandleFunc("POST /api/v1/knowledge-bases", a.createKnowledgeBase)
	mux.HandleFunc("POST /api/v1/memories", a.createMemory)
	mux.HandleFunc("POST /api/v1/memories/search", a.searchMemories)
	mux.HandleFunc("GET /api/v1/projects/{id}", a.projectDetail)
	mux.HandleFunc("GET /api/v1/projects/{id}/memories", a.listProjectMemories)
	mux.HandleFunc("POST /api/v1/projects/{id}/repositories", a.bindRepository)
	mux.HandleFunc("DELETE /api/v1/projects/{id}/repositories", a.unbindRepository)
	mux.HandleFunc("DELETE /api/v1/projects/{id}", a.deleteProject)
	mux.HandleFunc("POST /api/v1/projects/{id}/merge", a.mergeProject)
	mux.HandleFunc("GET /api/v1/knowledge-bases", a.listKnowledgeBases)
	mux.HandleFunc("PATCH /api/v1/knowledge-bases/{id}", a.updateKnowledgeBase)
	mux.HandleFunc("DELETE /api/v1/knowledge-bases/{id}", a.deleteKnowledgeBase)
	mux.HandleFunc("GET /api/v1/memories", a.listMemories)
	mux.HandleFunc("GET /api/v1/memories/{id}", a.getMemory)
	mux.HandleFunc("PATCH /api/v1/memories/{id}", a.updateMemory)
	mux.HandleFunc("DELETE /api/v1/memories/{id}", a.deleteMemory)
	mux.HandleFunc("POST /api/v1/memories/{id}/restore", a.restoreMemory)
	mux.HandleFunc("DELETE /api/v1/memories/{id}/permanent", a.permanentlyDeleteMemory)
	mux.HandleFunc("GET /api/v1/memories/{id}/history", a.memoryHistory)
	log.Printf("MemDock listening on %s (mode=%s)", addr, mode)
	server := &http.Server{Addr: addr, Handler: a.authenticate(a.authorizeProjectAccess(mux)), ReadHeaderTimeout: 10 * time.Second}
	if tlsCert != "" {
		log.Fatal(server.ListenAndServeTLS(tlsCert, tlsKey))
	}
	log.Fatal(server.ListenAndServe())
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func migrate(ctx context.Context, db *sql.DB) error {
	var version int
	if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version > 3 {
		return fmt.Errorf("database schema version %d is newer than supported", version)
	}
	if version == 0 {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		statements := []string{
			`CREATE TABLE projects (id TEXT PRIMARY KEY, name TEXT NOT NULL, created_at TEXT NOT NULL)`,
			`CREATE TABLE repositories (canonical TEXT PRIMARY KEY, project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE)`,
			`CREATE TABLE knowledge_bases (id TEXT PRIMARY KEY, project_id TEXT REFERENCES projects(id) ON DELETE CASCADE, name TEXT NOT NULL, kind TEXT NOT NULL CHECK(kind IN ('project','shared')), is_default INTEGER NOT NULL DEFAULT 0 CHECK(is_default IN (0,1)), created_at TEXT NOT NULL, CHECK((kind='project' AND project_id IS NOT NULL) OR (kind='shared' AND project_id IS NULL)), CHECK(kind='project' OR is_default=0))`,
			`CREATE UNIQUE INDEX project_default_kb ON knowledge_bases(project_id) WHERE kind='project' AND is_default=1`,
			`CREATE TABLE memories (id TEXT PRIMARY KEY, knowledge_base_id TEXT NOT NULL REFERENCES knowledge_bases(id), title TEXT NOT NULL, content TEXT NOT NULL, category TEXT NOT NULL DEFAULT '', status TEXT NOT NULL DEFAULT 'active', created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
			`CREATE VIRTUAL TABLE memories_fts USING fts5(title, content, content='memories', content_rowid='rowid', tokenize='trigram')`,
			`CREATE TRIGGER memories_ai AFTER INSERT ON memories BEGIN INSERT INTO memories_fts(rowid,title,content) VALUES(new.rowid,new.title,new.content); END`,
			`CREATE TRIGGER memories_ad AFTER DELETE ON memories BEGIN INSERT INTO memories_fts(memories_fts,rowid,title,content) VALUES('delete',old.rowid,old.title,old.content); END`,
			`CREATE TRIGGER memories_au AFTER UPDATE ON memories BEGIN INSERT INTO memories_fts(memories_fts,rowid,title,content) VALUES('delete',old.rowid,old.title,old.content); INSERT INTO memories_fts(rowid,title,content) VALUES(new.rowid,new.title,new.content); END`,
			`CREATE INDEX memories_kb_status ON memories(knowledge_base_id,status,updated_at)`,
		}
		for _, statement := range statements {
			if _, err := tx.ExecContext(ctx, statement); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, "PRAGMA user_version=1"); err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		version = 1
	}
	if version == 1 {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if _, err := tx.ExecContext(ctx, `CREATE TABLE used_write_authorizations (nonce TEXT PRIMARY KEY, used_at INTEGER NOT NULL)`); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "PRAGMA user_version=2"); err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		version = 2
	}
	if version == 2 {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		for _, statement := range []string{
			`CREATE TABLE memory_tags (memory_id TEXT NOT NULL REFERENCES memories(id) ON DELETE CASCADE, tag TEXT NOT NULL, PRIMARY KEY(memory_id,tag))`,
			`CREATE INDEX memory_tags_tag ON memory_tags(tag,memory_id)`,
			`CREATE TABLE memory_revisions (id INTEGER PRIMARY KEY AUTOINCREMENT, memory_id TEXT NOT NULL, title TEXT NOT NULL, content TEXT NOT NULL, category TEXT NOT NULL, status TEXT NOT NULL, tags_json TEXT NOT NULL DEFAULT '[]', updated_at TEXT NOT NULL, revised_at TEXT NOT NULL)`,
			`CREATE INDEX memory_revisions_memory ON memory_revisions(memory_id,id)`,
			`CREATE TABLE project_operations (id INTEGER PRIMARY KEY AUTOINCREMENT, source_project_id TEXT NOT NULL, target_project_id TEXT NOT NULL, action TEXT NOT NULL, created_at TEXT NOT NULL)`,
		} {
			if _, err := tx.ExecContext(ctx, statement); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, "PRAGMA user_version=3"); err != nil {
			return err
		}
		return tx.Commit()
	}
	return nil
}

func (a *app) health(w http.ResponseWriter, r *http.Request) {
	if err := a.db.PingContext(r.Context()); err != nil {
		writeError(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (a *app) renameProject(w http.ResponseWriter, r *http.Request) {
	if !a.verifyWriteSignature(w, r) {
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if !decode(w, r, &req) {
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" || len(req.Name) > 200 {
		writeError(w, 400, "project name is required and must be <= 200 bytes")
		return
	}
	result, err := a.db.ExecContext(r.Context(), `UPDATE projects SET name=? WHERE id=?`, req.Name, r.PathValue("id"))
	if err != nil {
		writeError(w, 500, "could not rename project")
		return
	}
	count, _ := result.RowsAffected()
	if count == 0 {
		writeError(w, 404, "project not found")
		return
	}
	writeJSON(w, 200, map[string]string{"id": r.PathValue("id"), "name": req.Name})
}

func (a *app) listProjects(w http.ResponseWriter, r *http.Request) {
	rows, err := a.db.QueryContext(r.Context(), `SELECT id,name FROM projects ORDER BY name,id`)
	if err != nil {
		writeError(w, 500, "database error")
		return
	}
	defer rows.Close()
	projects := make([]map[string]string, 0)
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			writeError(w, 500, "database error")
			return
		}
		if grant, ok := grantFromRequest(r); ok && !grant.canProject(id, false) {
			continue
		}
		projects = append(projects, map[string]string{"id": id, "name": name})
	}
	if err := rows.Err(); err != nil {
		writeError(w, 500, "database error")
		return
	}
	writeJSON(w, 200, map[string]any{"projects": projects})
}

func (a *app) lookupProject(w http.ResponseWriter, r *http.Request) {
	var req resolveRequest
	if !decode(w, r, &req) {
		return
	}
	repo, err := normalizeRepository(req.Repository)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	if req.ProjectID == "" && repo == "" {
		writeError(w, 400, "repository or project_id is required")
		return
	}
	var id, name string
	if req.ProjectID != "" {
		id = req.ProjectID
		if err := a.db.QueryRowContext(r.Context(), `SELECT name FROM projects WHERE id=?`, id).Scan(&name); err != nil {
			writeError(w, 404, "project not found")
			return
		}
		if repo != "" {
			var bound string
			if err := a.db.QueryRowContext(r.Context(), `SELECT project_id FROM repositories WHERE canonical=?`, repo).Scan(&bound); err != nil || bound != id {
				writeError(w, 404, "repository is not bound to selected project")
				return
			}
		}
	} else {
		err = a.db.QueryRowContext(r.Context(), `SELECT p.id,p.name FROM projects p JOIN repositories x ON x.project_id=p.id WHERE x.canonical=?`, repo).Scan(&id, &name)
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, 404, "project is not resolved; explicitly initialize or bind it first")
			return
		}
		if err != nil {
			writeError(w, 500, "database error")
			return
		}
	}
	var kbID string
	_ = a.db.QueryRowContext(r.Context(), `SELECT id FROM knowledge_bases WHERE project_id=? AND kind='project' AND is_default=1`, id).Scan(&kbID)
	writeJSON(w, 200, map[string]string{"project_id": id, "name": name, "repository": repo, "knowledge_base_id": kbID})
}

type resolveRequest struct {
	Repository string `json:"repository"`
	Name       string `json:"name"`
	ProjectID  string `json:"project_id"`
}

func (a *app) resolveProject(w http.ResponseWriter, r *http.Request) {
	if !a.verifyWriteSignature(w, r) {
		return
	}
	var req resolveRequest
	if !decode(w, r, &req) {
		return
	}
	repo, err := normalizeRepository(req.Repository)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	name := strings.TrimSpace(req.Name)
	if repo == "" && name == "" && req.ProjectID == "" {
		writeError(w, 400, "repository, project name, or project_id is required")
		return
	}
	if req.ProjectID != "" {
		var projectName string
		if err := a.db.QueryRowContext(r.Context(), `SELECT name FROM projects WHERE id=?`, req.ProjectID).Scan(&projectName); err != nil {
			writeError(w, 404, "project not found")
			return
		}
		if repo != "" {
			var boundID string
			err := a.db.QueryRowContext(r.Context(), `SELECT project_id FROM repositories WHERE canonical=?`, repo).Scan(&boundID)
			if err == nil && boundID != req.ProjectID {
				writeError(w, 409, "repository is bound to another project")
				return
			}
			if errors.Is(err, sql.ErrNoRows) {
				if _, err = a.db.ExecContext(r.Context(), `INSERT INTO repositories(canonical,project_id) VALUES(?,?)`, repo, req.ProjectID); err != nil {
					writeError(w, 409, "could not bind repository")
					return
				}
			} else if err != nil {
				writeError(w, 500, "database error")
				return
			}
		}
		var kbID string
		_ = a.db.QueryRowContext(r.Context(), `SELECT id FROM knowledge_bases WHERE project_id=? AND kind='project' AND is_default=1`, req.ProjectID).Scan(&kbID)
		writeJSON(w, 200, map[string]string{"project_id": req.ProjectID, "name": projectName, "repository": repo, "knowledge_base_id": kbID})
		return
	}
	if name == "" {
		name = repo[strings.LastIndex(repo, "/")+1:]
	}
	ctx := r.Context()
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		writeError(w, 500, "database error")
		return
	}
	defer tx.Rollback()
	var id, projectName string
	err = sql.ErrNoRows
	if repo != "" {
		err = tx.QueryRowContext(ctx, `SELECT p.id,p.name FROM projects p JOIN repositories x ON x.project_id=p.id WHERE x.canonical=?`, repo).Scan(&id, &projectName)
	}
	if err == nil {
		writeJSON(w, 200, map[string]string{"project_id": id, "name": projectName, "repository": repo})
		return
	}
	if !errors.Is(err, sql.ErrNoRows) {
		writeError(w, 500, "database error")
		return
	}
	id = makeID("prj")
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err = tx.ExecContext(ctx, `INSERT INTO projects(id,name,created_at) VALUES(?,?,?)`, id, name, now); err != nil {
		writeError(w, 500, "database error")
		return
	}
	if repo != "" {
		if _, err = tx.ExecContext(ctx, `INSERT INTO repositories(canonical,project_id) VALUES(?,?)`, repo, id); err != nil {
			writeError(w, 409, "repository is already bound to another project")
			return
		}
	}
	kb := makeID("kb")
	if _, err = tx.ExecContext(ctx, `INSERT INTO knowledge_bases(id,project_id,name,kind,is_default,created_at) VALUES(?,?,?,'project',1,?)`, kb, id, "默认项目库", now); err != nil {
		writeError(w, 500, "database error")
		return
	}
	if err = tx.Commit(); err != nil {
		writeError(w, 500, "database error")
		return
	}
	writeJSON(w, 201, map[string]string{"project_id": id, "name": name, "repository": repo, "knowledge_base_id": kb})
}

type createKnowledgeBaseRequest struct {
	ProjectID string `json:"project_id"`
	Name      string `json:"name"`
	Kind      string `json:"kind"`
}

func (a *app) createKnowledgeBase(w http.ResponseWriter, r *http.Request) {
	if !a.verifyWriteSignature(w, r) {
		return
	}
	var req createKnowledgeBaseRequest
	if !decode(w, r, &req) {
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" || (req.Kind != "project" && req.Kind != "shared") || (req.Kind == "project" && req.ProjectID == "") || (req.Kind == "shared" && req.ProjectID != "") {
		writeError(w, 400, "provide a name and valid kind/project_id combination")
		return
	}
	id := makeID("kb")
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := a.db.ExecContext(r.Context(), `INSERT INTO knowledge_bases(id,project_id,name,kind,created_at) VALUES(?,?,?,?,?)`, id, nullString(req.ProjectID), req.Name, req.Kind, now); err != nil {
		writeError(w, 400, "could not create knowledge base")
		return
	}
	writeJSON(w, 201, map[string]string{"id": id, "name": req.Name, "kind": req.Kind})
}
func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// verifyWriteSignature enforces independent Ed25519 approval in secure mode and HMAC integrity in private mode.
func (a *app) verifyWriteSignature(w http.ResponseWriter, r *http.Request) bool {
	if a.mode == "secure" && len(a.approvalPublicKey) != ed25519.PublicKeySize {
		writeError(w, 503, "independent approval public key is not configured")
		return false
	}
	if a.mode != "secure" && len(a.approvalKey) < 32 {
		writeError(w, 503, "write signature key is not configured")
		return false
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		writeError(w, 400, "request body too large or unreadable")
		return false
	}
	parts := strings.Split(r.Header.Get("X-MemDock-Write-Signature"), ":")
	if a.mode == "secure" {
		if len(parts) != 4 || parts[0] != "approval" {
			writeError(w, 401, "one-time independent user approval required")
			return false
		}
	} else if len(parts) != 3 {
		writeError(w, 401, "one-time request signature required")
		return false
	}
	index := 0
	if a.mode == "secure" {
		index = 1
	}
	timestamp, err := strconv.ParseInt(parts[index], 10, 64)
	if err != nil || time.Since(time.Unix(timestamp, 0)) > time.Minute || time.Until(time.Unix(timestamp, 0)) > time.Minute {
		writeError(w, 401, "request signature expired")
		return false
	}
	noncePart := parts[index+1]
	if len(noncePart) != 32 {
		writeError(w, 401, "invalid request signature")
		return false
	}
	nonce, err := hex.DecodeString(noncePart)
	if err != nil || len(nonce) != 16 {
		writeError(w, 401, "invalid request signature")
		return false
	}
	signature, err := hex.DecodeString(parts[index+2])
	if err != nil {
		writeError(w, 401, "invalid request signature")
		return false
	}
	message := fmt.Sprintf("%s\n%s\n%s\n%s\n%s", parts[index], noncePart, r.Method, r.URL.Path, body)
	valid := false
	if a.mode == "secure" {
		valid = ed25519.Verify(a.approvalPublicKey, []byte(message), signature)
	} else {
		mac := hmac.New(sha256.New, a.approvalKey)
		_, _ = mac.Write([]byte(message))
		valid = hmac.Equal(signature, mac.Sum(nil))
	}
	if !valid {
		writeError(w, 401, "invalid request signature")
		return false
	}
	tx, err := a.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeError(w, 500, "database error")
		return false
	}
	defer tx.Rollback()
	_, _ = tx.ExecContext(r.Context(), `DELETE FROM used_write_authorizations WHERE used_at < ?`, time.Now().Add(-10*time.Minute).Unix())
	if _, err := tx.ExecContext(r.Context(), `INSERT INTO used_write_authorizations(nonce,used_at) VALUES(?,?)`, noncePart, time.Now().Unix()); err != nil {
		writeError(w, 401, "request signature already used")
		return false
	}
	if err := tx.Commit(); err != nil {
		writeError(w, 500, "database error")
		return false
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	return true
}

type createMemoryRequest struct {
	ProjectID       string   `json:"project_id"`
	KnowledgeBaseID string   `json:"knowledge_base_id"`
	Title           string   `json:"title"`
	Content         string   `json:"content"`
	Category        string   `json:"category"`
	Tags            []string `json:"tags"`
}

func (a *app) createMemory(w http.ResponseWriter, r *http.Request) {
	if !a.verifyWriteSignature(w, r) {
		return
	}
	var req createMemoryRequest
	if !decode(w, r, &req) {
		return
	}
	req.Title = strings.TrimSpace(req.Title)
	req.Content = strings.TrimSpace(req.Content)
	if req.Title == "" || req.Content == "" || len(req.Title) > 300 || len(req.Content) > 20000 {
		writeError(w, 400, "title and content are required (title <= 300, content <= 20000 bytes)")
		return
	}
	if req.ProjectID == "" {
		writeError(w, 400, "project_id is required")
		return
	}
	if !a.projectExists(w, r, req.ProjectID) {
		return
	}
	ctx := r.Context()
	var kbID string
	if req.KnowledgeBaseID != "" {
		kbID = req.KnowledgeBaseID
		if err := a.db.QueryRowContext(ctx, `SELECT id FROM knowledge_bases WHERE id=? AND (kind='shared' OR project_id=?)`, kbID, req.ProjectID).Scan(&kbID); err != nil {
			writeError(w, 404, "knowledge base not found for project")
			return
		}
	} else {
		if req.ProjectID == "" {
			writeError(w, 400, "project_id is required")
			return
		}
		if err := a.db.QueryRowContext(ctx, `SELECT id FROM knowledge_bases WHERE project_id=? AND kind='project' AND is_default=1`, req.ProjectID).Scan(&kbID); err != nil {
			writeError(w, 404, "project not found")
			return
		}
	}
	tags := cleanTags(req.Tags)
	for _, tag := range tags {
		if len(tag) > 100 {
			writeError(w, 400, "tag must be <= 100 bytes")
			return
		}
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	id := makeID("mem")
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		writeError(w, 500, "database error")
		return
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO memories(id,knowledge_base_id,title,content,category,created_at,updated_at) VALUES(?,?,?,?,?,?,?)`, id, kbID, req.Title, req.Content, req.Category, now, now); err != nil {
		writeError(w, 500, "could not save memory")
		return
	}
	for _, tag := range tags {
		if _, err := tx.ExecContext(ctx, `INSERT INTO memory_tags(memory_id,tag) VALUES(?,?)`, id, tag); err != nil {
			writeError(w, 500, "could not save memory tags")
			return
		}
	}
	if err := tx.Commit(); err != nil {
		writeError(w, 500, "could not save memory")
		return
	}
	writeJSON(w, 201, map[string]string{"id": id, "knowledge_base_id": kbID, "title": req.Title, "created_at": now})
}

type searchRequest struct {
	ProjectID       string `json:"project_id"`
	KnowledgeBaseID string `json:"knowledge_base_id"`
	Query           string `json:"query"`
	Tag             string `json:"tag"`
	Limit           int    `json:"limit"`
}
type searchResult struct {
	ID                string `json:"id"`
	Title             string `json:"title"`
	Content           string `json:"content"`
	Category          string `json:"category"`
	KnowledgeBaseID   string `json:"knowledge_base_id"`
	KnowledgeBaseName string `json:"knowledge_base_name"`
	KnowledgeBaseType string `json:"knowledge_base_type"`
	UpdatedAt         string `json:"updated_at"`
}

func (a *app) searchMemories(w http.ResponseWriter, r *http.Request) {
	var req searchRequest
	if !decode(w, r, &req) {
		return
	}
	req.Query = strings.TrimSpace(req.Query)
	if req.ProjectID == "" || req.Query == "" {
		writeError(w, 400, "project_id and query are required")
		return
	}
	if !a.projectExists(w, r, req.ProjectID) {
		return
	}
	if req.Limit <= 0 || req.Limit > 20 {
		req.Limit = 5
	}
	base := `SELECT m.id,m.title,m.content,m.category,k.id,k.name,k.kind,m.updated_at FROM memories m JOIN knowledge_bases k ON k.id=m.knowledge_base_id WHERE m.status='active' AND `
	args := []any{}
	if grant, ok := grantFromRequest(r); ok && !grant.canShared(false) && req.KnowledgeBaseID == "" {
		base += `k.kind='project' AND `
	}
	if req.KnowledgeBaseID != "" {
		base += `k.id=? AND (k.kind='shared' OR k.project_id=?) AND `
		args = append(args, req.KnowledgeBaseID, req.ProjectID)
	} else {
		base += `(k.kind='shared' OR k.project_id=?) AND `
		args = append(args, req.ProjectID)
	}
	if tag := strings.TrimSpace(req.Tag); tag != "" {
		base += `EXISTS(SELECT 1 FROM memory_tags t WHERE t.memory_id=m.id AND t.tag=?) AND `
		args = append(args, tag)
	}
	// Trigram FTS does not match terms shorter than three Unicode characters;
	// use ANDed substring conditions for those terms, otherwise AND FTS terms.
	terms := strings.Fields(req.Query)
	shortTerm := false
	for _, term := range terms {
		if len([]rune(term)) < 3 {
			shortTerm = true
			break
		}
	}
	if shortTerm {
		conditions := make([]string, 0, len(terms))
		for _, term := range terms {
			conditions = append(conditions, `(m.title LIKE ? OR m.content LIKE ?)`)
			q := "%" + term + "%"
			args = append(args, q, q)
		}
		base += strings.Join(conditions, ` AND `) + ` ORDER BY m.updated_at DESC LIMIT ?`
		args = append(args, req.Limit)
	} else {
		ftsTerms := make([]string, 0, len(terms))
		for _, term := range terms {
			ftsTerms = append(ftsTerms, quoteFTS(term))
		}
		base += `m.rowid IN (SELECT rowid FROM memories_fts WHERE memories_fts MATCH ?) ORDER BY m.updated_at DESC LIMIT ?`
		args = append(args, strings.Join(ftsTerms, ` AND `), req.Limit)
	}
	rows, err := a.db.QueryContext(r.Context(), base, args...)
	if err != nil {
		writeError(w, 400, "invalid search query")
		return
	}
	defer rows.Close()
	results := []searchResult{}
	for rows.Next() {
		var x searchResult
		if err := rows.Scan(&x.ID, &x.Title, &x.Content, &x.Category, &x.KnowledgeBaseID, &x.KnowledgeBaseName, &x.KnowledgeBaseType, &x.UpdatedAt); err != nil {
			writeError(w, 500, "database error")
			return
		}
		if len([]rune(x.Content)) > 1000 {
			x.Content = string([]rune(x.Content)[:1000])
		}
		results = append(results, x)
	}
	if err := rows.Err(); err != nil {
		writeError(w, 500, "database error")
		return
	}
	writeJSON(w, 200, map[string]any{"project_id": req.ProjectID, "results": results})
}

func (a *app) projectExists(w http.ResponseWriter, r *http.Request, id string) bool {
	var exists int
	err := a.db.QueryRowContext(r.Context(), `SELECT 1 FROM projects WHERE id=?`, id).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, 404, "project not found")
		return false
	}
	if err != nil {
		writeError(w, 500, "database error")
		return false
	}
	return true
}

func quoteFTS(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		writeError(w, 400, "invalid JSON request")
		return false
	}
	return true
}
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, apiError{Error: msg})
}
