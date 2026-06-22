package ai

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"thoughtflow/internal/pkg/appconfig"
	"thoughtflow/internal/pkg/models"
)

func TestLocalProviderRefineIncludesEmbedding(t *testing.T) {
	provider := NewLocalRefineProvider()
	refinement, err := provider.Refine(context.Background(), RefineRequest{
		Thought: models.Thought{ID: "thought-1"},
		Content: models.ThoughtContent{Original: "DuckDB search and embedding should work together."},
	})
	if err != nil {
		t.Fatalf("Refine() error = %v", err)
	}
	if refinement.Summary == "" {
		t.Fatalf("expected summary")
	}
	if refinement.Embedding == nil {
		t.Fatalf("expected embedding")
	}
	if refinement.Embedding.Dimension != len(refinement.Embedding.Vector) {
		t.Fatalf("embedding dimension = %d len = %d", refinement.Embedding.Dimension, len(refinement.Embedding.Vector))
	}
}

func TestOpenAICompatibleProviderRefineAndEmbed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(res http.ResponseWriter, req *http.Request) {
		if req.Header.Get("Authorization") != "Bearer test-key" {
			t.Fatalf("authorization header = %q", req.Header.Get("Authorization"))
		}
		res.Header().Set("Content-Type", "application/json")
		switch req.URL.Path {
		case "/v1/chat/completions":
			_ = json.NewEncoder(res).Encode(map[string]any{
				"choices": []map[string]any{
					{
						"message": map[string]string{
							"content": `{"summary":"Cloud summary","key_points":["Point A"],"tags":["cloud"],"title":"Cloud title"}`,
						},
					},
				},
			})
		case "/v1/embeddings":
			_ = json.NewEncoder(res).Encode(map[string]any{
				"data": []map[string]any{
					{"embedding": []float64{0.1, 0.2, 0.3}},
				},
			})
		default:
			http.NotFound(res, req)
		}
	}))
	defer server.Close()

	provider := NewOpenAICompatibleProvider(appconfig.LLMConfig{
		BaseURL:   server.URL,
		APIKey:    "test-key",
		ChatModel: "chat-model",
		Timeout:   time.Second,
	}, appconfig.EmbeddingConfig{
		BaseURL: server.URL,
		APIKey:  "test-key",
		Model:   "embedding-model",
		Timeout: time.Second,
	})
	refinement, err := provider.Refine(context.Background(), RefineRequest{
		Thought: models.Thought{ID: "thought-1", UserTitle: "Original title"},
		Content: models.ThoughtContent{Original: "Content to refine."},
	})
	if err != nil {
		t.Fatalf("Refine() error = %v", err)
	}
	if refinement.Summary != "Cloud summary" {
		t.Fatalf("summary = %q", refinement.Summary)
	}
	if refinement.ExtractedTitle != "Cloud title" {
		t.Fatalf("title = %q", refinement.ExtractedTitle)
	}
	if refinement.Embedding == nil || refinement.Embedding.Model != "embedding-model" {
		t.Fatalf("embedding = %#v", refinement.Embedding)
	}
	if len(refinement.Embedding.Vector) != 3 {
		t.Fatalf("embedding vector = %#v", refinement.Embedding.Vector)
	}
}

func TestOpenAICompatibleProviderRetriesTransientStatus(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(res http.ResponseWriter, req *http.Request) {
		attempts++
		if attempts < 3 {
			http.Error(res, "temporary provider outage", http.StatusBadGateway)
			return
		}
		res.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(res).Encode(map[string]any{
			"data": []map[string]any{
				{"embedding": []float64{0.4, 0.5}},
			},
		})
	}))
	defer server.Close()

	provider := NewOpenAICompatibleEmbeddingProvider(appconfig.EmbeddingConfig{
		BaseURL: server.URL,
		APIKey:  "test-key",
		Model:   "embedding-model",
		Timeout: time.Second,
	})
	embedding, err := provider.Embed(context.Background(), EmbedRequest{ThoughtID: "thought-1", Text: "retry embedding"})
	if err != nil {
		t.Fatalf("Embed() error = %v", err)
	}
	if attempts != openAIMaxAttempts {
		t.Fatalf("attempts = %d, want %d", attempts, openAIMaxAttempts)
	}
	if embedding.Dimension != 2 {
		t.Fatalf("embedding = %#v", embedding)
	}
}

func TestOpenAICompatibleProviderDoesNotRetryNonRetryableStatus(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(res http.ResponseWriter, req *http.Request) {
		attempts++
		http.Error(res, "bad request", http.StatusBadRequest)
	}))
	defer server.Close()

	provider := NewOpenAICompatibleEmbeddingProvider(appconfig.EmbeddingConfig{
		BaseURL: server.URL,
		APIKey:  "test-key",
		Model:   "embedding-model",
		Timeout: time.Second,
	})
	_, err := provider.Embed(context.Background(), EmbedRequest{ThoughtID: "thought-1", Text: "bad embedding"})
	if err == nil {
		t.Fatalf("expected provider error")
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1", attempts)
	}
	var providerErr ProviderError
	if !errors.As(err, &providerErr) {
		t.Fatalf("error type = %T, want ProviderError", err)
	}
	if providerErr.Retryable || providerErr.StatusCode != http.StatusBadRequest {
		t.Fatalf("provider error = %#v", providerErr)
	}
}

func TestLocalProviderWeaveInsertsIntoMatchingOutline(t *testing.T) {
	provider := NewLocalRefineProvider()
	result, err := provider.Weave(context.Background(), models.TopicWeaveRequest{
		Topic: models.Topic{
			ID:   "duckdb-notes",
			Name: "DuckDB Notes",
			Outline: []models.OutlineNode{
				{Title: "Background"},
				{Title: "Engineering Practice"},
			},
		},
		CurrentDocument: "# DuckDB Notes\n\n## Background\n\n## Engineering Practice\n",
		Thought:         models.Thought{ID: "thought-1", DisplayTitle: "Indexing note"},
		Content:         models.ThoughtContent{Original: "Engineering practice for DuckDB semantic indexing."},
		Membership:      models.TopicMembership{Reasons: []string{"keyword:duckdb"}},
		SourceLink:      "../../thoughts/2026/06/thought-1.md",
	})
	if err != nil {
		t.Fatalf("Weave() error = %v", err)
	}
	backgroundIdx := strings.Index(result.Document, "## Background")
	practiceIdx := strings.Index(result.Document, "## Engineering Practice")
	noteIdx := strings.Index(result.Document, "### Indexing note")
	if backgroundIdx < 0 || practiceIdx < 0 || noteIdx < 0 {
		t.Fatalf("unexpected woven document:\n%s", result.Document)
	}
	if !(backgroundIdx < practiceIdx && practiceIdx < noteIdx) {
		t.Fatalf("note should be inserted under Engineering Practice:\n%s", result.Document)
	}
	if !strings.Contains(result.Document, "Sources: [[../../thoughts/2026/06/thought-1.md]]") {
		t.Fatalf("expected source link:\n%s", result.Document)
	}
}

func TestOpenAICompatibleProviderWeaveRequiresSourceLink(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(res http.ResponseWriter, req *http.Request) {
		res.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(res).Encode(map[string]any{
			"choices": []map[string]any{
				{
					"message": map[string]string{
						"content": `{"document":"# Missing source"}`,
					},
				},
			},
		})
	}))
	defer server.Close()

	provider := NewOpenAICompatibleProvider(appconfig.LLMConfig{
		BaseURL:   server.URL,
		APIKey:    "test-key",
		ChatModel: "chat-model",
		Timeout:   time.Second,
	}, appconfig.EmbeddingConfig{})
	_, err := provider.Weave(context.Background(), models.TopicWeaveRequest{
		Topic:           models.Topic{ID: "duckdb-notes", Name: "DuckDB Notes"},
		CurrentDocument: "# DuckDB Notes",
		Thought:         models.Thought{ID: "thought-1"},
		Content:         models.ThoughtContent{Original: "content"},
		SourceLink:      "../../thoughts/2026/06/thought-1.md",
	})
	if err == nil {
		t.Fatalf("expected missing source link error")
	}
}

func TestOpenAICompatibleProviderSynthesizePreservesSourceLinks(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(res http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/v1/chat/completions" {
			http.NotFound(res, req)
			return
		}
		res.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(res).Encode(map[string]any{
			"choices": []map[string]any{
				{
					"message": map[string]string{
						"content": `{"content":"# Cloud draft\n\nSynthesized by cloud model."}`,
					},
				},
			},
		})
	}))
	defer server.Close()

	provider := NewOpenAICompatibleProvider(appconfig.LLMConfig{
		BaseURL:   server.URL,
		APIKey:    "test-key",
		ChatModel: "chat-model",
		Timeout:   time.Second,
	}, appconfig.EmbeddingConfig{})
	draft, err := provider.Synthesize(context.Background(), SynthesisRequest{
		ThoughtIDs: []string{"thought-1"},
		Goal:       "Cloud outline",
		Format:     "outline",
		Snapshots: []models.ThoughtSnapshot{
			{
				Thought: models.Thought{ID: "thought-1", Path: "thoughts/2026/06/thought-1.md", DisplayTitle: "Cloud note"},
				Content: models.ThoughtContent{Original: "Cloud synthesis source."},
			},
		},
		SourceLinks: []string{"thoughts/2026/06/thought-1.md"},
	})
	if err != nil {
		t.Fatalf("Synthesize() error = %v", err)
	}
	if draft.Model != "chat-model" {
		t.Fatalf("model = %q", draft.Model)
	}
	if !strings.Contains(draft.Content, "Cloud draft") {
		t.Fatalf("content = %q", draft.Content)
	}
	if !strings.Contains(draft.Content, "[[thoughts/2026/06/thought-1.md]]") {
		t.Fatalf("expected source link to be appended, content = %q", draft.Content)
	}
}

func TestLocalProviderExpandCoversFiveSections(t *testing.T) {
	provider := NewLocalRefineProvider()
	result, err := provider.Expand(context.Background(), ExpandRequest{
		Thought: models.Thought{ID: "thought-1", Type: models.ThoughtTypeText},
		Content: models.ThoughtContent{Original: "需要开发一个 web 页面采集工具"},
		Summary: "用户提交了一条 web 采集相关笔记",
		Tags:    []string{"web", "scraping"},
	})
	if err != nil {
		t.Fatalf("Expand() error = %v", err)
	}
	for _, heading := range []string{
		"## 背景与现状分析",
		"## 可能的处理方向",
		"## 推荐的具体步骤",
		"## 关键注意事项",
		"## 延伸阅读建议",
	} {
		if !strings.Contains(result.Plan, heading) {
			t.Fatalf("plan missing %q:\n%s", heading, result.Plan)
		}
	}
	if result.Model != "local-rule" {
		t.Fatalf("model = %q", result.Model)
	}
}

func TestLocalProviderBuildCaptureContextReturnsStructuredSummary(t *testing.T) {
	provider := NewLocalRefineProvider()
	result, err := provider.BuildCaptureContext(context.Background(), CaptureContextRequest{
		SessionID: "s1",
		Content:   "我需要整理一个主题方向\n\n补充背景、目标和预期产出",
	})
	if err != nil {
		t.Fatalf("BuildCaptureContext() error = %v", err)
	}
	for _, want := range []string{
		"**目标理解**",
		"**已确认信息**",
		"**可展开方向**",
		"需求说明、创作设定、调研提纲、方案草案或行动清单",
		"**下一步**",
	} {
		if !strings.Contains(result.CandidateSummary, want) {
			t.Fatalf("summary missing %q:\n%s", want, result.CandidateSummary)
		}
	}
	if strings.TrimSpace(result.CandidateSummary) == strings.TrimSpace(result.CandidateBody) {
		t.Fatalf("summary should not be a verbatim body replay")
	}
}

func TestOpenAICompatibleProviderBuildCaptureContextUsesPromptFile(t *testing.T) {
	promptPath := filepath.Join(t.TempDir(), "capture-context-system.md")
	customPrompt := "Custom capture context system prompt for a configured business template."
	if err := os.WriteFile(promptPath, []byte(customPrompt), 0o600); err != nil {
		t.Fatalf("write prompt: %v", err)
	}
	var requestBody string
	server := httptest.NewServer(http.HandlerFunc(func(res http.ResponseWriter, req *http.Request) {
		raw, err := io.ReadAll(req.Body)
		if err != nil {
			t.Fatalf("read request body: %v", err)
		}
		requestBody = string(raw)
		res.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(res).Encode(map[string]any{
			"choices": []map[string]any{
				{
					"message": map[string]string{
						"content": `{"topic":"t","goal":"g","confirmed_facts":["f"],"open_questions":[],"conflicts":[],"candidate_title":"title","candidate_tags":[],"candidate_summary":"summary","candidate_body":"body","source_links":[],"related_thought_ids":[],"suggested_topic_ids":[],"archive_intent":"none","archive_strategy":"new"}`,
					},
				},
			},
		})
	}))
	defer server.Close()

	provider := NewOpenAICompatibleProvider(appconfig.LLMConfig{
		BaseURL:   server.URL,
		APIKey:    "test-key",
		ChatModel: "chat-model",
		Timeout:   time.Second,
		Prompts: appconfig.LLMPromptsConfig{
			CaptureContextSystemPath: promptPath,
		},
	}, appconfig.EmbeddingConfig{})
	result, err := provider.BuildCaptureContext(context.Background(), CaptureContextRequest{
		SessionID: "s1",
		Content:   "整理一个主题方向",
	})
	if err != nil {
		t.Fatalf("BuildCaptureContext() error = %v", err)
	}
	if result.CandidateSummary != "summary" {
		t.Fatalf("CandidateSummary = %q", result.CandidateSummary)
	}
	if !strings.Contains(requestBody, customPrompt) {
		t.Fatalf("request body does not contain configured prompt:\n%s", requestBody)
	}
	if strings.Contains(requestBody, "You maintain ThoughtFlow capture session context") {
		t.Fatalf("request body should not contain default prompt when a prompt file is configured:\n%s", requestBody)
	}
}

func TestBuildCaptureContextParsesFirstBalancedJSONObject(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(res http.ResponseWriter, req *http.Request) {
		res.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(res).Encode(map[string]any{
			"choices": []map[string]any{
				{
					"message": map[string]string{
						"content": "```json\n" +
							`{"topic":"t","goal":"g","confirmed_facts":["f"],"open_questions":[],"conflicts":[],"candidate_title":"title","candidate_tags":[],"candidate_summary":"summary with {braces}","candidate_body":"body","source_links":[],"related_thought_ids":[],"suggested_topic_ids":[],"archive_intent":"none","archive_strategy":"new"}` +
							"\n```\nSure, here is an explanation with another {not_json:true} block.",
					},
				},
			},
		})
	}))
	defer server.Close()

	provider := NewOpenAICompatibleProvider(appconfig.LLMConfig{
		BaseURL:   server.URL,
		APIKey:    "test-key",
		ChatModel: "chat-model",
		Timeout:   time.Second,
	}, appconfig.EmbeddingConfig{})

	result, err := provider.BuildCaptureContext(context.Background(), CaptureContextRequest{
		SessionID: "s1",
		Content:   "整理一个主题方向",
	})
	if err != nil {
		t.Fatalf("BuildCaptureContext() error = %v", err)
	}
	if result.CandidateSummary != "summary with {braces}" {
		t.Fatalf("CandidateSummary = %q", result.CandidateSummary)
	}
}

func TestDefaultCaptureContextPromptRequiresRichFirstTurnExpansion(t *testing.T) {
	provider := NewOpenAICompatibleProvider(appconfig.LLMConfig{}, appconfig.EmbeddingConfig{})
	prompt, err := provider.captureContextSystemPrompt()
	if err != nil {
		t.Fatalf("captureContextSystemPrompt: %v", err)
	}
	for _, want := range []string{
		"Everyday conversation style",
		"archive_intent is \"none\"",
		"not like a complete requirements document",
		"not a formal Thought document",
		"First-turn expansion rule",
		"only one user turn",
		"several concrete expansion points",
		"初步推断",
		"Multi-turn convergence rule",
		"reduce ambiguity",
		"remove obsolete open_questions",
		"Only when archive_intent is \"llm\"",
		"complete formal Thought document",
		"final information already converged",
		"High-value research/specification lens",
		"senior domain expert",
		"全局原则与价值观",
		"场景差异化对比",
		"关键模块深挖",
		"跨场景禁忌 / Anti-Patterns",
		"文案与表达规范",
		"正确做法 / 错误做法",
		"Missing context must not block useful output",
		"concrete, opinionated first draft",
		"not just a proposed outline",
		"usable v0.1 answer",
		"8-12 concrete bullets",
		"structured draft that could later be archived",
		"shallow framing phrases",
		"component/process choices",
		"acceptance criteria",
		"do not make the first response mostly questions",
		"open_questions should be limited",
		"normally 3 or fewer",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("default prompt missing %q:\n%s", want, prompt)
		}
	}
}

func TestOpenAICompatibleProviderExpandStripsFence(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(res http.ResponseWriter, req *http.Request) {
		res.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(res).Encode(map[string]any{
			"choices": []map[string]any{
				{
					"message": map[string]string{
						"content": "```markdown\n## 背景\n线下调研\n```\n",
					},
				},
			},
		})
	}))
	defer server.Close()

	provider := NewOpenAICompatibleProvider(appconfig.LLMConfig{
		BaseURL:   server.URL,
		APIKey:    "test-key",
		ChatModel: "chat-model",
		Timeout:   time.Second,
	}, appconfig.EmbeddingConfig{})
	result, err := provider.Expand(context.Background(), ExpandRequest{
		Thought: models.Thought{ID: "thought-1", Type: models.ThoughtTypeText, UserTitle: "采集工具"},
		Content: models.ThoughtContent{Original: "需要做个 web 页面采集"},
		Summary: "采集工具笔记",
		Tags:    []string{"scraping"},
	})
	if err != nil {
		t.Fatalf("Expand() error = %v", err)
	}
	if strings.Contains(result.Plan, "```") {
		t.Fatalf("expected fence stripped, got: %q", result.Plan)
	}
	if !strings.Contains(result.Plan, "## 背景") {
		t.Fatalf("plan = %q", result.Plan)
	}
	if result.Model != "chat-model" {
		t.Fatalf("model = %q", result.Model)
	}
}

func TestOpenAICompatibleProviderExpandRejectsEmpty(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(res http.ResponseWriter, req *http.Request) {
		res.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(res).Encode(map[string]any{
			"choices": []map[string]any{
				{
					"message": map[string]string{"content": "   \n"},
				},
			},
		})
	}))
	defer server.Close()

	provider := NewOpenAICompatibleProvider(appconfig.LLMConfig{
		BaseURL:   server.URL,
		APIKey:    "test-key",
		ChatModel: "chat-model",
		Timeout:   time.Second,
	}, appconfig.EmbeddingConfig{})
	_, err := provider.Expand(context.Background(), ExpandRequest{
		Thought: models.Thought{ID: "thought-1", Type: models.ThoughtTypeText},
		Content: models.ThoughtContent{Original: "x"},
	})
	if err == nil {
		t.Fatalf("expected error for empty plan")
	}
}
