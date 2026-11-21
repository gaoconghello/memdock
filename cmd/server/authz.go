package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

type accessGrant struct {
	TokenSHA256 string   `json:"token_sha256"`
	Role        string   `json:"role"`
	Projects    []string `json:"projects,omitempty"`
	SharedRead  bool     `json:"shared_read,omitempty"`
	SharedWrite bool     `json:"shared_write,omitempty"`
}

type accessFile struct {
	Credentials []accessGrant `json:"credentials"`
}

type accessContextKey struct{}

func tokenDigest(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func loadAccessGrants(path, readToken, writeToken string) (map[string]accessGrant, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("MEMDOCK_PROJECT_ACL_FILE is required in secure mode")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read project ACL file: %w", err)
	}
	var file accessFile
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&file); err != nil {
		return nil, fmt.Errorf("parse project ACL file: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, fmt.Errorf("project ACL file must contain one JSON object")
	}
	grants := make(map[string]accessGrant, len(file.Credentials))
	for _, grant := range file.Credentials {
		if len(grant.TokenSHA256) != 64 {
			return nil, fmt.Errorf("project ACL token_sha256 must be a 64-character SHA-256 hex digest")
		}
		decoded, err := hex.DecodeString(grant.TokenSHA256)
		if err != nil || len(decoded) != sha256.Size {
			return nil, fmt.Errorf("project ACL token_sha256 is invalid")
		}
		grant.TokenSHA256 = strings.ToLower(grant.TokenSHA256)
		if _, exists := grants[grant.TokenSHA256]; exists {
			return nil, fmt.Errorf("duplicate project ACL credential")
		}
		if grant.Role != "read" && grant.Role != "write" && grant.Role != "admin" {
			return nil, fmt.Errorf("project ACL role must be read, write, or admin")
		}
		if grant.Role == "read" && grant.SharedWrite {
			return nil, fmt.Errorf("read-only project ACL role cannot enable shared-write")
		}
		seen := map[string]bool{}
		for _, projectID := range grant.Projects {
			if strings.TrimSpace(projectID) == "" || seen[projectID] {
				return nil, fmt.Errorf("project ACL project IDs must be non-empty and unique")
			}
			seen[projectID] = true
		}
		grants[grant.TokenSHA256] = grant
	}
	readGrant, readOK := grants[tokenDigest(readToken)]
	writeGrant, writeOK := grants[tokenDigest(writeToken)]
	if !readOK || readGrant.Role != "read" {
		return nil, fmt.Errorf("Secure read token must have a read-only project ACL grant")
	}
	if !writeOK || writeGrant.Role != "write" {
		return nil, fmt.Errorf("Secure write token must have a write project ACL grant")
	}
	return grants, nil
}

func grantFromRequest(r *http.Request) (accessGrant, bool) {
	grant, ok := r.Context().Value(accessContextKey{}).(accessGrant)
	return grant, ok
}

func (g accessGrant) canProject(projectID string, write bool) bool {
	if g.Role == "admin" {
		return true
	}
	if write && g.Role != "write" {
		return false
	}
	for _, allowed := range g.Projects {
		if allowed == projectID {
			return true
		}
	}
	return false
}

func (g accessGrant) canShared(write bool) bool {
	if g.Role == "admin" {
		return true
	}
	if write {
		return g.Role == "write" && g.SharedWrite
	}
	return g.SharedRead
}

func (g accessGrant) canAnyProject() bool {
	return g.Role == "admin" || len(g.Projects) > 0 || g.SharedRead
}

func (a *app) authorizeProjectAccess(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a.mode != "secure" || r.URL.Path == "/healthz" {
			next.ServeHTTP(w, r)
			return
		}
		grant, ok := grantFromRequest(r)
		if !ok {
			writeError(w, http.StatusUnauthorized, "project access credential required")
			return
		}
		if !a.authorizeRequest(w, r, grant) {
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *app) authorizeRequest(w http.ResponseWriter, r *http.Request, grant accessGrant) bool {
	path := r.URL.Path
	if path == "/healthz" || path == "/" {
		return true
	}
	if path == "/mcp" {
		return a.authorizeMCP(w, r, grant)
	}
	if path == "/api/v1/backup" {
		if grant.Role == "admin" {
			return true
		}
		return denyAccess(w)
	}
	if path == "/api/v1/status" {
		return true
	}
	if path == "/api/v1/projects" {
		if r.Method == http.MethodGet && grant.canAnyProject() {
			return true
		}
		return denyAccess(w)
	}
	if path == "/api/v1/projects/lookup" || path == "/api/v1/projects/resolve" {
		var input resolveRequest
		if !readRequestJSON(w, r, &input) {
			return false
		}
		projectID := input.ProjectID
		if projectID == "" && input.Repository != "" {
			repo, err := normalizeRepository(input.Repository)
			if err != nil {
				return denyAccess(w)
			}
			_ = a.db.QueryRowContext(r.Context(), `SELECT project_id FROM repositories WHERE canonical=?`, repo).Scan(&projectID)
		}
		if projectID == "" {
			if path == "/api/v1/projects/lookup" || path == "/api/v1/projects/resolve" && grant.Role == "admin" {
				return true
			}
			return denyAccess(w)
		}
		write := path == "/api/v1/projects/resolve"
		if grant.canProject(projectID, write) {
			return true
		}
		return denyAccess(w)
	}
	if path == "/api/v1/knowledge-bases" {
		if r.Method == http.MethodGet && grant.canAnyProject() {
			return true
		}
		if r.Method == http.MethodPost {
			var input createKnowledgeBaseRequest
			if !readRequestJSON(w, r, &input) {
				return false
			}
			if input.Kind == "shared" && grant.canShared(true) || input.Kind == "project" && grant.canProject(input.ProjectID, true) {
				return true
			}
		}
		return denyAccess(w)
	}
	if path == "/api/v1/memories" {
		switch r.Method {
		case http.MethodGet:
			projectID := r.URL.Query().Get("project_id")
			kbID := r.URL.Query().Get("knowledge_base_id")
			return a.authorizeLibrary(w, r, grant, projectID, kbID, false)
		case http.MethodPost:
			var input createMemoryRequest
			if !readRequestJSON(w, r, &input) {
				return false
			}
			return a.authorizeLibrary(w, r, grant, input.ProjectID, input.KnowledgeBaseID, true)
		}
	}
	if path == "/api/v1/memories/search" && r.Method == http.MethodPost {
		var input searchRequest
		if !readRequestJSON(w, r, &input) {
			return false
		}
		return a.authorizeLibrary(w, r, grant, input.ProjectID, input.KnowledgeBaseID, false)
	}
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) >= 4 && parts[0] == "api" && parts[1] == "v1" {
		switch parts[2] {
		case "projects":
			if len(parts) >= 4 {
				if len(parts) == 5 && parts[4] == "memories" {
					return a.authorizeLibrary(w, r, grant, parts[3], r.URL.Query().Get("knowledge_base_id"), false)
				}
				write := r.Method != http.MethodGet
				if len(parts) == 5 && parts[4] == "merge" {
					var input struct {
						TargetID string `json:"target_project_id"`
					}
					if !readRequestJSON(w, r, &input) {
						return false
					}
					if grant.canProject(parts[3], true) && grant.canProject(input.TargetID, true) {
						return true
					}
					return denyAccess(w)
				}
				if grant.canProject(parts[3], write) {
					return true
				}
			}
		case "knowledge-bases":
			if len(parts) == 4 {
				return a.authorizeKnowledgeBase(w, r, grant, parts[3])
			}
		case "memories":
			if len(parts) >= 4 {
				return a.authorizeMemory(w, r, grant, parts[3], r.Method != http.MethodGet)
			}
		}
	}
	if strings.HasPrefix(path, "/api/v1/") {
		return denyAccess(w)
	}
	return true
}

func (a *app) authorizeLibrary(w http.ResponseWriter, r *http.Request, grant accessGrant, projectID, kbID string, write bool) bool {
	if !grant.canProject(projectID, write) {
		return denyAccess(w)
	}
	if kbID == "" {
		return true
	}
	var kind, owner string
	if err := a.db.QueryRowContext(r.Context(), `SELECT kind,coalesce(project_id,'') FROM knowledge_bases WHERE id=?`, kbID).Scan(&kind, &owner); err != nil {
		return denyAccess(w)
	}
	if kind == "shared" {
		if grant.canShared(write) {
			return true
		}
		return denyAccess(w)
	}
	if owner == projectID {
		return true
	}
	return denyAccess(w)
}

func (a *app) authorizeKnowledgeBase(w http.ResponseWriter, r *http.Request, grant accessGrant, kbID string) bool {
	var kind, projectID string
	if err := a.db.QueryRowContext(r.Context(), `SELECT kind,coalesce(project_id,'') FROM knowledge_bases WHERE id=?`, kbID).Scan(&kind, &projectID); err != nil {
		return denyAccess(w)
	}
	write := r.Method != http.MethodGet
	if !write {
		if kind == "shared" {
			return requireDecision(w, grant.canShared(false))
		}
		return requireDecision(w, grant.canProject(projectID, false))
	}
	if kind == "shared" && !grant.canShared(true) || kind == "project" && !grant.canProject(projectID, true) {
		return denyAccess(w)
	}
	if r.Method == http.MethodPatch {
		var input struct {
			Kind      string `json:"kind"`
			ProjectID string `json:"project_id"`
		}
		if !readRequestJSON(w, r, &input) {
			return false
		}
		targetKind, targetProject := kind, projectID
		if input.Kind != "" {
			targetKind = input.Kind
		}
		if input.ProjectID != "" {
			targetProject = input.ProjectID
		}
		if targetKind == "shared" {
			targetProject = ""
		}
		if targetKind == "shared" && !grant.canShared(true) || targetKind == "project" && !grant.canProject(targetProject, true) {
			return denyAccess(w)
		}
	}
	return true
}

func (a *app) authorizeMemory(w http.ResponseWriter, r *http.Request, grant accessGrant, memoryID string, write bool) bool {
	var kind, owner string
	if err := a.db.QueryRowContext(r.Context(), `SELECT k.kind,coalesce(k.project_id,'') FROM memories m JOIN knowledge_bases k ON k.id=m.knowledge_base_id WHERE m.id=?`, memoryID).Scan(&kind, &owner); err != nil {
		return denyAccess(w)
	}
	projectID := r.URL.Query().Get("project_id")
	if r.Method != http.MethodGet {
		var input struct {
			ProjectID string `json:"project_id"`
		}
		if !readRequestJSON(w, r, &input) {
			return false
		}
		projectID = input.ProjectID
	}
	if kind == "shared" {
		if projectID == "" || !grant.canProject(projectID, write) || !grant.canShared(write) {
			return denyAccess(w)
		}
		return true
	}
	if projectID != owner || !grant.canProject(owner, write) {
		return denyAccess(w)
	}
	return true
}

func (a *app) authorizeMCP(w http.ResponseWriter, r *http.Request, grant accessGrant) bool {
	var req mcpRequest
	if !readRequestJSON(w, r, &req) {
		return false
	}
	if req.Method != "tools/call" {
		return true
	}
	var args map[string]json.RawMessage
	if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
		return denyAccess(w)
	}
	get := func(name string) string {
		var value string
		_ = json.Unmarshal(args[name], &value)
		return value
	}
	projectID := get("project_id")
	writeProposal := req.Params.Name == "memory_save" || req.Params.Name == "memory_update" || req.Params.Name == "memory_delete"
	if req.Params.Name == "memory_search" || req.Params.Name == "memory_list" || req.Params.Name == "project_info" || writeProposal {
		if projectID == "" || !grant.canProject(projectID, false) {
			return denyAccess(w)
		}
		kbID := get("knowledge_base_id")
		if req.Params.Name == "memory_save" && kbID != "" || (req.Params.Name == "memory_search" || req.Params.Name == "memory_list") && kbID != "" {
			return a.authorizeLibrary(w, r, grant, projectID, kbID, false)
		}
		if req.Params.Name == "memory_update" || req.Params.Name == "memory_delete" {
			var memoryID = get("id")
			var kind, owner string
			if err := a.db.QueryRowContext(r.Context(), `SELECT k.kind,coalesce(k.project_id,'') FROM memories m JOIN knowledge_bases k ON k.id=m.knowledge_base_id WHERE m.id=?`, memoryID).Scan(&kind, &owner); err != nil {
				return denyAccess(w)
			}
			if kind == "shared" && !grant.canShared(false) || kind == "project" && owner != projectID {
				return denyAccess(w)
			}
		}
		return true
	}
	return true
}

func readRequestJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid or oversized request body")
		return false
	}
	r.Body = io.NopCloser(strings.NewReader(string(body)))
	if err := json.Unmarshal(body, target); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return false
	}
	return true
}

func denyAccess(w http.ResponseWriter) bool {
	writeError(w, http.StatusForbidden, "identity is not authorized for this project or library")
	return false
}

func requireDecision(w http.ResponseWriter, allowed bool) bool {
	if !allowed {
		return denyAccess(w)
	}
	return true
}
