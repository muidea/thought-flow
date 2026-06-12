package biz

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"thoughtflow/internal/pkg/ai"
	"thoughtflow/internal/pkg/jobstore"
	"thoughtflow/internal/pkg/markdown"
	"thoughtflow/internal/pkg/models"
	"thoughtflow/internal/pkg/topicstore"
)

func TestServiceCreateTopicAndMatchThought(t *testing.T) {
	root := t.TempDir()
	ws := &models.Workspace{
		ID:           "local",
		RootPath:     root,
		ThoughtsPath: filepath.Join(root, "thoughts"),
		TopicsPath:   filepath.Join(root, "topics"),
		RuntimePath:  filepath.Join(root, ".thoughtflow"),
		JobsPath:     filepath.Join(root, ".thoughtflow", "jobs"),
	}
	for _, dir := range []string{ws.ThoughtsPath, ws.TopicsPath, ws.JobsPath} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}

	service := NewService(ws, jobstore.New(ws.JobsPath), topicstore.New(root), nil, nil, nil, nil)
	ctx := context.Background()
	topic, err := service.CreateTopic(ctx, models.TopicCreateRequest{
		Name: "Engineering Search",
		Rules: models.TopicRule{
			Keywords: models.KeywordRule{Any: []string{"duckdb"}},
		},
	})
	if err != nil {
		t.Fatalf("CreateTopic() error = %v", err)
	}

	thought := serviceTestThought("20260609-143010-8f3a", "Search architecture")
	content := models.ThoughtContent{Original: "DuckDB indexes should support the first local search baseline."}
	if err := markdown.WriteThought(root, thought, content); err != nil {
		t.Fatalf("WriteThought() error = %v", err)
	}

	memberships, err := service.MatchThought(ctx, thought.ID)
	if err != nil {
		t.Fatalf("MatchThought() error = %v", err)
	}
	if len(memberships) != 1 {
		t.Fatalf("membership count = %d, want 1", len(memberships))
	}
	if memberships[0].TopicID != topic.ID {
		t.Fatalf("membership topic = %q, want %q", memberships[0].TopicID, topic.ID)
	}

	detail, err := service.GetTopic(ctx, topic.ID)
	if err != nil {
		t.Fatalf("GetTopic() error = %v", err)
	}
	if detail.Topic.MemberCount != 1 {
		t.Fatalf("topic member count = %d", detail.Topic.MemberCount)
	}
	if !strings.Contains(detail.Document, "DuckDB indexes should support") {
		t.Fatalf("expected matched content in topic document:\n%s", detail.Document)
	}
	updatedThought, updatedContent, err := markdown.ReadThought(root, thought.ID)
	if err != nil {
		t.Fatalf("ReadThought() error = %v", err)
	}
	if updatedThought.TopicStatus != models.TopicStatusMatched {
		t.Fatalf("topic status = %q", updatedThought.TopicStatus)
	}
	if !strings.Contains(updatedContent.Links, "<!-- topic:engineering-search -->") {
		t.Fatalf("expected topic backlink in thought links:\n%s", updatedContent.Links)
	}
}

func TestServiceMatchThoughtBySemanticRule(t *testing.T) {
	root := t.TempDir()
	ws := &models.Workspace{
		ID:           "local",
		RootPath:     root,
		ThoughtsPath: filepath.Join(root, "thoughts"),
		TopicsPath:   filepath.Join(root, "topics"),
		RuntimePath:  filepath.Join(root, ".thoughtflow"),
		JobsPath:     filepath.Join(root, ".thoughtflow", "jobs"),
	}
	for _, dir := range []string{ws.ThoughtsPath, ws.TopicsPath, ws.JobsPath} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}

	service := NewService(ws, jobstore.New(ws.JobsPath), topicstore.New(root), nil, nil, ai.NewLocalRefineProvider(), nil)
	ctx := context.Background()
	topic, err := service.CreateTopic(ctx, models.TopicCreateRequest{
		Name:        "Semantic Retrieval",
		Description: "vector embeddings semantic retrieval",
		Rules: models.TopicRule{
			Semantic: models.SemanticRule{Enabled: true, Threshold: 0.4},
		},
	})
	if err != nil {
		t.Fatalf("CreateTopic() error = %v", err)
	}

	thought := serviceTestThought("20260609-143010-semantic", "Embedding note")
	content := models.ThoughtContent{Original: "semantic retrieval with vector embeddings for local notes"}
	if err := markdown.WriteThought(root, thought, content); err != nil {
		t.Fatalf("WriteThought() error = %v", err)
	}

	memberships, err := service.MatchThought(ctx, thought.ID)
	if err != nil {
		t.Fatalf("MatchThought() error = %v", err)
	}
	if len(memberships) != 1 {
		t.Fatalf("membership count = %d, want 1", len(memberships))
	}
	if memberships[0].TopicID != topic.ID {
		t.Fatalf("membership topic = %q, want %q", memberships[0].TopicID, topic.ID)
	}
	if memberships[0].MatchType != "semantic" {
		t.Fatalf("match type = %q", memberships[0].MatchType)
	}
	if memberships[0].Score < 0.4 {
		t.Fatalf("semantic score = %v", memberships[0].Score)
	}
	detail, err := service.GetTopic(ctx, topic.ID)
	if err != nil {
		t.Fatalf("GetTopic() error = %v", err)
	}
	if len(detail.Members) != 1 || detail.Members[0].MatchType != "semantic" {
		t.Fatalf("detail members = %#v", detail.Members)
	}
}

func TestServiceSemanticMatchUsesCachedThoughtEmbedding(t *testing.T) {
	root := t.TempDir()
	ws := &models.Workspace{
		ID:           "local",
		RootPath:     root,
		ThoughtsPath: filepath.Join(root, "thoughts"),
		TopicsPath:   filepath.Join(root, "topics"),
		RuntimePath:  filepath.Join(root, ".thoughtflow"),
		JobsPath:     filepath.Join(root, ".thoughtflow", "jobs"),
	}
	for _, dir := range []string{ws.ThoughtsPath, ws.TopicsPath, ws.JobsPath} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}

	cache := &testEmbeddingCache{
		record: models.EmbeddingRecord{
			ThoughtID: "20260609-143010-cached",
			Model:     "test-embedding",
			Dimension: 3,
			Vector:    []float64{1, 0, 0},
		},
	}
	embedder := &countingEmbeddingProvider{
		record: models.EmbeddingRecord{
			Model:     "test-embedding",
			Dimension: 3,
			Vector:    []float64{1, 0, 0},
		},
	}
	service := NewService(ws, jobstore.New(ws.JobsPath), topicstore.New(root), nil, nil, embedder, cache)
	ctx := context.Background()
	topic, err := service.CreateTopic(ctx, models.TopicCreateRequest{
		Name:        "Cached Semantic Retrieval",
		Description: "vector embeddings semantic retrieval",
		Rules: models.TopicRule{
			Semantic: models.SemanticRule{Enabled: true, Threshold: 0.9},
		},
	})
	if err != nil {
		t.Fatalf("CreateTopic() error = %v", err)
	}

	thought := serviceTestThought("20260609-143010-cached", "Cached embedding note")
	content := models.ThoughtContent{Original: "semantic retrieval with cached vector embeddings"}
	if err := markdown.WriteThought(root, thought, content); err != nil {
		t.Fatalf("WriteThought() error = %v", err)
	}

	memberships, err := service.MatchThought(ctx, thought.ID)
	if err != nil {
		t.Fatalf("MatchThought() error = %v", err)
	}
	if len(memberships) != 1 || memberships[0].TopicID != topic.ID {
		t.Fatalf("memberships = %#v", memberships)
	}
	if cache.calls != 1 {
		t.Fatalf("cache calls = %d, want 1", cache.calls)
	}
	if embedder.calls != 1 {
		t.Fatalf("embedder calls = %d, want only topic embedding call", embedder.calls)
	}
}

func TestServiceSemanticMatchUsesCachedSemanticScores(t *testing.T) {
	root := t.TempDir()
	ws := &models.Workspace{
		ID:           "local",
		RootPath:     root,
		ThoughtsPath: filepath.Join(root, "thoughts"),
		TopicsPath:   filepath.Join(root, "topics"),
		RuntimePath:  filepath.Join(root, ".thoughtflow"),
		JobsPath:     filepath.Join(root, ".thoughtflow", "jobs"),
	}
	for _, dir := range []string{ws.ThoughtsPath, ws.TopicsPath, ws.JobsPath} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}

	cache := &testSemanticScoreCache{
		scores: map[string]float64{"20260609-143010-ann": 0.96},
	}
	embedder := &countingEmbeddingProvider{
		record: models.EmbeddingRecord{
			Model:     "test-embedding",
			Dimension: 3,
			Vector:    []float64{1, 0, 0},
		},
	}
	service := NewService(ws, jobstore.New(ws.JobsPath), topicstore.New(root), nil, nil, embedder, cache)
	ctx := context.Background()
	topic, err := service.CreateTopic(ctx, models.TopicCreateRequest{
		Name:        "ANN Semantic Retrieval",
		Description: "vector embeddings semantic retrieval",
		Rules: models.TopicRule{
			Semantic: models.SemanticRule{Enabled: true, Threshold: 0.9},
		},
	})
	if err != nil {
		t.Fatalf("CreateTopic() error = %v", err)
	}

	thought := serviceTestThought("20260609-143010-ann", "ANN embedding note")
	content := models.ThoughtContent{Original: "semantic retrieval with hnsw cached scores"}
	if err := markdown.WriteThought(root, thought, content); err != nil {
		t.Fatalf("WriteThought() error = %v", err)
	}

	memberships, err := service.MatchThought(ctx, thought.ID)
	if err != nil {
		t.Fatalf("MatchThought() error = %v", err)
	}
	if len(memberships) != 1 || memberships[0].TopicID != topic.ID {
		t.Fatalf("memberships = %#v", memberships)
	}
	if cache.semanticCalls != 1 {
		t.Fatalf("semantic score cache calls = %d, want 1", cache.semanticCalls)
	}
	if cache.embeddingCalls != 0 {
		t.Fatalf("embedding cache calls = %d, want 0", cache.embeddingCalls)
	}
	if embedder.calls != 1 {
		t.Fatalf("embedder calls = %d, want only topic embedding call", embedder.calls)
	}
}

func TestServicePreviewAndAcceptWeave(t *testing.T) {
	root := t.TempDir()
	ws := &models.Workspace{
		ID:           "local",
		RootPath:     root,
		ThoughtsPath: filepath.Join(root, "thoughts"),
		TopicsPath:   filepath.Join(root, "topics"),
		RuntimePath:  filepath.Join(root, ".thoughtflow"),
		JobsPath:     filepath.Join(root, ".thoughtflow", "jobs"),
	}
	for _, dir := range []string{ws.ThoughtsPath, ws.TopicsPath, ws.JobsPath} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	service := NewService(ws, jobstore.New(ws.JobsPath), topicstore.New(root), nil, nil, nil, nil)
	ctx := context.Background()
	topic, err := service.CreateTopic(ctx, models.TopicCreateRequest{
		Name: "Manual Weave",
		Rules: models.TopicRule{
			Keywords: models.KeywordRule{Any: []string{"duckdb"}},
		},
	})
	if err != nil {
		t.Fatalf("CreateTopic() error = %v", err)
	}
	thought := serviceTestThought("20260609-143010-weave", "Weave note")
	content := models.ThoughtContent{Original: "DuckDB weave review should show a diff."}
	if err := markdown.WriteThought(root, thought, content); err != nil {
		t.Fatalf("WriteThought() error = %v", err)
	}

	proposal, err := service.PreviewWeave(ctx, topic.ID, thought.ID)
	if err != nil {
		t.Fatalf("PreviewWeave() error = %v", err)
	}
	if proposal.TopicID != topic.ID || proposal.ThoughtID != thought.ID {
		t.Fatalf("proposal ids = %#v", proposal)
	}
	if proposal.ID == "" || proposal.Status != "pending" {
		t.Fatalf("proposal should be persisted as pending, got %#v", proposal)
	}
	if !strings.Contains(proposal.ProposedDocument, proposal.SourceLink) {
		t.Fatalf("proposal missing source link %q:\n%s", proposal.SourceLink, proposal.ProposedDocument)
	}
	if !hasDiffOp(proposal.Diff, "add") {
		t.Fatalf("expected added diff lines, got %#v", proposal.Diff)
	}
	if len(proposal.Patch.Hunks) == 0 || proposal.Patch.BaseHash == "" || proposal.Patch.ProposedHash == "" {
		t.Fatalf("expected structured patch in proposal, got %#v", proposal.Patch)
	}
	proposals, err := service.ListWeaveProposals(ctx, topic.ID)
	if err != nil {
		t.Fatalf("ListWeaveProposals() error = %v", err)
	}
	if len(proposals) != 1 || proposals[0].ID != proposal.ID {
		t.Fatalf("proposals = %#v", proposals)
	}

	confirmed := proposal.ProposedDocument + "\n\nAccepted edit.\n"
	detail, err := service.AcceptWeave(ctx, topic.ID, models.TopicWeaveAcceptRequest{
		ProposalID: proposal.ID,
		Document:   confirmed,
	})
	if err != nil {
		t.Fatalf("AcceptWeave() error = %v", err)
	}
	if detail.Topic.MemberCount != 1 {
		t.Fatalf("member count = %d", detail.Topic.MemberCount)
	}
	if !strings.Contains(detail.Document, "Accepted edit.") {
		t.Fatalf("expected accepted document edit:\n%s", detail.Document)
	}
	if len(detail.Members) != 1 || detail.Members[0].ThoughtID != thought.ID {
		t.Fatalf("detail members = %#v", detail.Members)
	}
	accepted, err := service.GetWeaveProposal(ctx, topic.ID, proposal.ID)
	if err != nil {
		t.Fatalf("GetWeaveProposal() error = %v", err)
	}
	if accepted.Status != "accepted" || accepted.AcceptedAt == nil {
		t.Fatalf("accepted proposal = %#v", accepted)
	}
	if !strings.Contains(accepted.AcceptedDocument, "Accepted edit.") {
		t.Fatalf("accepted document = %q", accepted.AcceptedDocument)
	}
}

func TestServiceAcceptWeaveAppliesStructuredPatch(t *testing.T) {
	root := t.TempDir()
	ws := &models.Workspace{
		ID:           "local",
		RootPath:     root,
		ThoughtsPath: filepath.Join(root, "thoughts"),
		TopicsPath:   filepath.Join(root, "topics"),
		RuntimePath:  filepath.Join(root, ".thoughtflow"),
		JobsPath:     filepath.Join(root, ".thoughtflow", "jobs"),
	}
	for _, dir := range []string{ws.ThoughtsPath, ws.TopicsPath, ws.JobsPath} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	service := NewService(ws, jobstore.New(ws.JobsPath), topicstore.New(root), nil, nil, nil, nil)
	ctx := context.Background()
	topic, err := service.CreateTopic(ctx, models.TopicCreateRequest{
		Name: "Patch Weave",
		Rules: models.TopicRule{
			Keywords: models.KeywordRule{Any: []string{"duckdb"}},
		},
	})
	if err != nil {
		t.Fatalf("CreateTopic() error = %v", err)
	}
	thought := serviceTestThought("20260609-143010-patch", "Patch note")
	content := models.ThoughtContent{Original: "DuckDB patch apply should preserve source links."}
	if err := markdown.WriteThought(root, thought, content); err != nil {
		t.Fatalf("WriteThought() error = %v", err)
	}
	proposal, err := service.PreviewWeave(ctx, topic.ID, thought.ID)
	if err != nil {
		t.Fatalf("PreviewWeave() error = %v", err)
	}

	detail, err := service.AcceptWeave(ctx, topic.ID, models.TopicWeaveAcceptRequest{ProposalID: proposal.ID})
	if err != nil {
		t.Fatalf("AcceptWeave() error = %v", err)
	}
	if detail.Topic.MemberCount != 1 || !strings.Contains(detail.Document, proposal.SourceLink) {
		t.Fatalf("detail = %#v document=\n%s", detail.Topic, detail.Document)
	}
	accepted, err := service.GetWeaveProposal(ctx, topic.ID, proposal.ID)
	if err != nil {
		t.Fatalf("GetWeaveProposal() error = %v", err)
	}
	if accepted.Status != "accepted" || strings.TrimSpace(accepted.AcceptedDocument) != strings.TrimSpace(proposal.ProposedDocument) {
		t.Fatalf("accepted proposal = %#v", accepted)
	}
}

func TestServiceAcceptWeaveRejectsStaleStructuredPatch(t *testing.T) {
	root := t.TempDir()
	ws := &models.Workspace{
		ID:           "local",
		RootPath:     root,
		ThoughtsPath: filepath.Join(root, "thoughts"),
		TopicsPath:   filepath.Join(root, "topics"),
		RuntimePath:  filepath.Join(root, ".thoughtflow"),
		JobsPath:     filepath.Join(root, ".thoughtflow", "jobs"),
	}
	for _, dir := range []string{ws.ThoughtsPath, ws.TopicsPath, ws.JobsPath} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	store := topicstore.New(root)
	service := NewService(ws, jobstore.New(ws.JobsPath), store, nil, nil, nil, nil)
	ctx := context.Background()
	topic, err := service.CreateTopic(ctx, models.TopicCreateRequest{
		Name: "Patch Conflict",
		Rules: models.TopicRule{
			Keywords: models.KeywordRule{Any: []string{"duckdb"}},
		},
	})
	if err != nil {
		t.Fatalf("CreateTopic() error = %v", err)
	}
	thought := serviceTestThought("20260609-143010-stale", "Stale patch note")
	content := models.ThoughtContent{Original: "DuckDB stale patch should fail."}
	if err := markdown.WriteThought(root, thought, content); err != nil {
		t.Fatalf("WriteThought() error = %v", err)
	}
	proposal, err := service.PreviewWeave(ctx, topic.ID, thought.ID)
	if err != nil {
		t.Fatalf("PreviewWeave() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "topics", topic.Slug, "index.md"), []byte(proposal.BaseDocument+"\nConcurrent edit.\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(index.md) error = %v", err)
	}

	_, err = service.AcceptWeave(ctx, topic.ID, models.TopicWeaveAcceptRequest{ProposalID: proposal.ID})
	if err == nil || !strings.Contains(err.Error(), "patch base document mismatch") {
		t.Fatalf("expected stale patch mismatch, got %v", err)
	}
}

type testEmbeddingCache struct {
	record models.EmbeddingRecord
	calls  int
}

func (c *testEmbeddingCache) CachedEmbedding(ctx context.Context, thoughtID string, model string) (models.EmbeddingRecord, bool) {
	_ = ctx
	c.calls++
	if c.record.ThoughtID != thoughtID {
		return models.EmbeddingRecord{}, false
	}
	if model != "" && c.record.Model != "" && model != c.record.Model {
		return models.EmbeddingRecord{}, false
	}
	return c.record, true
}

type testSemanticScoreCache struct {
	scores         map[string]float64
	semanticCalls  int
	embeddingCalls int
}

func (c *testSemanticScoreCache) CachedEmbedding(ctx context.Context, thoughtID string, model string) (models.EmbeddingRecord, bool) {
	_ = ctx
	_ = thoughtID
	_ = model
	c.embeddingCalls++
	return models.EmbeddingRecord{}, false
}

func (c *testSemanticScoreCache) CachedSemanticScores(ctx context.Context, queryVector []float64, model string, limit int) (map[string]float64, string, bool) {
	_ = ctx
	_ = queryVector
	_ = model
	_ = limit
	c.semanticCalls++
	return c.scores, "duckdb_hnsw", true
}

type countingEmbeddingProvider struct {
	mu     sync.Mutex
	record models.EmbeddingRecord
	calls  int
}

func (p *countingEmbeddingProvider) Refine(ctx context.Context, req ai.RefineRequest) (models.ThoughtRefinement, error) {
	_ = ctx
	_ = req
	return models.ThoughtRefinement{}, nil
}

func (p *countingEmbeddingProvider) Embed(ctx context.Context, req ai.EmbedRequest) (models.EmbeddingRecord, error) {
	_ = ctx
	_ = req
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	return p.record, nil
}

func serviceTestThought(id string, title string) models.Thought {
	now := time.Date(2026, 6, 9, 14, 30, 10, 0, time.UTC)
	return models.Thought{
		ID:            id,
		Type:          models.ThoughtTypeText,
		Source:        models.ThoughtSourceManual,
		UserTitle:     title,
		DisplayTitle:  title,
		Path:          filepath.ToSlash(markdown.ThoughtRelativePath(id)),
		CreatedAt:     now,
		UpdatedAt:     now,
		ContentHash:   models.ContentHash(title),
		CaptureStatus: models.CaptureStatusCaptured,
		RefineStatus:  models.RefineStatusRefined,
		IndexStatus:   models.IndexStatusIndexed,
		TopicStatus:   models.TopicStatusUnmatched,
	}
}

func hasDiffOp(lines []models.TopicDocumentDiffLine, op string) bool {
	for _, line := range lines {
		if line.Op == op {
			return true
		}
	}
	return false
}

func TestServiceNearMissTopicsReturnsBelowThreshold(t *testing.T) {
	root := t.TempDir()
	ws := &models.Workspace{
		ID:           "local",
		RootPath:     root,
		ThoughtsPath: filepath.Join(root, "thoughts"),
		TopicsPath:   filepath.Join(root, "topics"),
		RuntimePath:  filepath.Join(root, ".thoughtflow"),
		JobsPath:     filepath.Join(root, ".thoughtflow", "jobs"),
	}
	for _, dir := range []string{ws.ThoughtsPath, ws.TopicsPath, ws.JobsPath} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}

	embedder := &fixedEmbeddingProvider{record: models.EmbeddingRecord{
		Model:     "test-embedding",
		Dimension: 4,
		Vector:    []float64{1, 0, 0, 0},
	}}
	service := NewService(ws, jobstore.New(ws.JobsPath), topicstore.New(root), nil, nil, embedder, nil)
	ctx := context.Background()

	// A high-threshold semantic topic: a thought with 60% similarity
	// (topic vec = [1,0,0,0], thought vec = [0.6,0.8,0,0] normalized
	// yields 0.6) should NOT auto-match but should show up as
	// near-miss.
	highThresholdTopic, err := service.CreateTopic(ctx, models.TopicCreateRequest{
		Name:        "Strict Topic",
		Description: "high bar",
		Rules: models.TopicRule{
			Semantic: models.SemanticRule{Enabled: true, Threshold: 0.75},
		},
	})
	if err != nil {
		t.Fatalf("CreateTopic() error = %v", err)
	}

	// A keyword-only topic that does not match the thought's content
	// at all (no overlap, no semantic enabled).
	keywordTopic, err := service.CreateTopic(ctx, models.TopicCreateRequest{
		Name: "Keyword Only",
		Rules: models.TopicRule{
			Keywords: models.KeywordRule{Any: []string{"definitely-not-in-the-thought"}},
		},
	})
	if err != nil {
		t.Fatalf("CreateTopic() error = %v", err)
	}

	thought := serviceTestThought("20260612-near-miss-1", "Near miss target")
	if err := markdown.WriteThought(root, thought, models.ThoughtContent{Original: "loosely related topic body"}); err != nil {
		t.Fatalf("WriteThought() error = %v", err)
	}

	suggestions, err := service.NearMissTopics(ctx, thought.ID, 3, 0.4)
	if err != nil {
		t.Fatalf("NearMissTopics() error = %v", err)
	}
	if len(suggestions) == 0 {
		t.Fatalf("expected at least one near-miss suggestion")
	}
	hit := false
	for _, suggestion := range suggestions {
		if suggestion.TopicID == highThresholdTopic.ID {
			hit = true
			if suggestion.TopicName != "Strict Topic" {
				t.Fatalf("topic name = %q", suggestion.TopicName)
			}
			if suggestion.Score < 0.4 {
				t.Fatalf("score = %v, want >= 0.4", suggestion.Score)
			}
		}
		if suggestion.TopicID == keywordTopic.ID {
			t.Fatalf("keyword-only topic with no semantic path should not appear in near-miss")
		}
	}
	if !hit {
		t.Fatalf("expected high-threshold topic in near-miss suggestions: %#v", suggestions)
	}
}

func TestServiceNearMissTopicsSortsByScoreDesc(t *testing.T) {
	root := t.TempDir()
	ws := &models.Workspace{
		ID:           "local",
		RootPath:     root,
		ThoughtsPath: filepath.Join(root, "thoughts"),
		TopicsPath:   filepath.Join(root, "topics"),
		RuntimePath:  filepath.Join(root, ".thoughtflow"),
		JobsPath:     filepath.Join(root, ".thoughtflow", "jobs"),
	}
	for _, dir := range []string{ws.ThoughtsPath, ws.TopicsPath, ws.JobsPath} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}

	embedder := &fixedEmbeddingProvider{record: models.EmbeddingRecord{
		Model:     "test-embedding",
		Dimension: 3,
		Vector:    []float64{1, 0, 0},
	}}
	service := NewService(ws, jobstore.New(ws.JobsPath), topicstore.New(root), nil, nil, embedder, nil)
	ctx := context.Background()

	topicA, err := service.CreateTopic(ctx, models.TopicCreateRequest{Name: "A", Rules: models.TopicRule{
		Semantic: models.SemanticRule{Enabled: true, Threshold: 0.95},
	}})
	if err != nil {
		t.Fatalf("CreateTopic A: %v", err)
	}
	topicB, err := service.CreateTopic(ctx, models.TopicCreateRequest{Name: "B", Rules: models.TopicRule{
		Semantic: models.SemanticRule{Enabled: true, Threshold: 0.95},
	}})
	if err != nil {
		t.Fatalf("CreateTopic B: %v", err)
	}

	thought := serviceTestThought("20260612-near-miss-2", "Sort target")
	if err := markdown.WriteThought(root, thought, models.ThoughtContent{Original: "alpha beta gamma"}); err != nil {
		t.Fatalf("WriteThought() error = %v", err)
	}

	suggestions, err := service.NearMissTopics(ctx, thought.ID, 5, 0)
	if err != nil {
		t.Fatalf("NearMissTopics() error = %v", err)
	}
	if len(suggestions) < 2 {
		t.Fatalf("expected 2 suggestions, got %d", len(suggestions))
	}
	for i := 1; i < len(suggestions); i++ {
		if suggestions[i-1].Score < suggestions[i].Score {
			t.Fatalf("suggestions not sorted by score desc: %#v", suggestions)
		}
	}
	first := suggestions[0]
	if first.TopicID != topicA.ID && first.TopicID != topicB.ID {
		t.Fatalf("unexpected first suggestion: %#v", first)
	}
}

// fixedEmbeddingProvider is a deterministic embedding stub. Unlike
// countingEmbeddingProvider, it does not record calls and is shared
// across multiple tests that don't care about call counts.
type fixedEmbeddingProvider struct {
	record models.EmbeddingRecord
}

func (p *fixedEmbeddingProvider) Refine(ctx context.Context, req ai.RefineRequest) (models.ThoughtRefinement, error) {
	_ = ctx
	_ = req
	return models.ThoughtRefinement{}, nil
}

func (p *fixedEmbeddingProvider) Embed(ctx context.Context, req ai.EmbedRequest) (models.EmbeddingRecord, error) {
	_ = ctx
	_ = req
	return p.record, nil
}
