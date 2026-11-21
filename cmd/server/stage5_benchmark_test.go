//go:build stage5benchmark

package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestStage5Benchmark100k(t *testing.T) {
	db, err := sql.Open("sqlite", t.TempDir()+"/bench.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err = db.Exec(`PRAGMA journal_mode=WAL`); err != nil {
		t.Fatal(err)
	}
	if err = migrate(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO projects(id,name,created_at) VALUES('p','benchmark','2026-01-01')`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO knowledge_bases(id,project_id,name,kind,is_default,created_at) VALUES('k','p','default','project',1,'2026-01-01')`); err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	stmt, err := tx.Prepare(`INSERT INTO memories(id,knowledge_base_id,title,content,category,created_at,updated_at) VALUES(?,?,?,?,?,?,?)`)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 100000; i++ {
		content := fmt.Sprintf("system design notes component-%06d database cache deployment reliability", i)
		if i%100 == 0 {
			content += " architecture decision benchmark phrase"
		}
		id := fmt.Sprintf("m-%06d", i)
		if _, err = stmt.Exec(id, "k", fmt.Sprintf("Engineering note %06d", i), content, "design", "2026-01-01", "2026-01-01"); err != nil {
			t.Fatal(err)
		}
	}
	_ = stmt.Close()
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	a := &app{db: db, writeSecret: []byte(strings.Repeat("k", 32)), mode: "private"}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/memories/search", a.searchMemories)
	mux.HandleFunc("POST /api/v1/memories", a.createMemory)
	server := httptest.NewServer(a.authenticate(mux))
	defer server.Close()
	client := server.Client()
	dbTimes := make([]int64, 100)
	apiTimes := make([]int64, 100)
	saveTimes := make([]int64, 100)
	// Run ten untimed warm-up rounds before collecting 100 measured samples.
	for i := -10; i < 100; i++ {
		start := time.Now()
		rows, err := db.Query(`SELECT m.id,m.title,m.content,m.category,k.id,k.name,k.kind,m.updated_at FROM memories m JOIN knowledge_bases k ON k.id=m.knowledge_base_id WHERE m.status='active' AND (k.kind='shared' OR k.project_id=?) AND m.rowid IN (SELECT rowid FROM memories_fts WHERE memories_fts MATCH ?) ORDER BY m.updated_at DESC LIMIT ?`, "p", `"architecture" AND "decision"`, 5)
		if err != nil {
			t.Fatal(err)
		}
		count := 0
		for rows.Next() {
			var x [8]string
			if err = rows.Scan(&x[0], &x[1], &x[2], &x[3], &x[4], &x[5], &x[6], &x[7]); err != nil {
				t.Fatal(err)
			}
			count++
		}
		if err = rows.Err(); err != nil {
			t.Fatal(err)
		}
		if err = rows.Close(); err != nil {
			t.Fatal(err)
		}
		if count != 5 {
			t.Fatalf("database search returned %d results, want 5", count)
		}
		if i >= 0 {
			dbTimes[i] = time.Since(start).Nanoseconds()
		}

		body, err := json.Marshal(searchRequest{ProjectID: "p", Query: "architecture decision", Limit: 5})
		if err != nil {
			t.Fatal(err)
		}
		start = time.Now()
		response, err := client.Post(server.URL+"/api/v1/memories/search", "application/json", strings.NewReader(string(body)))
		if err != nil {
			t.Fatal(err)
		}
		responseBody, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		if err = response.Body.Close(); err != nil {
			t.Fatal(err)
		}
		if i >= 0 {
			apiTimes[i] = time.Since(start).Nanoseconds()
		}
		var searchResponse struct {
			Results []searchResult `json:"results"`
		}
		if response.StatusCode != 200 {
			t.Fatalf("search status %d: %s", response.StatusCode, responseBody)
		}
		if err = json.Unmarshal(responseBody, &searchResponse); err != nil {
			t.Fatal(err)
		}
		if len(searchResponse.Results) != 5 {
			t.Fatalf("API search returned %d results, want 5", len(searchResponse.Results))
		}

		saveBody, err := json.Marshal(createMemoryRequest{ProjectID: "p", Title: fmt.Sprintf("benchmark save %d", i), Content: "small representative memory payload"})
		if err != nil {
			t.Fatal(err)
		}
		req, err := http.NewRequest("POST", server.URL+"/api/v1/memories", strings.NewReader(string(saveBody)))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-MemDock-Write-Secret", string(a.writeSecret))
		start = time.Now()
		response, err = client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		responseBody, err = io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		if err = response.Body.Close(); err != nil {
			t.Fatal(err)
		}
		if i >= 0 {
			saveTimes[i] = time.Since(start).Nanoseconds()
		}
		if response.StatusCode != 201 {
			t.Fatalf("save status %d: %s", response.StatusCode, responseBody)
		}
	}
	percentile := func(v []int64) float64 {
		sort.Slice(v, func(i, j int) bool { return v[i] < v[j] })
		return float64(v[94]) / 1e6
	}
	fmt.Printf("STAGE5_BENCH records=100000 iterations=100 concurrency=1 db_search_p95_ms=%.3f api_search_p95_ms=%.3f api_save_p95_ms=%.3f\n", percentile(dbTimes), percentile(apiTimes), percentile(saveTimes))
	var count int
	if err = db.QueryRow(`SELECT count(*) FROM memories`).Scan(&count); err != nil || count != 100110 {
		t.Fatalf("memory count %d, want 100110; err %v", count, err)
	}
}
