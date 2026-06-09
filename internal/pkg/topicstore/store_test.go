package topicstore

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"thoughtflow/internal/pkg/markdown"
	"thoughtflow/internal/pkg/models"
)

func TestStoreCreateMatchAndAddMembership(t *testing.T) {
	root := t.TempDir()
	store := New(root)
	ctx := context.Background()

	topic, err := store.Create(ctx, models.TopicCreateRequest{
		Name:        "DuckDB Notes",
		Description: "Local analytical database notes.",
		Rules: models.TopicRule{
			Keywords: models.KeywordRule{Any: []string{"duckdb"}},
			Tags:     models.TagRule{Any: []string{"engineering"}},
		},
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if topic.ID != "duckdb-notes" {
		t.Fatalf("topic id = %q", topic.ID)
	}

	thought := testThought("20260609-143010-8f3a", "Query planner note", []string{"engineering"})
	content := models.ThoughtContent{Original: "DuckDB can keep local thought search fast."}
	if err := markdown.WriteThought(root, thought, content); err != nil {
		t.Fatalf("WriteThought() error = %v", err)
	}

	membership, ok := store.MatchThought(topic, thought, content)
	if !ok {
		t.Fatalf("expected topic rule match")
	}
	if membership.Score <= 0 {
		t.Fatalf("expected positive match score, got %v", membership.Score)
	}
	if !containsString(membership.Reasons, "keyword:duckdb") {
		t.Fatalf("expected keyword reason, got %#v", membership.Reasons)
	}

	updated, changed, err := store.AddMembership(ctx, topic, thought, content, membership)
	if err != nil {
		t.Fatalf("AddMembership() error = %v", err)
	}
	if !changed {
		t.Fatalf("expected membership to be added")
	}
	if updated.MemberCount != 1 {
		t.Fatalf("member count = %d", updated.MemberCount)
	}

	document, err := store.ReadDocument(ctx, updated.ID)
	if err != nil {
		t.Fatalf("ReadDocument() error = %v", err)
	}
	if !strings.Contains(document, "## Query planner note") {
		t.Fatalf("expected thought section in document:\n%s", document)
	}
	if !strings.Contains(document, "Sources: [[../../thoughts/2026/06/20260609-143010-8f3a.md]]") {
		t.Fatalf("expected relative source link in document:\n%s", document)
	}
	if !strings.Contains(document, "members:\n  - 20260609-143010-8f3a") {
		t.Fatalf("expected member snapshot in topic document front matter:\n%s", document)
	}
	membershipPath := filepath.Join(root, "topics", updated.Slug, "memberships", thought.ID+".yaml")
	membershipRaw, err := os.ReadFile(membershipPath)
	if err != nil {
		t.Fatalf("ReadFile(%s) error = %v", membershipPath, err)
	}
	membershipText := string(membershipRaw)
	if !strings.Contains(membershipText, "topic_id: duckdb-notes") ||
		!strings.Contains(membershipText, "thought_id: 20260609-143010-8f3a") ||
		!strings.Contains(membershipText, "status: accepted") ||
		!strings.Contains(membershipText, "keyword:duckdb") {
		t.Fatalf("unexpected membership fact:\n%s", membershipText)
	}
	rewrittenDocument := strings.Replace(document, "Match: keyword:duckdb, tag:engineering", "Match: semantic:0.111", 1)
	if err := store.writeDocument(updated, rewrittenDocument); err != nil {
		t.Fatalf("writeDocument() error = %v", err)
	}
	detail, err := store.Detail(ctx, updated.ID)
	if err != nil {
		t.Fatalf("Detail() error = %v", err)
	}
	if len(detail.Members) != 1 {
		t.Fatalf("detail member count = %d", len(detail.Members))
	}
	if detail.Members[0].MatchType != membership.MatchType || detail.Members[0].Score != membership.Score {
		t.Fatalf("detail membership should come from fact file, got %#v", detail.Members[0])
	}
	if !containsString(detail.Members[0].Reasons, "keyword:duckdb") {
		t.Fatalf("detail reasons should come from fact file, got %#v", detail.Members[0].Reasons)
	}
	updatedThought, updatedContent, err := markdown.ReadThought(root, thought.ID)
	if err != nil {
		t.Fatalf("ReadThought() error = %v", err)
	}
	if updatedThought.TopicStatus != models.TopicStatusMatched {
		t.Fatalf("topic status = %q", updatedThought.TopicStatus)
	}
	if !containsString(updatedThought.TopicIDs, updated.ID) {
		t.Fatalf("expected topic id on thought, got %#v", updatedThought.TopicIDs)
	}
	if !strings.Contains(updatedContent.Links, "<!-- topic:duckdb-notes -->") {
		t.Fatalf("expected topic backlink in thought links:\n%s", updatedContent.Links)
	}

	_, changed, err = store.AddMembership(ctx, updated, thought, content, membership)
	if err != nil {
		t.Fatalf("second AddMembership() error = %v", err)
	}
	if changed {
		t.Fatalf("duplicate membership should not change topic")
	}
}

func TestStoreRebuildRemovesStaleMembershipFacts(t *testing.T) {
	root := t.TempDir()
	store := New(root)
	ctx := context.Background()

	topic, err := store.Create(ctx, models.TopicCreateRequest{
		Name: "DuckDB Notes",
		Rules: models.TopicRule{
			Keywords: models.KeywordRule{Any: []string{"duckdb"}},
		},
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	thought := testThought("20260609-143010-8f3a", "Query planner note", nil)
	content := models.ThoughtContent{Original: "DuckDB can keep local thought search fast."}
	if err := markdown.WriteThought(root, thought, content); err != nil {
		t.Fatalf("WriteThought() error = %v", err)
	}
	membership, ok := store.MatchThought(topic, thought, content)
	if !ok {
		t.Fatalf("expected topic rule match")
	}
	updated, changed, err := store.AddMembership(ctx, topic, thought, content, membership)
	if err != nil {
		t.Fatalf("AddMembership() error = %v", err)
	}
	if !changed {
		t.Fatalf("expected membership to be added")
	}
	membershipPath := filepath.Join(root, "topics", updated.Slug, "memberships", thought.ID+".yaml")
	if _, err := os.Stat(membershipPath); err != nil {
		t.Fatalf("expected membership fact before rebuild, stat error = %v", err)
	}

	rebuilt, count, _, err := store.RebuildWithMatcher(ctx, updated.ID, func(context.Context, models.Topic, models.Thought, models.ThoughtContent) (models.TopicMembership, bool) {
		return models.TopicMembership{}, false
	})
	if err != nil {
		t.Fatalf("RebuildWithMatcher() error = %v", err)
	}
	if count != 0 || rebuilt.MemberCount != 0 || len(rebuilt.Members) != 0 {
		t.Fatalf("unexpected rebuild result: topic=%#v count=%d", rebuilt, count)
	}
	if _, err := os.Stat(membershipPath); !os.IsNotExist(err) {
		t.Fatalf("expected stale membership fact to be removed, stat error = %v", err)
	}
}

func TestStoreAddMembershipUsesWeaveProvider(t *testing.T) {
	root := t.TempDir()
	store := New(root, WithWeaveProvider(fakeWeaver{}))
	ctx := context.Background()

	topic, err := store.Create(ctx, models.TopicCreateRequest{
		Name: "DuckDB Notes",
		Rules: models.TopicRule{
			Keywords: models.KeywordRule{Any: []string{"duckdb"}},
		},
		Outline: []models.OutlineNode{{Title: "Engineering Practice"}},
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	thought := testThought("20260609-143010-8f3a", "Query planner note", nil)
	content := models.ThoughtContent{Original: "DuckDB query planner engineering practice."}
	if err := markdown.WriteThought(root, thought, content); err != nil {
		t.Fatalf("WriteThought() error = %v", err)
	}
	membership, ok := store.MatchThought(topic, thought, content)
	if !ok {
		t.Fatalf("expected topic rule match")
	}

	updated, changed, err := store.AddMembership(ctx, topic, thought, content, membership)
	if err != nil {
		t.Fatalf("AddMembership() error = %v", err)
	}
	if !changed {
		t.Fatalf("expected membership change")
	}
	document, err := store.ReadDocument(ctx, updated.ID)
	if err != nil {
		t.Fatalf("ReadDocument() error = %v", err)
	}
	if !strings.Contains(document, "provider-woven") {
		t.Fatalf("expected provider-generated document:\n%s", document)
	}
	if strings.Contains(document, "## Query planner note") {
		t.Fatalf("fallback append should not be used when provider succeeds:\n%s", document)
	}
}

func TestStorePreviewAndApplyMembershipDocument(t *testing.T) {
	root := t.TempDir()
	store := New(root)
	ctx := context.Background()

	topic, err := store.Create(ctx, models.TopicCreateRequest{
		Name: "DuckDB Notes",
		Rules: models.TopicRule{
			Keywords: models.KeywordRule{Any: []string{"duckdb"}},
		},
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	thought := testThought("20260609-143010-8f3a", "Query planner note", nil)
	content := models.ThoughtContent{Original: "DuckDB query planner notes."}
	if err := markdown.WriteThought(root, thought, content); err != nil {
		t.Fatalf("WriteThought() error = %v", err)
	}
	membership, ok := store.MatchThought(topic, thought, content)
	if !ok {
		t.Fatalf("expected topic rule match")
	}

	baseDocument, proposedDocument, sourceLink, err := store.PreviewMembership(ctx, topic, thought, content, membership)
	if err != nil {
		t.Fatalf("PreviewMembership() error = %v", err)
	}
	if strings.Contains(baseDocument, sourceLink) {
		t.Fatalf("base document should not contain source link before apply:\n%s", baseDocument)
	}
	if !strings.Contains(proposedDocument, sourceLink) {
		t.Fatalf("proposed document missing source link %q:\n%s", sourceLink, proposedDocument)
	}
	proposal, err := store.SaveWeaveProposal(ctx, topic, models.TopicWeaveProposal{
		ID:               "proposal-test",
		TopicID:          topic.ID,
		ThoughtID:        thought.ID,
		SourceLink:       sourceLink,
		Membership:       membership,
		BaseDocument:     baseDocument,
		ProposedDocument: proposedDocument,
		Diff:             []models.TopicDocumentDiffLine{{Op: "add", Text: "added"}},
	})
	if err != nil {
		t.Fatalf("SaveWeaveProposal() error = %v", err)
	}
	if proposal.Status != "pending" || proposal.CreatedAt.IsZero() || proposal.UpdatedAt.IsZero() {
		t.Fatalf("unexpected saved proposal = %#v", proposal)
	}
	proposalPath := filepath.Join(root, "topics", topic.Slug, "approvals", "proposal-test.yaml")
	proposalRaw, err := os.ReadFile(proposalPath)
	if err != nil {
		t.Fatalf("ReadFile(%s) error = %v", proposalPath, err)
	}
	if !strings.Contains(string(proposalRaw), "status: pending") || !strings.Contains(string(proposalRaw), "thought_id: 20260609-143010-8f3a") {
		t.Fatalf("unexpected proposal YAML:\n%s", string(proposalRaw))
	}
	proposals, err := store.ListWeaveProposals(ctx, topic.ID)
	if err != nil {
		t.Fatalf("ListWeaveProposals() error = %v", err)
	}
	if len(proposals) != 1 || proposals[0].ID != proposal.ID {
		t.Fatalf("proposals = %#v", proposals)
	}
	confirmedDocument := proposedDocument + "\n\nConfirmed edit.\n"
	updated, changed, err := store.ApplyMembershipDocument(ctx, topic, thought, content, membership, confirmedDocument)
	if err != nil {
		t.Fatalf("ApplyMembershipDocument() error = %v", err)
	}
	if !changed {
		t.Fatalf("expected apply to change topic")
	}
	document, err := store.ReadDocument(ctx, updated.ID)
	if err != nil {
		t.Fatalf("ReadDocument() error = %v", err)
	}
	if !strings.Contains(document, "Confirmed edit.") {
		t.Fatalf("expected confirmed document to be written:\n%s", document)
	}
	if !strings.Contains(document, "members:\n  - 20260609-143010-8f3a") {
		t.Fatalf("expected member snapshot in confirmed document:\n%s", document)
	}
	updatedThought, updatedContent, err := markdown.ReadThought(root, thought.ID)
	if err != nil {
		t.Fatalf("ReadThought() error = %v", err)
	}
	if updatedThought.TopicStatus != models.TopicStatusMatched || !containsString(updatedThought.TopicIDs, topic.ID) {
		t.Fatalf("thought topic state = %#v", updatedThought)
	}
	if !strings.Contains(updatedContent.Links, "<!-- topic:duckdb-notes -->") {
		t.Fatalf("expected topic backlink:\n%s", updatedContent.Links)
	}
	accepted, err := store.MarkWeaveProposalAccepted(ctx, updated, proposal.ID, confirmedDocument)
	if err != nil {
		t.Fatalf("MarkWeaveProposalAccepted() error = %v", err)
	}
	if accepted.Status != "accepted" || accepted.AcceptedAt == nil {
		t.Fatalf("accepted proposal = %#v", accepted)
	}
	if !strings.Contains(accepted.AcceptedDocument, "Confirmed edit.") {
		t.Fatalf("accepted document = %q", accepted.AcceptedDocument)
	}
}

func TestStoreMatchHonorsManualExclude(t *testing.T) {
	store := New(t.TempDir())
	thought := testThought("20260609-143010-8f3a", "Excluded", nil)
	content := models.ThoughtContent{Original: "DuckDB appears in this text."}
	topic := models.Topic{
		ID:   "duckdb-notes",
		Name: "DuckDB Notes",
		Rules: models.TopicRule{
			Keywords:      models.KeywordRule{Any: []string{"duckdb"}},
			ManualExclude: []string{thought.ID},
		},
	}

	if _, ok := store.MatchThought(topic, thought, content); ok {
		t.Fatalf("expected manual exclude to suppress rule match")
	}
}

func testThought(id string, title string, tags []string) models.Thought {
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
		UserTags:      tags,
		CaptureStatus: models.CaptureStatusCaptured,
		RefineStatus:  models.RefineStatusRefined,
		IndexStatus:   models.IndexStatusIndexed,
		TopicStatus:   models.TopicStatusUnmatched,
	}
}

func containsString(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

type fakeWeaver struct{}

func (fakeWeaver) Weave(ctx context.Context, req models.TopicWeaveRequest) (models.TopicWeaveResult, error) {
	_ = ctx
	return models.TopicWeaveResult{
		Document: req.CurrentDocument + "\n\n<!-- provider-woven -->\n> Sources: [[" + req.SourceLink + "]]\n",
		Model:    "fake",
		Strategy: "test",
	}, nil
}
