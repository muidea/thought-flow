package appconfig

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Server    ServerConfig
	Workspace WorkspaceConfig
	Capture   CaptureConfig
	Refiner   RefinerConfig
	GitSync   GitSyncConfig
	Search    SearchConfig
	Topic     TopicConfig
	Events    EventsConfig
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

type CaptureConfig struct {
	DuplicatePolicy string
}

type RefinerConfig struct {
	Concurrency        int
	URLFetchTimeout    time.Duration
	URLFetchTimeoutRaw int
}

type GitSyncConfig struct {
	Enabled          bool
	DebounceDuration time.Duration
	DebounceSeconds  int
}

type SearchConfig struct {
	DuckDBPath  string
	DefaultMode string
}

type TopicConfig struct {
	AutoWeave        bool
	MinSemanticScore float64
}

type EventsConfig struct {
	SSEHeartbeat        time.Duration
	SSEHeartbeatSeconds int
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
		loaded = defaultConfig()
		rootForLocalConfig := envString("THOUGHTFLOW_WORKSPACE_ROOT", loaded.Workspace.Root)
		loaded = applyLocalConfig(loaded, filepath.Join(rootForLocalConfig, ".thoughtflow", "config.local.yaml"))
		applyEnvOverrides(&loaded)
	})
	return loaded
}

func ResetForTesting() {
	loadOnce = sync.Once{}
	loaded = Config{}
}

func defaultConfig() Config {
	return Config{
		Server: ServerConfig{
			Host: "127.0.0.1",
			Port: "8080",
		},
		Workspace: WorkspaceConfig{
			Root:        "./thoughtflow-workspace",
			AutoInitGit: true,
		},
		Capture: CaptureConfig{
			DuplicatePolicy: "warn",
		},
		Refiner: RefinerConfig{
			Concurrency:        2,
			URLFetchTimeout:    30 * time.Second,
			URLFetchTimeoutRaw: 30,
		},
		GitSync: GitSyncConfig{
			Enabled:          true,
			DebounceDuration: 5 * time.Second,
			DebounceSeconds:  5,
		},
		Search: SearchConfig{
			DuckDBPath:  ".thoughtflow/thoughtflow.duckdb",
			DefaultMode: "hybrid",
		},
		Topic: TopicConfig{
			AutoWeave:        true,
			MinSemanticScore: 0.78,
		},
		Events: EventsConfig{
			SSEHeartbeat:        20 * time.Second,
			SSEHeartbeatSeconds: 20,
		},
		AI: AIConfig{
			BaseURL:        "https://api.openai.com",
			APIKey:         "",
			ChatModel:      "gpt-4o-mini",
			EmbeddingModel: "text-embedding-3-small",
			Timeout:        30 * time.Second,
		},
	}
}

func applyLocalConfig(cfg Config, path string) Config {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return cfg
	}
	if err != nil {
		return cfg
	}
	var local localConfig
	if err := yaml.Unmarshal(raw, &local); err != nil {
		return cfg
	}
	applyLocalOverrides(&cfg, local)
	return cfg
}

type localConfig struct {
	Server    *localServerConfig    `yaml:"server"`
	Workspace *localWorkspaceConfig `yaml:"workspace"`
	Capture   *localCaptureConfig   `yaml:"capture"`
	Refiner   *localRefinerConfig   `yaml:"refiner"`
	GitSync   *localGitSyncConfig   `yaml:"git_sync"`
	Search    *localSearchConfig    `yaml:"search"`
	Topic     *localTopicConfig     `yaml:"topic"`
	Events    *localEventsConfig    `yaml:"events"`
	AI        *localAIConfig        `yaml:"ai"`
}

type localServerConfig struct {
	Host *string `yaml:"host"`
	Port *string `yaml:"port"`
}

type localWorkspaceConfig struct {
	Root        *string `yaml:"root"`
	AutoInitGit *bool   `yaml:"auto_init_git"`
}

type localCaptureConfig struct {
	DuplicatePolicy *string `yaml:"duplicate_policy"`
}

type localRefinerConfig struct {
	Concurrency            *int `yaml:"concurrency"`
	URLFetchTimeoutSeconds *int `yaml:"url_fetch_timeout_seconds"`
}

type localGitSyncConfig struct {
	Enabled         *bool `yaml:"enabled"`
	DebounceSeconds *int  `yaml:"debounce_seconds"`
}

type localSearchConfig struct {
	DuckDBPath  *string `yaml:"duckdb_path"`
	DefaultMode *string `yaml:"default_mode"`
}

type localTopicConfig struct {
	AutoWeave        *bool    `yaml:"auto_weave"`
	MinSemanticScore *float64 `yaml:"min_semantic_score"`
}

type localEventsConfig struct {
	SSEHeartbeatSeconds *int `yaml:"sse_heartbeat_seconds"`
}

type localAIConfig struct {
	BaseURL        *string `yaml:"base_url"`
	APIKey         *string `yaml:"api_key"`
	ChatModel      *string `yaml:"chat_model"`
	EmbeddingModel *string `yaml:"embedding_model"`
	TimeoutSeconds *int    `yaml:"timeout_seconds"`
}

func applyLocalOverrides(cfg *Config, local localConfig) {
	if local.Server != nil {
		if local.Server.Host != nil {
			cfg.Server.Host = *local.Server.Host
		}
		if local.Server.Port != nil {
			cfg.Server.Port = *local.Server.Port
		}
	}
	if local.Workspace != nil {
		if local.Workspace.Root != nil {
			cfg.Workspace.Root = *local.Workspace.Root
		}
		if local.Workspace.AutoInitGit != nil {
			cfg.Workspace.AutoInitGit = *local.Workspace.AutoInitGit
		}
	}
	if local.Capture != nil && local.Capture.DuplicatePolicy != nil {
		cfg.Capture.DuplicatePolicy = *local.Capture.DuplicatePolicy
	}
	if local.Refiner != nil {
		if local.Refiner.Concurrency != nil {
			cfg.Refiner.Concurrency = *local.Refiner.Concurrency
		}
		if local.Refiner.URLFetchTimeoutSeconds != nil {
			cfg.Refiner.URLFetchTimeoutRaw = *local.Refiner.URLFetchTimeoutSeconds
			cfg.Refiner.URLFetchTimeout = time.Duration(*local.Refiner.URLFetchTimeoutSeconds) * time.Second
		}
	}
	if local.GitSync != nil {
		if local.GitSync.Enabled != nil {
			cfg.GitSync.Enabled = *local.GitSync.Enabled
		}
		if local.GitSync.DebounceSeconds != nil {
			cfg.GitSync.DebounceSeconds = *local.GitSync.DebounceSeconds
			cfg.GitSync.DebounceDuration = time.Duration(*local.GitSync.DebounceSeconds) * time.Second
		}
	}
	if local.Search != nil {
		if local.Search.DuckDBPath != nil {
			cfg.Search.DuckDBPath = *local.Search.DuckDBPath
		}
		if local.Search.DefaultMode != nil {
			cfg.Search.DefaultMode = *local.Search.DefaultMode
		}
	}
	if local.Topic != nil {
		if local.Topic.AutoWeave != nil {
			cfg.Topic.AutoWeave = *local.Topic.AutoWeave
		}
		if local.Topic.MinSemanticScore != nil {
			cfg.Topic.MinSemanticScore = *local.Topic.MinSemanticScore
		}
	}
	if local.Events != nil && local.Events.SSEHeartbeatSeconds != nil {
		cfg.Events.SSEHeartbeatSeconds = *local.Events.SSEHeartbeatSeconds
		cfg.Events.SSEHeartbeat = time.Duration(*local.Events.SSEHeartbeatSeconds) * time.Second
	}
	if local.AI != nil {
		if local.AI.BaseURL != nil {
			cfg.AI.BaseURL = *local.AI.BaseURL
		}
		if local.AI.APIKey != nil {
			cfg.AI.APIKey = *local.AI.APIKey
		}
		if local.AI.ChatModel != nil {
			cfg.AI.ChatModel = *local.AI.ChatModel
		}
		if local.AI.EmbeddingModel != nil {
			cfg.AI.EmbeddingModel = *local.AI.EmbeddingModel
		}
		if local.AI.TimeoutSeconds != nil {
			cfg.AI.Timeout = time.Duration(*local.AI.TimeoutSeconds) * time.Second
		}
	}
}

func applyEnvOverrides(cfg *Config) {
	cfg.Server.Host = envString("THOUGHTFLOW_HOST", cfg.Server.Host)
	cfg.Server.Port = envString("THOUGHTFLOW_PORT", cfg.Server.Port)
	cfg.Workspace.Root = envString("THOUGHTFLOW_WORKSPACE_ROOT", cfg.Workspace.Root)
	cfg.Workspace.AutoInitGit = envBool("THOUGHTFLOW_AUTO_INIT_GIT", cfg.Workspace.AutoInitGit)
	cfg.GitSync.Enabled = envBool("THOUGHTFLOW_GIT_ENABLED", cfg.GitSync.Enabled)
	cfg.GitSync.DebounceSeconds = envInt("THOUGHTFLOW_GIT_DEBOUNCE_SECONDS", cfg.GitSync.DebounceSeconds)
	cfg.GitSync.DebounceDuration = time.Duration(cfg.GitSync.DebounceSeconds) * time.Second
	cfg.Search.DuckDBPath = envString("THOUGHTFLOW_DUCKDB_PATH", cfg.Search.DuckDBPath)
	cfg.AI.BaseURL = envString("THOUGHTFLOW_AI_BASE_URL", cfg.AI.BaseURL)
	cfg.AI.APIKey = envString("THOUGHTFLOW_AI_API_KEY", cfg.AI.APIKey)
	cfg.AI.ChatModel = envString("THOUGHTFLOW_AI_CHAT_MODEL", cfg.AI.ChatModel)
	cfg.AI.EmbeddingModel = envString("THOUGHTFLOW_AI_EMBEDDING_MODEL", cfg.AI.EmbeddingModel)
	cfg.AI.Timeout = time.Duration(envInt("THOUGHTFLOW_AI_TIMEOUT_SECONDS", int(cfg.AI.Timeout/time.Second))) * time.Second
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
