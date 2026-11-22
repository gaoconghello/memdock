package main

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMCPRepositoryBindingLifecycle(t *testing.T) {
	a := newMCPTestApp(t)
	for _, query := range []string{
		`INSERT INTO projects VALUES ('pulse','pulse','now'),('other','other','now')`,
		`INSERT INTO repositories VALUES ('d/Project/pulse','pulse'),('gitee.com/example/taken','other')`,
		`INSERT INTO memories(id,project_id,title,content,created_at,updated_at) VALUES ('memory','pulse','title','keep me','now','now')`,
	} {
		if _, err := a.db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest("POST", "/mcp", nil)
	check := func(tool, args, want string) {
		t.Helper()
		_, err := a.callMCPTool(req, tool, json.RawMessage(args))
		if (want == "" && err != nil) || (want != "" && (err == nil || !strings.Contains(err.Error(), want))) {
			t.Fatalf("%s: got %v, want error containing %q", tool, err, want)
		}
	}
	update := "project_repository_update"
	remove := "project_repository_delete"
	replacement := `{"project_id":"pulse","repository":"d/Project/pulse","new_repository":"https://gitee.com/example/pulse.git"}`
	check(update, replacement, "401")
	check(remove, `{"project_id":"pulse","repository":"d/Project/pulse"}`, "401")
	req.Header.Set("X-MemDock-Write-Secret", string(a.writeSecret))
	check(update, `{"project_id":"pulse","repository":"d/Project/pulse"}`, "new_repository")
	check(update, `{"project_id":"pulse","repository":"d/Project/pulse","new_repository":"https://gitee.com/example/taken"}`, "409")
	check(remove, `{"project_id":"other","repository":"d/Project/pulse"}`, "404")
	check(update, replacement, "")
	check(remove, `{"project_id":"pulse","repository":"d/Project/pulse"}`, "404")
	check(remove, `{"project_id":"pulse","repository":"gitee.com/example/pulse"}`, "")
	var projects, bindings int
	if err := a.db.QueryRow(`SELECT count(*) FROM projects`).Scan(&projects); err != nil {
		t.Fatal(err)
	}
	if err := a.db.QueryRow(`SELECT count(*) FROM repositories WHERE project_id='other' AND canonical='gitee.com/example/taken'`).Scan(&bindings); err != nil {
		t.Fatal(err)
	}
	var content string
	if err := a.db.QueryRow(`SELECT content FROM memories WHERE id='memory' AND project_id='pulse'`).Scan(&content); err != nil || content != "keep me" {
		t.Fatalf("memory not preserved: content=%q err=%v", content, err)
	}
	if projects != 2 || bindings != 1 {
		t.Fatalf("unexpected projects=%d or other bindings=%d", projects, bindings)
	}
}
