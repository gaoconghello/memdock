package main

import (
	"encoding/json"
	"testing"
)

func TestValidateMCPArgumentsRejectsMissingAndMalformedFields(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{"search missing query", "{\"project_id\":\"p1\"}"},
		{"search query has wrong type", "{\"project_id\":\"p1\",\"query\":7}"},
		{"search limit has wrong type", "{\"project_id\":\"p1\",\"query\":\"note\",\"limit\":\"5\"}"},
		{"save missing project", "{\"title\":\"title\",\"content\":\"body\"}"},
		{"save content has wrong type", "{\"project_id\":\"p1\",\"title\":\"title\",\"content\":7}"},
		{"save tags contain non-string", "{\"project_id\":\"p1\",\"title\":\"title\",\"content\":\"body\",\"tags\":[\"ok\",2]}"},
		{"list missing project", "{}"},
		{"list status has wrong type", "{\"project_id\":\"p1\",\"status\":1}"},
		{"update missing id", "{\"project_id\":\"p1\",\"title\":\"new\"}"},
		{"update has no fields", "{\"id\":\"mem1\",\"project_id\":\"p1\"}"},
		{"update tags cannot be null", "{\"id\":\"mem1\",\"project_id\":\"p1\",\"tags\":null}"},
		{"delete missing action", "{\"id\":\"mem1\",\"project_id\":\"p1\"}"},
		{"delete invalid action", "{\"id\":\"mem1\",\"project_id\":\"p1\",\"action\":\"purge\"}"},
		{"delete action has wrong type", "{\"id\":\"mem1\",\"project_id\":\"p1\",\"action\":true}"},
		{"project info id has wrong type", "{\"project_id\":[]}"},
		{"unknown argument", "{\"project_id\":\"p1\",\"query\":\"x\",\"unexpected\":true}"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var args map[string]json.RawMessage
			if err := json.Unmarshal([]byte(tc.raw), &args); err != nil {
				t.Fatal(err)
			}
			name := "memory_search"
			switch tc.name {
			case "save missing project", "save content has wrong type", "save tags contain non-string":
				name = "memory_save"
			case "list missing project", "list status has wrong type":
				name = "memory_list"
			case "update missing id", "update has no fields", "update tags cannot be null":
				name = "memory_update"
			case "delete missing action", "delete invalid action", "delete action has wrong type":
				name = "memory_delete"
			case "project info id has wrong type":
				name = "project_info"
			}
			if err := validateMCPArguments(name, args); err == nil {
				t.Fatalf("accepted invalid arguments: %s", tc.raw)
			}
		})
	}
}

func TestValidateMCPArgumentsAcceptsValidToolCalls(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{"memory_search", `{"project_id":"p1","query":"search","limit":5}`},
		{"memory_save", `{"project_id":"p1","title":"title","content":"body","tags":["tag"]}`},
		{"memory_list", `{"project_id":"p1","status":"deleted"}`},
		{"memory_update", `{"id":"mem1","project_id":"p1","tags":[]}`},
		{"memory_delete", `{"id":"mem1","project_id":"p1","action":"delete"}`},
		{"memory_delete restore", `{"id":"mem1","project_id":"p1","action":"restore"}`},
		{"project_info", `{"project_id":"p1"}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			name := tc.name
			if name == "memory_delete restore" {
				name = "memory_delete"
			}
			var args map[string]json.RawMessage
			if err := json.Unmarshal([]byte(tc.raw), &args); err != nil {
				t.Fatal(err)
			}
			if err := validateMCPArguments(name, args); err != nil {
				t.Fatalf("rejected valid arguments: %v", err)
			}
		})
	}
}
