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

	cfg := LoadWithConfigDir(configDir)

	if cfg.Server.Host != "127.0.0.1" || cfg.Server.Port != "8080" {
		t.Fatalf("server config = %#v", cfg.Server)
	}
	if cfg.Workspace.ContentDir != "./thoughtflow-workspace" || cfg.Runtime.StateDir != "./thoughtflow-runtime" || !cfg.Workspace.AutoInitGit {
		t.Fatalf("workspace config = %#v", cfg.Workspace)
	}
	if cfg.GitSync.DebounceDuration != 5*time.Second {
		t.Fatalf("git debounce = %v", cfg.GitSync.DebounceDuration)
	}
	if cfg.Search.DuckDBPath != "thoughtflow.duckdb" || cfg.Search.DefaultMode != "hybrid" {
		t.Fatalf("search config = %#v", cfg.Search)
	}
}

func TestLoadWithConfigDirUsesConfiguredDirectory(t *testing.T) {
	ResetForTesting()
	t.Cleanup(ResetForTesting)
	configDir := t.TempDir()
	writeApplicationConfig(t, configDir, `[server]
port = "9090"
`)

	cfg := LoadWithConfigDir(configDir)
	if cfg.Server.Port != "9090" {
		t.Fatalf("server port = %q", cfg.Server.Port)
	}
}

func TestValidateDirectorySeparationAcceptsDifferentHierarchies(t *testing.T) {
	base := t.TempDir()
	configDir := filepath.Join(base, "workspace", "config")
	cfg := defaultConfig()
	cfg.Workspace.ContentDir = filepath.Join(base, "workspace")
	cfg.Runtime.StateDir = filepath.Join(base, "runtime")

	if err := ValidateDirectorySeparation(configDir, cfg); err != nil {
		t.Fatalf("ValidateDirectorySeparation() error = %v", err)
	}
}

func TestValidateDirectorySeparationRejectsDataDirectoryHierarchy(t *testing.T) {
	base := t.TempDir()
	tests := []struct {
		name      string
		configDir string
		dataDir   string
	}{
		{
			name:      "same path",
			configDir: filepath.Join(base, "data"),
			dataDir:   filepath.Join(base, "data"),
		},
		{
			name:      "config under data",
			configDir: filepath.Join(base, "data", "config"),
			dataDir:   filepath.Join(base, "data"),
		},
		{
			name:      "data under config",
			configDir: filepath.Join(base, "config"),
			dataDir:   filepath.Join(base, "config", "data"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := defaultConfig()
			cfg.Runtime.StateDir = tt.dataDir
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
	dataDir := t.TempDir()
	writeApplicationConfig(t, configDir, `[server]
host = "0.0.0.0"
port = "9090"

[workspace]
content_dir = "`+filepath.ToSlash(root)+`"
auto_init_git = false

[runtime]
state_dir = "`+filepath.ToSlash(dataDir)+`"

[capture]
duplicate_policy = "skip"

[refiner]
concurrency = 4
url_fetch_timeout_seconds = 12

[search]
duckdb_path = "custom.duckdb"
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

	cfg := LoadWithConfigDir(configDir)

	if cfg.Server.Host != "0.0.0.0" || cfg.Server.Port != "9090" {
		t.Fatalf("server config = %#v", cfg.Server)
	}
	if cfg.Workspace.ContentDir != filepath.ToSlash(root) || cfg.Runtime.StateDir != filepath.ToSlash(dataDir) || cfg.Workspace.AutoInitGit {
		t.Fatalf("workspace config = %#v", cfg.Workspace)
	}
	if cfg.Capture.DuplicatePolicy != "skip" {
		t.Fatalf("capture config = %#v", cfg.Capture)
	}
	if cfg.Refiner.Concurrency != 4 || cfg.Refiner.URLFetchTimeout != 12*time.Second {
		t.Fatalf("refiner config = %#v", cfg.Refiner)
	}
	if cfg.Search.DuckDBPath != "custom.duckdb" || cfg.Search.DefaultMode != "semantic" {
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
	raw, err := os.ReadFile(filepath.Clean("../../../doc/application.example.toml"))
	if err != nil {
		t.Fatalf("ReadFile(template) error = %v", err)
	}
	writeApplicationConfig(t, configDir, string(raw))

	cfg := LoadWithConfigDir(configDir)

	if cfg.Server.Host != "127.0.0.1" || cfg.Server.Port != "8080" {
		t.Fatalf("server config = %#v", cfg.Server)
	}
	if cfg.Workspace.ContentDir != "./thoughtflow-workspace" || cfg.Runtime.StateDir != "./thoughtflow-runtime" || !cfg.Workspace.AutoInitGit {
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
	if cfg.Search.DuckDBPath != "thoughtflow.duckdb" || cfg.Search.DefaultMode != "hybrid" {
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

func TestLoadIgnoresEnvironmentConfigurationOverrides(t *testing.T) {
	ResetForTesting()
	t.Cleanup(ResetForTesting)
	configDir := t.TempDir()
	t.Setenv("SERVER_PORT", "7070")
	t.Setenv("GIT_SYNC_ENABLED", "true")
	t.Setenv("GIT_SYNC_DEBOUNCE_SECONDS", "2")
	t.Setenv("SEARCH_DUCKDB_PATH", "env.duckdb")
	t.Setenv("AI_API_KEY", "env-key")
	t.Setenv("AI_TIMEOUT_SECONDS", "3")
	writeApplicationConfig(t, configDir, `[server]
port = "9090"

[git_sync]
enabled = false
debounce_seconds = 9

[search]
duckdb_path = "local.duckdb"

[ai]
api_key = "local-key"
timeout_seconds = 17
`)

	cfg := LoadWithConfigDir(configDir)

	if cfg.Server.Port != "9090" {
		t.Fatalf("server port = %q", cfg.Server.Port)
	}
	if cfg.GitSync.Enabled || cfg.GitSync.DebounceDuration != 9*time.Second {
		t.Fatalf("git config = %#v", cfg.GitSync)
	}
	if cfg.Search.DuckDBPath != "local.duckdb" {
		t.Fatalf("duckdb path = %q", cfg.Search.DuckDBPath)
	}
	if cfg.AI.APIKey != "local-key" || cfg.AI.Timeout != 17*time.Second {
		t.Fatalf("ai config = %#v", cfg.AI)
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
