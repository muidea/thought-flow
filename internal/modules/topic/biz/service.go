package biz

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/muidea/magicCommon/event"
	"github.com/muidea/magicCommon/task"

	"thoughtflow/internal/pkg/ai"
	"thoughtflow/internal/pkg/eventutil"
	"thoughtflow/internal/pkg/jobstore"
	"thoughtflow/internal/pkg/markdown"
	"thoughtflow/internal/pkg/models"
	"thoughtflow/internal/pkg/observability"
	"thoughtflow/internal/pkg/scratchpad"
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
	scratchpad ScratchpadProvider
}

type EmbeddingCache interface {
	CachedEmbedding(ctx context.Context, thoughtID string, model string) (models.EmbeddingRecord, bool)
}

type SemanticScoreCache interface {
	CachedSemanticScores(ctx context.Context, queryVector []float64, model string, limit int) (map[string]float64, string, bool)
}

// ScratchpadProvider is the subset of scratchpad operations the
// topic service needs to match unarchived sessions against topics.
// Defined as an interface so the topic module can be wired without
// depending on the capture service (which would form a cycle when
// the capture service subscribes back to topic events).
type ScratchpadProvider interface {
	List() []scratchpad.Summary
	Get(sessionID string) (scratchpad.Scratchpad, error)
}

func NewService(workspace *models.Workspace, jobs *jobstore.Store, store *topicstore.Store, eventHub event.Hub, background task.BackgroundRoutine, embedder ai.EmbeddingProvider, cache EmbeddingCache, scratchpadProvider ScratchpadProvider) *Service {
	return &Service{
		workspace:  workspace,
		jobs:       jobs,
		store:      store,
		eventHub:   eventHub,
		background: background,
		embedder:   embedder,
		cache:      cache,
		scratchpad: scratchpadProvider,
	}
}

func (s *Service) ID() string {
	return "topic.thought-observer"
}

// SetScratchpadProvider injects the scratchpad store after
// construction. The topic module's Setup cannot do this directly
// because the scratchpad store is owned by the capture module and
// the application layer decides the wiring order.
func (s *Service) SetScratchpadProvider(provider ScratchpadProvider) {
	s.scratchpad = provider
}

func (s *Service) Notify(ev event.Event, result event.Result) {
	domainEvent, ok := ev.Data().(models.DomainEvent)
	if !ok {
		if result != nil {
			result.Set(nil, nil)
		}
		return
	}
	switch domainEvent.EventType {
	case models.EventScratchpadContextUpdated, models.EventScratchpadCommitted:
		sessionID := extractSessionID(domainEvent.Payload)
		if sessionID != "" {
			_, _ = s.MatchScratchpadAsync(sessionID)
		}
	case models.EventThoughtPatched, models.EventThoughtRefined, models.EventSearchIndexUpdated:
		if domainEvent.ResourceType == models.ResourceTypeThought && domainEvent.ResourceID != "" {
			_, _ = s.MatchThoughtAsync(domainEvent.ResourceID)
		}
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

// SearchCandidates surfaces up to limit topics whose name, description
// or rules match the user's query/tags. The match_type is one of
// "tag_hint" | "keyword"; matched_count is the size of the topic's
// existing member set (a populated topic is more likely to be the
// user's intent than an empty one with the right words).
func (s *Service) SearchCandidates(ctx context.Context, query string, tags []string, topicID string, limit int) []models.SearchResultCandidate {
	if limit <= 0 {
		limit = 5
	}
	topics, err := s.store.List(ctx)
	if err != nil {
		return nil
	}
	needle := strings.ToLower(strings.TrimSpace(query))
	wantedTags := make([]string, 0, len(tags))
	for _, tag := range tags {
		tag = strings.ToLower(strings.TrimSpace(tag))
		if tag != "" {
			wantedTags = append(wantedTags, tag)
		}
	}
	scores := make([]models.SearchResultCandidate, 0, len(topics))
	for _, topic := range topics {
		if topicID != "" && topic.ID != topicID {
			continue
		}
		score, matchType := scoreTopicCandidate(topic, needle, wantedTags)
		if score <= 0 {
			continue
		}
		scores = append(scores, models.SearchResultCandidate{
			TopicID:      topic.ID,
			TopicName:    topic.Name,
			Slug:         topic.Slug,
			MatchType:    matchType,
			Score:        score,
			MatchedCount: len(topic.Members),
		})
	}
	sort.Slice(scores, func(i, j int) bool { return scores[i].Score > scores[j].Score })
	if len(scores) > limit {
		scores = scores[:limit]
	}
	return scores
}

// scoreTopicCandidate returns a non-negative score for a topic
// against the user's needle/tags. Keyword hits on name and
// description beat tag-hint matches; an exact name match wins.
func scoreTopicCandidate(topic models.Topic, needle string, wantedTags []string) (float64, string) {
	name := strings.ToLower(strings.TrimSpace(topic.Name))
	description := strings.ToLower(strings.TrimSpace(topic.Description))
	rulesKeywords := topicKeywordCorpus(topic)
	if needle != "" {
		switch {
		case name == needle:
			return 1.0, "keyword"
		case strings.Contains(name, needle):
			return 0.85, "keyword"
		case strings.Contains(description, needle):
			return 0.6, "keyword"
		case rulesKeywords != "" && strings.Contains(rulesKeywords, needle):
			return 0.5, "keyword"
		}
	}
	if len(wantedTags) > 0 {
		ruleTags := topicRuleTags(topic)
		for _, want := range wantedTags {
			for _, ruleTag := range ruleTags {
				if ruleTag == want {
					return 0.4, "tag_hint"
				}
			}
		}
	}
	return 0, ""
}

func topicKeywordCorpus(topic models.Topic) string {
	parts := []string{}
	if kws := topic.Rules.Keywords.All; len(kws) > 0 {
		for _, k := range kws {
			parts = append(parts, k)
		}
	}
	if kws := topic.Rules.Keywords.Any; len(kws) > 0 {
		for _, k := range kws {
			parts = append(parts, k)
		}
	}
	if kws := topic.Rules.Keywords.Exclude; len(kws) > 0 {
		for _, k := range kws {
			parts = append(parts, k)
		}
	}
	return strings.ToLower(strings.Join(parts, " "))
}

func topicRuleTags(topic models.Topic) []string {
	out := []string{}
	for _, tag := range topic.Rules.Tags.Any {
		if tag = strings.ToLower(strings.TrimSpace(tag)); tag != "" {
			out = append(out, tag)
		}
	}
	return out
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

// ListSessionCandidates returns the unarchived scratchpad sessions
// currently matching the given topic. The list is a snapshot read
// of the per-topic candidates.yaml — it does not recompute matches;
// callers wanting a fresh pass should trigger MatchScratchpadAsync
// on each session first.
func (s *Service) ListSessionCandidates(ctx context.Context, topicID string) ([]models.TopicSessionCandidate, error) {
	topicID = strings.TrimSpace(topicID)
	if topicID == "" {
		return nil, errors.New("topic id is required")
	}
	candidates, err := s.store.ListSessionCandidates(ctx, topicID)
	if err != nil {
		return nil, err
	}
	if candidates == nil {
		candidates = []models.TopicSessionCandidate{}
	}
	return candidates, nil
}

// ListCandidates returns the Web-facing candidate impact list for a
// topic. It fuses four sources:
//
//   - capture_session: the unarchived scratchpad sessions that
//     currently match the topic (the same data as
//     ListSessionCandidates, exposed under the new DTO shape).
//   - thought_reopen_session: scratchpad sessions flagged as
//     "reopen"; today there is no separate reopen state, so the
//     same candidate list is rendered with the reopen source label
//     and the candidate's status field is left to the caller.
//   - thought: each member of the topic, in topic.Members order,
//     is exposed as an impact so the Web can show a "currently
//     associated thoughts" panel.
//   - compose_draft: this source is reserved for compose-module
//     integration; the biz layer returns no entries until the
//     compose side wires its draft index through.
//
// The result is sorted by Score descending then by UpdatedAt
// descending so the most-prominent candidates surface first.
func (s *Service) ListCandidates(ctx context.Context, topicID string) ([]models.TopicCandidateImpact, error) {
	topicID = strings.TrimSpace(topicID)
	if topicID == "" {
		return nil, errors.New("topic id is required")
	}
	topic, err := s.store.Get(ctx, topicID)
	if err != nil {
		return nil, err
	}
	candidates := make([]models.TopicCandidateImpact, 0)
	if sessions, err := s.store.ListSessionCandidates(ctx, topicID); err == nil {
		for _, session := range sessions {
			candidates = append(candidates, models.TopicCandidateImpact{
				Source:      models.TopicCandidateSourceCaptureSession,
				CandidateID: session.SessionID,
				SessionID:   session.SessionID,
				Title:       session.Title,
				MatchType:   session.MatchType,
				Score:       session.Score,
				Status:      session.Status,
				Reasons:     append([]string{}, session.Reasons...),
				UpdatedAt:   session.UpdatedAt,
			})
			candidates = append(candidates, models.TopicCandidateImpact{
				Source:      models.TopicCandidateSourceThoughtReopen,
				CandidateID: session.SessionID,
				SessionID:   session.SessionID,
				Title:       session.Title,
				MatchType:   session.MatchType,
				Score:       session.Score,
				Status:      session.Status,
				Reasons:     append([]string{}, session.Reasons...),
				UpdatedAt:   session.UpdatedAt,
			})
		}
	}
	for _, thoughtID := range topic.Members {
		candidates = append(candidates, models.TopicCandidateImpact{
			Source:      models.TopicCandidateSourceThought,
			CandidateID: thoughtID,
			ThoughtID:   thoughtID,
			Title:       thoughtID,
			MatchType:   "member",
			Score:       1,
			Status:      "member",
			UpdatedAt:   topic.UpdatedAt,
		})
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].Score != candidates[j].Score {
			return candidates[i].Score > candidates[j].Score
		}
		return candidates[i].UpdatedAt.After(candidates[j].UpdatedAt)
	})
	return candidates, nil
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
		observability.IncrementTopicWeave()
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
	membership, ok := s.matchTopic(ctx, topic, thought, content, 0)
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

// MatchScratchpadAsync enqueues a job that re-evaluates a single
// unarchived session against every topic and writes the result into
// the per-topic candidates.yaml. A session with no matches produces
// no candidates (the file is left empty if it was empty before).
//
// Returned errors are limited to job-creation failures; the actual
// matching always runs in the background so a single scratchpad
// can never block the calling event dispatch.
func (s *Service) MatchScratchpadAsync(sessionID string) (models.Job, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return models.Job{}, errors.New("session id is required")
	}
	if s.scratchpad == nil {
		return models.Job{}, errors.New("scratchpad provider is not configured")
	}
	job, err := s.jobs.Create(models.JobTypeTopicMatch, models.ResourceTypeSession, sessionID, "session match queued")
	if err != nil {
		return models.Job{}, err
	}
	eventutil.Post(s.eventHub, jobEvent(s.workspace.ID, job))
	run := func() {
		s.matchScratchpadJob(job)
	}
	if s.background != nil {
		if err := s.background.AsyncFunction(run); err == nil {
			return job, nil
		}
	}
	go run()
	return job, nil
}

func (s *Service) matchScratchpadJob(job models.Job) {
	job, _ = s.jobs.MarkRunning(job)
	eventutil.Post(s.eventHub, jobEvent(s.workspace.ID, job))
	count, err := s.matchScratchpad(context.Background(), job.ResourceID)
	if err != nil {
		errRef := models.NewErrorRef("thoughtflow.topic.session_match_failed", err.Error(), true)
		job, _ = s.jobs.MarkFailed(job, errRef)
		eventutil.Post(s.eventHub, jobEvent(s.workspace.ID, job))
		return
	}
	job.Message = "session match succeeded"
	job, _ = s.jobs.MarkSucceeded(job, fmt.Sprintf("session matched to %d topics", count))
	eventutil.Post(s.eventHub, jobEvent(s.workspace.ID, job))
}

// matchScratchpad reads one session, computes candidates for every
// topic, and writes the per-topic candidates.yaml. Returns the
// number of (topic, session) candidate pairs successfully written
// — used only for the job summary message.
func (s *Service) matchScratchpad(ctx context.Context, sessionID string) (int, error) {
	if s.scratchpad == nil {
		return 0, errors.New("scratchpad provider is not configured")
	}
	sp, err := s.scratchpad.Get(sessionID)
	if err != nil {
		return 0, err
	}
	if sp.CommittedThoughtID != "" {
		_ = s.removeSessionFromAllCandidateLists(ctx, sessionID)
		return 0, nil
	}
	topics, err := s.store.List(ctx)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, topic := range topics {
		candidate, matched := s.buildSessionCandidate(ctx, sp, topic)
		if !matched {
			if err := s.removeSessionFromCandidateList(ctx, topic, sessionID); err != nil {
				return count, err
			}
			continue
		}
		if err := s.upsertSessionCandidate(ctx, topic, candidate); err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}

// buildSessionCandidate applies the matching rules in priority order
// (tag hint > keyword > semantic) and returns the candidate. The
// boolean is false when the session does not match the topic at any
// level — the caller then prunes the session from the topic's
// candidate list.
func (s *Service) buildSessionCandidate(ctx context.Context, sp scratchpad.Scratchpad, topic models.Topic) (models.TopicSessionCandidate, bool) {
	now := time.Now().UTC()
	hints := sp.SessionContext.SuggestedTopicIDs
	for _, id := range hints {
		if strings.TrimSpace(id) == topic.ID {
			return models.TopicSessionCandidate{
				SessionID: sp.SessionID,
				TopicID:   topic.ID,
				Title:     sessionTitle(sp),
				MatchType: "tag_hint",
				Score:     1.0,
				Reasons:   []string{fmt.Sprintf("session_context.suggested_topic_ids includes %q", topic.ID)},
				Status:    candidateStatus(sp),
				UpdatedAt: now,
			}, true
		}
	}
	if score, reasons, ok := sessionKeywordScore(topic, sp); ok {
		return models.TopicSessionCandidate{
			SessionID: sp.SessionID,
			TopicID:   topic.ID,
			Title:     sessionTitle(sp),
			MatchType: "keyword",
			Score:     score,
			Reasons:   reasons,
			Status:    candidateStatus(sp),
			UpdatedAt: now,
		}, true
	}
	if score, reasons, ok := s.sessionSemanticScore(ctx, topic, sp); ok {
		return models.TopicSessionCandidate{
			SessionID: sp.SessionID,
			TopicID:   topic.ID,
			Title:     sessionTitle(sp),
			MatchType: "semantic",
			Score:     score,
			Reasons:   reasons,
			Status:    candidateStatus(sp),
			UpdatedAt: now,
		}, true
	}
	return models.TopicSessionCandidate{}, false
}

func sessionTitle(sp scratchpad.Scratchpad) string {
	if t := strings.TrimSpace(sp.SessionContext.CandidateTitle); t != "" {
		return t
	}
	return strings.TrimSpace(sp.Title)
}

func candidateStatus(sp scratchpad.Scratchpad) string {
	if len(sp.SessionContext.Conflicts) > 0 {
		return "conflict"
	}
	if len(sp.SessionContext.OpenQuestions) > 0 {
		return "near_miss"
	}
	return "candidate"
}

// sessionKeywordScore mirrors the keyword path of topicstore.MatchThought
// for a session. We deliberately do NOT call the store method
// directly: the store works on markdown Thought records, while
// sessions have a different content shape (no title hierarchy, no
// extracted content, no AINotes). Keeping the matcher local to the
// topic service means a future change to the session shape does
// not have to ripple through the topic store.
func sessionKeywordScore(topic models.Topic, sp scratchpad.Scratchpad) (float64, []string, bool) {
	searchText := strings.ToLower(strings.Join([]string{
		sp.SessionContext.Topic,
		sp.SessionContext.Goal,
		sp.SessionContext.CandidateTitle,
		sp.SessionContext.CandidateSummary,
		sp.SessionContext.CandidateBody,
		strings.Join(sp.SessionContext.CandidateTags, " "),
		strings.Join(sp.SessionContext.ConfirmedFacts, " "),
	}, "\n"))
	if strings.TrimSpace(searchText) == "" {
		return 0, nil, false
	}
	for _, excluded := range topic.Rules.Keywords.Exclude {
		excluded = strings.ToLower(strings.TrimSpace(excluded))
		if excluded != "" && strings.Contains(searchText, excluded) {
			return 0, nil, false
		}
	}
	reasons := []string{}
	score := 0.0
	for _, required := range topic.Rules.Keywords.All {
		required = strings.ToLower(strings.TrimSpace(required))
		if required != "" && !strings.Contains(searchText, required) {
			return 0, nil, false
		}
	}
	for _, keyword := range topic.Rules.Keywords.Any {
		keyword = strings.ToLower(strings.TrimSpace(keyword))
		if keyword != "" && strings.Contains(searchText, keyword) {
			reasons = append(reasons, "keyword:"+keyword)
			score += 0.4
		}
	}
	for _, expected := range topic.Rules.Tags.Any {
		if containsStringFold(sp.SessionContext.CandidateTags, expected) {
			reasons = append(reasons, "tag:"+expected)
			score += 0.5
		}
	}
	if score <= 0 {
		return 0, nil, false
	}
	if score > 1 {
		score = 1
	}
	return score, reasons, true
}

func (s *Service) sessionSemanticScore(ctx context.Context, topic models.Topic, sp scratchpad.Scratchpad) (float64, []string, bool) {
	if !topic.Rules.Semantic.Enabled || s.embedder == nil {
		return 0, nil, false
	}
	topicText := semanticTopicText(topic)
	sessionText := strings.TrimSpace(strings.Join([]string{
		sp.SessionContext.Topic,
		sp.SessionContext.Goal,
		sp.SessionContext.CandidateTitle,
		sp.SessionContext.CandidateSummary,
		sp.SessionContext.CandidateBody,
	}, "\n"))
	if topicText == "" || sessionText == "" {
		return 0, nil, false
	}
	topicEmbedding, err := s.embedder.Embed(ctx, ai.EmbedRequest{Text: topicText})
	if err != nil {
		return 0, nil, false
	}
	sessionEmbedding, err := s.embedder.Embed(ctx, ai.EmbedRequest{Text: sessionText})
	if err != nil {
		return 0, nil, false
	}
	score := cosine(topicEmbedding.Vector, sessionEmbedding.Vector)
	threshold := topic.Rules.Semantic.Threshold
	if threshold <= 0 {
		threshold = 0.75
	}
	if score < threshold {
		return 0, nil, false
	}
	return score, []string{fmt.Sprintf("semantic:%.3f", score)}, true
}

func (s *Service) upsertSessionCandidate(ctx context.Context, topic models.Topic, candidate models.TopicSessionCandidate) error {
	existing, err := s.store.ListSessionCandidates(ctx, topic.ID)
	if err != nil {
		return err
	}
	replaced := false
	for idx := range existing {
		if existing[idx].SessionID == candidate.SessionID {
			existing[idx] = candidate
			replaced = true
			break
		}
	}
	if !replaced {
		existing = append(existing, candidate)
	}
	return s.store.SaveSessionCandidates(ctx, topic.ID, existing)
}

func (s *Service) removeSessionFromCandidateList(ctx context.Context, topic models.Topic, sessionID string) error {
	existing, err := s.store.ListSessionCandidates(ctx, topic.ID)
	if err != nil {
		return err
	}
	filtered := existing[:0]
	changed := false
	for _, c := range existing {
		if c.SessionID == sessionID {
			changed = true
			continue
		}
		filtered = append(filtered, c)
	}
	if !changed {
		return nil
	}
	return s.store.SaveSessionCandidates(ctx, topic.ID, filtered)
}

func (s *Service) removeSessionFromAllCandidateLists(ctx context.Context, sessionID string) error {
	topics, err := s.store.List(ctx)
	if err != nil {
		return err
	}
	for _, topic := range topics {
		if err := s.removeSessionFromCandidateList(ctx, topic, sessionID); err != nil {
			return err
		}
	}
	return nil
}

func containsStringFold(values []string, expected string) bool {
	expected = strings.ToLower(strings.TrimSpace(expected))
	if expected == "" {
		return false
	}
	for _, value := range values {
		if strings.ToLower(strings.TrimSpace(value)) == expected {
			return true
		}
	}
	return false
}

func extractSessionID(payload any) string {
	switch p := payload.(type) {
	case map[string]any:
		if v, ok := p["session_id"].(string); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func (s *Service) RefreshTopic(ctx context.Context, id string) (models.Job, error) {
	if strings.TrimSpace(id) == "" {
		return models.Job{}, errors.New("topic id is required")
	}
	job, err := s.jobs.Create(models.JobTypeTopicWeave, models.ResourceTypeTopic, id, "topic refresh queued")
	if err != nil {
		return models.Job{}, err
	}
	eventutil.Post(s.eventHub, topicEvent(models.EventTopicRefreshStarted, s.workspace.ID, models.ResourceTypeTopic, id, job))
	eventutil.Post(s.eventHub, jobEvent(s.workspace.ID, job))
	run := func() {
		s.refreshTopicJob(job)
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
		eventutil.Post(s.eventHub, topicEvent(models.EventTopicRefreshFailed, s.workspace.ID, models.ResourceTypeThought, job.ResourceID, errRef))
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
		membership, ok := s.matchTopic(ctx, topic, thought, content, 0)
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
				observability.IncrementTopicWeave()
				eventutil.Post(s.eventHub, topicEvent(models.EventTopicUpdated, s.workspace.ID, models.ResourceTypeTopic, updatedTopic.ID, updatedTopic))
				eventutil.Post(s.eventHub, gitTopicEvent(s.workspace.ID, updatedTopic, "topic_update", thought.Path))
			}
		}
	}
	return memberships, nil
}

// NearMissTopics returns up to topK topics whose semantic score
// against the given thought is below the topic's own threshold but
// still meaningful. The expander uses this to suggest topics to the
// user that did not auto-match; minScore acts as a hard floor (any
// score strictly below it is dropped), and the topK cap bounds the
// number of suggestions shown in the UI.
//
// The default minScore (0) returns every scored topic, including
// zeros, so the ranking is purely by score. Callers that want only
// "near" matches should pass a positive floor such as 0.4.
func (s *Service) NearMissTopics(ctx context.Context, thoughtID string, topK int, minScore float64) ([]models.TopicMatchSuggestion, error) {
	if topK <= 0 {
		topK = 3
	}
	thought, content, err := markdown.ReadThought(s.workspace.RootPath, thoughtID)
	if err != nil {
		return nil, err
	}
	topics, err := s.store.List(ctx)
	if err != nil {
		return nil, err
	}
	suggestions := []models.TopicMatchSuggestion{}
	for _, topic := range topics {
		membership, _ := s.matchTopic(ctx, topic, thought, content, minScore)
		if membership.Score <= 0 || membership.Score < minScore {
			continue
		}
		suggestions = append(suggestions, models.TopicMatchSuggestion{
			TopicID:   topic.ID,
			TopicName: topic.Name,
			Score:     membership.Score,
		})
	}
	sort.Slice(suggestions, func(i, j int) bool { return suggestions[i].Score > suggestions[j].Score })
	if len(suggestions) > topK {
		suggestions = suggestions[:topK]
	}
	return suggestions, nil
}

func (s *Service) refreshTopicJob(job models.Job) {
	job, _ = s.jobs.MarkRunning(job)
	eventutil.Post(s.eventHub, jobEvent(s.workspace.ID, job))
	topic, count, changedThoughtPaths, err := s.store.RefreshWithMatcher(context.Background(), job.ResourceID, func(ctx context.Context, topic models.Topic, thought models.Thought, content models.ThoughtContent) (models.TopicMembership, bool) {
		return s.matchTopic(ctx, topic, thought, content, 0)
	})
	if err != nil {
		errRef := models.NewErrorRef("thoughtflow.topic.refresh_failed", err.Error(), true)
		job, _ = s.jobs.MarkFailed(job, errRef)
		eventutil.Post(s.eventHub, jobEvent(s.workspace.ID, job))
		eventutil.Post(s.eventHub, topicEvent(models.EventTopicRefreshFailed, s.workspace.ID, models.ResourceTypeTopic, job.ResourceID, errRef))
		return
	}
	job.Message = "topic refreshed"
	job, _ = s.jobs.MarkSucceeded(job, "topic refreshed")
	observability.AddTopicWeave(uint64(count))
	eventutil.Post(s.eventHub, jobEvent(s.workspace.ID, job))
	eventutil.Post(s.eventHub, topicEvent(models.EventTopicUpdated, s.workspace.ID, models.ResourceTypeTopic, topic.ID, map[string]any{"topic": topic, "matched_count": count}))
	eventutil.Post(s.eventHub, gitTopicEvent(s.workspace.ID, topic, "topic_update", changedThoughtPaths...))
}

func (s *Service) matchTopic(ctx context.Context, topic models.Topic, thought models.Thought, content models.ThoughtContent, minScore float64) (models.TopicMembership, bool) {
	if membership, ok := s.store.MatchThought(topic, thought, content); ok {
		if membership.Score < minScore {
			return models.TopicMembership{}, false
		}
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
	threshold := topic.Rules.Semantic.Threshold
	if threshold <= 0 {
		threshold = 0.75
	}
	score := 0.0
	if cached, ok := s.cachedSemanticScore(ctx, thought.ID, topicEmbedding); ok {
		score = cached
	} else {
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
		score = cosine(topicEmbedding.Vector, thoughtEmbedding.Vector)
	}
	if score < minScore {
		return models.TopicMembership{Score: score}, false
	}
	if score < threshold {
		return models.TopicMembership{Score: score}, false
	}
	return semanticMembership(topic.ID, thought.ID, score), true
}

func semanticMembership(topicID string, thoughtID string, score float64) models.TopicMembership {
	now := time.Now().UTC()
	return models.TopicMembership{
		TopicID:   topicID,
		ThoughtID: thoughtID,
		MatchType: "semantic",
		Score:     score,
		Reasons:   []string{fmt.Sprintf("semantic:%.3f", score)},
		Status:    "accepted",
		CreatedAt: now,
		UpdatedAt: now,
	}
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

func (s *Service) cachedSemanticScore(ctx context.Context, thoughtID string, embedding models.EmbeddingRecord) (float64, bool) {
	if s.cache == nil || strings.TrimSpace(thoughtID) == "" || len(embedding.Vector) == 0 {
		return 0, false
	}
	scoreCache, ok := s.cache.(SemanticScoreCache)
	if !ok {
		return 0, false
	}
	scores, _, ok := scoreCache.CachedSemanticScores(ctx, embedding.Vector, embedding.Model, 100)
	if !ok {
		return 0, false
	}
	score, ok := scores[thoughtID]
	if !ok {
		return 0, false
	}
	return score, true
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
