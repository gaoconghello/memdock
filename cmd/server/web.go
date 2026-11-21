package main

import (
	"embed"
	"net/http"
)

//go:embed web/*
var webFiles embed.FS

func (a *app) webIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	http.ServeFileFS(w, r, webFiles, "web/index.html")
}

func (a *app) systemStatus(w http.ResponseWriter, r *http.Request) {
	var projects, memories int
	if err := a.db.QueryRowContext(r.Context(), `SELECT count(*) FROM projects`).Scan(&projects); err != nil {
		writeError(w, 503, "database unavailable")
		return
	}
	if err := a.db.QueryRowContext(r.Context(), `SELECT count(*) FROM memories WHERE status='active'`).Scan(&memories); err != nil {
		writeError(w, 503, "database unavailable")
		return
	}
	mode := a.mode
	if mode == "" {
		mode = "private"
	}
	writeJSON(w, 200, map[string]any{"status": "ok", "version": "1.0.0", "mode": mode, "database": "ok", "projects": projects, "memories": memories, "counts_visible": true})
}
