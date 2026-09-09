package runtimeconfig

import (
	"reflect"
	"strings"
	"testing"
)

const testIdentity = "history-rag-test@fixture-project.iam.gserviceaccount.com"

func validProductionEnvironment() map[string]string {
	return map[string]string{
		"CLAUDE_HISTORY_RAG_RUNTIME_CONTRACT":           "production",
		"CLAUDE_HISTORY_RAG_SPANNER_PROJECT":            "fixture-project",
		"CLAUDE_HISTORY_RAG_SPANNER_INSTANCE":           "fixture-instance",
		"CLAUDE_HISTORY_RAG_SPANNER_DATABASE":           "fixture-database",
		"CLAUDE_HISTORY_RAG_STORAGE_BACKEND":            "spanner",
		"CLAUDE_HISTORY_RAG_SPANNER_EMBEDDING_MODE":     "spanner",
		"CLAUDE_HISTORY_RAG_SPANNER_EMBEDDING_MODEL_ID": "ConversationEmbeddingModel",
		"CLAUDE_HISTORY_RAG_EMBEDDING_PROVIDER":         "vertex",
		"CLAUDE_HISTORY_RAG_EMBEDDING_MODEL":            "gemini-embedding-001",
		"CLAUDE_HISTORY_RAG_EMBEDDING_DIMENSION":        "3072",
		"CLAUDE_HISTORY_RAG_STATUS_SERVER_HOST":         "127.0.0.1",
		"CLAUDE_HISTORY_RAG_STATUS_SERVER_PORT":         "4680",
		"CLAUDE_HISTORY_RAG_CREDENTIALS_SOURCE":         "application_default",
		"CLAUDE_HISTORY_RAG_CREDENTIALS_PROFILE":        "impersonated_service_account",
		"CLAUDE_HISTORY_RAG_CREDENTIALS_IDENTITY":       testIdentity,
	}
}

func TestLoadProductionRequiresExactRuntimeShapeAndIdentity(t *testing.T) {
	env := validProductionEnvironment()
	config, err := LoadProduction(func(key string) string { return env[key] })
	if err != nil {
		t.Fatal(err)
	}
	if config.Spanner.Project != "fixture-project" || config.Spanner.Instance != "fixture-instance" || config.Spanner.Database != "fixture-database" {
		t.Fatalf("Spanner target = %+v", config.Spanner)
	}
	if config.GoogleCredentials.CredentialsIdentity != testIdentity {
		t.Fatalf("credentials identity = %q", config.GoogleCredentials.CredentialsIdentity)
	}

	for _, tt := range []struct {
		key   string
		value string
		want  string
	}{
		{key: "CLAUDE_HISTORY_RAG_RUNTIME_CONTRACT", value: "", want: "runtime_contract"},
		{key: "CLAUDE_HISTORY_RAG_RUNTIME_CONTRACT", value: "development", want: "runtime_contract"},
		{key: "CLAUDE_HISTORY_RAG_STORAGE_BACKEND", value: "unsupported-store", want: "storage_backend"},
		{key: "CLAUDE_HISTORY_RAG_SPANNER_EMBEDDING_MODE", value: "app", want: "spanner_embedding_mode"},
		{key: "CLAUDE_HISTORY_RAG_EMBEDDING_PROVIDER", value: "openai", want: "embedding_provider"},
		{key: "CLAUDE_HISTORY_RAG_EMBEDDING_MODEL", value: "nomic-embed-text", want: "embedding_model"},
		{key: "CLAUDE_HISTORY_RAG_EMBEDDING_DIMENSION", value: "256", want: "embedding_dimension"},
		{key: "CLAUDE_HISTORY_RAG_STATUS_SERVER_HOST", value: "0.0.0.0", want: "status_server_host"},
		{key: "CLAUDE_HISTORY_RAG_STATUS_SERVER_PORT", value: "8080", want: "status_server_port"},
		{key: "CLAUDE_HISTORY_RAG_SPANNER_PROJECT", value: "", want: "spanner_project"},
		{key: "CLAUDE_HISTORY_RAG_CREDENTIALS_SOURCE", value: "file", want: "credentials_source"},
		{key: "CLAUDE_HISTORY_RAG_CREDENTIALS_PROFILE", value: "", want: "credentials_profile"},
		{key: "CLAUDE_HISTORY_RAG_CREDENTIALS_IDENTITY", value: "person@example.com", want: "service-account email"},
	} {
		t.Run(tt.key+"="+tt.value, func(t *testing.T) {
			mutated := validProductionEnvironment()
			mutated[tt.key] = tt.value
			if _, err := LoadProduction(func(key string) string { return mutated[key] }); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want substring %q", err, tt.want)
			}
		})
	}
}

func TestConfigExposesNoCredentialFileCarrier(t *testing.T) {
	typeOfConfig := reflect.TypeOf(Config{})
	for index := 0; index < typeOfConfig.NumField(); index++ {
		field := typeOfConfig.Field(index)
		name := strings.ToLower(field.Name)
		if strings.Contains(name, "credentialfile") || strings.Contains(name, "keyfile") || strings.Contains(name, "keypath") {
			t.Fatalf("Config publishes forbidden key-file carrier %s", field.Name)
		}
	}
}

func TestSpannerResourceNameAndIntegerReaderAreClosed(t *testing.T) {
	target := SpannerTarget{Project: "project", Instance: "instance", Database: "database"}
	if got, want := target.ResourceName(), "projects/project/instances/instance/databases/database"; got != want {
		t.Fatalf("ResourceName() = %q, want %q", got, want)
	}
	for name, raw := range map[string]string{"missing": "", "not integer": "three-thousand"} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseRequiredInt(func(string) string { return raw }, "CLAUDE_HISTORY_RAG_VALUE"); err == nil {
				t.Fatal("parseRequiredInt accepted invalid value")
			}
		})
	}
	if got, err := parseRequiredInt(func(string) string { return " 42 " }, "CLAUDE_HISTORY_RAG_VALUE"); err != nil || got != 42 {
		t.Fatalf("parseRequiredInt(trimmed) = %d, %v", got, err)
	}
	if _, err := LoadProduction(nil); err == nil {
		t.Fatal("LoadProduction accepted nil environment reader")
	}
}
