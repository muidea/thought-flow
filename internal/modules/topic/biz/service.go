package biz

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/muidea/magicCommon/event"
	"github.com/muidea/magicCommon/task"

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
}

func NewService(workspace *models.Workspace, jobs *jobstore.Store, store *topicstore.Store, eventHub event.Hub, background task.BackgroundRoutine) *Service {
	return &Service{
		workspace:  workspace,
		jobs:       jobs,
		store:      store,
		eventHub:   eventHub,
		background: background,
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
	eventutil.Post(s.eventHub, topicEvent(models.EventTopicCreated, s.workspace.ID, topic.ID, topic))
	eventutil.Post(s.eventHub, gitTopicEvent(s.workspace.ID, topic, "topic_update"))
	return topic, nil
}

func (s *Service) UpdateTopic(ctx context.Context, id string, req models.TopicUpdateRequest) (models.Topic, error) {
	topic, err := s.store.Update(ctx, id, req)
	if err != nil {
		return models.Topic{}, err
	}
	eventutil.Post(s.eventHub, topicEvent(models.EventTopicUpdated, s.workspace.ID, topic.ID, topic))
	eventutil.Post(s.eventHub, gitTopicEvent(s.workspace.ID, topic, "topic_update"))
	return topic, nil
}

func (s *Service) ListTopics(ctx context.Context) ([]models.Topic, error) {
	return s.store.List(ctx)
}

func (s *Service) GetTopic(ctx context.Context, id string) (models.TopicDetail, error) {
	return s.store.Detail(ctx, id)
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
	eventutil.Post(s.eventHub, topicEvent(models.EventTopicRebuildStarted, s.workspace.ID, id, job))
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
		eventutil.Post(s.eventHub, topicEvent(models.EventTopicRebuildFailed, s.workspace.ID, job.ResourceID, errRef))
		return
	}
	job, _ = s.jobs.MarkSucceeded(job, "topic match succeeded")
	eventutil.Post(s.eventHub, jobEvent(s.workspace.ID, job))
	if len(memberships) > 0 {
		eventutil.Post(s.eventHub, topicEvent(models.EventTopicMatched, s.workspace.ID, job.ResourceID, memberships))
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
		membership, ok := s.store.MatchThought(topic, thought, content)
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
				eventutil.Post(s.eventHub, topicEvent(models.EventTopicUpdated, s.workspace.ID, updatedTopic.ID, updatedTopic))
				eventutil.Post(s.eventHub, gitTopicEvent(s.workspace.ID, updatedTopic, "topic_update"))
			}
		}
	}
	return memberships, nil
}

func (s *Service) rebuildTopicJob(job models.Job) {
	job, _ = s.jobs.MarkRunning(job)
	eventutil.Post(s.eventHub, jobEvent(s.workspace.ID, job))
	topic, count, err := s.store.Rebuild(context.Background(), job.ResourceID)
	if err != nil {
		errRef := models.NewErrorRef("thoughtflow.topic.rebuild_failed", err.Error(), true)
		job, _ = s.jobs.MarkFailed(job, errRef)
		eventutil.Post(s.eventHub, jobEvent(s.workspace.ID, job))
		eventutil.Post(s.eventHub, topicEvent(models.EventTopicRebuildFailed, s.workspace.ID, job.ResourceID, errRef))
		return
	}
	job.Message = "topic rebuilt"
	job, _ = s.jobs.MarkSucceeded(job, "topic rebuilt")
	eventutil.Post(s.eventHub, jobEvent(s.workspace.ID, job))
	eventutil.Post(s.eventHub, topicEvent(models.EventTopicUpdated, s.workspace.ID, topic.ID, map[string]any{"topic": topic, "matched_count": count}))
	eventutil.Post(s.eventHub, gitTopicEvent(s.workspace.ID, topic, "topic_update"))
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

func topicEvent(eventType string, workspaceID string, resourceID string, payload any) models.DomainEvent {
	resourceType := models.ResourceTypeTopic
	if strings.HasPrefix(resourceID, "20") {
		resourceType = models.ResourceTypeThought
	}
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

func gitTopicEvent(workspaceID string, topic models.Topic, reason string) models.DomainEvent {
	return models.DomainEvent{
		EventType:    models.EventGitCommitRequested,
		SourceUnit:   "topic",
		OccurredAt:   time.Now().UTC(),
		WorkspaceID:  workspaceID,
		ResourceType: models.ResourceTypeTopic,
		ResourceID:   topic.ID,
		Payload: models.GitCommitRequestedPayload{
			Paths: []string{
				"topics/" + topic.Slug + "/topic.yaml",
				"topics/" + topic.Slug + "/index.md",
			},
			Reason:      reason,
			ResourceIDs: []string{topic.ID},
		},
		PayloadVersion: 1,
	}
}
