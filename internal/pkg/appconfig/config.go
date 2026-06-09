package appconfig

import (
	"os"
	"strconv"
	"sync"
	"time"
)

type Config struct {
	Server    ServerConfig
	Workspace WorkspaceConfig
	GitSync   GitSyncConfig
	Search    SearchConfig
	AI        AIConfig
}

type ServerConfig struct {
	Host string
	Port string
}

type WorkspaceConfig struct {
	Root        string
	AutoInitGit bool
}

type GitSyncConfig struct {
	Enabled          bool
	DebounceDuration time.Duration
}

type SearchConfig struct {
	DuckDBPath string
}

type AIConfig struct {
	BaseURL        string
	APIKey         string
	ChatModel      string
	EmbeddingModel string
	Timeout        time.Duration
}

var (
	loadOnce sync.Once
	loaded   Config
)

func Load() Config {
	loadOnce.Do(func() {
		loaded = Config{
			Server: ServerConfig{
				Host: envString("THOUGHTFLOW_HOST", "127.0.0.1"),
				Port: envString("THOUGHTFLOW_PORT", "8080"),
			},
			Workspace: WorkspaceConfig{
				Root:        envString("THOUGHTFLOW_WORKSPACE_ROOT", "./thoughtflow-workspace"),
				AutoInitGit: envBool("THOUGHTFLOW_AUTO_INIT_GIT", true),
			},
			GitSync: GitSyncConfig{
				Enabled:          envBool("THOUGHTFLOW_GIT_ENABLED", true),
				DebounceDuration: time.Duration(envInt("THOUGHTFLOW_GIT_DEBOUNCE_SECONDS", 5)) * time.Second,
			},
			Search: SearchConfig{
				DuckDBPath: envString("THOUGHTFLOW_DUCKDB_PATH", ".thoughtflow/thoughtflow.duckdb"),
			},
			AI: AIConfig{
				BaseURL:        envString("THOUGHTFLOW_AI_BASE_URL", "https://api.openai.com"),
				APIKey:         envString("THOUGHTFLOW_AI_API_KEY", ""),
				ChatModel:      envString("THOUGHTFLOW_AI_CHAT_MODEL", "gpt-4o-mini"),
				EmbeddingModel: envString("THOUGHTFLOW_AI_EMBEDDING_MODEL", "text-embedding-3-small"),
				Timeout:        time.Duration(envInt("THOUGHTFLOW_AI_TIMEOUT_SECONDS", 30)) * time.Second,
			},
		}
	})
	return loaded
}

func ResetForTesting() {
	loadOnce = sync.Once{}
	loaded = Config{}
}

func envString(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok && value != "" {
		return value
	}
	return fallback
}

func envBool(key string, fallback bool) bool {
	value, ok := os.LookupEnv(key)
	if !ok || value == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func envInt(key string, fallback int) int {
	value, ok := os.LookupEnv(key)
	if !ok || value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
}
