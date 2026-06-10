package appconfig

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/muidea/magicCommon/framework/configuration"
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
		ensureFrameworkConfigManager()
		applyFrameworkOverrides(&loaded)
		applyEnvOverrides(&loaded)
	})
	return loaded
}

func ConfigDir() string {
	if value := envString("THOUGHTFLOW_CONFIG_DIR", ""); value != "" {
		return value
	}
	if value := envString("CONFIG_PATH", ""); value != "" {
		return value
	}
	if value, err := os.UserConfigDir(); err == nil && value != "" {
		return filepath.Join(value, "thoughtflow")
	}
	return filepath.Join(".", "thoughtflow-config")
}

func ResetForTesting() {
	loadOnce = sync.Once{}
	loaded = Config{}
	_ = configuration.CloseConfigManager()
	configuration.DefaultConfigManager = nil
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

func ensureFrameworkConfigManager() {
	if configuration.GetDefaultConfigManager() != nil {
		return
	}
	if err := configuration.InitDefaultConfigManager(ConfigDir()); err != nil {
		slog.Warn("initialize framework config manager failed", "error", err)
	}
}

func applyFrameworkOverrides(cfg *Config) {
	cfg.Server.Host = frameworkString("server.host", cfg.Server.Host)
	cfg.Server.Port = frameworkString("server.port", cfg.Server.Port)
	cfg.Workspace.Root = frameworkString("workspace.root", cfg.Workspace.Root)
	cfg.Workspace.AutoInitGit = configuration.GetBoolWithDefault("workspace.auto_init_git", cfg.Workspace.AutoInitGit)
	cfg.Capture.DuplicatePolicy = frameworkString("capture.duplicate_policy", cfg.Capture.DuplicatePolicy)
	cfg.Refiner.Concurrency = configuration.GetIntWithDefault("refiner.concurrency", cfg.Refiner.Concurrency)
	cfg.Refiner.URLFetchTimeoutRaw = configuration.GetIntWithDefault("refiner.url_fetch_timeout_seconds", cfg.Refiner.URLFetchTimeoutRaw)
	cfg.Refiner.URLFetchTimeout = time.Duration(cfg.Refiner.URLFetchTimeoutRaw) * time.Second
	cfg.GitSync.Enabled = configuration.GetBoolWithDefault("git_sync.enabled", cfg.GitSync.Enabled)
	cfg.GitSync.DebounceSeconds = configuration.GetIntWithDefault("git_sync.debounce_seconds", cfg.GitSync.DebounceSeconds)
	cfg.GitSync.DebounceDuration = time.Duration(cfg.GitSync.DebounceSeconds) * time.Second
	cfg.Search.DuckDBPath = frameworkString("search.duckdb_path", cfg.Search.DuckDBPath)
	cfg.Search.DefaultMode = frameworkString("search.default_mode", cfg.Search.DefaultMode)
	cfg.Topic.AutoWeave = configuration.GetBoolWithDefault("topic.auto_weave", cfg.Topic.AutoWeave)
	cfg.Topic.MinSemanticScore = configuration.GetFloat64WithDefault("topic.min_semantic_score", cfg.Topic.MinSemanticScore)
	cfg.Events.SSEHeartbeatSeconds = configuration.GetIntWithDefault("events.sse_heartbeat_seconds", cfg.Events.SSEHeartbeatSeconds)
	cfg.Events.SSEHeartbeat = time.Duration(cfg.Events.SSEHeartbeatSeconds) * time.Second
	cfg.AI.BaseURL = frameworkString("ai.base_url", cfg.AI.BaseURL)
	cfg.AI.APIKey = frameworkString("ai.api_key", cfg.AI.APIKey)
	cfg.AI.ChatModel = frameworkString("ai.chat_model", cfg.AI.ChatModel)
	cfg.AI.EmbeddingModel = frameworkString("ai.embedding_model", cfg.AI.EmbeddingModel)
	cfg.AI.Timeout = time.Duration(configuration.GetIntWithDefault("ai.timeout_seconds", int(cfg.AI.Timeout/time.Second))) * time.Second
}

func frameworkString(key, fallback string) string {
	manager := configuration.GetDefaultConfigManager()
	if manager == nil {
		return fallback
	}
	value, err := manager.Get(key)
	if err != nil || value == nil {
		return fallback
	}
	return fmt.Sprint(value)
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
