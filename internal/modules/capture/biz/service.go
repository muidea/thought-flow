package biz

import (
	"context"
	"errors"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/muidea/magicCommon/event"

	"thoughtflow/internal/pkg/eventutil"
	"thoughtflow/internal/pkg/jobstore"
	"thoughtflow/internal/pkg/markdown"
	"thoughtflow/internal/pkg/models"
)

type Service struct {
	workspace *models.Workspace
	jobs      *jobstore.Store
	eventHub  event.Hub
	now       func() time.Time
}

func NewService(workspace *models.Workspace, jobs *jobstore.Store, eventHub event.Hub) *Service {
	return &Service{
		workspace: workspace,
		jobs:      jobs,
		eventHub:  eventHub,
		now:       func() time.Time { return time.Now().UTC() },
	}
}

func (s *Service) Capture(ctx context.Context, cmd models.CaptureCommand) (models.CaptureResult, error) {
	_ = ctx
	if s == nil || s.workspace == nil {
		return models.CaptureResult{}, errors.New("capture service is not ready")
	}
	if err := validateCaptureCommand(cmd); err != nil {
		return models.CaptureResult{}, err
	}

	now := s.now()
	source := cmd.Source
	if source == "" {
		source = models.ThoughtSourceManual
	}
	original := originalContent(cmd)
	thoughtID := models.NewThoughtID(now, original)
	relPath := filepath.ToSlash(markdown.ThoughtRelativePath(thoughtID))
	thought := models.Thought{
		ID:            thoughtID,
		Type:          cmd.Type,
		Source:        source,
		UserTitle:     strings.TrimSpace(cmd.Title),
		URL:           strings.TrimSpace(cmd.URL),
		Path:          relPath,
		CreatedAt:     now,
		UpdatedAt:     now,
		ContentHash:   models.ContentHash(original),
		UserTags:      normalizeList(cmd.Tags),
		TopicIDs:      normalizeList(cmd.TopicHints),
		CaptureStatus: models.CaptureStatusCaptured,
		RefineStatus:  models.RefineStatusPending,
		IndexStatus:   models.IndexStatusPending,
		TopicStatus:   models.TopicStatusUnmatched,
	}
	thought.DisplayTitle = displayTitle(thought, original)
	content := models.ThoughtContent{Original: original}

	if err := markdown.WriteThought(s.workspace.RootPath, thought, content); err != nil {
		return models.CaptureResult{}, err
	}

	captured := models.DomainEvent{
		EventType:      models.EventThoughtCaptured,
		SourceUnit:     "capture",
		OccurredAt:     now,
		WorkspaceID:    s.workspace.ID,
		ResourceType:   models.ResourceTypeThought,
		ResourceID:     thought.ID,
		PayloadVersion: 1,
		Payload:        thought,
	}
	eventutil.Post(s.eventHub, captured)
	eventutil.Post(s.eventHub, models.DomainEvent{
		EventType:    models.EventGitCommitRequested,
		SourceUnit:   "capture",
		OccurredAt:   now,
		WorkspaceID:  s.workspace.ID,
		ResourceType: models.ResourceTypeThought,
		ResourceID:   thought.ID,
		Payload: models.GitCommitRequestedPayload{
			Paths:       []string{thought.Path},
			Reason:      "capture",
			ResourceIDs: []string{thought.ID},
		},
		PayloadVersion: 1,
	})

	return models.CaptureResult{Thought: thought, Jobs: []models.Job{}}, nil
}

func (s *Service) GetThought(ctx context.Context, thoughtID string) (models.ThoughtSnapshot, error) {
	_ = ctx
	if strings.TrimSpace(thoughtID) == "" {
		return models.ThoughtSnapshot{}, errors.New("thought id is required")
	}
	thought, content, err := markdown.ReadThought(s.workspace.RootPath, thoughtID)
	if err != nil {
		return models.ThoughtSnapshot{}, err
	}
	return models.ThoughtSnapshot{Thought: thought, Content: content}, nil
}

func (s *Service) Workspace() *models.Workspace {
	return s.workspace
}

func validateCaptureCommand(cmd models.CaptureCommand) error {
	switch cmd.Type {
	case models.ThoughtTypeText:
		if strings.TrimSpace(cmd.Content) == "" {
			return errors.New("content is required")
		}
	case models.ThoughtTypeURL:
		if strings.TrimSpace(cmd.URL) == "" {
			return errors.New("url is required")
		}
		parsed, err := url.ParseRequestURI(strings.TrimSpace(cmd.URL))
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			return errors.New("url is invalid")
		}
	default:
		return errors.New("type must be text or url")
	}
	return nil
}

func originalContent(cmd models.CaptureCommand) string {
	if cmd.Type == models.ThoughtTypeURL {
		if strings.TrimSpace(cmd.Content) == "" {
			return strings.TrimSpace(cmd.URL)
		}
		return strings.TrimSpace(cmd.URL) + "\n\n" + strings.TrimSpace(cmd.Content)
	}
	return strings.TrimSpace(cmd.Content)
}

func normalizeList(values []string) []string {
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

func displayTitle(thought models.Thought, original string) string {
	if thought.UserTitle != "" {
		return thought.UserTitle
	}
	if thought.Type == models.ThoughtTypeURL && thought.URL != "" {
		return thought.URL
	}
	firstLine := strings.TrimSpace(strings.Split(original, "\n")[0])
	if len(firstLine) > 80 {
		return firstLine[:80]
	}
	return firstLine
}
