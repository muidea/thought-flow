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
