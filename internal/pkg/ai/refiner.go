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
	"thoughtflow/internal/pkg/observability"
)

const localEmbeddingModel = "local-hash-embedding-v1"
const openAIMaxAttempts = 3

type RefineRequest struct {
	Thought models.Thought
	Content models.ThoughtContent
}

type EmbedRequest struct {
	ThoughtID string
	Text      string
}

type SynthesisRequest struct {
	ThoughtIDs  []string
	Goal        string
	Format      string
	Snapshots   []models.ThoughtSnapshot
	SourceLinks []string
}

type RefineProvider interface {
	Refine(ctx context.Context, req RefineRequest) (models.ThoughtRefinement, error)
}

type EmbeddingProvider interface {
	Embed(ctx context.Context, req EmbedRequest) (models.EmbeddingRecord, error)
}

type WeaveProvider interface {
	Weave(ctx context.Context, req models.TopicWeaveRequest) (models.TopicWeaveResult, error)
}

type SynthesisProvider interface {
	Synthesize(ctx context.Context, req SynthesisRequest) (models.SynthesisDraft, error)
}

type Provider interface {
	RefineProvider
	EmbeddingProvider
	WeaveProvider
	SynthesisProvider
}

type ProviderError struct {
	Code       string
	StatusCode int
	Message    string
	Retryable  bool
}

func (e ProviderError) Error() string {
	if e.StatusCode > 0 {
		return fmt.Sprintf("%s: status %d: %s", e.Code, e.StatusCode, e.Message)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

type LocalRefineProvider struct{}

func NewLocalRefineProvider() *LocalRefineProvider {
	return &LocalRefineProvider{}
}

func NewRefineProvider(cfg appconfig.AIConfig) RefineProvider {
	if strings.TrimSpace(cfg.APIKey) == "" {
		return observedRefineProvider{next: NewLocalRefineProvider()}
	}
	return observedRefineProvider{next: NewOpenAICompatibleProvider(cfg)}
}

func NewEmbeddingProvider(cfg appconfig.AIConfig) EmbeddingProvider {
	if strings.TrimSpace(cfg.APIKey) == "" {
		return observedEmbeddingProvider{next: NewLocalRefineProvider()}
	}
	return observedEmbeddingProvider{next: NewOpenAICompatibleProvider(cfg)}
}

func NewWeaveProvider(cfg appconfig.AIConfig) WeaveProvider {
	if strings.TrimSpace(cfg.APIKey) == "" {
		return observedWeaveProvider{next: NewLocalRefineProvider()}
	}
	return observedWeaveProvider{next: NewOpenAICompatibleProvider(cfg)}
}

func NewSynthesisProvider(cfg appconfig.AIConfig) SynthesisProvider {
	if strings.TrimSpace(cfg.APIKey) == "" {
		return observedSynthesisProvider{next: NewLocalRefineProvider()}
	}
	return observedSynthesisProvider{next: NewOpenAICompatibleProvider(cfg)}
}

type observedRefineProvider struct {
	next RefineProvider
}

func (p observedRefineProvider) Refine(ctx context.Context, req RefineRequest) (models.ThoughtRefinement, error) {
	observability.IncrementAIRequest()
	return p.next.Refine(ctx, req)
}

type observedEmbeddingProvider struct {
	next EmbeddingProvider
}

func (p observedEmbeddingProvider) Embed(ctx context.Context, req EmbedRequest) (models.EmbeddingRecord, error) {
	observability.IncrementAIRequest()
	return p.next.Embed(ctx, req)
}

type observedWeaveProvider struct {
	next WeaveProvider
}

func (p observedWeaveProvider) Weave(ctx context.Context, req models.TopicWeaveRequest) (models.TopicWeaveResult, error) {
	observability.IncrementAIRequest()
	return p.next.Weave(ctx, req)
}

type observedSynthesisProvider struct {
	next SynthesisProvider
}

func (p observedSynthesisProvider) Synthesize(ctx context.Context, req SynthesisRequest) (models.SynthesisDraft, error) {
	observability.IncrementAIRequest()
	return p.next.Synthesize(ctx, req)
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

func (p *LocalRefineProvider) Weave(ctx context.Context, req models.TopicWeaveRequest) (models.TopicWeaveResult, error) {
	_ = ctx
	document := strings.TrimRight(req.CurrentDocument, "\n")
	if document == "" {
		document = localInitialTopicDocument(req.Topic)
	}
	if strings.Contains(document, req.SourceLink) {
		return models.TopicWeaveResult{Document: document, Model: "local-rule", Strategy: "already-linked"}, nil
	}
	section := localThoughtSection(req, chooseOutlineSection(req))
	if target := chooseOutlineSection(req); target != "" {
		document = insertIntoOutlineSection(document, target, section)
	} else {
		document = strings.TrimRight(document, "\n") + "\n\n" + section
	}
	return models.TopicWeaveResult{Document: strings.TrimRight(document, "\n") + "\n", Model: "local-rule", Strategy: "outline-insert"}, nil
}

func (p *LocalRefineProvider) Synthesize(ctx context.Context, req SynthesisRequest) (models.SynthesisDraft, error) {
	_ = ctx
	contentParts := []string{}
	for _, snapshot := range req.Snapshots {
		title := firstNonEmpty(snapshot.Thought.DisplayTitle, snapshot.Thought.UserTitle, snapshot.Thought.ExtractedTitle, snapshot.Thought.ID)
		body := firstNonEmpty(snapshot.Thought.Summary, snapshot.Content.AINotes, snapshot.Content.ExtractedContent, snapshot.Content.Original)
		contentParts = append(contentParts, "## "+title+"\n\n"+body)
	}
	format := firstNonEmpty(req.Format, "summary")
	goal := firstNonEmpty(req.Goal, "Synthesize selected thoughts")
	now := time.Now().UTC()
	return models.SynthesisDraft{
		ID:          models.NewJobID("synthesis", now),
		ThoughtIDs:  req.ThoughtIDs,
		Goal:        goal,
		Format:      format,
		Content:     ensureSynthesisSourceLinks("# "+goal+"\n\n"+strings.Join(contentParts, "\n\n"), req.SourceLinks),
		SourceLinks: req.SourceLinks,
		Model:       "local-rule",
		Status:      "draft",
		CreatedAt:   now,
		UpdatedAt:   now,
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

func (p *OpenAICompatibleProvider) Weave(ctx context.Context, req models.TopicWeaveRequest) (models.TopicWeaveResult, error) {
	if strings.TrimSpace(req.CurrentDocument) == "" {
		req.CurrentDocument = localInitialTopicDocument(req.Topic)
	}
	section := localThoughtSection(req, "")
	payload := map[string]any{
		"model": p.chatModel,
		"messages": []map[string]string{
			{
				"role":    "system",
				"content": "You are a careful Markdown editor for ThoughtFlow topic documents. Return strict JSON only with field document string. Preserve YAML front matter, existing content, and source links. Insert or merge the new thought into the most appropriate existing outline section. Never remove the required source link.",
			},
			{
				"role": "user",
				"content": "Topic name: " + req.Topic.Name +
					"\nTopic description: " + req.Topic.Description +
					"\nRequired source link substring: " + req.SourceLink +
					"\nMatch reasons: " + strings.Join(req.Membership.Reasons, ", ") +
					"\n\nCurrent topic Markdown:\n" + req.CurrentDocument +
					"\n\nNew thought section to weave:\n" + section,
			},
		},
		"temperature": 0.2,
	}
	var response chatCompletionResponse
	if err := p.postJSON(ctx, "/chat/completions", payload, &response); err != nil {
		return models.TopicWeaveResult{}, err
	}
	if len(response.Choices) == 0 {
		return models.TopicWeaveResult{}, errors.New("topic weave returned no choices")
	}
	var parsed topicWeaveJSON
	if err := json.Unmarshal([]byte(extractJSONObject(response.Choices[0].Message.Content)), &parsed); err != nil {
		return models.TopicWeaveResult{}, fmt.Errorf("parse topic weave json: %w", err)
	}
	document := strings.TrimSpace(parsed.Document)
	if document == "" {
		return models.TopicWeaveResult{}, errors.New("topic weave document is empty")
	}
	if !strings.Contains(document, req.SourceLink) {
		return models.TopicWeaveResult{}, errors.New("topic weave document missing required source link")
	}
	return models.TopicWeaveResult{Document: document + "\n", Model: p.chatModel, Strategy: "llm-document-merge"}, nil
}

func (p *OpenAICompatibleProvider) Synthesize(ctx context.Context, req SynthesisRequest) (models.SynthesisDraft, error) {
	if len(req.Snapshots) == 0 {
		return models.SynthesisDraft{}, errors.New("synthesis snapshots are required")
	}
	goal := firstNonEmpty(req.Goal, "Synthesize selected thoughts")
	format := firstNonEmpty(req.Format, "summary")
	payload := map[string]any{
		"model": p.chatModel,
		"messages": []map[string]string{
			{
				"role":    "system",
				"content": "You synthesize ThoughtFlow notes into Markdown. Return strict JSON only with field content string. Preserve every provided source link in a Sources section. Do not invent sources.",
			},
			{
				"role": "user",
				"content": "Goal: " + goal +
					"\nFormat: " + format +
					"\nRequired source links:\n" + strings.Join(req.SourceLinks, "\n") +
					"\n\nInput notes:\n" + renderSynthesisInputs(req.Snapshots),
			},
		},
		"temperature": 0.2,
	}
	var response chatCompletionResponse
	if err := p.postJSON(ctx, "/chat/completions", payload, &response); err != nil {
		return models.SynthesisDraft{}, err
	}
	if len(response.Choices) == 0 {
		return models.SynthesisDraft{}, errors.New("synthesis returned no choices")
	}
	var parsed synthesisJSON
	if err := json.Unmarshal([]byte(extractJSONObject(response.Choices[0].Message.Content)), &parsed); err != nil {
		return models.SynthesisDraft{}, fmt.Errorf("parse synthesis json: %w", err)
	}
	content := strings.TrimSpace(parsed.Content)
	if content == "" {
		return models.SynthesisDraft{}, errors.New("synthesis content is empty")
	}
	now := time.Now().UTC()
	return models.SynthesisDraft{
		ID:          models.NewJobID("synthesis", now),
		ThoughtIDs:  req.ThoughtIDs,
		Goal:        goal,
		Format:      format,
		Content:     ensureSynthesisSourceLinks(content, req.SourceLinks),
		SourceLinks: req.SourceLinks,
		Model:       p.chatModel,
		Status:      "draft",
		CreatedAt:   now,
		UpdatedAt:   now,
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
	var lastErr error
	for attempt := 1; attempt <= openAIMaxAttempts; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.apiURL(path), bytes.NewReader(raw))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+p.apiKey)
		resp, err := p.client.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			lastErr = ProviderError{Code: "thoughtflow.ai.request_failed", Message: err.Error(), Retryable: true}
			if attempt < openAIMaxAttempts && waitRetryBackoff(ctx, attempt) == nil {
				continue
			}
			return lastErr
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		_ = resp.Body.Close()
		if readErr != nil {
			lastErr = ProviderError{Code: "thoughtflow.ai.read_failed", Message: readErr.Error(), Retryable: true}
			if attempt < openAIMaxAttempts && waitRetryBackoff(ctx, attempt) == nil {
				continue
			}
			return lastErr
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			providerErr := classifyProviderStatus(resp.StatusCode, body)
			lastErr = providerErr
			if providerErr.Retryable && attempt < openAIMaxAttempts && waitRetryBackoff(ctx, attempt) == nil {
				continue
			}
			return providerErr
		}
		if err := json.Unmarshal(body, target); err != nil {
			return ProviderError{Code: "thoughtflow.ai.invalid_json", Message: err.Error(), Retryable: false}
		}
		return nil
	}
	return lastErr
}

func classifyProviderStatus(statusCode int, body []byte) ProviderError {
	message := strings.TrimSpace(string(body))
	if message == "" {
		message = http.StatusText(statusCode)
	}
	retryable := statusCode == http.StatusTooManyRequests || statusCode >= 500
	code := "thoughtflow.ai.http_status"
	if retryable {
		code = "thoughtflow.ai.transient_status"
	}
	return ProviderError{
		Code:       code,
		StatusCode: statusCode,
		Message:    message,
		Retryable:  retryable,
	}
}

func waitRetryBackoff(ctx context.Context, attempt int) error {
	delay := time.Duration(attempt*attempt) * 10 * time.Millisecond
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
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

type topicWeaveJSON struct {
	Document string `json:"document"`
}

type synthesisJSON struct {
	Content string `json:"content"`
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

func renderSynthesisInputs(snapshots []models.ThoughtSnapshot) string {
	parts := []string{}
	for _, snapshot := range snapshots {
		title := firstNonEmpty(snapshot.Thought.DisplayTitle, snapshot.Thought.UserTitle, snapshot.Thought.ExtractedTitle, snapshot.Thought.ID)
		body := firstNonEmpty(snapshot.Thought.Summary, snapshot.Content.AINotes, snapshot.Content.ExtractedContent, snapshot.Content.Original)
		parts = append(parts, "ID: "+snapshot.Thought.ID+
			"\nTitle: "+title+
			"\nSource link: "+snapshot.Thought.Path+
			"\nContent:\n"+body)
	}
	return strings.Join(parts, "\n\n---\n\n")
}

func ensureSynthesisSourceLinks(content string, sourceLinks []string) string {
	content = strings.TrimSpace(content)
	missing := []string{}
	for _, link := range sourceLinks {
		link = strings.TrimSpace(link)
		if link == "" || strings.Contains(content, link) {
			continue
		}
		missing = append(missing, link)
	}
	if len(missing) == 0 {
		return content
	}
	var builder strings.Builder
	builder.WriteString(content)
	if strings.Contains(strings.ToLower(content), "### sources") {
		builder.WriteString("\n")
	} else {
		builder.WriteString("\n\n### Sources\n\n")
	}
	for _, link := range missing {
		builder.WriteString("- [[")
		builder.WriteString(link)
		builder.WriteString("]]\n")
	}
	return strings.TrimSpace(builder.String())
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

func localInitialTopicDocument(topic models.Topic) string {
	var builder strings.Builder
	builder.WriteString("---\n")
	builder.WriteString("id: ")
	builder.WriteString(topic.ID)
	builder.WriteString("\ntype: topic\nmembers: []\n---\n\n# ")
	builder.WriteString(topic.Name)
	builder.WriteString("\n")
	if strings.TrimSpace(topic.Description) != "" {
		builder.WriteString("\n")
		builder.WriteString(strings.TrimSpace(topic.Description))
		builder.WriteString("\n")
	}
	for _, node := range topic.Outline {
		if strings.TrimSpace(node.Title) != "" {
			builder.WriteString("\n## ")
			builder.WriteString(strings.TrimSpace(node.Title))
			builder.WriteString("\n")
		}
	}
	return builder.String()
}

func localThoughtSection(req models.TopicWeaveRequest, outlineTitle string) string {
	title := firstNonEmpty(req.Thought.DisplayTitle, req.Thought.UserTitle, req.Thought.ExtractedTitle, req.Thought.ID)
	body := firstNonEmpty(req.Thought.Summary, firstLine(req.Content.AINotes), firstLine(req.Content.ExtractedContent), firstLine(req.Content.Original))
	heading := "## "
	if outlineTitle != "" {
		heading = "### "
	}
	var builder strings.Builder
	builder.WriteString(heading)
	builder.WriteString(title)
	builder.WriteString("\n\n")
	if body != "" {
		builder.WriteString(body)
		builder.WriteString("\n\n")
	}
	if len(req.Membership.Reasons) > 0 {
		builder.WriteString("Match: ")
		builder.WriteString(strings.Join(req.Membership.Reasons, ", "))
		builder.WriteString("\n\n")
	}
	builder.WriteString("> Sources: [[")
	builder.WriteString(req.SourceLink)
	builder.WriteString("]]")
	return builder.String()
}

func chooseOutlineSection(req models.TopicWeaveRequest) string {
	if len(req.Topic.Outline) == 0 {
		return ""
	}
	searchText := strings.ToLower(strings.Join([]string{
		req.Thought.UserTitle,
		req.Thought.ExtractedTitle,
		req.Thought.Summary,
		req.Content.Original,
		req.Content.ExtractedContent,
		req.Content.AINotes,
		strings.Join(req.Membership.Reasons, " "),
	}, "\n"))
	bestTitle := ""
	bestScore := 0
	for _, node := range req.Topic.Outline {
		title := strings.TrimSpace(node.Title)
		if title == "" {
			continue
		}
		score := 0
		for _, token := range outlineTokens(title) {
			if strings.Contains(searchText, token) {
				score++
			}
		}
		if score > bestScore {
			bestTitle = title
			bestScore = score
		}
	}
	if bestTitle != "" {
		return bestTitle
	}
	return strings.TrimSpace(req.Topic.Outline[0].Title)
}

func insertIntoOutlineSection(document string, outlineTitle string, section string) string {
	marker := "## " + strings.TrimSpace(outlineTitle)
	start := strings.Index(document, marker)
	if start < 0 {
		return strings.TrimRight(document, "\n") + "\n\n" + section
	}
	bodyStart := start + len(marker)
	next := strings.Index(document[bodyStart:], "\n## ")
	if next < 0 {
		return strings.TrimRight(document, "\n") + "\n\n" + section
	}
	insertAt := bodyStart + next
	before := strings.TrimRight(document[:insertAt], "\n")
	after := strings.TrimLeft(document[insertAt:], "\n")
	return before + "\n\n" + section + "\n\n" + after
}

func outlineTokens(title string) []string {
	raw := regexp.MustCompile(`[a-zA-Z0-9_\-\p{Han}]+`).FindAllString(strings.ToLower(title), -1)
	ret := []string{}
	for _, token := range raw {
		if len([]rune(token)) <= 1 {
			continue
		}
		ret = append(ret, token)
	}
	return ret
}

func firstLine(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	line := strings.TrimSpace(strings.Split(value, "\n")[0])
	if len(line) > 240 {
		return line[:240]
	}
	return line
}
