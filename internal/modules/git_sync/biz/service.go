package biz

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/muidea/magicCommon/event"
	"github.com/muidea/magicCommon/task"

	"thoughtflow/internal/pkg/eventutil"
	"thoughtflow/internal/pkg/jobstore"
	"thoughtflow/internal/pkg/models"
	"thoughtflow/internal/pkg/workspace"
)

type Service struct {
	workspace  *models.Workspace
	jobs       *jobstore.Store
	eventHub   event.Hub
	background task.BackgroundRoutine
	enabled    bool
	debounce   time.Duration

	mu          sync.Mutex
	pending     map[string]struct{}
	resourceIDs map[string]struct{}
	timer       *time.Timer
}

func NewService(workspace *models.Workspace, jobs *jobstore.Store, eventHub event.Hub, background task.BackgroundRoutine, enabled bool, debounce time.Duration) *Service {
	return &Service{
		workspace:   workspace,
		jobs:        jobs,
		eventHub:    eventHub,
		background:  background,
		enabled:     enabled,
		debounce:    debounce,
		pending:     map[string]struct{}{},
		resourceIDs: map[string]struct{}{},
	}
}

func (s *Service) ID() string {
	return "git-sync.commit-request-observer"
}

func (s *Service) Notify(ev event.Event, result event.Result) {
	domainEvent, ok := ev.Data().(models.DomainEvent)
	if !ok {
		return
	}
	payload, ok := domainEvent.Payload.(models.GitCommitRequestedPayload)
	if !ok {
		return
	}
	s.Enqueue(payload.Paths, payload.ResourceIDs)
	if result != nil {
		result.Set(nil, nil)
	}
}

func (s *Service) Enqueue(paths []string, resourceIDs []string) {
	if !s.enabled {
		return
	}
	s.mu.Lock()
	for _, path := range paths {
		path = filepath.ToSlash(strings.TrimSpace(path))
		if path == "" || strings.HasPrefix(path, ".thoughtflow/") {
			continue
		}
		s.pending[path] = struct{}{}
	}
	for _, id := range resourceIDs {
		if strings.TrimSpace(id) != "" {
			s.resourceIDs[id] = struct{}{}
		}
	}
	if s.timer != nil {
		s.timer.Stop()
	}
	delay := s.debounce
	if delay < 0 {
		delay = 0
	}
	s.timer = time.AfterFunc(delay, func() {
		s.flushAsync()
	})
	s.mu.Unlock()
}

func (s *Service) Flush(ctx context.Context, timeout time.Duration) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.flush()
	}()
	select {
	case <-ctx.Done():
	case <-time.After(timeout):
	case <-done:
	}
}

func (s *Service) flushAsync() {
	if s.background != nil {
		if err := s.background.AsyncFunction(func() { s.flush() }); err == nil {
			return
		}
	}
	go s.flush()
}

func (s *Service) flush() {
	paths, resourceIDs := s.takePending()
	if len(paths) == 0 {
		return
	}

	job, err := s.jobs.Create(models.JobTypeGitCommit, models.ResourceTypeWorkspace, s.workspace.ID, "git commit queued")
	if err == nil {
		job, _ = s.jobs.MarkRunning(job)
		eventutil.Post(s.eventHub, jobEvent(s.workspace.ID, job))
	}

	record, commitErr := s.Commit(context.Background(), paths, resourceIDs)
	if commitErr != nil {
		errRef := models.NewErrorRef("thoughtflow.git.commit_failed", commitErr.Error(), true)
		record.Error = &errRef
		if err == nil {
			job, _ = s.jobs.MarkFailed(job, errRef)
			record.JobID = job.ID
			eventutil.Post(s.eventHub, jobEvent(s.workspace.ID, job))
		}
		eventutil.Post(s.eventHub, gitEvent(models.EventGitCommitFailed, s.workspace.ID, record))
		return
	}
	if err == nil {
		job, _ = s.jobs.MarkSucceeded(job, "git commit succeeded")
		record.JobID = job.ID
		eventutil.Post(s.eventHub, jobEvent(s.workspace.ID, job))
	}
	eventutil.Post(s.eventHub, gitEvent(models.EventGitCommitSucceeded, s.workspace.ID, record))
}

func (s *Service) takePending() ([]string, []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
	paths := make([]string, 0, len(s.pending))
	for path := range s.pending {
		paths = append(paths, path)
	}
	resourceIDs := make([]string, 0, len(s.resourceIDs))
	for id := range s.resourceIDs {
		resourceIDs = append(resourceIDs, id)
	}
	s.pending = map[string]struct{}{}
	s.resourceIDs = map[string]struct{}{}
	sort.Strings(paths)
	sort.Strings(resourceIDs)
	return paths, resourceIDs
}

func (s *Service) Commit(ctx context.Context, paths []string, resourceIDs []string) (models.GitCommitRecord, error) {
	if s.workspace == nil {
		return models.GitCommitRecord{}, errors.New("workspace is nil")
	}
	for _, path := range paths {
		targetPath := filepath.Join(s.workspace.RootPath, filepath.FromSlash(path))
		if err := workspace.EnsureInside(s.workspace.RootPath, targetPath); err != nil {
			return models.GitCommitRecord{}, err
		}
	}
	if err := s.ensureRepository(ctx); err != nil {
		return models.GitCommitRecord{}, err
	}
	args := append([]string{"-C", s.workspace.RootPath, "add", "--"}, paths...)
	if err := runGit(ctx, args...); err != nil {
		return models.GitCommitRecord{}, err
	}
	message := commitMessage(resourceIDs)
	if err := runGit(ctx, "-C", s.workspace.RootPath, "commit", "-m", message); err != nil {
		return models.GitCommitRecord{}, err
	}
	hashBytes, err := outputGit(ctx, "-C", s.workspace.RootPath, "rev-parse", "--short", "HEAD")
	if err != nil {
		return models.GitCommitRecord{}, err
	}
	return models.GitCommitRecord{
		CommitHash:  strings.TrimSpace(string(hashBytes)),
		Message:     message,
		Paths:       paths,
		ResourceIDs: resourceIDs,
		CommittedAt: time.Now().UTC(),
	}, nil
}

func (s *Service) ensureRepository(ctx context.Context) error {
	if err := runGit(ctx, "-C", s.workspace.RootPath, "rev-parse", "--is-inside-work-tree"); err == nil {
		return nil
	}
	return runGit(ctx, "-C", s.workspace.RootPath, "init")
}

func runGit(ctx context.Context, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", args...)
	raw, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git %s: %s: %w", strings.Join(args, " "), strings.TrimSpace(string(raw)), err)
	}
	return nil
}

func outputGit(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	raw, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("git %s: %s: %w", strings.Join(args, " "), strings.TrimSpace(string(raw)), err)
	}
	return raw, nil
}

func commitMessage(resourceIDs []string) string {
	if len(resourceIDs) == 0 {
		return "thoughtflow: update workspace"
	}
	if len(resourceIDs) == 1 {
		return "thoughtflow: update " + resourceIDs[0]
	}
	return fmt.Sprintf("thoughtflow: update %d resources", len(resourceIDs))
}

func jobEvent(workspaceID string, job models.Job) models.DomainEvent {
	return models.DomainEvent{
		EventType:      models.EventJobUpdated,
		SourceUnit:     "git-sync",
		OccurredAt:     time.Now().UTC(),
		WorkspaceID:    workspaceID,
		ResourceType:   job.ResourceType,
		ResourceID:     job.ResourceID,
		PayloadVersion: 1,
		Payload:        job,
	}
}

func gitEvent(eventType string, workspaceID string, record models.GitCommitRecord) models.DomainEvent {
	return models.DomainEvent{
		EventType:      eventType,
		SourceUnit:     "git-sync",
		OccurredAt:     time.Now().UTC(),
		WorkspaceID:    workspaceID,
		ResourceType:   models.ResourceTypeWorkspace,
		ResourceID:     workspaceID,
		PayloadVersion: 1,
		Payload:        record,
	}
}
