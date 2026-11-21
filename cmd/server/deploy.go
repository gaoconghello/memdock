package main

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func (a *app) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a.mode == "secure" && r.URL.Path != "/healthz" {
			if os.Getenv("MEMDOCK_TRUST_PROXY_TLS") == "true" {
				if !trustedProxyPeer(r.RemoteAddr, a.trustedProxyCIDRs) {
					writeError(w, 403, "request did not come from a trusted TLS proxy")
					return
				}
				if r.Header.Get("X-Forwarded-Proto") != "https" {
					writeError(w, 400, "HTTPS is required")
					return
				}
			} else if r.TLS == nil {
				writeError(w, 400, "HTTPS is required")
				return
			}
			provided := ""
			if h := strings.TrimSpace(r.Header.Get("Authorization")); strings.HasPrefix(h, "Bearer ") {
				provided = strings.TrimPrefix(h, "Bearer ")
			} else if u, p, ok := r.BasicAuth(); ok && u == "memdock" {
				provided = p
			}
			grant, known := a.projectGrants[tokenDigest(provided)]
			if len(provided) < 32 {
				known = false
			}
			if !known {
				w.Header().Set("WWW-Authenticate", `Basic realm="MemDock"`)
				writeError(w, http.StatusUnauthorized, "authentication required")
				return
			}
			if isWriteRequest(r) && grant.Role != "write" && grant.Role != "admin" {
				writeError(w, http.StatusForbidden, "write credential required")
				return
			}
			r = r.WithContext(context.WithValue(r.Context(), accessContextKey{}, grant))
		}
		next.ServeHTTP(w, r)
	})
}

func parseTrustedProxyCIDRs(value string) ([]*net.IPNet, error) {
	parts := strings.Split(value, ",")
	var networks []*net.IPNet
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		_, network, err := net.ParseCIDR(part)
		if err != nil {
			return nil, fmt.Errorf("invalid trusted proxy CIDR %q", part)
		}
		networks = append(networks, network)
	}
	if len(networks) == 0 {
		return nil, fmt.Errorf("at least one trusted proxy CIDR is required")
	}
	return networks, nil
}

func trustedProxyPeer(remoteAddr string, networks []*net.IPNet) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	for _, network := range networks {
		if network.Contains(ip) {
			return true
		}
	}
	return false
}

func isWriteRequest(r *http.Request) bool {
	if r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions {
		return false
	}
	if r.Method == http.MethodPost && (r.URL.Path == "/api/v1/projects/lookup" || r.URL.Path == "/api/v1/memories/search" || r.URL.Path == "/mcp") {
		return false
	}
	if r.URL.Path == "/mcp" && r.Method == http.MethodPost {
		// MCP write tools only prepare an approval envelope. Actual writes go
		// through the REST endpoint using the independent approver credential.
		return false
	}
	return true
}

func (a *app) backup(w http.ResponseWriter, r *http.Request) {
	dir, err := os.MkdirTemp("", "memdock-backup-")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not create backup")
		return
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "backup.sqlite")
	if _, err := a.db.ExecContext(r.Context(), `VACUUM INTO ?`, path); err != nil {
		writeError(w, http.StatusInternalServerError, "could not create consistent SQLite backup")
		return
	}
	if err := os.Chmod(path, 0600); err != nil {
		writeError(w, 500, "could not protect backup file")
		return
	}
	check, err := sql.Open("sqlite", path)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "backup validation failed")
		return
	}
	defer check.Close()
	if err := validateMemDockDatabase(check); err != nil {
		writeError(w, http.StatusInternalServerError, "backup validation failed")
		return
	}
	w.Header().Set("Content-Type", "application/vnd.sqlite3")
	w.Header().Set("Content-Disposition", `attachment; filename="memdock-backup-`+time.Now().UTC().Format("20060102T150405Z")+`.sqlite"`)
	http.ServeFile(w, r, path)
}

// restoreDatabase validates the complete SQLite image before atomically replacing the target.
// The service must be stopped so no process is using the destination database.
func restoreDatabase(source, target string) error {
	if strings.TrimSpace(source) == "" || strings.TrimSpace(target) == "" {
		return fmt.Errorf("backup and target paths are required")
	}
	info, err := os.Stat(source)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("backup must be a regular file")
	}
	check, err := sql.Open("sqlite", source)
	if err != nil {
		return fmt.Errorf("open backup: %w", err)
	}
	if err := validateMemDockDatabase(check); err != nil {
		check.Close()
		return fmt.Errorf("invalid MemDock backup: %w", err)
	}
	dir := filepath.Dir(target)
	stageDir, err := os.MkdirTemp(dir, ".memdock-restore-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stageDir)
	tmpPath := filepath.Join(stageDir, "database.sqlite")
	if _, err := check.Exec(`VACUUM INTO ?`, tmpPath); err != nil {
		check.Close()
		return fmt.Errorf("create validated restore snapshot: %w", err)
	}
	if err := check.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpPath, 0600); err != nil {
		return err
	}
	staged, err := sql.Open("sqlite", tmpPath)
	if err != nil {
		return err
	}
	if err := validateMemDockDatabase(staged); err != nil {
		staged.Close()
		return fmt.Errorf("staged restore validation: %w", err)
	}
	if err := staged.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpPath, 0600); err != nil {
		return err
	}
	return replaceDatabaseFiles(tmpPath, target)
}

func validateMemDockDatabase(db *sql.DB) error {
	var integrity string
	if err := db.QueryRow(`PRAGMA integrity_check`).Scan(&integrity); err != nil || integrity != "ok" {
		return fmt.Errorf("integrity check failed: %s", integrity)
	}
	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version < 1 || version > 3 {
		return fmt.Errorf("unsupported schema version %d", version)
	}
	required := map[string][]string{
		"projects":        {"id", "name", "created_at"},
		"repositories":    {"canonical", "project_id"},
		"knowledge_bases": {"id", "project_id", "name", "kind", "is_default", "created_at"},
		"memories":        {"id", "knowledge_base_id", "title", "content", "category", "status", "created_at", "updated_at"},
		"memories_fts":    {"title", "content"},
	}
	objects := map[string]string{
		"memories_ai": "trigger", "memories_ad": "trigger", "memories_au": "trigger",
		"project_default_kb": "index", "memories_kb_status": "index",
	}
	if version >= 2 {
		required["used_write_authorizations"] = []string{"nonce", "used_at"}
	}
	if version >= 3 {
		required["memory_tags"] = []string{"memory_id", "tag"}
		required["memory_revisions"] = []string{"id", "memory_id", "title", "content", "category", "status", "tags_json", "updated_at", "revised_at"}
		required["project_operations"] = []string{"id", "source_project_id", "target_project_id", "action", "created_at"}
		objects["memory_tags_tag"] = "index"
		objects["memory_revisions_memory"] = "index"
	}
	for name, kind := range objects {
		var actualType string
		if err := db.QueryRow(`SELECT type FROM sqlite_master WHERE name=?`, name).Scan(&actualType); err != nil || actualType != kind {
			return fmt.Errorf("required MemDock %s %s missing", kind, name)
		}
	}
	var ftsSQL string
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE name='memories_fts' AND type='table'`).Scan(&ftsSQL); err != nil || !strings.Contains(strings.ToLower(ftsSQL), "using fts5") {
		return fmt.Errorf("MemDock FTS5 virtual table is missing")
	}
	for table, columns := range required {
		rows, err := db.Query(`PRAGMA table_info('` + table + `')`)
		if err != nil {
			return fmt.Errorf("inspect table %s: %w", table, err)
		}
		found := map[string]bool{}
		for rows.Next() {
			var cid int
			var name, typ string
			var notnull, pk int
			var def any
			if err := rows.Scan(&cid, &name, &typ, &notnull, &def, &pk); err != nil {
				rows.Close()
				return err
			}
			found[name] = true
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		for _, column := range columns {
			if !found[column] {
				return fmt.Errorf("required table/column missing: %s.%s", table, column)
			}
		}
	}
	return nil
}

func replaceDatabaseFiles(staged, target string) error {
	paths := []string{target, target + "-wal", target + "-shm"}
	for _, path := range paths {
		info, err := os.Lstat(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if info.IsDir() {
			return fmt.Errorf("refuse to replace database directory %s", path)
		}
	}
	type movedFile struct{ original, saved string }
	moved := make([]movedFile, 0, len(paths))
	rollback := func() error {
		var errs []string
		for i := len(moved) - 1; i >= 0; i-- {
			if err := os.Rename(moved[i].saved, moved[i].original); err != nil {
				errs = append(errs, fmt.Sprintf("could not restore %s from saved copy %s: %v", moved[i].original, moved[i].saved, err))
			}
		}
		if len(errs) > 0 {
			return fmt.Errorf("restore rollback failed: %s", strings.Join(errs, "; "))
		}
		return nil
	}
	for _, path := range paths {
		if _, err := os.Lstat(path); os.IsNotExist(err) {
			continue
		} else if err != nil {
			if rb := rollback(); rb != nil {
				return rb
			}
			return err
		}
		f, err := os.CreateTemp(filepath.Dir(target), ".memdock-old-*")
		if err != nil {
			if rb := rollback(); rb != nil {
				return rb
			}
			return err
		}
		saved := f.Name()
		if err := f.Close(); err != nil {
			os.Remove(saved)
			if rb := rollback(); rb != nil {
				return rb
			}
			return err
		}
		if err := os.Remove(saved); err != nil {
			if rb := rollback(); rb != nil {
				return rb
			}
			return err
		}
		if err := os.Rename(path, saved); err != nil {
			if rb := rollback(); rb != nil {
				return rb
			}
			return err
		}
		moved = append(moved, movedFile{path, saved})
	}
	if err := os.Rename(staged, target); err != nil {
		if rb := rollback(); rb != nil {
			return fmt.Errorf("install restore: %v; %w", err, rb)
		}
		return err
	}
	for _, old := range moved {
		if err := os.Remove(old.saved); err != nil {
			return fmt.Errorf("restored new database but could not remove saved old file %s: %w", old.saved, err)
		}
	}
	return nil
}
