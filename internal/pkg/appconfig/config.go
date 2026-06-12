package appconfig

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/muidea/magicCommon/framework/configuration"
)

type Config struct {
	Server    ServerConfig
	Workspace WorkspaceConfig
	Runtime   RuntimeConfig
	Capture   CaptureConfig
	Refiner   RefinerConfig
	Expander  ExpanderConfig
	GitSync   GitSyncConfig
	Search    SearchConfig
	Topic     TopicConfig
	Events    EventsConfig
	LLM       LLMConfig
	Embedding EmbeddingConfig
	Reader    ReaderConfig
}

type ServerConfig struct {
	Host string
	Port string
}

type WorkspaceConfig struct {
	ContentDir  string
	AutoInitGit bool
}

type RuntimeConfig struct {
	StateDir string
}

type CaptureConfig struct {
	DuplicatePolicy string
}

type RefinerConfig struct {
	Concurrency        int
	URLFetchTimeout    time.Duration
	URLFetchTimeoutRaw int
}

// ExpanderConfig governs the post-refine expansion pipeline. The
// default 30s pipeline deadline is fine for local LLMs but starves
// remote chat models whose 400-800 character plan can take 30-90s.
// PipelineTimeoutSeconds overrides the hard-coded default when > 0.
type ExpanderConfig struct {
	PipelineTimeoutSeconds int
	PipelineTimeout        time.Duration
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

type LLMConfig struct {
	BaseURL   string
	APIKey    string
	ChatModel string
	Timeout   time.Duration
}

type EmbeddingConfig struct {
	BaseURL string
	APIKey  string
	Model   string
	Timeout time.Duration
}

// ReaderConfig is the third-party web page reader / fetcher that
// ThoughtFlow falls back to when a URL thought cannot be summarized
// from raw HTML alone. The classic provider is Jina Reader
// (https://r.jina.ai/) but the URL is configurable so any
// open-source reader proxy can be substituted. The Configured
// field drives the privacy UI hint — when no API key is set the
// fetcher transparently uses the Jina public endpoint (which is
// itself an external request, so the hint still fires).
type ReaderConfig struct {
	Enabled bool
	BaseURL string
	APIKey  string
	Timeout time.Duration
}

var (
	loadOnce sync.Once
	loaded   Config
)

func Load() Config {
	return LoadWithConfigDir(ConfigDir())
}

func LoadWithConfigDir(configDir string) Config {
	loadOnce.Do(func() {
		loaded = defaultConfig()
		ensureFrameworkConfigManager(configDir)
		applyFrameworkOverrides(&loaded)
	})
	return loaded
}

func ConfigDir() string {
	if value, err := os.UserConfigDir(); err == nil && value != "" {
		return filepath.Join(value, "thoughtflow")
	}
	if value, err := os.UserHomeDir(); err == nil && value != "" {
		return filepath.Join(value, ".config", "thoughtflow")
	}
	return filepath.Join(os.TempDir(), "thoughtflow", "config")
}

func ValidateDirectorySeparation(configDir string, cfg Config) error {
	absConfigDir, err := filepath.Abs(configDir)
	if err != nil {
		return fmt.Errorf("resolve config directory: %w", err)
	}
	absDataDir, err := RuntimeStateDir(cfg)
	if err != nil {
		return err
	}
	if samePath(absConfigDir, absDataDir) {
		return fmt.Errorf("config directory must be separate from data directory: %s", absConfigDir)
	}
	if nestedPath(absConfigDir, absDataDir) || nestedPath(absDataDir, absConfigDir) {
		return fmt.Errorf("config directory and data directory must not be nested: config=%s data=%s", absConfigDir, absDataDir)
	}
	return nil
}

func RuntimeStateDir(cfg Config) (string, error) {
	dataDir := strings.TrimSpace(cfg.Runtime.StateDir)
	if dataDir == "" {
		dataDir = filepath.Join(cfg.Workspace.ContentDir, ".thoughtflow")
	}
	absDataDir, err := filepath.Abs(dataDir)
	if err != nil {
		return "", fmt.Errorf("resolve data directory: %w", err)
	}
	return absDataDir, nil
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
			ContentDir:  "./thoughtflow-workspace",
			AutoInitGit: true,
		},
		Runtime: RuntimeConfig{
			StateDir: "./thoughtflow-runtime",
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
			DuckDBPath:  "thoughtflow.duckdb",
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
		LLM: LLMConfig{
			BaseURL:   "https://api.openai.com",
			APIKey:    "",
			ChatModel: "gpt-4o-mini",
			Timeout:   30 * time.Second,
		},
		Embedding: EmbeddingConfig{
			BaseURL: "https://api.openai.com",
			APIKey:  "",
			Model:   "text-embedding-3-small",
			Timeout: 30 * time.Second,
		},
		Reader: ReaderConfig{
			Enabled: true,
			BaseURL: "https://r.jina.ai/http://",
			APIKey:  "",
			Timeout: 20 * time.Second,
		},
	}
}

func ensureFrameworkConfigManager(configDir string) {
	if configuration.GetDefaultConfigManager() != nil {
		return
	}
	if err := configuration.InitDefaultConfigManager(configDir); err != nil {
		slog.Warn("initialize framework config manager failed", "error", err)
	}
}

func applyFrameworkOverrides(cfg *Config) {
	appConfig := applicationConfig()
	cfg.Server.Host = configString(appConfig, "server.host", cfg.Server.Host)
	cfg.Server.Port = configString(appConfig, "server.port", cfg.Server.Port)
	cfg.Workspace.ContentDir = configString(appConfig, "workspace.content_dir", cfg.Workspace.ContentDir)
	cfg.Workspace.AutoInitGit = configBool(appConfig, "workspace.auto_init_git", cfg.Workspace.AutoInitGit)
	cfg.Runtime.StateDir = configString(appConfig, "runtime.state_dir", cfg.Runtime.StateDir)
	cfg.Capture.DuplicatePolicy = configString(appConfig, "capture.duplicate_policy", cfg.Capture.DuplicatePolicy)
	cfg.Refiner.Concurrency = configInt(appConfig, "refiner.concurrency", cfg.Refiner.Concurrency)
	cfg.Refiner.URLFetchTimeoutRaw = configInt(appConfig, "refiner.url_fetch_timeout_seconds", cfg.Refiner.URLFetchTimeoutRaw)
	cfg.Refiner.URLFetchTimeout = time.Duration(cfg.Refiner.URLFetchTimeoutRaw) * time.Second
	cfg.Expander.PipelineTimeoutSeconds = configInt(appConfig, "expander.pipeline_timeout_seconds", cfg.Expander.PipelineTimeoutSeconds)
	cfg.Expander.PipelineTimeout = time.Duration(cfg.Expander.PipelineTimeoutSeconds) * time.Second
	cfg.GitSync.Enabled = configBool(appConfig, "git_sync.enabled", cfg.GitSync.Enabled)
	cfg.GitSync.DebounceSeconds = configInt(appConfig, "git_sync.debounce_seconds", cfg.GitSync.DebounceSeconds)
	cfg.GitSync.DebounceDuration = time.Duration(cfg.GitSync.DebounceSeconds) * time.Second
	cfg.Search.DuckDBPath = configString(appConfig, "search.duckdb_path", cfg.Search.DuckDBPath)
	cfg.Search.DefaultMode = configString(appConfig, "search.default_mode", cfg.Search.DefaultMode)
	cfg.Topic.AutoWeave = configBool(appConfig, "topic.auto_weave", cfg.Topic.AutoWeave)
	cfg.Topic.MinSemanticScore = configFloat64(appConfig, "topic.min_semantic_score", cfg.Topic.MinSemanticScore)
	cfg.Events.SSEHeartbeatSeconds = configInt(appConfig, "events.sse_heartbeat_seconds", cfg.Events.SSEHeartbeatSeconds)
	cfg.Events.SSEHeartbeat = time.Duration(cfg.Events.SSEHeartbeatSeconds) * time.Second
	cfg.LLM.BaseURL = configString(appConfig, "llm.base_url", cfg.LLM.BaseURL)
	cfg.LLM.APIKey = configString(appConfig, "llm.api_key", cfg.LLM.APIKey)
	cfg.LLM.ChatModel = configString(appConfig, "llm.chat_model", cfg.LLM.ChatModel)
	cfg.LLM.Timeout = time.Duration(configInt(appConfig, "llm.timeout_seconds", int(cfg.LLM.Timeout/time.Second))) * time.Second
	cfg.Embedding.BaseURL = configString(appConfig, "embedding.base_url", cfg.Embedding.BaseURL)
	cfg.Embedding.APIKey = configString(appConfig, "embedding.api_key", cfg.Embedding.APIKey)
	cfg.Embedding.Model = configString(appConfig, "embedding.model", cfg.Embedding.Model)
	cfg.Embedding.Timeout = time.Duration(configInt(appConfig, "embedding.timeout_seconds", int(cfg.Embedding.Timeout/time.Second))) * time.Second
	cfg.Reader.Enabled = configBool(appConfig, "reader.enabled", cfg.Reader.Enabled)
	cfg.Reader.BaseURL = configString(appConfig, "reader.base_url", cfg.Reader.BaseURL)
	cfg.Reader.APIKey = configString(appConfig, "reader.api_key", cfg.Reader.APIKey)
	cfg.Reader.Timeout = time.Duration(configInt(appConfig, "reader.timeout_seconds", int(cfg.Reader.Timeout/time.Second))) * time.Second
}

func applicationConfig() map[string]any {
	manager := configuration.GetDefaultConfigManager()
	if manager == nil {
		return nil
	}
	exported, err := manager.ExportAllConfigs()
	if err != nil {
		return nil
	}
	appConfig, _ := exported["application"].(map[string]any)
	return appConfig
}

func configString(root map[string]any, key string, fallback string) string {
	value, ok := configValue(root, key)
	if !ok || value == nil {
		return fallback
	}
	return fmt.Sprint(value)
}

func configBool(root map[string]any, key string, fallback bool) bool {
	value, ok := configValue(root, key)
	if !ok {
		return fallback
	}
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		parsed, err := strconv.ParseBool(typed)
		if err == nil {
			return parsed
		}
	}
	return fallback
}

func configInt(root map[string]any, key string, fallback int) int {
	value, ok := configValue(root, key)
	if !ok {
		return fallback
	}
	switch typed := value.(type) {
	case int:
		return typed
	case int8:
		return int(typed)
	case int16:
		return int(typed)
	case int32:
		return int(typed)
	case int64:
		return int(typed)
	case uint:
		return int(typed)
	case uint8:
		return int(typed)
	case uint16:
		return int(typed)
	case uint32:
		return int(typed)
	case uint64:
		return int(typed)
	case float64:
		return int(typed)
	case string:
		parsed, err := strconv.Atoi(typed)
		if err == nil {
			return parsed
		}
	}
	return fallback
}

func configFloat64(root map[string]any, key string, fallback float64) float64 {
	value, ok := configValue(root, key)
	if !ok {
		return fallback
	}
	switch typed := value.(type) {
	case float64:
		return typed
	case float32:
		return float64(typed)
	case int:
		return float64(typed)
	case int64:
		return float64(typed)
	case string:
		parsed, err := strconv.ParseFloat(typed, 64)
		if err == nil {
			return parsed
		}
	}
	return fallback
}

func configValue(root map[string]any, key string) (any, bool) {
	if root == nil {
		return nil, false
	}
	current := root
	parts := strings.Split(key, ".")
	for index, part := range parts {
		value, ok := current[part]
		if !ok {
			return nil, false
		}
		if index == len(parts)-1 {
			return value, true
		}
		next, ok := value.(map[string]any)
		if !ok {
			return nil, false
		}
		current = next
	}
	return nil, false
}

func samePath(left, right string) bool {
	return filepath.Clean(left) == filepath.Clean(right)
}

func nestedPath(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	if rel == "." || rel == ".." {
		return false
	}
	if len(rel) >= 3 && rel[:3] == "../" {
		return false
	}
	return true
}
