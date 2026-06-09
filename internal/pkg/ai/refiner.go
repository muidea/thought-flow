package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"math"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	mnet "github.com/muidea/magicCommon/foundation/net"

	"thoughtflow/internal/pkg/appconfig"
	"thoughtflow/internal/pkg/models"
)

const localEmbeddingModel = "local-hash-embedding-v1"

type RefineRequest struct {
	Thought models.Thought
	Content models.ThoughtContent
}

type EmbedRequest struct {
	ThoughtID string
	Text      string
}

type RefineProvider interface {
	Refine(ctx context.Context, req RefineRequest) (models.ThoughtRefinement, error)
}

type EmbeddingProvider interface {
	Embed(ctx context.Context, req EmbedRequest) (models.EmbeddingRecord, error)
}

type Provider interface {
	RefineProvider
	EmbeddingProvider
}

type LocalRefineProvider struct{}

func NewLocalRefineProvider() *LocalRefineProvider {
	return &LocalRefineProvider{}
}

func NewRefineProvider(cfg appconfig.AIConfig) RefineProvider {
	if strings.TrimSpace(cfg.APIKey) == "" {
		return NewLocalRefineProvider()
	}
	return NewOpenAICompatibleProvider(cfg)
}

func NewEmbeddingProvider(cfg appconfig.AIConfig) EmbeddingProvider {
	if strings.TrimSpace(cfg.APIKey) == "" {
		return NewLocalRefineProvider()
	}
	return NewOpenAICompatibleProvider(cfg)
}

func (p *LocalRefineProvider) Refine(ctx context.Context, req RefineRequest) (models.ThoughtRefinement, error) {
	_ = ctx
	text := strings.TrimSpace(req.Content.ExtractedContent)
	if text == "" {
		text = strings.TrimSpace(req.Content.Original)
	}
	embedding, _ := p.Embed(ctx, EmbedRequest{ThoughtID: req.Thought.ID, Text: text})
	return models.ThoughtRefinement{
		ThoughtID:   req.Thought.ID,
		Status:      models.RefineStatusRefined,
		Summary:     summarize(text),
		KeyPoints:   keyPoints(text),
		AITags:      inferTags(text),
		Model:       "local-rule",
		InputHash:   models.ContentHash(text),
		GeneratedAt: time.Now().UTC(),
		Embedding:   &embedding,
	}, nil
}

func (p *LocalRefineProvider) Embed(ctx context.Context, req EmbedRequest) (models.EmbeddingRecord, error) {
	_ = ctx
	vector := localEmbedding(req.Text, 64)
	return models.EmbeddingRecord{
		ThoughtID:   req.ThoughtID,
		Model:       localEmbeddingModel,
		Dimension:   len(vector),
		Vector:      vector,
		ContentHash: models.ContentHash(req.Text),
		CreatedAt:   time.Now().UTC(),
	}, nil
}

type OpenAICompatibleProvider struct {
	baseURL        string
	apiKey         string
	chatModel      string
	embeddingModel string
	client         *http.Client
}

func NewOpenAICompatibleProvider(cfg appconfig.AIConfig) *OpenAICompatibleProvider {
	client := mnet.NewDNSCacheHttpClient()
	if cfg.Timeout > 0 {
		client.Timeout = cfg.Timeout
	}
	return &OpenAICompatibleProvider{
		baseURL:        strings.TrimRight(firstNonEmpty(cfg.BaseURL, "https://api.openai.com"), "/"),
		apiKey:         strings.TrimSpace(cfg.APIKey),
		chatModel:      firstNonEmpty(cfg.ChatModel, "gpt-4o-mini"),
		embeddingModel: firstNonEmpty(cfg.EmbeddingModel, "text-embedding-3-small"),
		client:         client,
	}
}

func (p *OpenAICompatibleProvider) Refine(ctx context.Context, req RefineRequest) (models.ThoughtRefinement, error) {
	text := strings.TrimSpace(req.Content.ExtractedContent)
	if text == "" {
		text = strings.TrimSpace(req.Content.Original)
	}
	if text == "" {
		return models.ThoughtRefinement{}, errors.New("refine text is empty")
	}
	payload := map[string]any{
		"model": p.chatModel,
		"messages": []map[string]string{
			{
				"role":    "system",
				"content": "You refine notes for ThoughtFlow. Return strict JSON only with fields summary string, key_points string array, tags string array, title string.",
			},
			{
				"role": "user",
				"content": "Refine this note without changing the original content.\n\nTitle: " +
					firstNonEmpty(req.Thought.UserTitle, req.Thought.ExtractedTitle, req.Thought.ID) +
					"\n\nContent:\n" + text,
			},
		},
		"temperature": 0.2,
	}
	var response chatCompletionResponse
	if err := p.postJSON(ctx, "/chat/completions", payload, &response); err != nil {
		return models.ThoughtRefinement{}, err
	}
	if len(response.Choices) == 0 {
		return models.ThoughtRefinement{}, errors.New("chat completion returned no choices")
	}
	content := strings.TrimSpace(response.Choices[0].Message.Content)
	var parsed refineJSON
	if err := json.Unmarshal([]byte(extractJSONObject(content)), &parsed); err != nil {
		return models.ThoughtRefinement{}, fmt.Errorf("parse refinement json: %w", err)
	}
	embedding, err := p.Embed(ctx, EmbedRequest{ThoughtID: req.Thought.ID, Text: text})
	if err != nil {
		embedding = models.EmbeddingRecord{}
	}
	refinement := models.ThoughtRefinement{
		ThoughtID:      req.Thought.ID,
		Status:         models.RefineStatusRefined,
		ExtractedTitle: strings.TrimSpace(parsed.Title),
		Summary:        strings.TrimSpace(parsed.Summary),
		KeyPoints:      normalizeList(parsed.KeyPoints),
		AITags:         normalizeList(parsed.Tags),
		Model:          p.chatModel,
		InputHash:      models.ContentHash(text),
		GeneratedAt:    time.Now().UTC(),
	}
	if len(embedding.Vector) > 0 {
		refinement.Embedding = &embedding
	}
	return refinement, nil
}

func (p *OpenAICompatibleProvider) Embed(ctx context.Context, req EmbedRequest) (models.EmbeddingRecord, error) {
	text := strings.TrimSpace(req.Text)
	if text == "" {
		return models.EmbeddingRecord{}, errors.New("embedding text is empty")
	}
	payload := map[string]any{
		"model": p.embeddingModel,
		"input": text,
	}
	var response embeddingResponse
	if err := p.postJSON(ctx, "/embeddings", payload, &response); err != nil {
		return models.EmbeddingRecord{}, err
	}
	if len(response.Data) == 0 || len(response.Data[0].Embedding) == 0 {
		return models.EmbeddingRecord{}, errors.New("embedding response is empty")
	}
	vector := response.Data[0].Embedding
	return models.EmbeddingRecord{
		ThoughtID:   req.ThoughtID,
		Model:       p.embeddingModel,
		Dimension:   len(vector),
		Vector:      vector,
		ContentHash: models.ContentHash(text),
		CreatedAt:   time.Now().UTC(),
	}, nil
}

func (p *OpenAICompatibleProvider) postJSON(ctx context.Context, path string, payload any, target any) error {
	if strings.TrimSpace(p.apiKey) == "" {
		return errors.New("ai api key is required")
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.apiURL(path), bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.apiKey)
	resp, err := p.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("ai provider returned status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if err := json.Unmarshal(body, target); err != nil {
		return err
	}
	return nil
}

func (p *OpenAICompatibleProvider) apiURL(path string) string {
	base := strings.TrimRight(p.baseURL, "/")
	if strings.HasSuffix(base, "/v1") {
		return base + path
	}
	return base + "/v1" + path
}

func summarize(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	if len(text) <= 240 {
		return text
	}
	return text[:240]
}

func keyPoints(text string) []string {
	splitter := regexp.MustCompile(`[。.!?\n]+`)
	raw := splitter.Split(text, -1)
	ret := []string{}
	for _, item := range raw {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if len(item) > 160 {
			item = item[:160]
		}
		ret = append(ret, item)
		if len(ret) >= 3 {
			break
		}
	}
	return ret
}

func inferTags(text string) []string {
	lower := strings.ToLower(text)
	candidates := map[string][]string{
		"ai":                   {"ai", "llm", "embedding", "model", "openai", "deepseek"},
		"knowledge-management": {"knowledge", "note", "markdown", "obsidian", "logseq", "search"},
		"engineering":          {"go", "golang", "api", "http", "duckdb", "git"},
		"research":             {"paper", "study", "analysis", "summary"},
	}
	ret := []string{}
	for tag, words := range candidates {
		for _, word := range words {
			if strings.Contains(lower, word) {
				ret = append(ret, tag)
				break
			}
		}
	}
	sort.Strings(ret)
	return ret
}

func localEmbedding(text string, dimension int) []float64 {
	vector := make([]float64, dimension)
	words := regexp.MustCompile(`[a-zA-Z0-9_\-\p{Han}]+`).FindAllString(strings.ToLower(text), -1)
	for _, word := range words {
		hash := fnv.New32a()
		_, _ = hash.Write([]byte(word))
		value := hash.Sum32()
		idx := int(value % uint32(dimension))
		weight := 1.0
		if value&1 == 1 {
			weight = -1
		}
		vector[idx] += weight
	}
	norm := 0.0
	for _, value := range vector {
		norm += value * value
	}
	if norm == 0 {
		return vector
	}
	norm = math.Sqrt(norm)
	for idx := range vector {
		vector[idx] = vector[idx] / norm
	}
	return vector
}

type chatCompletionResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

type embeddingResponse struct {
	Data []struct {
		Embedding []float64 `json:"embedding"`
	} `json:"data"`
}

type refineJSON struct {
	Summary   string   `json:"summary"`
	KeyPoints []string `json:"key_points"`
	Tags      []string `json:"tags"`
	Title     string   `json:"title"`
}

func extractJSONObject(value string) string {
	value = strings.TrimSpace(value)
	start := strings.Index(value, "{")
	end := strings.LastIndex(value, "}")
	if start >= 0 && end >= start {
		return value[start : end+1]
	}
	return value
}

func normalizeList(values []string) []string {
	seen := map[string]struct{}{}
	ret := []string{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		key := strings.ToLower(value)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		ret = append(ret, value)
	}
	sort.Strings(ret)
	return ret
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}
