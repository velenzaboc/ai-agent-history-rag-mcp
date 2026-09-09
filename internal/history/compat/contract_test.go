package compat

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func contractBytes(t *testing.T) []byte {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve contract test path")
	}
	data, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", "..", "..", "testdata", "native-contract", "v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestContractRecordsCompleteNativeReplacementSurface(t *testing.T) {
	contract, err := Parse(contractBytes(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(contract.MCPTools) != 5 || len(contract.HTTPEndpoints) != 23 || len(contract.Sources) != 6 || len(contract.Entrypoints) != 5 {
		t.Fatalf("contract cardinality = tools:%d endpoints:%d sources:%d entrypoints:%d", len(contract.MCPTools), len(contract.HTTPEndpoints), len(contract.Sources), len(contract.Entrypoints))
	}
	requireNames(t, names(contract.MCPTools, func(value MCPTool) string { return value.Name }), "search_conversations", "search_file_changes", "get_session_summary", "get_index_status", "get_server_status")
	requireNames(t, names(contract.Sources, func(value SourceFamily) string { return value.Name }), "claude_code", "codex", "gemini", "antigravity", "chatgpt_export", "claude_app_export")
	requireNames(t, names(contract.Entrypoints, func(value Entrypoint) string { return value.Name }), "docker", "systemd", "launchd", "scheduled_task", "mcp_stdio")
	for _, limit := range contract.PayloadLimits {
		if limit.MaxBytes != 1<<20 || !limit.Inclusive {
			t.Fatalf("payload limit %#v is not the inclusive 1 MiB contract", limit)
		}
	}
	for _, endpoint := range contract.HTTPEndpoints {
		if endpoint.Authentication != "bearer" && endpoint.Authentication != "dashboard_hash" {
			t.Fatalf("endpoint %#v has unknown authentication", endpoint)
		}
	}
}

func TestContractHasRequiredProductionEnvironmentCoordinates(t *testing.T) {
	contract, err := Parse(contractBytes(t))
	if err != nil {
		t.Fatal(err)
	}
	requireNames(t, names(contract.Environment, func(value Environment) string { return value.Name }),
		"CLAUDE_HISTORY_RAG_RUNTIME_CONTRACT", "CLAUDE_HISTORY_RAG_CREDENTIALS_SOURCE", "CLAUDE_HISTORY_RAG_CREDENTIALS_PROFILE", "CLAUDE_HISTORY_RAG_CREDENTIALS_IDENTITY",
		"CLAUDE_HISTORY_RAG_STORAGE_BACKEND", "CLAUDE_HISTORY_RAG_SPANNER_PROJECT", "CLAUDE_HISTORY_RAG_SPANNER_INSTANCE", "CLAUDE_HISTORY_RAG_SPANNER_DATABASE",
		"CLAUDE_HISTORY_RAG_STATUS_SERVER_HOST", "CLAUDE_HISTORY_RAG_STATUS_SERVER_PORT", "CLAUDE_HISTORY_RAG_AUTH_ENABLED")
	for _, source := range contract.Sources {
		if !strings.HasPrefix(source.RootEnv, "CLAUDE_HISTORY_RAG_") || !strings.HasPrefix(source.StateEnv, "CLAUDE_HISTORY_RAG_") {
			t.Fatalf("source %#v uses an unnamespaced coordinate", source)
		}
	}
}

func TestParseRejectsContractDrift(t *testing.T) {
	valid := string(contractBytes(t))
	for name, raw := range map[string]string{
		"unknown version": strings.Replace(valid, CurrentVersion, "2.0.0", 1),
		"duplicate tool":  strings.Replace(valid, `"search_file_changes"`, `"search_conversations"`, 1),
		"unbounded tool":  strings.Replace(valid, `"max_request_bytes": 1048576`, `"max_request_bytes": 0`, 1),
		"bad environment": strings.Replace(valid, `"CLAUDE_HISTORY_RAG_AUTH_ENABLED"`, `"AUTH_ENABLED"`, 1),
		"missing entry":   strings.Replace(valid, `"replacement": ["history-ragd", "start"]`, `"replacement": []`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse([]byte(raw)); err == nil {
				t.Fatal("Parse accepted contract drift")
			}
		})
	}
}

func TestValidateRejectsEveryContractSectionBoundary(t *testing.T) {
	baseline, err := Parse(contractBytes(t))
	if err != nil {
		t.Fatal(err)
	}
	tests := map[string]func(*Contract){
		"empty payload section":       func(contract *Contract) { contract.PayloadLimits = nil },
		"exclusive payload":           func(contract *Contract) { contract.PayloadLimits[0].Inclusive = false },
		"empty MCP section":           func(contract *Contract) { contract.MCPTools = nil },
		"invalid MCP path":            func(contract *Contract) { contract.MCPTools[0].Path = "api/search" },
		"empty endpoint section":      func(contract *Contract) { contract.HTTPEndpoints = nil },
		"invalid endpoint method":     func(contract *Contract) { contract.HTTPEndpoints[0].Method = "PUT" },
		"empty source section":        func(contract *Contract) { contract.Sources = nil },
		"incomplete source":           func(contract *Contract) { contract.Sources[0].StateEnv = "" },
		"empty environment section":   func(contract *Contract) { contract.Environment = nil },
		"missing environment purpose": func(contract *Contract) { contract.Environment[0].Purpose = "" },
		"empty entrypoint section":    func(contract *Contract) { contract.Entrypoints = nil },
		"missing entrypoint platform": func(contract *Contract) { contract.Entrypoints[0].Platform = "" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			candidate := cloneContract(baseline)
			mutate(&candidate)
			if err := candidate.Validate(); err == nil {
				t.Fatal("Validate accepted incomplete contract")
			}
		})
	}
	if _, err := Parse([]byte("{")); err == nil {
		t.Fatal("Parse accepted malformed JSON")
	}
}

func cloneContract(value Contract) Contract {
	clone := value
	clone.PayloadLimits = append([]PayloadLimit(nil), value.PayloadLimits...)
	clone.MCPTools = append([]MCPTool(nil), value.MCPTools...)
	clone.HTTPEndpoints = append([]HTTPEndpoint(nil), value.HTTPEndpoints...)
	clone.Sources = append([]SourceFamily(nil), value.Sources...)
	clone.Environment = append([]Environment(nil), value.Environment...)
	clone.Entrypoints = append([]Entrypoint(nil), value.Entrypoints...)
	return clone
}

func names[T any](values []T, name func(T) string) map[string]bool {
	result := make(map[string]bool, len(values))
	for _, value := range values {
		result[name(value)] = true
	}
	return result
}

func requireNames(t *testing.T, present map[string]bool, expected ...string) {
	t.Helper()
	for _, name := range expected {
		if !present[name] {
			t.Fatalf("contract does not contain %q", name)
		}
	}
}
