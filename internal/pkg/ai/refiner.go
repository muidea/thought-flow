package ai

import (
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

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"

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

// ExpandRequest is the post-refine expansion input. The expander
// passes the refined thought, its current content, and the LLM-side
// signals (summary / key points / ai tags) so the LLM can produce a
// self-contained "处理思路与方案" without re-reading the entire
// content body. This is intentionally narrower than RefineRequest —
// the plan does not need the full Content struct.
type ExpandRequest struct {
	Thought models.Thought
	Content models.ThoughtContent
	Summary string
	Tags    []string
}

// ExpandResult is the expansion plan as the LLM returned it. The
// caller persists Result.Plan to the thought's expansion_plan front
// matter and shows it under "处理思路与方案" in the UI.
type ExpandResult struct {
	Plan        string
	Model       string
	GeneratedAt time.Time
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

type ExpandProvider interface {
	Expand(ctx context.Context, req ExpandRequest) (ExpandResult, error)
}

type Provider interface {
	RefineProvider
	EmbeddingProvider
	WeaveProvider
	SynthesisProvider
}

// ProviderError is the wire-stable error contract every caller
// (refiner service, expander service, search/topic services, tests)
// keys off. Migration to openai-go keeps Code values, StatusCode
// passthrough, and Retryable semantics identical so the public
// surface is preserved.
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

func NewRefineProvider(cfg appconfig.LLMConfig, embeddingCfg appconfig.EmbeddingConfig) RefineProvider {
	if strings.TrimSpace(cfg.APIKey) == "" {
		return observedRefineProvider{next: NewLocalRefineProvider()}
	}
	return observedRefineProvider{next: NewOpenAICompatibleProvider(cfg, embeddingCfg)}
}

func NewEmbeddingProvider(cfg appconfig.EmbeddingConfig) EmbeddingProvider {
	if strings.TrimSpace(cfg.APIKey) == "" {
		return observedEmbeddingProvider{next: NewLocalRefineProvider()}
	}
	return observedEmbeddingProvider{next: NewOpenAICompatibleEmbeddingProvider(cfg)}
}

func NewWeaveProvider(cfg appconfig.LLMConfig) WeaveProvider {
	if strings.TrimSpace(cfg.APIKey) == "" {
		return observedWeaveProvider{next: NewLocalRefineProvider()}
	}
	return observedWeaveProvider{next: NewOpenAICompatibleProvider(cfg, appconfig.EmbeddingConfig{})}
}

func NewSynthesisProvider(cfg appconfig.LLMConfig) SynthesisProvider {
	if strings.TrimSpace(cfg.APIKey) == "" {
		return observedSynthesisProvider{next: NewLocalRefineProvider()}
	}
	return observedSynthesisProvider{next: NewOpenAICompatibleProvider(cfg, appconfig.EmbeddingConfig{})}
}

func NewExpandProvider(cfg appconfig.LLMConfig) ExpandProvider {
	if strings.TrimSpace(cfg.APIKey) == "" {
		return observedExpandProvider{next: NewLocalRefineProvider()}
	}
	return observedExpandProvider{next: NewOpenAICompatibleProvider(cfg, appconfig.EmbeddingConfig{})}
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

type observedExpandProvider struct {
	next ExpandProvider
}

func (p observedExpandProvider) Expand(ctx context.Context, req ExpandRequest) (ExpandResult, error) {
	observability.IncrementAIRequest()
	return p.next.Expand(ctx, req)
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

// Expand returns a best-effort local expansion plan built from the
// summary, key points, and original content. The result is good
// enough to populate the "处理思路与方案" section when no LLM is
// configured; the LLM path is the production path and is expected
// to overwrite this.
func (p *LocalRefineProvider) Expand(ctx context.Context, req ExpandRequest) (ExpandResult, error) {
	_ = ctx
	title := firstNonEmpty(req.Thought.UserTitle, req.Thought.ExtractedTitle, req.Thought.ID)
	summary := firstNonEmpty(req.Summary, req.Thought.Summary)
	body := strings.TrimSpace(req.Content.Original)
	if body == "" {
		body = strings.TrimSpace(req.Content.ExtractedContent)
	}
	var plan strings.Builder
	plan.WriteString("## 背景与现状分析\n\n")
	if summary != "" {
		plan.WriteString(summary)
		plan.WriteString("\n\n")
	} else {
		plan.WriteString("基于标题「")
		plan.WriteString(title)
		plan.WriteString("」补全背景：用户提交了一条碎片笔记，需要进一步梳理思路和落地步骤。\n\n")
	}
	if body != "" {
		plan.WriteString("笔记原文要点：\n\n")
		plan.WriteString("- ")
		plan.WriteString(strings.Join(strings.Fields(strings.SplitN(body, "\n", 2)[0]), " "))
		plan.WriteString("\n\n")
	}
	plan.WriteString("## 可能的处理方向\n\n")
	for _, tag := range req.Tags {
		plan.WriteString("- 围绕「")
		plan.WriteString(tag)
		plan.WriteString("」延伸出一个具体的下一步动作\n")
	}
	if len(req.Tags) == 0 {
		plan.WriteString("- 列出 3 个最直接的下一步动作\n")
	}
	plan.WriteString("\n## 推荐的具体步骤\n\n1. 复述本笔记的目标\n2. 关联 1-2 条已有 Thought（搜索 hybrid 模式）\n3. 写入专题并触发 weave\n\n## 关键注意事项\n\n- 涉及 URL 时走 Jina reader 抓取\n- 专题判定阈值默认 0.75，near-miss 阈值 0.55\n- 锁与 refiner 共享，避免与正在 refine 的 Thought 互踩\n\n## 延伸阅读建议\n\n- 现有的相关专题文档\n- 与本笔记同类型的前置笔记")
	return ExpandResult{Plan: plan.String(), Model: "local-rule", GeneratedAt: time.Now().UTC()}, nil
}

// --------------------------------------------------------------------------
// openai-go SDK plumbing
// --------------------------------------------------------------------------

// OpenAICompatibleProvider backs every LLM-backed chat path (Refine,
// Weave, Synthesize, Expand) and, when configured, also Embedding
// via the embedded provider. The struct now holds a typed
// *openai.Client; the previous hand-rolled http.Client + JSON marshal
// + retry loop is gone.
type OpenAICompatibleProvider struct {
	client            *openai.Client
	chatModel         string
	embeddingProvider EmbeddingProvider
}

type OpenAICompatibleEmbeddingProvider struct {
	client *openai.Client
	model  string
}

// newOpenAITransport clones http.DefaultTransport and lifts every
// per-leg timeout to `cfg.Timeout`. The SDK has no per-request
// timeout equivalent of Transport.ResponseHeaderTimeout, so without
// this a slow LLM cold-start would still be killed by Go's
// 0-default ResponseHeaderTimeout long before Client.Timeout fires.
// The wrapping RoundTripper also caps the response body at 4 MiB
// (matching the old postOpenAICompatibleJSON behaviour) so a runaway
// LLM cannot OOM the process.
func newOpenAITransport(timeout time.Duration) http.RoundTripper {
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		base = &http.Transport{}
	}
	tr := base.Clone()
	if timeout > 0 {
		tr.ResponseHeaderTimeout = timeout
		tr.TLSHandshakeTimeout = 30 * time.Second
		tr.ExpectContinueTimeout = 10 * time.Second
	}
	return &limitsBodyTransport{inner: tr, maxBytes: 4 << 20}
}

// limitsBodyTransport caps the response body at maxBytes to mirror
// the old io.LimitReader(4<<20) behaviour. A truncated body still
// flows through to the JSON decoder; if the truncation invalidates
// the JSON, the SDK returns an UnmarshalTypeError-equivalent which
// wrapSDKError translates to thoughtflow.ai.invalid_json.
type limitsBodyTransport struct {
	inner    http.RoundTripper
	maxBytes int64
}

func (t *limitsBodyTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.inner.RoundTrip(req)
	if err != nil || resp == nil || resp.Body == nil {
		return resp, err
	}
	resp.Body = struct {
		io.Reader
		io.Closer
	}{
		Reader: io.LimitReader(resp.Body, t.maxBytes),
		Closer: resp.Body,
	}
	return resp, nil
}

// openAICompatibleBaseURL accepts either a bare base ("https://api.openai.com")
// or one with the /v1 suffix already present ("https://proxy.example.com/v1")
// and returns a base URL suitable for option.WithBaseURL. The openai-go
// SDK does NOT auto-prepend /v1, so the canonical form is "<base>/v1"
// unless /v1 is already there.
func openAICompatibleBaseURL(raw string) string {
	base := strings.TrimRight(strings.TrimSpace(raw), "/")
	if base == "" {
		return "https://api.openai.com/v1"
	}
	if strings.HasSuffix(strings.ToLower(base), "/v1") {
		return base
	}
	return base + "/v1"
}

func newOpenAIClient(cfg appconfig.LLMConfig) *openai.Client {
	opts := []option.RequestOption{
		option.WithAPIKey(strings.TrimSpace(cfg.APIKey)),
		option.WithBaseURL(openAICompatibleBaseURL(cfg.BaseURL)),
		option.WithHTTPClient(&http.Client{
			Timeout:   cfg.Timeout,
			Transport: newOpenAITransport(cfg.Timeout),
		}),
		// openai-go's MaxRetries counts *additional* retries; 2 = 1
		// initial + 2 retries, matching the old openAIMaxAttempts=3.
		option.WithMaxRetries(2),
	}
	client := openai.NewClient(opts...)
	return &client
}

func newEmbeddingClient(cfg appconfig.EmbeddingConfig) *openai.Client {
	opts := []option.RequestOption{
		option.WithAPIKey(strings.TrimSpace(cfg.APIKey)),
		option.WithBaseURL(openAICompatibleBaseURL(cfg.BaseURL)),
		option.WithHTTPClient(&http.Client{
			Timeout:   cfg.Timeout,
			Transport: newOpenAITransport(cfg.Timeout),
		}),
		option.WithMaxRetries(2),
	}
	client := openai.NewClient(opts...)
	return &client
}

func NewOpenAICompatibleProvider(cfg appconfig.LLMConfig, embeddingCfg appconfig.EmbeddingConfig) *OpenAICompatibleProvider {
	chatModel := firstNonEmpty(cfg.ChatModel, "gpt-4o-mini")
	var embeddingProvider EmbeddingProvider
	if strings.TrimSpace(embeddingCfg.APIKey) != "" {
		embeddingProvider = NewOpenAICompatibleEmbeddingProvider(embeddingCfg)
	}
	return &OpenAICompatibleProvider{
		client:            newOpenAIClient(cfg),
		chatModel:         chatModel,
		embeddingProvider: embeddingProvider,
	}
}

func NewOpenAICompatibleEmbeddingProvider(cfg appconfig.EmbeddingConfig) *OpenAICompatibleEmbeddingProvider {
	model := firstNonEmpty(cfg.Model, "text-embedding-3-small")
	return &OpenAICompatibleEmbeddingProvider{
		client: newEmbeddingClient(cfg),
		model:  model,
	}
}

// chatCompletion runs a single non-streaming chat completion with
// the standard two-message shape (system + user). It is the only
// place that touches the SDK's Chat.Completions.New call, so any
// future addition (response_format, tools, structured outputs)
// only needs to flow through here.
func (p *OpenAICompatibleProvider) chatCompletion(ctx context.Context, systemPrompt string, userPrompt string, temperature float64) (string, error) {
	resp, err := p.client.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{
		Model: p.chatModel,
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.SystemMessage(systemPrompt),
			openai.UserMessage(userPrompt),
		},
		Temperature: openai.Float(temperature),
	})
	if err != nil {
		return "", wrapSDKError(err)
	}
	if len(resp.Choices) == 0 {
		return "", errors.New("chat completion returned no choices")
	}
	return resp.Choices[0].Message.Content, nil
}

// wrapSDKError maps an openai-go / SDK transport error to the
// wire-stable ProviderError contract that callers (refiner service,
// tests) rely on. The five error codes from the old
// postOpenAICompatibleJSON path are preserved verbatim so jobs and
// logs still key off the same strings.
func wrapSDKError(err error) error {
	if err == nil {
		return nil
	}
	var apiErr *openai.Error
	if errors.As(err, &apiErr) {
		retryable := apiErr.StatusCode == http.StatusTooManyRequests || apiErr.StatusCode >= 500
		code := "thoughtflow.ai.http_status"
		if retryable {
			code = "thoughtflow.ai.transient_status"
		}
		message := strings.TrimSpace(apiErr.Message)
		if message == "" {
			message = http.StatusText(apiErr.StatusCode)
		}
		return ProviderError{
			Code:       code,
			StatusCode: apiErr.StatusCode,
			Message:    message,
			Retryable:  retryable,
		}
	}
	// Non-API errors: network/transport/JSON decode. JSON decode
	// failures surface from the SDK as apierror.Error with
	// StatusCode=0 and Code containing "invalid", so they go through
	// the branch above; everything else (DNS, dial, TLS) lands here.
	if ctxErr := ctxError(err); ctxErr != nil {
		return ctxErr
	}
	return ProviderError{Code: "thoughtflow.ai.request_failed", Message: err.Error(), Retryable: true}
}

// ctxError surfaces a non-nil context error (deadline / cancellation)
// as-is so callers can distinguish "client gave up" from "server
// failed".
func ctxError(err error) error {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}

func (p *OpenAICompatibleProvider) Refine(ctx context.Context, req RefineRequest) (models.ThoughtRefinement, error) {
	text := strings.TrimSpace(req.Content.ExtractedContent)
	if text == "" {
		text = strings.TrimSpace(req.Content.Original)
	}
	if text == "" {
		return models.ThoughtRefinement{}, errors.New("refine text is empty")
	}
	content, err := p.chatCompletion(ctx,
		"You refine notes for ThoughtFlow. Return strict JSON only with fields summary string, key_points string array, tags string array, title string.",
		"Refine this note without changing the original content.\n\nTitle: "+
			firstNonEmpty(req.Thought.UserTitle, req.Thought.ExtractedTitle, req.Thought.ID)+
			"\n\nContent:\n"+text,
		0.2,
	)
	if err != nil {
		return models.ThoughtRefinement{}, err
	}
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
	if p.embeddingProvider == nil {
		return models.EmbeddingRecord{}, errors.New("embedding provider is not configured")
	}
	return p.embeddingProvider.Embed(ctx, req)
}

func (p *OpenAICompatibleEmbeddingProvider) Embed(ctx context.Context, req EmbedRequest) (models.EmbeddingRecord, error) {
	text := strings.TrimSpace(req.Text)
	if text == "" {
		return models.EmbeddingRecord{}, errors.New("embedding text is empty")
	}
	resp, err := p.client.Embeddings.New(ctx, openai.EmbeddingNewParams{
		Model: p.model,
		Input: openai.EmbeddingNewParamsInputUnion{OfString: openai.String(text)},
	})
	if err != nil {
		return models.EmbeddingRecord{}, wrapSDKError(err)
	}
	if len(resp.Data) == 0 || len(resp.Data[0].Embedding) == 0 {
		return models.EmbeddingRecord{}, errors.New("embedding response is empty")
	}
	// SDK returns []float64 directly; assign without copy because
	// EmbeddingRecord owns the slice.
	vec := resp.Data[0].Embedding
	return models.EmbeddingRecord{
		ThoughtID:   req.ThoughtID,
		Model:       p.model,
		Dimension:   len(vec),
		Vector:      vec,
		ContentHash: models.ContentHash(text),
		CreatedAt:   time.Now().UTC(),
	}, nil
}

func (p *OpenAICompatibleProvider) Weave(ctx context.Context, req models.TopicWeaveRequest) (models.TopicWeaveResult, error) {
	if strings.TrimSpace(req.CurrentDocument) == "" {
		req.CurrentDocument = localInitialTopicDocument(req.Topic)
	}
	section := localThoughtSection(req, "")
	content, err := p.chatCompletion(ctx,
		"You are a careful Markdown editor for ThoughtFlow topic documents. Return strict JSON only with field document string. Preserve YAML front matter, existing content, and source links. Insert or merge the new thought into the most appropriate existing outline section. Never remove the required source link.",
		"Topic name: "+req.Topic.Name+
			"\nTopic description: "+req.Topic.Description+
			"\nRequired source link substring: "+req.SourceLink+
			"\nMatch reasons: "+strings.Join(req.Membership.Reasons, ", ")+
			"\n\nCurrent topic Markdown:\n"+req.CurrentDocument+
			"\n\nNew thought section to weave:\n"+section,
		0.2,
	)
	if err != nil {
		return models.TopicWeaveResult{}, err
	}
	var parsed topicWeaveJSON
	if err := json.Unmarshal([]byte(extractJSONObject(content)), &parsed); err != nil {
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
	content, err := p.chatCompletion(ctx,
		"You synthesize ThoughtFlow notes into Markdown. Return strict JSON only with field content string. Preserve every provided source link in a Sources section. Do not invent sources.",
		"Goal: "+goal+
			"\nFormat: "+format+
			"\nRequired source links:\n"+strings.Join(req.SourceLinks, "\n")+
			"\n\nInput notes:\n"+renderSynthesisInputs(req.Snapshots),
		0.2,
	)
	if err != nil {
		return models.SynthesisDraft{}, err
	}
	var parsed synthesisJSON
	if err := json.Unmarshal([]byte(extractJSONObject(content)), &parsed); err != nil {
		return models.SynthesisDraft{}, fmt.Errorf("parse synthesis json: %w", err)
	}
	synthContent := strings.TrimSpace(parsed.Content)
	if synthContent == "" {
		return models.SynthesisDraft{}, errors.New("synthesis content is empty")
	}
	now := time.Now().UTC()
	return models.SynthesisDraft{
		ID:          models.NewJobID("synthesis", now),
		ThoughtIDs:  req.ThoughtIDs,
		Goal:        goal,
		Format:      format,
		Content:     ensureSynthesisSourceLinks(synthContent, req.SourceLinks),
		SourceLinks: req.SourceLinks,
		Model:       p.chatModel,
		Status:      "draft",
		CreatedAt:   now,
		UpdatedAt:   now,
	}, nil
}

// Expand asks the LLM for a "处理思路与方案" Markdown plan. Unlike
// Refine/Weave/Synthesize the response is plain Markdown, not JSON —
// the prompt explicitly forbids code fences, so the caller can show
// the raw content directly.
func (p *OpenAICompatibleProvider) Expand(ctx context.Context, req ExpandRequest) (ExpandResult, error) {
	title := firstNonEmpty(req.Thought.UserTitle, req.Thought.ExtractedTitle, req.Thought.ID)
	original := firstNonEmpty(strings.TrimSpace(req.Content.Original), strings.TrimSpace(req.Content.ExtractedContent))
	summary := firstNonEmpty(req.Summary, req.Thought.Summary)
	content, err := p.chatCompletion(ctx,
		expandSystemPrompt(),
		expandUserPrompt(title, summary, req.Thought.KeyPoints, req.Tags, original, req.Thought.Type, req.Thought.URL),
		0.3,
	)
	if err != nil {
		return ExpandResult{}, err
	}
	plan := strings.TrimSpace(stripMarkdownFence(content))
	if plan == "" {
		return ExpandResult{}, errors.New("expand plan is empty")
	}
	return ExpandResult{Plan: plan, Model: p.chatModel, GeneratedAt: time.Now().UTC()}, nil
}

func expandSystemPrompt() string {
	return "你是 ThoughtFlow 的研究助手。用户提交了一条碎片笔记，请你基于笔记内容产出「处理思路与方案」。" +
		"要求：1. 直接给方案，不要反问、不要「我需要更多信息」。" +
		"2. 必须包含三段并使用 Markdown 二级标题：## 设计思路（分析问题与核心抓手）、## 推荐步骤（用有序列表给 3-6 步可执行动作）、## 参考与延伸（列 2-4 条延伸阅读或相关方向）。" +
		"3. 使用中文，简洁专业；可用列表、引用、加粗；不要输出 ```markdown 围栏，不要复述原文。" +
		"4. 长度 300-500 字；原内容不足时基于常识合理补全，给出清晰的方向与可执行步骤。"
}

func expandUserPrompt(title string, summary string, keyPoints []string, tags []string, original string, thoughtType string, thoughtURL string) string {
	var b strings.Builder
	b.WriteString("标题：")
	b.WriteString(title)
	b.WriteString("\n摘要：")
	b.WriteString(firstNonEmpty(summary, "(无摘要)"))
	b.WriteString("\n关键点：\n")
	if len(keyPoints) == 0 {
		b.WriteString("- (无关键点)\n")
	} else {
		for _, kp := range keyPoints {
			b.WriteString("- ")
			b.WriteString(kp)
			b.WriteString("\n")
		}
	}
	b.WriteString("AI 标签：")
	if len(tags) == 0 {
		b.WriteString("(无)")
	} else {
		b.WriteString(strings.Join(tags, ", "))
	}
	b.WriteString("\n类型：")
	b.WriteString(firstNonEmpty(thoughtType, "text"))
	if thoughtURL != "" {
		b.WriteString("\nURL：")
		b.WriteString(thoughtURL)
	}
	b.WriteString("\n原始内容：\n")
	if strings.TrimSpace(original) == "" {
		b.WriteString("(无原始内容)")
	} else {
		b.WriteString(original)
	}
	b.WriteString("\n\n请输出处理思路与方案。")
	return b.String()
}

// stripMarkdownFence removes an optional ``` ... ``` wrapping that
// some chat models still emit even when prompted not to. It is
// conservative: if the fence markers are not present, the input is
// returned unchanged.
func stripMarkdownFence(value string) string {
	trimmed := strings.TrimSpace(value)
	if !strings.HasPrefix(trimmed, "```") {
		return value
	}
	body := strings.TrimPrefix(trimmed, "```")
	if newline := strings.Index(body, "\n"); newline >= 0 {
		body = body[newline+1:]
	}
	body = strings.TrimRight(body, "`")
	body = strings.TrimRight(body, "\n")
	return strings.TrimSpace(body)
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
		line = line[:240]
	}
	return line
}
