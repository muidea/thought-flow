package appconfig

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadUsesDefaultsWhenLocalConfigIsMissing(t *testing.T) {
	ResetForTesting()
	t.Cleanup(ResetForTesting)
	configDir := t.TempDir()
	root := t.TempDir()
	t.Setenv("THOUGHTFLOW_CONFIG_DIR", configDir)
	t.Setenv("THOUGHTFLOW_WORKSPACE_ROOT", root)

	cfg := Load()

	if cfg.Server.Host != "127.0.0.1" || cfg.Server.Port != "8080" {
		t.Fatalf("server config = %#v", cfg.Server)
	}
	if cfg.Workspace.Root != root || !cfg.Workspace.AutoInitGit {
		t.Fatalf("workspace config = %#v", cfg.Workspace)
	}
	if cfg.GitSync.DebounceDuration != 5*time.Second {
		t.Fatalf("git debounce = %v", cfg.GitSync.DebounceDuration)
	}
	if cfg.Search.DuckDBPath != ".thoughtflow/thoughtflow.duckdb" || cfg.Search.DefaultMode != "hybrid" {
		t.Fatalf("search config = %#v", cfg.Search)
	}
}

func TestConfigDirUsesConfiguredDirectory(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("THOUGHTFLOW_CONFIG_DIR", configDir)

	if got := ConfigDir(); got != configDir {
		t.Fatalf("ConfigDir() = %q, want %q", got, configDir)
	}
}

func TestConfigDirFallsBackToMagicCommonConfigPath(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("CONFIG_PATH", configDir)

	if got := ConfigDir(); got != configDir {
		t.Fatalf("ConfigDir() = %q, want %q", got, configDir)
	}
}

func TestValidateDirectorySeparationAcceptsDifferentHierarchies(t *testing.T) {
	base := t.TempDir()
	configDir := filepath.Join(base, "etc", "thoughtflow")
	cfg := defaultConfig()
	cfg.Workspace.Root = filepath.Join(base, "var", "lib", "thoughtflow")

	if err := ValidateDirectorySeparation(configDir, cfg); err != nil {
		t.Fatalf("ValidateDirectorySeparation() error = %v", err)
	}
}

func TestValidateDirectorySeparationRejectsWorkspaceHierarchy(t *testing.T) {
	base := t.TempDir()
	tests := []struct {
		name      string
		configDir string
		workspace string
	}{
		{
			name:      "same path",
			configDir: filepath.Join(base, "workspace"),
			workspace: filepath.Join(base, "workspace"),
		},
		{
			name:      "config under workspace",
			configDir: filepath.Join(base, "workspace", "config"),
			workspace: filepath.Join(base, "workspace"),
		},
		{
			name:      "workspace under config",
			configDir: filepath.Join(base, "config"),
			workspace: filepath.Join(base, "config", "workspace"),
		},
		{
			name:      "sibling directories",
			configDir: filepath.Join(base, "config"),
			workspace: filepath.Join(base, "workspace"),
		},
		{
			name:      "runtime data directory",
			configDir: filepath.Join(base, "workspace", ".thoughtflow"),
			workspace: filepath.Join(base, "workspace"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := defaultConfig()
			cfg.Workspace.Root = tt.workspace
			if err := ValidateDirectorySeparation(tt.configDir, cfg); err == nil {
				t.Fatal("expected directory separation error")
			}
		})
	}
}

func TestLoadAppliesFrameworkWorkspaceConfig(t *testing.T) {
	ResetForTesting()
	t.Cleanup(ResetForTesting)
	configDir := t.TempDir()
	root := t.TempDir()
	t.Setenv("THOUGHTFLOW_CONFIG_DIR", configDir)
	t.Setenv("THOUGHTFLOW_WORKSPACE_ROOT", root)
	writeApplicationConfig(t, configDir, `[server]
host = "0.0.0.0"
port = "9090"

[workspace]
auto_init_git = false

[capture]
duplicate_policy = "skip"

[refiner]
concurrency = 4
url_fetch_timeout_seconds = 12

[search]
duckdb_path = ".thoughtflow/custom.duckdb"
default_mode = "semantic"

[topic]
auto_weave = false
min_semantic_score = 0.91

[git_sync]
enabled = false
debounce_seconds = 9

[events]
sse_heartbeat_seconds = 33

[ai]
base_url = "https://ai.example.test"
api_key = "local-key"
chat_model = "local-chat"
embedding_model = "local-embed"
timeout_seconds = 17
`)

	cfg := Load()

	if cfg.Server.Host != "0.0.0.0" || cfg.Server.Port != "9090" {
		t.Fatalf("server config = %#v", cfg.Server)
	}
	if cfg.Workspace.Root != root || cfg.Workspace.AutoInitGit {
		t.Fatalf("workspace config = %#v", cfg.Workspace)
	}
	if cfg.Capture.DuplicatePolicy != "skip" {
		t.Fatalf("capture config = %#v", cfg.Capture)
	}
	if cfg.Refiner.Concurrency != 4 || cfg.Refiner.URLFetchTimeout != 12*time.Second {
		t.Fatalf("refiner config = %#v", cfg.Refiner)
	}
	if cfg.Search.DuckDBPath != ".thoughtflow/custom.duckdb" || cfg.Search.DefaultMode != "semantic" {
		t.Fatalf("search config = %#v", cfg.Search)
	}
	if cfg.Topic.AutoWeave || cfg.Topic.MinSemanticScore != 0.91 {
		t.Fatalf("topic config = %#v", cfg.Topic)
	}
	if cfg.GitSync.Enabled || cfg.GitSync.DebounceDuration != 9*time.Second {
		t.Fatalf("git config = %#v", cfg.GitSync)
	}
	if cfg.Events.SSEHeartbeat != 33*time.Second {
		t.Fatalf("events config = %#v", cfg.Events)
	}
	if cfg.AI.BaseURL != "https://ai.example.test" ||
		cfg.AI.APIKey != "local-key" ||
		cfg.AI.ChatModel != "local-chat" ||
		cfg.AI.EmbeddingModel != "local-embed" ||
		cfg.AI.Timeout != 17*time.Second {
		t.Fatalf("ai config = %#v", cfg.AI)
	}
}

func TestConfigTemplateLoadsAsFrameworkApplicationConfig(t *testing.T) {
	ResetForTesting()
	t.Cleanup(ResetForTesting)
	configDir := t.TempDir()
	root := t.TempDir()
	t.Setenv("THOUGHTFLOW_CONFIG_DIR", configDir)
	t.Setenv("THOUGHTFLOW_WORKSPACE_ROOT", root)
	raw, err := os.ReadFile(filepath.Clean("../../../doc/application.example.toml"))
	if err != nil {
		t.Fatalf("ReadFile(template) error = %v", err)
	}
	writeApplicationConfig(t, configDir, string(raw))

	cfg := Load()

	if cfg.Server.Host != "127.0.0.1" || cfg.Server.Port != "8080" {
		t.Fatalf("server config = %#v", cfg.Server)
	}
	if cfg.Workspace.Root != root || !cfg.Workspace.AutoInitGit {
		t.Fatalf("workspace config = %#v", cfg.Workspace)
	}
	if cfg.Capture.DuplicatePolicy != "warn" {
		t.Fatalf("capture config = %#v", cfg.Capture)
	}
	if cfg.Refiner.Concurrency != 2 || cfg.Refiner.URLFetchTimeout != 30*time.Second {
		t.Fatalf("refiner config = %#v", cfg.Refiner)
	}
	if !cfg.GitSync.Enabled || cfg.GitSync.DebounceDuration != 5*time.Second {
		t.Fatalf("git config = %#v", cfg.GitSync)
	}
	if cfg.Search.DuckDBPath != ".thoughtflow/thoughtflow.duckdb" || cfg.Search.DefaultMode != "hybrid" {
		t.Fatalf("search config = %#v", cfg.Search)
	}
	if !cfg.Topic.AutoWeave || cfg.Topic.MinSemanticScore != 0.78 {
		t.Fatalf("topic config = %#v", cfg.Topic)
	}
	if cfg.Events.SSEHeartbeat != 20*time.Second {
		t.Fatalf("events config = %#v", cfg.Events)
	}
	if cfg.AI.BaseURL != "https://api.openai.com" ||
		cfg.AI.APIKey != "" ||
		cfg.AI.ChatModel != "gpt-4o-mini" ||
		cfg.AI.EmbeddingModel != "text-embedding-3-small" ||
		cfg.AI.Timeout != 30*time.Second {
		t.Fatalf("ai config = %#v", cfg.AI)
	}
}

func TestLoadEnvironmentOverridesLocalConfig(t *testing.T) {
	ResetForTesting()
	t.Cleanup(ResetForTesting)
	configDir := t.TempDir()
	root := t.TempDir()
	t.Setenv("THOUGHTFLOW_CONFIG_DIR", configDir)
	t.Setenv("THOUGHTFLOW_WORKSPACE_ROOT", root)
	t.Setenv("THOUGHTFLOW_PORT", "7070")
	t.Setenv("THOUGHTFLOW_GIT_ENABLED", "true")
	t.Setenv("THOUGHTFLOW_GIT_DEBOUNCE_SECONDS", "2")
	t.Setenv("THOUGHTFLOW_DUCKDB_PATH", ".thoughtflow/env.duckdb")
	t.Setenv("THOUGHTFLOW_AI_API_KEY", "env-key")
	t.Setenv("THOUGHTFLOW_AI_TIMEOUT_SECONDS", "3")
	writeApplicationConfig(t, configDir, `[server]
port = "9090"

[git_sync]
enabled = false
debounce_seconds = 9

[search]
duckdb_path = ".thoughtflow/local.duckdb"

[ai]
api_key = "local-key"
timeout_seconds = 17
`)

	cfg := Load()

	if cfg.Server.Port != "7070" {
		t.Fatalf("server port = %q", cfg.Server.Port)
	}
	if !cfg.GitSync.Enabled || cfg.GitSync.DebounceDuration != 2*time.Second {
		t.Fatalf("git config = %#v", cfg.GitSync)
	}
	if cfg.Search.DuckDBPath != ".thoughtflow/env.duckdb" {
		t.Fatalf("duckdb path = %q", cfg.Search.DuckDBPath)
	}
	if cfg.AI.APIKey != "env-key" || cfg.AI.Timeout != 3*time.Second {
		t.Fatalf("ai config = %#v", cfg.AI)
	}
}

func TestLoadAppliesThoughtflowEnvironmentAfterFrameworkEnvironment(t *testing.T) {
	ResetForTesting()
	t.Cleanup(ResetForTesting)
	configDir := t.TempDir()
	root := t.TempDir()
	t.Setenv("THOUGHTFLOW_CONFIG_DIR", configDir)
	t.Setenv("THOUGHTFLOW_WORKSPACE_ROOT", root)
	t.Setenv("SERVER_HOST", "0.0.0.0")
	t.Setenv("SERVER_PORT", "6060")
	t.Setenv("THOUGHTFLOW_PORT", "7070")

	cfg := Load()

	if cfg.Server.Host != "0.0.0.0" {
		t.Fatalf("server host = %q, want framework env value", cfg.Server.Host)
	}
	if cfg.Server.Port != "7070" {
		t.Fatalf("server port = %q, want THOUGHTFLOW override", cfg.Server.Port)
	}
}

func writeApplicationConfig(t *testing.T, configDir string, content string) {
	t.Helper()
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "application.toml"), []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
}
