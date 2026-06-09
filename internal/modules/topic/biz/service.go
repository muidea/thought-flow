package biz

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/muidea/magicCommon/event"
	"github.com/muidea/magicCommon/task"

	"thoughtflow/internal/pkg/ai"
	"thoughtflow/internal/pkg/eventutil"
	"thoughtflow/internal/pkg/jobstore"
	"thoughtflow/internal/pkg/markdown"
	"thoughtflow/internal/pkg/models"
	"thoughtflow/internal/pkg/topicstore"
)

type Service struct {
	workspace  *models.Workspace
	jobs       *jobstore.Store
	store      *topicstore.Store
	eventHub   event.Hub
	background task.BackgroundRoutine
	embedder   ai.EmbeddingProvider
	cache      EmbeddingCache
}

type EmbeddingCache interface {
	CachedEmbedding(ctx context.Context, thoughtID string, model string) (models.EmbeddingRecord, bool)
}

func NewService(workspace *models.Workspace, jobs *jobstore.Store, store *topicstore.Store, eventHub event.Hub, background task.BackgroundRoutine, embedder ai.EmbeddingProvider, cache EmbeddingCache) *Service {
	return &Service{
		workspace:  workspace,
		jobs:       jobs,
		store:      store,
		eventHub:   eventHub,
		background: background,
		embedder:   embedder,
		cache:      cache,
	}
}

func (s *Service) ID() string {
	return "topic.thought-observer"
}

func (s *Service) Notify(ev event.Event, result event.Result) {
	domainEvent, ok := ev.Data().(models.DomainEvent)
	if ok && domainEvent.ResourceType == models.ResourceTypeThought && domainEvent.ResourceID != "" {
		_, _ = s.MatchThoughtAsync(domainEvent.ResourceID)
	}
	if result != nil {
		result.Set(nil, nil)
	}
}

func (s *Service) CreateTopic(ctx context.Context, req models.TopicCreateRequest) (models.Topic, error) {
	topic, err := s.store.Create(ctx, req)
	if err != nil {
		return models.Topic{}, err
	}
	eventutil.Post(s.eventHub, topicEvent(models.EventTopicCreated, s.workspace.ID, models.ResourceTypeTopic, topic.ID, topic))
	eventutil.Post(s.eventHub, gitTopicEvent(s.workspace.ID, topic, "topic_update"))
	return topic, nil
}

func (s *Service) UpdateTopic(ctx context.Context, id string, req models.TopicUpdateRequest) (models.Topic, error) {
	topic, err := s.store.Update(ctx, id, req)
	if err != nil {
		return models.Topic{}, err
	}
	eventutil.Post(s.eventHub, topicEvent(models.EventTopicUpdated, s.workspace.ID, models.ResourceTypeTopic, topic.ID, topic))
	eventutil.Post(s.eventHub, gitTopicEvent(s.workspace.ID, topic, "topic_update"))
	return topic, nil
}

func (s *Service) ListTopics(ctx context.Context) ([]models.Topic, error) {
	return s.store.List(ctx)
}

func (s *Service) GetTopic(ctx context.Context, id string) (models.TopicDetail, error) {
	return s.store.Detail(ctx, id)
}

func (s *Service) PreviewWeave(ctx context.Context, topicID string, thoughtID string) (models.TopicWeaveProposal, error) {
	topic, thought, content, membership, err := s.weaveInput(ctx, topicID, thoughtID)
	if err != nil {
		return models.TopicWeaveProposal{}, err
	}
	baseDocument, proposedDocument, sourceLink, err := s.store.PreviewMembership(ctx, topic, thought, content, membership)
	if err != nil {
		return models.TopicWeaveProposal{}, err
	}
	now := time.Now().UTC()
	proposal, err := s.store.SaveWeaveProposal(ctx, topic, models.TopicWeaveProposal{
		ID:               models.NewJobID("topic-weave-proposal", now),
		TopicID:          topic.ID,
		ThoughtID:        thought.ID,
		Status:           "pending",
		SourceLink:       sourceLink,
		Membership:       membership,
		BaseDocument:     baseDocument,
		ProposedDocument: proposedDocument,
		Diff:             documentLineDiff(baseDocument, proposedDocument),
		Patch:            documentPatch(baseDocument, proposedDocument),
		CreatedAt:        now,
		UpdatedAt:        now,
	})
	if err != nil {
		return models.TopicWeaveProposal{}, err
	}
	eventutil.Post(s.eventHub, gitTopicEvent(s.workspace.ID, topic, "topic_weave_proposal"))
	return proposal, nil
}

func (s *Service) ListWeaveProposals(ctx context.Context, topicID string) ([]models.TopicWeaveProposal, error) {
	return s.store.ListWeaveProposals(ctx, topicID)
}

func (s *Service) GetWeaveProposal(ctx context.Context, topicID string, proposalID string) (models.TopicWeaveProposal, error) {
	return s.store.GetWeaveProposal(ctx, topicID, proposalID)
}

func (s *Service) AcceptWeave(ctx context.Context, topicID string, req models.TopicWeaveAcceptRequest) (models.TopicDetail, error) {
	var proposal models.TopicWeaveProposal
	usePatch := false
	if strings.TrimSpace(req.ProposalID) != "" {
		var err error
		proposal, err = s.store.GetWeaveProposal(ctx, topicID, req.ProposalID)
		if err != nil {
			return models.TopicDetail{}, err
		}
		if proposal.Status != "" && proposal.Status != "pending" {
			return models.TopicDetail{}, errors.New("proposal is not pending")
		}
		if strings.TrimSpace(req.ThoughtID) == "" {
			req.ThoughtID = proposal.ThoughtID
		}
		if req.ThoughtID != proposal.ThoughtID {
			return models.TopicDetail{}, errors.New("proposal thought id does not match request")
		}
		if strings.TrimSpace(req.Document) == "" {
			req.Document = proposal.ProposedDocument
		}
		usePatch = len(proposal.Patch.Hunks) > 0 && strings.TrimSpace(req.Document) == strings.TrimSpace(proposal.ProposedDocument)
	}
	topic, thought, content, membership, err := s.weaveInput(ctx, topicID, req.ThoughtID)
	if err != nil {
		return models.TopicDetail{}, err
	}
	updatedTopic := models.Topic{}
	changed := false
	acceptedDocument := req.Document
	if usePatch {
		updatedTopic, changed, acceptedDocument, err = s.store.ApplyMembershipPatch(ctx, topic, thought, content, membership, proposal.Patch)
		if err != nil {
			return models.TopicDetail{}, err
		}
	} else {
		updatedTopic, changed, err = s.store.ApplyMembershipDocument(ctx, topic, thought, content, membership, req.Document)
		if err != nil {
			return models.TopicDetail{}, err
		}
	}
	if strings.TrimSpace(req.ProposalID) != "" {
		if _, err := s.store.MarkWeaveProposalAccepted(ctx, updatedTopic, req.ProposalID, acceptedDocument); err != nil {
			return models.TopicDetail{}, err
		}
		changed = true
	}
	if changed {
		eventutil.Post(s.eventHub, topicEvent(models.EventTopicUpdated, s.workspace.ID, models.ResourceTypeTopic, updatedTopic.ID, updatedTopic))
		eventutil.Post(s.eventHub, gitTopicEvent(s.workspace.ID, updatedTopic, "topic_update", thought.Path))
	}
	return s.store.Detail(ctx, updatedTopic.ID)
}

func (s *Service) weaveInput(ctx context.Context, topicID string, thoughtID string) (models.Topic, models.Thought, models.ThoughtContent, models.TopicMembership, error) {
	if strings.TrimSpace(topicID) == "" {
		return models.Topic{}, models.Thought{}, models.ThoughtContent{}, models.TopicMembership{}, errors.New("topic id is required")
	}
	if strings.TrimSpace(thoughtID) == "" {
		return models.Topic{}, models.Thought{}, models.ThoughtContent{}, models.TopicMembership{}, errors.New("thought id is required")
	}
	topic, err := s.store.Get(ctx, topicID)
	if err != nil {
		return models.Topic{}, models.Thought{}, models.ThoughtContent{}, models.TopicMembership{}, err
	}
	thought, content, err := markdown.ReadThought(s.workspace.RootPath, thoughtID)
	if err != nil {
		return models.Topic{}, models.Thought{}, models.ThoughtContent{}, models.TopicMembership{}, err
	}
	membership, ok := s.matchTopic(ctx, topic, thought, content)
	if !ok {
		now := time.Now().UTC()
		membership = models.TopicMembership{
			TopicID:   topic.ID,
			ThoughtID: thought.ID,
			MatchType: "manual",
			Score:     1,
			Reasons:   []string{"manual weave"},
			Status:    "accepted",
			CreatedAt: now,
			UpdatedAt: now,
		}
	}
	return topic, thought, content, membership, nil
}

func (s *Service) MatchThoughtAsync(thoughtID string) (models.Job, error) {
	if strings.TrimSpace(thoughtID) == "" {
		return models.Job{}, errors.New("thought id is required")
	}
	job, err := s.jobs.Create(models.JobTypeTopicMatch, models.ResourceTypeThought, thoughtID, "topic match queued")
	if err != nil {
		return models.Job{}, err
	}
	eventutil.Post(s.eventHub, jobEvent(s.workspace.ID, job))
	run := func() {
		s.matchThoughtJob(job)
	}
	if s.background != nil {
		if err := s.background.AsyncFunction(run); err == nil {
			return job, nil
		}
	}
	go run()
	return job, nil
}

func (s *Service) RebuildTopic(ctx context.Context, id string) (models.Job, error) {
	if strings.TrimSpace(id) == "" {
		return models.Job{}, errors.New("topic id is required")
	}
	job, err := s.jobs.Create(models.JobTypeTopicWeave, models.ResourceTypeTopic, id, "topic rebuild queued")
	if err != nil {
		return models.Job{}, err
	}
	eventutil.Post(s.eventHub, topicEvent(models.EventTopicRebuildStarted, s.workspace.ID, models.ResourceTypeTopic, id, job))
	eventutil.Post(s.eventHub, jobEvent(s.workspace.ID, job))
	run := func() {
		s.rebuildTopicJob(job)
	}
	if s.background != nil {
		if err := s.background.AsyncFunction(run); err == nil {
			return job, nil
		}
	}
	go run()
	return job, nil
}

func (s *Service) matchThoughtJob(job models.Job) {
	job, _ = s.jobs.MarkRunning(job)
	eventutil.Post(s.eventHub, jobEvent(s.workspace.ID, job))
	memberships, err := s.MatchThought(context.Background(), job.ResourceID)
	if err != nil {
		errRef := models.NewErrorRef("thoughtflow.topic.match_failed", err.Error(), true)
		job, _ = s.jobs.MarkFailed(job, errRef)
		eventutil.Post(s.eventHub, jobEvent(s.workspace.ID, job))
		eventutil.Post(s.eventHub, topicEvent(models.EventTopicRebuildFailed, s.workspace.ID, models.ResourceTypeThought, job.ResourceID, errRef))
		return
	}
	job, _ = s.jobs.MarkSucceeded(job, "topic match succeeded")
	eventutil.Post(s.eventHub, jobEvent(s.workspace.ID, job))
	if len(memberships) > 0 {
		eventutil.Post(s.eventHub, topicEvent(models.EventTopicMatched, s.workspace.ID, models.ResourceTypeThought, job.ResourceID, memberships))
	}
}

func (s *Service) MatchThought(ctx context.Context, thoughtID string) ([]models.TopicMembership, error) {
	thought, content, err := markdown.ReadThought(s.workspace.RootPath, thoughtID)
	if err != nil {
		return nil, err
	}
	topics, err := s.store.List(ctx)
	if err != nil {
		return nil, err
	}
	memberships := []models.TopicMembership{}
	for _, topic := range topics {
		membership, ok := s.matchTopic(ctx, topic, thought, content)
		if !ok {
			continue
		}
		memberships = append(memberships, membership)
		if topic.AutoWeave {
			updatedTopic, changed, err := s.store.AddMembership(ctx, topic, thought, content, membership)
			if err != nil {
				return memberships, err
			}
			if changed {
				eventutil.Post(s.eventHub, topicEvent(models.EventTopicUpdated, s.workspace.ID, models.ResourceTypeTopic, updatedTopic.ID, updatedTopic))
				eventutil.Post(s.eventHub, gitTopicEvent(s.workspace.ID, updatedTopic, "topic_update", thought.Path))
			}
		}
	}
	return memberships, nil
}

func (s *Service) rebuildTopicJob(job models.Job) {
	job, _ = s.jobs.MarkRunning(job)
	eventutil.Post(s.eventHub, jobEvent(s.workspace.ID, job))
	topic, count, changedThoughtPaths, err := s.store.RebuildWithMatcher(context.Background(), job.ResourceID, s.matchTopic)
	if err != nil {
		errRef := models.NewErrorRef("thoughtflow.topic.rebuild_failed", err.Error(), true)
		job, _ = s.jobs.MarkFailed(job, errRef)
		eventutil.Post(s.eventHub, jobEvent(s.workspace.ID, job))
		eventutil.Post(s.eventHub, topicEvent(models.EventTopicRebuildFailed, s.workspace.ID, models.ResourceTypeTopic, job.ResourceID, errRef))
		return
	}
	job.Message = "topic rebuilt"
	job, _ = s.jobs.MarkSucceeded(job, "topic rebuilt")
	eventutil.Post(s.eventHub, jobEvent(s.workspace.ID, job))
	eventutil.Post(s.eventHub, topicEvent(models.EventTopicUpdated, s.workspace.ID, models.ResourceTypeTopic, topic.ID, map[string]any{"topic": topic, "matched_count": count}))
	eventutil.Post(s.eventHub, gitTopicEvent(s.workspace.ID, topic, "topic_update", changedThoughtPaths...))
}

func (s *Service) matchTopic(ctx context.Context, topic models.Topic, thought models.Thought, content models.ThoughtContent) (models.TopicMembership, bool) {
	if membership, ok := s.store.MatchThought(topic, thought, content); ok {
		return membership, true
	}
	if !topic.Rules.Semantic.Enabled || s.embedder == nil || semanticHardExcluded(topic, thought, content) {
		return models.TopicMembership{}, false
	}
	topicText := semanticTopicText(topic)
	thoughtText := semanticThoughtText(thought, content)
	if strings.TrimSpace(topicText) == "" || strings.TrimSpace(thoughtText) == "" {
		return models.TopicMembership{}, false
	}
	topicEmbedding, err := s.embedder.Embed(ctx, ai.EmbedRequest{Text: topicText})
	if err != nil {
		return models.TopicMembership{}, false
	}
	thoughtEmbedding, ok := s.cachedThoughtEmbedding(ctx, thought.ID, topicEmbedding.Model)
	if ok && len(thoughtEmbedding.Vector) != len(topicEmbedding.Vector) {
		ok = false
	}
	if !ok {
		thoughtEmbedding, err = s.embedder.Embed(ctx, ai.EmbedRequest{ThoughtID: thought.ID, Text: thoughtText})
		if err != nil {
			return models.TopicMembership{}, false
		}
	}
	score := cosine(topicEmbedding.Vector, thoughtEmbedding.Vector)
	threshold := topic.Rules.Semantic.Threshold
	if threshold <= 0 {
		threshold = 0.75
	}
	if score < threshold {
		return models.TopicMembership{}, false
	}
	now := time.Now().UTC()
	return models.TopicMembership{
		TopicID:   topic.ID,
		ThoughtID: thought.ID,
		MatchType: "semantic",
		Score:     score,
		Reasons:   []string{fmt.Sprintf("semantic:%.3f", score)},
		Status:    "accepted",
		CreatedAt: now,
		UpdatedAt: now,
	}, true
}

func (s *Service) cachedThoughtEmbedding(ctx context.Context, thoughtID string, model string) (models.EmbeddingRecord, bool) {
	if s.cache == nil {
		return models.EmbeddingRecord{}, false
	}
	record, ok := s.cache.CachedEmbedding(ctx, thoughtID, model)
	if !ok || len(record.Vector) == 0 {
		return models.EmbeddingRecord{}, false
	}
	return record, true
}

func semanticHardExcluded(topic models.Topic, thought models.Thought, content models.ThoughtContent) bool {
	if contains(topic.Rules.ManualExclude, thought.ID) {
		return true
	}
	searchText := strings.ToLower(semanticThoughtText(thought, content))
	for _, excluded := range topic.Rules.Keywords.Exclude {
		excluded = strings.TrimSpace(strings.ToLower(excluded))
		if excluded != "" && strings.Contains(searchText, excluded) {
			return true
		}
	}
	for _, required := range topic.Rules.Keywords.All {
		required = strings.TrimSpace(strings.ToLower(required))
		if required != "" && !strings.Contains(searchText, required) {
			return true
		}
	}
	return false
}

func semanticTopicText(topic models.Topic) string {
	parts := []string{
		topic.Name,
		topic.Description,
		strings.Join(topic.Rules.Keywords.Any, " "),
		strings.Join(topic.Rules.Keywords.All, " "),
		strings.Join(topic.Rules.Tags.Any, " "),
	}
	for _, node := range topic.Outline {
		parts = append(parts, node.Title)
	}
	return strings.Join(parts, "\n")
}

func semanticThoughtText(thought models.Thought, content models.ThoughtContent) string {
	return strings.Join([]string{
		thought.UserTitle,
		thought.ExtractedTitle,
		thought.DisplayTitle,
		thought.Summary,
		strings.Join(thought.UserTags, " "),
		strings.Join(thought.AITags, " "),
		content.Original,
		content.ExtractedContent,
		content.AINotes,
	}, "\n")
}

func cosine(left []float64, right []float64) float64 {
	if len(left) == 0 || len(left) != len(right) {
		return 0
	}
	dot := 0.0
	leftNorm := 0.0
	rightNorm := 0.0
	for idx := range left {
		dot += left[idx] * right[idx]
		leftNorm += left[idx] * left[idx]
		rightNorm += right[idx] * right[idx]
	}
	if leftNorm == 0 || rightNorm == 0 {
		return 0
	}
	score := dot / (math.Sqrt(leftNorm) * math.Sqrt(rightNorm))
	if score < 0 {
		return 0
	}
	return score
}

func jobEvent(workspaceID string, job models.Job) models.DomainEvent {
	return models.DomainEvent{
		EventType:      models.EventJobUpdated,
		SourceUnit:     "topic",
		OccurredAt:     time.Now().UTC(),
		WorkspaceID:    workspaceID,
		ResourceType:   job.ResourceType,
		ResourceID:     job.ResourceID,
		PayloadVersion: 1,
		Payload:        job,
	}
}

func topicEvent(eventType string, workspaceID string, resourceType string, resourceID string, payload any) models.DomainEvent {
	return models.DomainEvent{
		EventType:      eventType,
		SourceUnit:     "topic",
		OccurredAt:     time.Now().UTC(),
		WorkspaceID:    workspaceID,
		ResourceType:   resourceType,
		ResourceID:     resourceID,
		PayloadVersion: 1,
		Payload:        payload,
	}
}

func gitTopicEvent(workspaceID string, topic models.Topic, reason string, extraPaths ...string) models.DomainEvent {
	paths := []string{
		"topics/" + topic.Slug + "/topic.yaml",
		"topics/" + topic.Slug + "/index.md",
		"topics/" + topic.Slug + "/memberships",
		"topics/" + topic.Slug + "/approvals",
	}
	paths = append(paths, extraPaths...)
	return models.DomainEvent{
		EventType:    models.EventGitCommitRequested,
		SourceUnit:   "topic",
		OccurredAt:   time.Now().UTC(),
		WorkspaceID:  workspaceID,
		ResourceType: models.ResourceTypeTopic,
		ResourceID:   topic.ID,
		Payload: models.GitCommitRequestedPayload{
			Paths:       uniqueStrings(paths),
			Reason:      reason,
			ResourceIDs: []string{topic.ID},
		},
		PayloadVersion: 1,
	}
}

func uniqueStrings(values []string) []string {
	seen := map[string]struct{}{}
	ret := []string{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		ret = append(ret, value)
	}
	return ret
}

func documentLineDiff(baseDocument string, proposedDocument string) []models.TopicDocumentDiffLine {
	baseLines := splitDocumentLines(baseDocument)
	proposedLines := splitDocumentLines(proposedDocument)
	lcs := make([][]int, len(baseLines)+1)
	for idx := range lcs {
		lcs[idx] = make([]int, len(proposedLines)+1)
	}
	for left := len(baseLines) - 1; left >= 0; left-- {
		for right := len(proposedLines) - 1; right >= 0; right-- {
			if baseLines[left] == proposedLines[right] {
				lcs[left][right] = lcs[left+1][right+1] + 1
				continue
			}
			if lcs[left+1][right] >= lcs[left][right+1] {
				lcs[left][right] = lcs[left+1][right]
			} else {
				lcs[left][right] = lcs[left][right+1]
			}
		}
	}
	diff := []models.TopicDocumentDiffLine{}
	left, right := 0, 0
	for left < len(baseLines) && right < len(proposedLines) {
		switch {
		case baseLines[left] == proposedLines[right]:
			diff = append(diff, models.TopicDocumentDiffLine{Op: "context", Text: baseLines[left]})
			left++
			right++
		case lcs[left+1][right] >= lcs[left][right+1]:
			diff = append(diff, models.TopicDocumentDiffLine{Op: "remove", Text: baseLines[left]})
			left++
		default:
			diff = append(diff, models.TopicDocumentDiffLine{Op: "add", Text: proposedLines[right]})
			right++
		}
	}
	for left < len(baseLines) {
		diff = append(diff, models.TopicDocumentDiffLine{Op: "remove", Text: baseLines[left]})
		left++
	}
	for right < len(proposedLines) {
		diff = append(diff, models.TopicDocumentDiffLine{Op: "add", Text: proposedLines[right]})
		right++
	}
	return diff
}

func documentPatch(baseDocument string, proposedDocument string) models.TopicDocumentPatch {
	diff := documentLineDiff(baseDocument, proposedDocument)
	if len(diff) == 0 {
		return models.TopicDocumentPatch{}
	}
	baseCount := 0
	proposedCount := 0
	for _, line := range diff {
		if line.Op != "add" {
			baseCount++
		}
		if line.Op != "remove" {
			proposedCount++
		}
	}
	return models.TopicDocumentPatch{
		BaseHash:     models.ContentHash(strings.TrimRight(baseDocument, "\n")),
		ProposedHash: models.ContentHash(strings.TrimRight(proposedDocument, "\n")),
		Hunks: []models.TopicDocumentPatchHunk{{
			BaseStart:     1,
			BaseCount:     baseCount,
			ProposedStart: 1,
			ProposedCount: proposedCount,
			Lines:         diff,
		}},
	}
}

func splitDocumentLines(document string) []string {
	document = strings.TrimRight(document, "\n")
	if document == "" {
		return []string{}
	}
	return strings.Split(document, "\n")
}

func contains(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}
