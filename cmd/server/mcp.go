package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
)

const mcpProtocolVersion = "2025-03-26"

type mcpRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      any    `json:"id"`
	Method  string `json:"method"`
	Params  struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	} `json:"params"`
}

func (a *app) mcp(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("MCP-Protocol-Version", mcpProtocolVersion)
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeError(w, http.StatusMethodNotAllowed, "MCP accepts POST requests")
		return
	}
	if !strings.Contains(r.Header.Get("Accept"), "application/json") && !strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
		writeError(w, http.StatusNotAcceptable, "Accept must include application/json or text/event-stream")
		return
	}
	var req mcpRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil || req.JSONRPC != "2.0" {
		writeJSON(w, http.StatusBadRequest, rpcError(nil, -32700, "invalid JSON-RPC request"))
		return
	}
	var result any
	switch req.Method {
	case "initialize":
		result = map[string]any{"protocolVersion": mcpProtocolVersion, "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]string{"name": "memdock", "version": "0.1"}}
	case "notifications/initialized", "notifications/cancelled":
		w.WriteHeader(http.StatusAccepted)
		return
	case "ping":
		result = map[string]any{}
	case "tools/list":
		result = map[string]any{"tools": mcpTools()}
	case "tools/call":
		var err error
		result, err = a.callMCPTool(r, req.Params.Name, req.Params.Arguments)
		if err != nil {
			result = map[string]any{"content": []any{map[string]string{"type": "text", "text": err.Error()}}, "isError": true}
		}
	default:
		writeJSON(w, http.StatusOK, rpcError(req.ID, -32601, "method not found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
}

func rpcError(id any, code int, message string) map[string]any {
	return map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": code, "message": message}}
}

func mcpTools() []map[string]any {
	obj := func(props map[string]any, required ...string) map[string]any {
		if required == nil {
			required = []string{}
		}
		return map[string]any{"type": "object", "properties": props, "required": required, "additionalProperties": false}
	}
	str := map[string]any{"type": "string"}
	props := func(names ...string) map[string]any {
		m := map[string]any{}
		for _, n := range names {
			m[n] = str
		}
		return m
	}
	lookupProjectID := map[string]any{"type": "string", "description": "Verified project ID. Use the current directory binding or project_lookup with the Git remote. For an explicitly named project, use project_list to find its ID."}
	sharedLibraryID := map[string]any{"type": "string", "description": "Shared library ID returned by knowledge_base_list. If the user gives a library name, discover and call knowledge_base_list, then use the ID of the exact name match."}
	return []map[string]any{
		{"name": "memory_search", "description": "Search active memories by title and content. Resolve the project ID first. By default, search that project and all shared libraries. To search only a named shared library, discover and call knowledge_base_list, match the name, and pass its ID as knowledge_base_id. A valid project_id is still required. If multiple libraries match, ask the user to choose.", "inputSchema": obj(map[string]any{"project_id": lookupProjectID, "query": str, "knowledge_base_id": sharedLibraryID, "tag": str, "limit": map[string]any{"type": "integer"}}, "project_id", "query")},
		{"name": "memory_save", "description": "Save only after the client verifies project_id against the current directory binding or an explicit user selection; A request to save to MemDock refers to the service unless the user explicitly names a target project. Apply this rule with or without Git. For an unbound folder without a Git remote, use its basename as the default for new project creation after user confirmation. Never guess a project from project_list or a name. Omit knowledge_base_id to save a project memory. Use knowledge_base_list to find the ID of the shared library named by the user. For a request to save to both destinations, resolve both first, then make two separate calls. The server cannot inspect the client directory.", "inputSchema": obj(map[string]any{"project_id": lookupProjectID, "knowledge_base_id": sharedLibraryID, "title": str, "content": str, "category": str, "tags": map[string]any{"type": "array", "items": str}}, "project_id", "title", "content")},
		{"name": "memory_list", "description": "List memories without a keyword query. By default, list the selected project and all shared libraries. For a named shared library, discover and call knowledge_base_list, match the name, and pass its ID as knowledge_base_id. A valid project_id is still required.", "inputSchema": obj(map[string]any{"project_id": lookupProjectID, "knowledge_base_id": sharedLibraryID, "status": str, "tag": str}, "project_id")},
		{"name": "memory_update", "description": "Update a memory by ID.", "inputSchema": obj(map[string]any{"id": str, "project_id": str, "title": str, "content": str, "category": str, "tags": map[string]any{"type": "array", "items": str}}, "id", "project_id")},
		{"name": "memory_delete", "description": "Soft-delete or restore a memory.", "inputSchema": obj(map[string]any{"id": str, "project_id": str, "action": map[string]any{"type": "string", "enum": []string{"delete", "restore"}}}, "id", "project_id", "action")},
		{"name": "project_info", "description": "Get project details including bound repositories.", "inputSchema": obj(props("project_id"), "project_id")},
		{"name": "knowledge_base_list", "description": "Find shared knowledge bases by name before searching, listing, or saving memories. Returns all shared library names and IDs. Match the name specified by the user and ask the user to choose if multiple libraries match.", "inputSchema": obj(map[string]any{})},
		{"name": "project_list", "description": "List accessible projects as candidates; for an unbound local folder the user must choose a project, never select one by guessing.", "inputSchema": obj(map[string]any{})},
		{"name": "project_lookup", "description": "Read-only lookup by client-provided Git remote or project ID; does not create or bind projects.", "inputSchema": obj(props("repository", "project_id"))},
		{"name": "project_resolve", "description": "After user confirmation, create a project using name and/or repository, omitting project_id. For an unbound folder without a Git remote, use the client folder basename as the default name; the server generates and returns the ID. To select an existing project or bind its repository, provide its existing project_id. The server cannot inspect the client's working directory.", "inputSchema": obj(props("repository", "name", "project_id"))},
	}
}

type mcpArgumentSpec struct {
	kind     string
	required bool
}

func validateMCPArguments(name string, args map[string]json.RawMessage) error {
	str := func(required bool) mcpArgumentSpec { return mcpArgumentSpec{kind: "string", required: required} }
	schemas := map[string]map[string]mcpArgumentSpec{
		"memory_search":       {"project_id": str(true), "query": str(true), "knowledge_base_id": str(false), "tag": str(false), "limit": {kind: "integer"}},
		"memory_save":         {"project_id": str(true), "knowledge_base_id": str(false), "title": str(true), "content": str(true), "category": str(false), "tags": {kind: "string_array"}},
		"memory_list":         {"project_id": str(true), "knowledge_base_id": str(false), "status": str(false), "tag": str(false)},
		"memory_update":       {"id": str(true), "project_id": str(true), "title": str(false), "content": str(false), "category": str(false), "tags": {kind: "string_array"}},
		"memory_delete":       {"id": str(true), "project_id": str(true), "action": str(true)},
		"project_info":        {"project_id": str(true)},
		"knowledge_base_list": {},
		"project_list":        {},
		"project_lookup":      {"repository": str(false), "project_id": str(false)},
		"project_resolve":     {"repository": str(false), "name": str(false), "project_id": str(false)},
	}
	schema, ok := schemas[name]
	if !ok {
		return fmt.Errorf("unknown tool %q", name)
	}
	for key, value := range args {
		spec, ok := schema[key]
		if !ok {
			return fmt.Errorf("unknown argument %q for %s", key, name)
		}
		value = bytes.TrimSpace(value)
		if len(value) == 0 || bytes.Equal(value, []byte("null")) {
			return fmt.Errorf("argument %q must not be null", key)
		}
		switch spec.kind {
		case "string":
			var text string
			if err := json.Unmarshal(value, &text); err != nil {
				return fmt.Errorf("argument %q must be a string", key)
			}
			if spec.required && strings.TrimSpace(text) == "" {
				return fmt.Errorf("argument %q is required", key)
			}
			if (key == "id" || key == "project_id") && strings.ContainsAny(text, "/?#") {
				return fmt.Errorf("argument %q must be a single identifier", key)
			}
		case "integer":
			var number int
			if err := json.Unmarshal(value, &number); err != nil {
				return fmt.Errorf("argument %q must be an integer", key)
			}
		case "string_array":
			var items []json.RawMessage
			if err := json.Unmarshal(value, &items); err != nil {
				return fmt.Errorf("argument %q must be an array of strings", key)
			}
			for _, item := range items {
				item = bytes.TrimSpace(item)
				var text string
				if bytes.Equal(item, []byte("null")) || json.Unmarshal(item, &text) != nil {
					return fmt.Errorf("argument %q must contain only strings", key)
				}
				if len(strings.TrimSpace(text)) > 100 {
					return fmt.Errorf("argument %q contains a tag longer than 100 bytes", key)
				}
			}
		}
	}
	for key, spec := range schema {
		if spec.required {
			if _, ok := args[key]; !ok {
				return fmt.Errorf("argument %q is required", key)
			}
		}
	}
	getString := func(key string) string {
		var value string
		_ = json.Unmarshal(args[key], &value)
		return value
	}
	switch name {
	case "project_lookup":
		if strings.TrimSpace(getString("repository")) == "" && strings.TrimSpace(getString("project_id")) == "" {
			return fmt.Errorf("repository or project_id is required")
		}
	case "project_resolve":
		if strings.TrimSpace(getString("repository")) == "" && strings.TrimSpace(getString("name")) == "" && strings.TrimSpace(getString("project_id")) == "" {
			return fmt.Errorf("repository, name, or project_id is required")
		}
	case "memory_search":
		if strings.TrimSpace(getString("query")) == "" {
			return fmt.Errorf("argument %q is required", "query")
		}
	case "memory_save":
		if title, content := strings.TrimSpace(getString("title")), strings.TrimSpace(getString("content")); title == "" || content == "" {
			return fmt.Errorf("title and content are required")
		} else if len(title) > 300 || len(content) > maxMemoryContentBytes {
			return fmt.Errorf("title must be <= 300 bytes and content <= %d bytes", maxMemoryContentBytes)
		}
	case "memory_list":
		if status := getString("status"); status != "" && status != "active" && status != "deleted" {
			return fmt.Errorf("argument %q must be active or deleted", "status")
		}
	case "memory_update":
		changed := false
		for _, key := range []string{"title", "content", "category", "tags"} {
			if _, present := args[key]; present {
				changed = true
				if key == "title" || key == "content" {
					value := strings.TrimSpace(getString(key))
					if value == "" {
						return fmt.Errorf("argument %q must not be empty", key)
					}
					max := 300
					if key == "content" {
						max = maxMemoryContentBytes
					}
					if len(value) > max {
						return fmt.Errorf("argument %q exceeds %d bytes", key, max)
					}
				}
			}
		}
		if !changed {
			return fmt.Errorf("at least one update field is required")
		}
	case "memory_delete":
		if action := getString("action"); action != "delete" && action != "restore" {
			return fmt.Errorf("argument %q must be delete or restore", "action")
		}
	}
	return nil
}

func (a *app) callMCPTool(parent *http.Request, name string, raw json.RawMessage) (any, error) {
	args := map[string]json.RawMessage{}
	if len(raw) == 0 {
		raw = []byte(`{}`)
	}
	if err := json.Unmarshal(raw, &args); err != nil || args == nil {
		return nil, fmt.Errorf("arguments must be an object")
	}
	if err := validateMCPArguments(name, args); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}
	get := func(k string) string { var s string; _ = json.Unmarshal(args[k], &s); return s }
	var method, path string
	switch name {
	case "knowledge_base_list":
		method, path = "GET", "/api/v1/knowledge-bases"
	case "project_list":
		method, path = "GET", "/api/v1/projects"
	case "project_lookup":
		method, path = "POST", "/api/v1/projects/lookup"
	case "project_resolve":
		method, path = "POST", "/api/v1/projects/resolve"
	case "memory_search":
		method, path = "POST", "/api/v1/memories/search"
	case "memory_save":
		method, path = "POST", "/api/v1/memories"
	case "memory_list":
		method = "GET"
		path = "/api/v1/memories"
		query := url.Values{}
		query.Set("project_id", get("project_id"))
		for _, k := range []string{"knowledge_base_id", "status", "tag"} {
			if v := get(k); v != "" {
				query.Set(k, v)
			}
		}
		path += "?" + query.Encode()
	case "memory_update":
		method, path = "PATCH", "/api/v1/memories/"+get("id")
	case "memory_delete":
		if get("action") == "restore" {
			method, path = "POST", "/api/v1/memories/"+get("id")+"/restore"
		} else {
			method, path = "DELETE", "/api/v1/memories/"+get("id")
		}
	case "project_info":
		method, path = "GET", "/api/v1/projects/"+get("project_id")
	default:
		return nil, fmt.Errorf("unknown tool %q", name)
	}
	var body []byte
	if method != "GET" {
		if name == "memory_update" || name == "memory_delete" {
			delete(args, "id")
		}
		if name == "memory_delete" {
			delete(args, "action")
		}
		body, _ = json.Marshal(args)
	}
	pathOnly := strings.SplitN(path, "?", 2)[0]
	url := path
	if !strings.HasPrefix(url, "/") {
		return nil, fmt.Errorf("invalid tool path")
	}
	req := httptest.NewRequest(method, url, bytes.NewReader(body))
	req = req.WithContext(parent.Context())
	req.Header.Set("Content-Type", "application/json")
	if writeKey := parent.Header.Get("X-MemDock-Write-Secret"); writeKey != "" {
		req.Header.Set("X-MemDock-Write-Secret", writeKey)
	}
	if name == "memory_update" || name == "memory_delete" || name == "project_info" {
		parts := strings.Split(strings.Trim(pathOnly, "/"), "/")
		if len(parts) > 0 {
			req.SetPathValue("id", parts[len(parts)-1])
			if name == "memory_delete" && len(parts) > 1 && parts[len(parts)-1] == "restore" {
				req.SetPathValue("id", parts[len(parts)-2])
			}
		}
	}
	rec := httptest.NewRecorder()
	var handler http.HandlerFunc
	switch name {
	case "knowledge_base_list":
		handler = a.listKnowledgeBases
	case "project_list":
		handler = a.listProjects
	case "project_lookup":
		handler = a.lookupProject
	case "project_resolve":
		handler = a.resolveProject
	case "memory_search":
		handler = a.searchMemories
	case "memory_save":
		handler = a.createMemory
	case "memory_list":
		handler = a.listMemories
	case "memory_update":
		handler = a.updateMemory
	case "memory_delete":
		if get("action") == "restore" {
			handler = a.restoreMemory
		} else {
			handler = a.deleteMemory
		}
	case "project_info":
		handler = a.projectDetail
	}
	handler(rec, req)
	payload := rec.Body.Bytes()
	if rec.Code < 200 || rec.Code >= 300 {
		return nil, fmt.Errorf("MemDock API returned %d: %s", rec.Code, strings.TrimSpace(string(payload)))
	}
	if len(payload) == 0 {
		payload = []byte(`{}`)
	}
	return map[string]any{"content": []any{map[string]string{"type": "text", "text": string(payload)}}, "structuredContent": json.RawMessage(payload), "isError": false}, nil
}
