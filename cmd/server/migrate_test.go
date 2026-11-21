package main

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestMigrateV5ToV6MovesProjectLibrariesToProjectOwnership(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "v5.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	for _, statement := range []string{
		`PRAGMA foreign_keys=ON`,
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
		`CREATE TABLE memory_tags (memory_id TEXT NOT NULL REFERENCES memories(id) ON DELETE CASCADE, tag TEXT NOT NULL, PRIMARY KEY(memory_id,tag))`,
		`CREATE INDEX memory_tags_tag ON memory_tags(tag,memory_id)`,
		`CREATE TABLE memory_revisions (id INTEGER PRIMARY KEY AUTOINCREMENT, memory_id TEXT NOT NULL, title TEXT NOT NULL, content TEXT NOT NULL, category TEXT NOT NULL, status TEXT NOT NULL, tags_json TEXT NOT NULL DEFAULT '[]', updated_at TEXT NOT NULL, revised_at TEXT NOT NULL)`,
		`CREATE INDEX memory_revisions_memory ON memory_revisions(memory_id,id)`,
		`CREATE TABLE project_operations (id INTEGER PRIMARY KEY AUTOINCREMENT, source_project_id TEXT NOT NULL, target_project_id TEXT NOT NULL, action TEXT NOT NULL, created_at TEXT NOT NULL)`,
		`CREATE TABLE admin_credentials (id INTEGER PRIMARY KEY CHECK(id=1), password_hash TEXT NOT NULL)`,
		`CREATE TABLE admin_sessions (token_hash TEXT PRIMARY KEY, csrf_hash TEXT NOT NULL, csrf_token TEXT NOT NULL, expires_at INTEGER NOT NULL)`,
		`CREATE INDEX admin_sessions_expiry ON admin_sessions(expires_at)`,
		`INSERT INTO projects(id,name,created_at) VALUES('prj','project','2026-01-01')`,
		`INSERT INTO knowledge_bases(id,project_id,name,kind,is_default,created_at) VALUES('kb-project','prj','default','project',1,'2026-01-01')`,
		`INSERT INTO knowledge_bases(id,project_id,name,kind,is_default,created_at) VALUES('kb-shared',NULL,'shared','shared',0,'2026-01-01')`,
		`INSERT INTO memories(id,knowledge_base_id,title,content,category,status,created_at,updated_at) VALUES('mem-project','kb-project','project title','project body','decision','deleted','2026-01-01','2026-01-02')`,
		`INSERT INTO memories(id,knowledge_base_id,title,content,category,status,created_at,updated_at) VALUES('mem-shared','kb-shared','shared title','shared body','reference','active','2026-01-03','2026-01-04')`,
		`INSERT INTO memory_tags(memory_id,tag) VALUES('mem-project','kept-tag')`,
		`INSERT INTO memory_revisions(memory_id,title,content,category,status,tags_json,updated_at,revised_at) VALUES('mem-project','project title','project body','decision','active','["kept-tag"]','2026-01-02','2026-01-02')`,
		`PRAGMA user_version=5`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("prepare v5 database: %v", err)
		}
	}
	if err := migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != 6 {
		t.Fatalf("schema version = %d, err=%v; want 6", version, err)
	}
	var projectID, kbID sql.NullString
	var title, content, category, status, created, updated string
	if err := db.QueryRow(`SELECT project_id,knowledge_base_id,title,content,category,status,created_at,updated_at FROM memories WHERE id='mem-project'`).Scan(&projectID, &kbID, &title, &content, &category, &status, &created, &updated); err != nil {
		t.Fatal(err)
	}
	if projectID.String != "prj" || kbID.Valid || title != "project title" || content != "project body" || category != "decision" || status != "deleted" || created != "2026-01-01" || updated != "2026-01-02" {
		t.Fatalf("project memory changed during migration: project=%v kb=%v title=%q content=%q category=%q status=%q created=%q updated=%q", projectID, kbID, title, content, category, status, created, updated)
	}
	if err := db.QueryRow(`SELECT project_id,knowledge_base_id,title,content FROM memories WHERE id='mem-shared'`).Scan(&projectID, &kbID, &title, &content); err != nil {
		t.Fatal(err)
	}
	if projectID.Valid || kbID.String != "kb-shared" || title != "shared title" || content != "shared body" {
		t.Fatalf("shared memory changed during migration: project=%v kb=%v title=%q content=%q", projectID, kbID, title, content)
	}
	var tag, revisionStatus, revisionTags string
	if err := db.QueryRow(`SELECT tag FROM memory_tags WHERE memory_id='mem-project'`).Scan(&tag); err != nil || tag != "kept-tag" {
		t.Fatalf("tag lost during migration: tag=%q err=%v", tag, err)
	}
	if err := db.QueryRow(`SELECT status,tags_json FROM memory_revisions WHERE memory_id='mem-project'`).Scan(&revisionStatus, &revisionTags); err != nil || revisionStatus != "active" || revisionTags != `["kept-tag"]` {
		t.Fatalf("revision lost during migration: status=%q tags=%q err=%v", revisionStatus, revisionTags, err)
	}
	for query, id := range map[string]string{"project body": "mem-project", "shared body": "mem-shared"} {
		var count int
		if err := db.QueryRow(`SELECT count(*) FROM memories_fts WHERE memories_fts MATCH ?`, query).Scan(&count); err != nil || count != 1 {
			t.Fatalf("FTS index for %q: count=%d err=%v", query, count, err)
		}
		var found string
		if err := db.QueryRow(`SELECT m.id FROM memories m WHERE m.rowid IN (SELECT rowid FROM memories_fts WHERE memories_fts MATCH ?)`, query).Scan(&found); err != nil || found != id {
			t.Fatalf("FTS lookup for %q = %q, err=%v; want %q", query, found, err, id)
		}
	}
	var kbCount int
	if err := db.QueryRow(`SELECT count(*) FROM knowledge_bases`).Scan(&kbCount); err != nil || kbCount != 1 {
		t.Fatalf("project libraries were not removed: count=%d err=%v", kbCount, err)
	}
	if err := db.QueryRow(`SELECT id FROM knowledge_bases WHERE id='kb-shared'`).Scan(&tag); err != nil {
		t.Fatalf("shared library lost during migration: %v", err)
	}
	var object string
	if err := db.QueryRow(`SELECT name FROM sqlite_master WHERE name='project_default_kb'`).Scan(&object); err == nil {
		t.Fatal("obsolete project_default_kb index remains")
	}
	if err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type='index' AND name='memories_project_status'`).Scan(&object); err != nil {
		t.Fatalf("missing memories_project_status index: %v", err)
	}
	var invalid int
	if err := db.QueryRow(`SELECT count(*) FROM memories WHERE (project_id IS NULL)=(knowledge_base_id IS NULL)`).Scan(&invalid); err != nil || invalid != 0 {
		t.Fatalf("migrated memories violate ownership: count=%d err=%v", invalid, err)
	}
	foreignKeyRows, err := db.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	if foreignKeyRows.Next() {
		foreignKeyRows.Close()
		t.Fatal("migrated database has foreign-key violations")
	}
	if err := foreignKeyRows.Err(); err != nil {
		foreignKeyRows.Close()
		t.Fatal(err)
	}
	if err := foreignKeyRows.Close(); err != nil {
		t.Fatal(err)
	}
	if err := validateMemDockDatabase(db); err != nil {
		t.Fatalf("migrated schema should be accepted as a valid backup: %v", err)
	}
}

func TestMigrateV1ToV6DropsObsoleteAuthorizationTable(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "legacy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE used_write_authorizations (nonce TEXT PRIMARY KEY, used_at INTEGER NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`PRAGMA user_version=0`); err != nil {
		t.Fatal(err)
	}
	if err := migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 6 {
		t.Fatalf("schema version = %d, want 6", version)
	}
	var table string
	for _, expected := range []string{"memory_tags", "memory_revisions", "project_operations", "admin_credentials", "admin_sessions"} {
		if err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, expected).Scan(&table); err != nil {
			t.Fatalf("missing table %s: %v", expected, err)
		}
	}
	if err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name='used_write_authorizations'`).Scan(&table); err == nil {
		t.Fatal("obsolete used_write_authorizations table was not removed")
	}
}
