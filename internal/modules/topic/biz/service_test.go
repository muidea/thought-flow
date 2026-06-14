package biz

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/muidea/magicCommon/event"
	"github.com/muidea/magicCommon/task"

	"thoughtflow/internal/pkg/ai"
	"thoughtflow/internal/pkg/jobstore"
	"thoughtflow/internal/pkg/markdown"
	"thoughtflow/internal/pkg/models"
	"thoughtflow/internal/pkg/scratchpad"
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

	service := NewService(ws, jobstore.New(ws.JobsPath), topicstore.New(root), nil, nil, nil, nil, nil)
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

	service := NewService(ws, jobstore.New(ws.JobsPath), topicstore.New(root), nil, nil, ai.NewLocalRefineProvider(), nil, nil)
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
	service := NewService(ws, jobstore.New(ws.JobsPath), topicstore.New(root), nil, nil, embedder, cache, nil)
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
	service := NewService(ws, jobstore.New(ws.JobsPath), topicstore.New(root), nil, nil, embedder, cache, nil)
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
	service := NewService(ws, jobstore.New(ws.JobsPath), topicstore.New(root), nil, nil, nil, nil, nil)
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
	service := NewService(ws, jobstore.New(ws.JobsPath), topicstore.New(root), nil, nil, nil, nil, nil)
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
	service := NewService(ws, jobstore.New(ws.JobsPath), store, nil, nil, nil, nil, nil)
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
	service := NewService(ws, jobstore.New(ws.JobsPath), topicstore.New(root), nil, nil, embedder, nil, nil)
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
	service := NewService(ws, jobstore.New(ws.JobsPath), topicstore.New(root), nil, nil, embedder, nil, nil)
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

// stubScratchpadProvider is a minimal in-memory ScratchpadProvider
// for the candidate-matching tests. It deliberately implements the
// interface (not the full scratchpad.Store) so the topic module is
// tested against the contract, not a leaky abstraction.
type stubScratchpadProvider struct {
	sessions map[string]scratchpad.Scratchpad
}

func newStubScratchpadProvider() *stubScratchpadProvider {
	return &stubScratchpadProvider{sessions: map[string]scratchpad.Scratchpad{}}
}

func (s *stubScratchpadProvider) put(sp scratchpad.Scratchpad) {
	s.sessions[sp.SessionID] = sp
}

func (s *stubScratchpadProvider) List() []scratchpad.Summary {
	out := []scratchpad.Summary{}
	for _, sp := range s.sessions {
		out = append(out, scratchpad.Summary{
			SessionID:          sp.SessionID,
			Title:              sp.Title,
			CommittedThoughtID: sp.CommittedThoughtID,
			UpdatedAt:          sp.UpdatedAt,
		})
	}
	return out
}

func (s *stubScratchpadProvider) Get(sessionID string) (scratchpad.Scratchpad, error) {
	sp, ok := s.sessions[sessionID]
	if !ok {
		return scratchpad.Scratchpad{}, errors.New("scratchpad not found: " + sessionID)
	}
	return sp, nil
}

func TestServiceMatchScratchpadAsyncWritesCandidates(t *testing.T) {
	root := t.TempDir()
	ws := topicTestWorkspace(root)
	store := topicstore.New(root)
	scratchpads := newStubScratchpadProvider()
	background := &captureBackground{}
	service := NewService(ws, jobstore.New(ws.JobsPath), store, nil, background, nil, nil, scratchpads)
	ctx := context.Background()

	topic, err := service.CreateTopic(ctx, models.TopicCreateRequest{
		Name:  "Engineering Search",
		Rules: models.TopicRule{Keywords: models.KeywordRule{Any: []string{"duckdb"}}},
	})
	if err != nil {
		t.Fatalf("CreateTopic() error = %v", err)
	}

	scratchpads.put(scratchpad.Scratchpad{
		SessionID: "sp-1",
		Title:     "DuckDB index design",
		SessionContext: scratchpad.SessionContext{
			CandidateTitle:   "DuckDB index design",
			CandidateSummary: "Notes on the local DuckDB index layer",
			CandidateTags:    []string{"search", "engine"},
		},
		UpdatedAt: time.Now().UTC(),
	})

	// Drive the synchronous matcher directly so the test does not
	// race the async job-queue cleanup. The async job-creation
	// contract is exercised by TestServiceNotifyDispatchesScratchpad
	// ContextUpdated.
	count, err := service.matchScratchpad(ctx, "sp-1")
	if err != nil {
		t.Fatalf("matchScratchpad() error = %v", err)
	}
	if count != 1 {
		t.Fatalf("matchScratchpad() count = %d, want 1", count)
	}

	candidates, err := service.ListSessionCandidates(ctx, topic.ID)
	if err != nil {
		t.Fatalf("ListSessionCandidates() error = %v", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("candidates len = %d, want 1", len(candidates))
	}
	if candidates[0].SessionID != "sp-1" {
		t.Fatalf("candidate session = %q", candidates[0].SessionID)
	}
	if candidates[0].MatchType != "keyword" {
		t.Fatalf("candidate match_type = %q, want keyword", candidates[0].MatchType)
	}
}

func TestServiceMatchScratchpadAsyncJobMetadata(t *testing.T) {
	root := t.TempDir()
	ws := topicTestWorkspace(root)
	store := topicstore.New(root)
	scratchpads := newStubScratchpadProvider()
	background := &captureBackground{}
	service := NewService(ws, jobstore.New(ws.JobsPath), store, nil, background, nil, nil, scratchpads)

	scratchpads.put(scratchpad.Scratchpad{
		SessionID: "sp-job",
		Title:     "title",
		UpdatedAt: time.Now().UTC(),
	})

	job, err := service.MatchScratchpadAsync("sp-job")
	if err != nil {
		t.Fatalf("MatchScratchpadAsync() error = %v", err)
	}
	if job.Type != models.JobTypeTopicMatch {
		t.Fatalf("job type = %q, want %q", job.Type, models.JobTypeTopicMatch)
	}
	if job.ResourceType != models.ResourceTypeSession {
		t.Fatalf("job resource_type = %q, want %q", job.ResourceType, models.ResourceTypeSession)
	}
	if job.ResourceID != "sp-job" {
		t.Fatalf("job resource_id = %q, want sp-job", job.ResourceID)
	}
	// Let the dispatched goroutine finish writing before TempDir
	// cleanup runs. The matcher is fast (one topic, no LLM) so a
	// short sleep is sufficient; without it the cleanup race
	// reports "directory not empty" as a test failure.
	time.Sleep(50 * time.Millisecond)
}

func TestServiceMatchScratchpadUsesTagHintPriority(t *testing.T) {
	root := t.TempDir()
	ws := topicTestWorkspace(root)
	store := topicstore.New(root)
	scratchpads := newStubScratchpadProvider()
	service := NewService(ws, jobstore.New(ws.JobsPath), store, nil, nil, nil, nil, scratchpads)
	ctx := context.Background()

	topic, err := service.CreateTopic(ctx, models.TopicCreateRequest{
		Name:  "AI safety",
		Rules: models.TopicRule{Keywords: models.KeywordRule{Any: []string{"alignment"}}},
	})
	if err != nil {
		t.Fatalf("CreateTopic() error = %v", err)
	}

	scratchpads.put(scratchpad.Scratchpad{
		SessionID: "sp-2",
		Title:     "Alignment research",
		SessionContext: scratchpad.SessionContext{
			SuggestedTopicIDs: []string{topic.ID, "other-topic"},
			CandidateBody:     "Some content with the word alignment in it",
		},
		UpdatedAt: time.Now().UTC(),
	})

	if _, err := service.matchScratchpad(ctx, "sp-2"); err != nil {
		t.Fatalf("matchScratchpad() error = %v", err)
	}
	candidates, err := service.ListSessionCandidates(ctx, topic.ID)
	if err != nil {
		t.Fatalf("ListSessionCandidates() error = %v", err)
	}
	if len(candidates) != 1 || candidates[0].MatchType != "tag_hint" {
		t.Fatalf("expected tag_hint match, got %+v", candidates)
	}
}

func TestServiceMatchScratchpadPrunesStaleOnContextChange(t *testing.T) {
	root := t.TempDir()
	ws := topicTestWorkspace(root)
	store := topicstore.New(root)
	scratchpads := newStubScratchpadProvider()
	service := NewService(ws, jobstore.New(ws.JobsPath), store, nil, nil, nil, nil, scratchpads)
	ctx := context.Background()

	topic, err := service.CreateTopic(ctx, models.TopicCreateRequest{
		Name:  "Engineering Search",
		Rules: models.TopicRule{Keywords: models.KeywordRule{Any: []string{"duckdb"}}},
	})
	if err != nil {
		t.Fatalf("CreateTopic() error = %v", err)
	}

	sp := scratchpad.Scratchpad{
		SessionID: "sp-3",
		Title:     "Notes",
		SessionContext: scratchpad.SessionContext{
			CandidateTitle:   "Notes",
			CandidateSummary: "about the duckdb index layer",
		},
		UpdatedAt: time.Now().UTC(),
	}
	scratchpads.put(sp)
	if _, err := service.matchScratchpad(ctx, "sp-3"); err != nil {
		t.Fatalf("matchScratchpad() error = %v", err)
	}
	candidates, err := service.ListSessionCandidates(ctx, topic.ID)
	if err != nil {
		t.Fatalf("ListSessionCandidates() after add error = %v", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("after add: candidates len = %d, want 1", len(candidates))
	}

	// Update the session so it no longer matches; the matcher
	// should prune the candidate from the topic.
	sp.SessionContext.CandidateTitle = "Something"
	sp.SessionContext.CandidateSummary = "completely different"
	sp.Title = "Something"
	scratchpads.put(sp)
	if _, err := service.matchScratchpad(ctx, "sp-3"); err != nil {
		t.Fatalf("matchScratchpad() error = %v", err)
	}
	candidates, err = service.ListSessionCandidates(ctx, topic.ID)
	if err != nil {
		t.Fatalf("ListSessionCandidates() after prune error = %v", err)
	}
	if len(candidates) != 0 {
		t.Fatalf("after prune: candidates len = %d, want 0", len(candidates))
	}
}

func TestServiceMatchScratchpadPurgesOnCommit(t *testing.T) {
	root := t.TempDir()
	ws := topicTestWorkspace(root)
	store := topicstore.New(root)
	scratchpads := newStubScratchpadProvider()
	service := NewService(ws, jobstore.New(ws.JobsPath), store, nil, nil, nil, nil, scratchpads)
	ctx := context.Background()

	topic, err := service.CreateTopic(ctx, models.TopicCreateRequest{
		Name:  "Engineering Search",
		Rules: models.TopicRule{Keywords: models.KeywordRule{Any: []string{"duckdb"}}},
	})
	if err != nil {
		t.Fatalf("CreateTopic() error = %v", err)
	}

	scratchpads.put(scratchpad.Scratchpad{
		SessionID: "sp-4",
		Title:     "DuckDB",
		SessionContext: scratchpad.SessionContext{
			CandidateSummary: "duckdb index design notes",
		},
		UpdatedAt: time.Now().UTC(),
	})
	if _, err := service.matchScratchpad(ctx, "sp-4"); err != nil {
		t.Fatalf("matchScratchpad() error = %v", err)
	}
	if candidates, _ := service.ListSessionCandidates(ctx, topic.ID); len(candidates) != 1 {
		t.Fatalf("setup: expected 1 candidate, got %d", len(candidates))
	}

	// Simulate commit by setting CommittedThoughtID in the stub.
	committed := scratchpads.sessions["sp-4"]
	committed.CommittedThoughtID = "thought-1"
	scratchpads.put(committed)
	if _, err := service.matchScratchpad(ctx, "sp-4"); err != nil {
		t.Fatalf("matchScratchpad(committed) error = %v", err)
	}
	candidates, err := service.ListSessionCandidates(ctx, topic.ID)
	if err != nil {
		t.Fatalf("ListSessionCandidates() error = %v", err)
	}
	if len(candidates) != 0 {
		t.Fatalf("after commit: candidates len = %d, want 0", len(candidates))
	}
}

func TestServiceNotifyDispatchesScratchpadContextUpdated(t *testing.T) {
	root := t.TempDir()
	ws := topicTestWorkspace(root)
	store := topicstore.New(root)
	scratchpads := newStubScratchpadProvider()
	background := &captureBackground{}
	service := NewService(ws, jobstore.New(ws.JobsPath), store, nil, background, nil, nil, scratchpads)
	ctx := context.Background()

	if _, err := service.CreateTopic(ctx, models.TopicCreateRequest{
		Name:  "Engineering Search",
		Rules: models.TopicRule{Keywords: models.KeywordRule{Any: []string{"duckdb"}}},
	}); err != nil {
		t.Fatalf("CreateTopic() error = %v", err)
	}

	scratchpads.put(scratchpad.Scratchpad{
		SessionID: "sp-5",
		Title:     "duckdb",
		SessionContext: scratchpad.SessionContext{
			CandidateSummary: "duckdb index design",
		},
		UpdatedAt: time.Now().UTC(),
	})

	ev := event.NewEvent(models.EventScratchpadContextUpdated, "capture", "#", event.NewHeader(), models.DomainEvent{
		EventType:    models.EventScratchpadContextUpdated,
		ResourceType: models.ResourceTypeSession,
		ResourceID:   "sp-5",
		Payload:      map[string]any{"session_id": "sp-5"},
	})
	result := event.NewResult(models.EventScratchpadContextUpdated, "capture", "#")
	// Notify should queue the match through BackgroundRoutine; run
	// it synchronously so TempDir cleanup cannot race with the
	// candidate file write.
	service.Notify(ev, result)
	if background.last == nil {
		t.Fatalf("Notify did not queue scratchpad match")
	}
	background.last()
	candidates, err := service.ListSessionCandidates(ctx, "engineering-search")
	if err != nil {
		t.Fatalf("ListSessionCandidates() error = %v", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("candidate count = %d, want 1", len(candidates))
	}
}

// captureBackground records the most recent async function passed
// to it so tests can assert that an event handler queued a job.
type captureBackground struct {
	last func()
}

func (c *captureBackground) AsyncFunction(fn func()) error {
	c.last = fn
	return nil
}

func (c *captureBackground) AsyncTask(_ task.Task) error {
	return nil
}

func (c *captureBackground) SyncTask(_ task.Task) error { return nil }

func (c *captureBackground) SyncTaskWithTimeOut(_ task.Task, _ time.Duration) error { return nil }

func (c *captureBackground) SyncFunction(_ func()) error { return nil }

func (c *captureBackground) SyncFunctionWithTimeOut(_ func(), _ time.Duration) error {
	return nil
}

func (c *captureBackground) Timer(_ context.Context, _ task.Task, _ time.Duration, _ time.Duration) error {
	return nil
}

func (c *captureBackground) Shutdown(_ context.Context) bool { return true }

var _ task.BackgroundRoutine = (*captureBackground)(nil)

type stubComposeDraftProvider struct {
	drafts []models.ComposeDraft
	err    error
}

func (p stubComposeDraftProvider) ListDrafts(_ context.Context) ([]models.ComposeDraft, error) {
	if p.err != nil {
		return nil, p.err
	}
	return append([]models.ComposeDraft(nil), p.drafts...), nil
}

func topicTestWorkspace(root string) *models.Workspace {
	return &models.Workspace{
		ID:           "local",
		RootPath:     root,
		ThoughtsPath: filepath.Join(root, "thoughts"),
		TopicsPath:   filepath.Join(root, "topics"),
		RuntimePath:  filepath.Join(root, ".thoughtflow"),
		JobsPath:     filepath.Join(root, ".thoughtflow", "jobs"),
	}
}

func TestServiceListCandidatesFusesSourcesAndSortsByScore(t *testing.T) {
	root := t.TempDir()
	ws := topicTestWorkspace(root)
	store := topicstore.New(root)
	scratchpads := newStubScratchpadProvider()
	service := NewService(ws, jobstore.New(ws.JobsPath), store, nil, nil, nil, nil, scratchpads)
	ctx := context.Background()

	topic, err := service.CreateTopic(ctx, models.TopicCreateRequest{
		Name: "Vector Notes",
		Rules: models.TopicRule{
			Keywords: models.KeywordRule{Any: []string{"vector"}},
		},
	})
	if err != nil {
		t.Fatalf("CreateTopic() error = %v", err)
	}

	// Seed a scratchpad session, then run the synchronous matcher so
	// the topic picks up a candidate entry we can render through
	// the new TopicCandidateImpact DTO. Goal contains the topic's
	// keyword so the matcher scores it as a keyword hit.
	scratchpads.put(scratchpad.Scratchpad{
		SessionID: "sp-cand",
		Title:     "Vector scratchpad",
		UpdatedAt: time.Now().UTC(),
		SessionContext: scratchpad.SessionContext{
			Goal: "Investigate vector search scaling patterns.",
		},
	})
	if _, err := service.matchScratchpad(ctx, "sp-cand"); err != nil {
		t.Fatalf("matchScratchpad() error = %v", err)
	}

	// And seed two thoughts that should appear as thought impacts.
	thoughtA := models.Thought{
		ID:        "20260101-100000-aaaa",
		UserTitle: "Vector search primer",
		Path:      "thoughts/2026/01/20260101-100000-aaaa.md",
	}
	thoughtB := models.Thought{
		ID:        "20260101-100000-bbbb",
		UserTitle: "Vector scratchpad follow-up",
		Path:      "thoughts/2026/01/20260101-100000-bbbb.md",
	}
	// AddMembership is on the store, not the service: it expects the
	// caller to feed the freshly-returned topic back in, otherwise
	// the second call overwrites the first because it started from
	// the still-empty Members list we got from CreateTopic.
	current := topic
	for _, thought := range []models.Thought{thoughtA, thoughtB} {
		updated, _, err := service.store.AddMembership(ctx, current, thought, models.ThoughtContent{}, models.TopicMembership{Score: 0.9, MatchType: "keyword"})
		if err != nil {
			t.Fatalf("AddMembership() error = %v", err)
		}
		current = updated
	}
	service.SetComposeDraftProvider(stubComposeDraftProvider{drafts: []models.ComposeDraft{
		{
			ID:     "draft-vector",
			Goal:   "Vector notes synthesis",
			Status: models.ComposeStatusDraft,
			Sources: []models.ComposeSource{{
				SourceType: models.ComposeSourceTypeTopicSection,
				SourceID:   topic.ID,
				Title:      topic.Name,
				SourceLink: "topics/vector-notes/index.md#context",
			}},
			UpdatedAt: time.Now().UTC().Add(time.Second),
		},
	}})

	impacts, err := service.ListCandidates(ctx, topic.ID)
	if err != nil {
		t.Fatalf("ListCandidates() error = %v", err)
	}

	sourceCount := map[models.TopicCandidateImpactSource]int{}
	for _, impact := range impacts {
		sourceCount[impact.Source]++
	}
	if sourceCount[models.TopicCandidateSourceCaptureSession] != 1 {
		t.Fatalf("capture_session count = %d, want 1", sourceCount[models.TopicCandidateSourceCaptureSession])
	}
	if sourceCount[models.TopicCandidateSourceThoughtReopen] != 1 {
		t.Fatalf("thought_reopen_session count = %d, want 1", sourceCount[models.TopicCandidateSourceThoughtReopen])
	}
	if sourceCount[models.TopicCandidateSourceThought] != 2 {
		t.Fatalf("thought count = %d, want 2", sourceCount[models.TopicCandidateSourceThought])
	}
	if sourceCount[models.TopicCandidateSourceComposeDraft] != 1 {
		t.Fatalf("compose_draft count = %d, want 1", sourceCount[models.TopicCandidateSourceComposeDraft])
	}

	// capture_session and thought_reopen_session come from the same
	// session data; their IDs must match.
	var capture, reopen, draft models.TopicCandidateImpact
	for _, impact := range impacts {
		switch impact.Source {
		case models.TopicCandidateSourceCaptureSession:
			capture = impact
		case models.TopicCandidateSourceThoughtReopen:
			reopen = impact
		case models.TopicCandidateSourceComposeDraft:
			draft = impact
		}
	}
	if capture.SessionID != "sp-cand" || reopen.SessionID != "sp-cand" {
		t.Fatalf("session IDs = (%q, %q), want (sp-cand, sp-cand)", capture.SessionID, reopen.SessionID)
	}
	if draft.DraftID != "draft-vector" || draft.Status != models.ComposeStatusDraft {
		t.Fatalf("compose draft impact = %+v", draft)
	}

	// Highest score first.
	if impacts[0].Score < impacts[len(impacts)-1].Score {
		t.Fatalf("impacts not sorted by score desc: %+v", impacts)
	}
}

func TestServiceNotifyComposeDraftChangeRefreshesTopics(t *testing.T) {
	root := t.TempDir()
	ws := topicTestWorkspace(root)
	background := &captureBackground{}
	service := NewService(ws, jobstore.New(ws.JobsPath), topicstore.New(root), nil, background, nil, nil, nil)
	ctx := context.Background()

	topic, err := service.CreateTopic(ctx, models.TopicCreateRequest{Name: "Draft Topic"})
	if err != nil {
		t.Fatalf("CreateTopic() error = %v", err)
	}

	ev := event.NewEvent("compose.draft_created", "compose", "#", event.NewHeader(), models.DomainEvent{
		EventType:    "compose.draft_created",
		ResourceType: models.ResourceTypeWorkspace,
		ResourceID:   ws.ID,
	})
	result := event.NewResult("compose.draft_created", "compose", "#")
	service.Notify(ev, result)
	if background.last == nil {
		t.Fatalf("Notify did not queue topic refresh")
	}
	background.last()

	jobs, err := service.jobs.RecentByResource(topic.ID, 1)
	if err != nil {
		t.Fatalf("RecentByResource() error = %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("jobs len = %d, want 1", len(jobs))
	}
	job := jobs[0]
	if job.Type != models.JobTypeTopicWeave {
		t.Fatalf("job type = %q, want %q", job.Type, models.JobTypeTopicWeave)
	}
}

func TestServiceListCandidatesRequiresTopicID(t *testing.T) {
	root := t.TempDir()
	ws := topicTestWorkspace(root)
	store := topicstore.New(root)
	service := NewService(ws, jobstore.New(ws.JobsPath), store, nil, nil, nil, nil, newStubScratchpadProvider())

	_, err := service.ListCandidates(context.Background(), "")
	if err == nil || !strings.Contains(err.Error(), "topic id is required") {
		t.Fatalf("err = %v, want required", err)
	}
}

func TestServiceListCandidatesPropagatesStoreError(t *testing.T) {
	root := t.TempDir()
	ws := topicTestWorkspace(root)
	store := topicstore.New(root)
	service := NewService(ws, jobstore.New(ws.JobsPath), store, nil, nil, nil, nil, newStubScratchpadProvider())

	_, err := service.ListCandidates(context.Background(), "missing-topic-id")
	if err == nil {
		t.Fatalf("expected error for missing topic")
	}
}
