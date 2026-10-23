package biz

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
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
	"thoughtflow/internal/pkg/objectlocation"
	"thoughtflow/internal/pkg/workspace"
)

type Service struct {
	workspace  *models.Workspace
	jobs       *jobstore.Store
	eventHub   event.Hub
	background task.BackgroundRoutine
	enabled    bool
	debounce   time.Duration

	flushMu      sync.Mutex
	inflight     *models.GitChangeSet
	retryDelay   time.Duration
	closed       bool
	pendingError string
	commitMu     sync.Mutex
	mu           sync.Mutex
	pending      map[string]struct{}
	resourceIDs  map[string]struct{}
	reasons      map[string]struct{}
	changeSetID  string
	createdAt    time.Time
	debounceTo   time.Time
	timer        *time.Timer
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
		reasons:     map[string]struct{}{},
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
	s.EnqueueChangeSet(payload.Paths, payload.Reason, payload.ResourceIDs)
	if result != nil {
		result.Set(nil, nil)
	}
}

func (s *Service) Enqueue(paths []string, resourceIDs []string) {
	_ = s.EnqueueChangeSet(paths, "", resourceIDs)
}

func (s *Service) EnqueueChangeSet(paths []string, reason string, resourceIDs []string) models.GitChangeSet {
	if !s.enabled {
		return models.GitChangeSet{}
	}
	now := time.Now().UTC()
	s.mu.Lock()
	for _, path := range paths {
		path, ok := normalizedCommitPath(path)
		if !ok || s.isRuntimeCommitPath(path) {
			continue
		}
		s.pending[path] = struct{}{}
	}
	for _, id := range resourceIDs {
		if strings.TrimSpace(id) != "" {
			s.resourceIDs[id] = struct{}{}
		}
	}
	if reason = strings.TrimSpace(reason); reason != "" {
		s.reasons[reason] = struct{}{}
	}
	if len(s.pending) == 0 {
		s.mu.Unlock()
		return models.GitChangeSet{}
	}
	if s.changeSetID == "" {
		s.changeSetID = models.NewJobID("git_changeset", now)
		s.createdAt = now
	}
	if s.timer != nil {
		s.timer.Stop()
	}
	delay := s.debounce
	if delay < 0 {
		delay = 0
	}
	s.debounceTo = now.Add(delay)
	if !s.closed {
		s.timer = time.AfterFunc(delay, func() { s.flushAsync() })
	}
	changeSet := s.pendingChangeSetLocked()
	s.persistPendingLocked()
	s.mu.Unlock()
	return changeSet
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
	s.flushMu.Lock()
	defer s.flushMu.Unlock()
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return
	}
	paths, resourceIDs := s.takePending()
	if len(paths) == 0 {
		return
	}

	job, err := s.jobs.Create(models.JobTypeGitCommit, models.ResourceTypeWorkspace, s.workspace.ID, "git commit queued")
	if err == nil {
		job, _ = s.jobs.MarkRunning(job)
		eventutil.Post(s.eventHub, jobEvent(s.workspace.ID, job))
	}

	commitCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	record, commitErr := s.Commit(commitCtx, paths, resourceIDs)
	if commitErr != nil {
		errRef := models.NewErrorRef("thoughtflow.git.commit_failed", commitErr.Error(), true)
		record.Error = &errRef
		if err == nil {
			job, _ = s.jobs.MarkFailed(job, errRef)
			record.JobID = job.ID
			eventutil.Post(s.eventHub, jobEvent(s.workspace.ID, job))
		}
		eventutil.Post(s.eventHub, gitEvent(models.EventGitCommitFailed, s.workspace.ID, record))
		s.finishPending(false)
		return
	}
	s.finishPending(true)
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
	snapshot := s.pendingChangeSetLocked()
	if len(paths) > 0 {
		s.inflight = &snapshot
	}
	s.pending = map[string]struct{}{}
	s.resourceIDs = map[string]struct{}{}
	s.reasons = map[string]struct{}{}
	s.changeSetID = ""
	s.createdAt = time.Time{}
	s.debounceTo = time.Time{}
	sort.Strings(paths)
	sort.Strings(resourceIDs)
	return paths, resourceIDs
}

func (s *Service) pendingChangeSetLocked() models.GitChangeSet {
	paths := make([]string, 0, len(s.pending))
	for path := range s.pending {
		paths = append(paths, path)
	}
	resourceIDs := make([]string, 0, len(s.resourceIDs))
	for id := range s.resourceIDs {
		resourceIDs = append(resourceIDs, id)
	}
	reasons := make([]string, 0, len(s.reasons))
	for reason := range s.reasons {
		reasons = append(reasons, reason)
	}
	sort.Strings(paths)
	sort.Strings(resourceIDs)
	sort.Strings(reasons)
	return models.GitChangeSet{
		ID:            s.changeSetID,
		Paths:         paths,
		Reason:        strings.Join(reasons, ","),
		ResourceIDs:   resourceIDs,
		CreatedAt:     s.createdAt,
		DebounceUntil: s.debounceTo,
	}
}

func normalizedCommitPath(value string) (string, bool) {
	value = filepath.ToSlash(strings.TrimSpace(value))
	if value == "" {
		return "", false
	}
	value = filepath.ToSlash(filepath.Clean(filepath.FromSlash(value)))
	if value == "." || value == ".." || strings.HasPrefix(value, "../") || filepath.IsAbs(value) {
		return "", false
	}
	lower := strings.ToLower(value)
	for _, part := range strings.Split(lower, "/") {
		if part == ".private" || part == "private" || strings.HasPrefix(part, ".stage-") || strings.HasPrefix(part, ".write-") {
			return "", false
		}
	}
	if strings.HasPrefix(lower, ".content-registry/") {
		parts := strings.Split(lower, "/")
		allowed := lower == ".content-registry/schema.json" || (len(parts) == 3 && (parts[1] == "objects" || parts[1] == "resources" || parts[1] == "topic-deletions") && strings.HasSuffix(lower, ".json")) || (len(parts) == 4 && parts[1] == "bindings" && strings.HasSuffix(lower, ".json"))
		if !allowed {
			return "", false
		}
	}
	if strings.HasPrefix(lower, ".scratchpad/") || (strings.HasPrefix(lower, "topics/") && strings.HasSuffix(lower, "/candidates.yaml")) || strings.HasPrefix(lower, "documents/capture-") || strings.HasPrefix(lower, "documents/compose-") {
		return "", false
	}
	if lower == ".thoughtflow" || strings.HasPrefix(lower, ".thoughtflow/") {
		return "", false
	}
	base := strings.ToLower(filepath.Base(value))
	if strings.HasSuffix(base, ".duckdb") || strings.Contains(base, ".duckdb.") {
		return "", false
	}
	if !businessCommitPath(value) {
		return "", false
	}
	return value, true
}
func businessCommitPath(value string) bool {
	p := strings.Split(value, "/")
	if len(p) < 2 {
		return false
	}
	switch p[0] {
	case ".content-registry":
		return true
	case "thoughts":
		return strings.HasSuffix(value, ".md")
	case "attachments":
		return len(p) == 3
	case "document-formats":
		return len(p) == 4 && p[1] == "published" && strings.HasSuffix(p[3], ".md")
	case "documents":
		return documentCommitSuffix(p[2:])
	case "topics":
		if len(p) == 3 {
			switch p[2] {
			case "topic.yaml", "index.md", "catalog.json", "catalog.md", "catalog.archived.json", "resources.json", "storage.snapshot.json":
				return true
			}
			return false
		}
		if len(p) < 4 {
			return false
		}
		switch p[2] {
		case "thoughts":
			return len(p) == 4 && strings.HasSuffix(value, ".md")
		case "memberships", "approvals":
			return len(p) == 4 && strings.HasSuffix(value, ".yaml")
		case "documents":
			return len(p) > 4 && documentCommitSuffix(p[4:])
		case "attachments":
			return len(p) == 5
		case "assets":
			return len(p) == 4
		case "references":
			return len(p) == 5 && (p[4] == "snapshot.json" || p[4] == "body.md") || len(p) == 6 && p[4] == "assets"
		}
		return false
	}
	return false
}
func documentCommitSuffix(p []string) bool {
	if len(p) == 1 {
		return p[0] == "head.json"
	}
	if len(p) == 2 {
		return (p[0] == "reports" || p[0] == "finalizations") && strings.HasSuffix(p[1], ".json")
	}
	if len(p) == 3 && p[0] == "revisions" {
		switch p[2] {
		case "manifest.json", "body.md", "contract.json", "source-manifest.json", "profile.snapshot.md", "projection.json", "resources.json":
			return true
		}
	}
	return false
}

func (s *Service) isRuntimeCommitPath(path string) bool {
	if s == nil || s.workspace == nil || strings.TrimSpace(s.workspace.RootPath) == "" || strings.TrimSpace(s.workspace.RuntimePath) == "" {
		return false
	}
	rel, err := filepath.Rel(s.workspace.RootPath, s.workspace.RuntimePath)
	if err != nil {
		return false
	}
	rel = filepath.ToSlash(filepath.Clean(rel))
	if rel == "." {
		return true
	}
	if rel == ".." || strings.HasPrefix(rel, "../") || filepath.IsAbs(rel) {
		return false
	}
	lowerPath := strings.ToLower(filepath.ToSlash(path))
	lowerRel := strings.ToLower(rel)
	return lowerPath == lowerRel || strings.HasPrefix(lowerPath, lowerRel+"/")
}

func (s *Service) Commit(ctx context.Context, paths []string, resourceIDs []string) (models.GitCommitRecord, error) {
	if s.workspace == nil {
		return models.GitCommitRecord{}, errors.New("workspace is nil")
	}
	s.commitMu.Lock()
	defer s.commitMu.Unlock()
	if err := s.ensureRepository(ctx); err != nil {
		return models.GitCommitRecord{}, err
	}
	conflicts, err := outputGit(ctx, "-C", s.workspace.RootPath, "ls-files", "-u")
	if err != nil {
		return models.GitCommitRecord{}, err
	}
	if len(conflicts) > 0 {
		return models.GitCommitRecord{}, errors.New("git has unresolved conflicts; resolve them before retrying")
	}
	selected := []string{}
	pathspecs := []string{}
	addPathspecs := []string{}
	for _, path := range paths {
		path, ok := normalizedCommitPath(path)
		if !ok || s.isRuntimeCommitPath(path) {
			continue
		}
		targetPath, pathErr := objectlocation.SafePath(s.workspace.RootPath, path)
		if pathErr != nil {
			return models.GitCommitRecord{}, pathErr
		}
		if err := workspace.EnsureInside(s.workspace.RootPath, targetPath); err != nil {
			return models.GitCommitRecord{}, err
		}
		alreadyStagedDeletion := false
		info, err := os.Stat(targetPath)
		if errors.Is(err, os.ErrNotExist) {
			// A missing, previously tracked path is a deletion. Missing optional
			// layout files that were never tracked must still be skipped.
			tracked, gitErr := outputGit(ctx, "-C", s.workspace.RootPath, "ls-files", "-z", "--", ":(literal)"+path)
			if gitErr != nil {
				return models.GitCommitRecord{}, gitErr
			}
			if len(tracked) == 0 {
				alreadyStagedDeletion = true
				// An already-staged deletion has also left the real index.
				// Check HEAD before treating it as a never-created path.
				if _, err := outputGit(ctx, "-C", s.workspace.RootPath, "rev-parse", "--verify", "HEAD"); err == nil {
					tracked, gitErr = outputGit(ctx, "-C", s.workspace.RootPath, "ls-tree", "-z", "--name-only", "HEAD", "--", path)
					if gitErr != nil {
						return models.GitCommitRecord{}, gitErr
					}
				}
				if len(tracked) == 0 {
					continue
				}
			}
		} else if err != nil {
			return models.GitCommitRecord{}, fmt.Errorf("stat %s: %w", targetPath, err)
		} else if info.IsDir() {
			return models.GitCommitRecord{}, fmt.Errorf("commit path must be a file: %s", path)
		}
		selected = append(selected, path)
		pathspecs = append(pathspecs, ":(literal)"+path)
		if !alreadyStagedDeletion {
			addPathspecs = append(addPathspecs, ":(literal)"+path)
		}
	}
	message := commitMessage(resourceIDs)
	if len(addPathspecs) > 0 {
		args := append([]string{"-C", s.workspace.RootPath, "add", "-A", "--"}, addPathspecs...)
		if err := runGit(ctx, args...); err != nil {
			return models.GitCommitRecord{}, err
		}
	}
	args := []string{"-C", s.workspace.RootPath, "commit", "--only", "--allow-empty", "-m", message}
	if len(pathspecs) > 0 {
		args = append(append(args, "--"), pathspecs...)
	}
	if err := runGit(ctx, args...); err != nil {
		return models.GitCommitRecord{}, err
	}
	hashBytes, err := outputGit(ctx, "-C", s.workspace.RootPath, "rev-parse", "--short", "HEAD")
	if err != nil {
		return models.GitCommitRecord{}, err
	}
	return models.GitCommitRecord{
		CommitHash:  strings.TrimSpace(string(hashBytes)),
		Message:     message,
		Paths:       selected,
		ResourceIDs: resourceIDs,
		CommittedAt: time.Now().UTC(),
	}, nil
}

func (s *Service) RecentCommits(ctx context.Context, relativePath string, resourceID string, limit int) []models.GitCommitRecord {
	if s == nil || s.workspace == nil || strings.TrimSpace(s.workspace.RootPath) == "" || strings.TrimSpace(relativePath) == "" || limit == 0 {
		return nil
	}
	if _, err := exec.LookPath("git"); err != nil {
		return nil
	}
	gitCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := runGit(gitCtx, "-C", s.workspace.RootPath, "rev-parse", "--is-inside-work-tree"); err != nil {
		return nil
	}
	if limit < 0 {
		limit = 5
	}
	path := filepath.ToSlash(relativePath)
	raw, err := outputGit(gitCtx, "-C", s.workspace.RootPath, "log", "-n", fmt.Sprintf("%d", limit), "--format=%H%x1f%ct%x1f%s", "--", path)
	if err != nil {
		return nil
	}
	records := []models.GitCommitRecord{}
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\x1f", 3)
		if len(parts) != 3 {
			continue
		}
		seconds, err := parseUnixSeconds(parts[1])
		if err != nil {
			continue
		}
		records = append(records, models.GitCommitRecord{
			CommitHash:  parts[0],
			Message:     parts[2],
			Paths:       []string{path},
			ResourceIDs: []string{resourceID},
			CommittedAt: time.Unix(seconds, 0).UTC(),
		})
	}
	return records
}

func (s *Service) RuntimeStatus(ctx context.Context) models.GitRuntimeStatus {
	status := models.GitRuntimeStatus{Status: "disabled"}
	if s == nil || s.workspace == nil {
		status.Status = "degraded"
		status.Error = "workspace is not ready"
		return status
	}
	status.Enabled = s.enabled
	if !s.enabled {
		return status
	}
	if _, err := exec.LookPath("git"); err != nil {
		status.Status = "degraded"
		status.Error = err.Error()
		return status
	}
	gitCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	status.Repository = gitCommandOK(gitCtx, s.workspace.RootPath, "rev-parse", "--is-inside-work-tree")
	nameOK := gitCommandOK(gitCtx, s.workspace.RootPath, "config", "--get", "user.name")
	emailOK := gitCommandOK(gitCtx, s.workspace.RootPath, "config", "--get", "user.email")
	status.IdentityConfigured = nameOK && emailOK
	if status.Repository {
		raw, err := outputGit(gitCtx, "-C", s.workspace.RootPath, "status", "--porcelain")
		if err == nil {
			status.Dirty = strings.TrimSpace(string(raw)) != ""
		}
	}
	if !status.IdentityConfigured {
		status.Status = "degraded"
		status.Error = "git user.name and user.email are required for commits"
		return status
	}
	if !status.Repository {
		status.Status = "pending_init"
		return status
	}
	s.mu.Lock()
	pendingError := s.pendingError
	pending := len(s.pending) > 0 || s.inflight != nil
	s.mu.Unlock()
	status.Status = "ready"
	if pending {
		status.Status = "pending"
	}
	if pendingError != "" {
		status.Status = "degraded"
		status.Error = "persist pending changes: " + pendingError
	}
	return status
}

func gitCommandOK(ctx context.Context, rootPath string, args ...string) bool {
	cmdArgs := append([]string{"-C", rootPath}, args...)
	_, err := outputGit(ctx, cmdArgs...)
	return err == nil
}

func parseUnixSeconds(value string) (int64, error) {
	var seconds int64
	_, err := fmt.Sscanf(value, "%d", &seconds)
	return seconds, err
}

// ensureRepository makes sure the workspace path is its own git
// work tree. A bare `rev-parse --is-inside-work-tree` returns success
// when the workspace is nested inside any ancestor git repo (e.g. the
// checkout of thought-flow itself), so the previous version of this
// function happily reused the parent repo — only for the subsequent
// `git add` to fail with "ignored by .gitignore" once the parent
// repo's ignore rules covered the workspace directory. Verifying the
// toplevel equals the workspace path catches that case and forces a
// local `git init` so commits land in a dedicated repo.
func (s *Service) ensureRepository(ctx context.Context) error {
	toplevel, toplevelErr := outputGit(ctx, "-C", s.workspace.RootPath, "rev-parse", "--show-toplevel")
	if toplevelErr == nil {
		clean, err := filepath.EvalSymlinks(strings.TrimSpace(string(toplevel)))
		if err != nil {
			clean = strings.TrimSpace(string(toplevel))
		}
		root, err := filepath.EvalSymlinks(s.workspace.RootPath)
		if err != nil {
			root = s.workspace.RootPath
		}
		if filepath.Clean(clean) == filepath.Clean(root) {
			return nil
		}
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

// pending lives in runtime, outside content commits, and includes an in-flight
// snapshot until Git confirms success. New events during commit stay pending.
func (s *Service) pendingPath() string {
	if s.workspace == nil || s.workspace.RuntimePath == "" {
		return ""
	}
	return filepath.Join(s.workspace.RuntimePath, "git-pending.json")
}
func (s *Service) persistPendingLocked() {
	path := s.pendingPath()
	if path == "" {
		return
	}
	pending := s.pendingChangeSetLocked()
	if s.inflight != nil {
		pending.Paths = append(pending.Paths, s.inflight.Paths...)
		pending.ResourceIDs = append(pending.ResourceIDs, s.inflight.ResourceIDs...)
		pending.Reason += "," + s.inflight.Reason
	}
	if len(pending.Paths) == 0 {
		err := os.Remove(path)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			s.pendingError = err.Error()
		}
		return
	}
	raw, err := json.Marshal(pending)
	if err == nil {
		err = os.MkdirAll(filepath.Dir(path), 0700)
	}
	if err == nil {
		var tmp *os.File
		tmp, err = os.CreateTemp(filepath.Dir(path), ".git-pending-*.tmp")
		if err == nil {
			defer os.Remove(tmp.Name())
			_, err = tmp.Write(raw)
			if err == nil {
				err = tmp.Sync()
			}
			closeErr := tmp.Close()
			if err == nil {
				err = closeErr
			}
			if err == nil {
				err = os.Rename(tmp.Name(), path)
			}
		}
	}
	if err != nil {
		s.pendingError = err.Error()
	} else {
		s.pendingError = ""
	}
}
func (s *Service) finishPending(success bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !success && s.inflight != nil {
		for _, path := range s.inflight.Paths {
			s.pending[path] = struct{}{}
		}
		for _, id := range s.inflight.ResourceIDs {
			s.resourceIDs[id] = struct{}{}
		}
		if s.changeSetID == "" {
			s.changeSetID = s.inflight.ID
			s.createdAt = s.inflight.CreatedAt
		}
		s.reasons[s.inflight.Reason] = struct{}{}
	}
	s.inflight = nil
	if success {
		s.retryDelay = 0
	} else if !s.closed {
		if s.retryDelay == 0 {
			s.retryDelay = time.Second
		} else {
			s.retryDelay *= 2
		}
		if s.retryDelay > time.Minute {
			s.retryDelay = time.Minute
		}
		if s.timer != nil {
			s.timer.Stop()
		}
		s.debounceTo = time.Now().Add(s.retryDelay)
		s.timer = time.AfterFunc(s.retryDelay, s.flushAsync)
	}
	s.persistPendingLocked()
}
func (s *Service) Recover() error {
	path := s.pendingPath()
	if !s.enabled || path == "" {
		return nil
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var pending models.GitChangeSet
	if err = json.Unmarshal(raw, &pending); err != nil {
		return err
	}
	s.EnqueueChangeSet(pending.Paths, pending.Reason, pending.ResourceIDs)
	return nil
}
func (s *Service) Close() {
	s.mu.Lock()
	s.closed = true
	if s.timer != nil {
		s.timer.Stop()
	}
	s.mu.Unlock()
	s.flushMu.Lock()
	defer s.flushMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.persistPendingLocked()
}
