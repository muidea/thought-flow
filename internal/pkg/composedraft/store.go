// Package composedraft persists Compose drafts to
// workspace/compose/drafts/{draft_id}.yaml. It is the YAML-backed
// successor to internal/pkg/synthesisstore; the two stores are not
// wire-compatible because ComposeDraft carries a discriminated
// sources[] list (thought / search_result / topic_section /
// capture_session) instead of the older thought_ids[] shape.
//
// The store is intentionally narrow: it only owns the draft file
// (CRUD + a single status transition draft → saved). Generation,
// hydration of Search/Topic/Capture source metadata, and the
// downstream capture-to-Thought call all live in the compose module
// and capture module respectively.
package composedraft

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"thoughtflow/internal/pkg/models"
	"thoughtflow/internal/pkg/workspace"
)

type Store struct {
	rootPath string
}

func New(rootPath string) *Store {
	return &Store{rootPath: rootPath}
}

func (s *Store) SaveDraft(_ context.Context, draft models.ComposeDraft) (models.ComposeDraft, error) {
	now := time.Now().UTC()
	draft = normalizeDraft(draft, now)
	if strings.TrimSpace(draft.ID) == "" {
		return models.ComposeDraft{}, errors.New("draft id is required")
	}
	if len(draft.Sources) == 0 {
		return models.ComposeDraft{}, errors.New("sources are required")
	}
	if strings.TrimSpace(draft.Content) == "" {
		return models.ComposeDraft{}, errors.New("content is required")
	}
	if len(draft.History) == 0 {
		draft.History = append(draft.History, models.ComposeDraftHistory{
			Status:  draft.Status,
			Message: "draft created",
			At:      draft.CreatedAt,
		})
	}
	if err := s.writeDraft(draft); err != nil {
		return models.ComposeDraft{}, err
	}
	return draft, nil
}

func (s *Store) ListDrafts(_ context.Context) ([]models.ComposeDraft, error) {
	dir, err := s.draftDir()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return []models.ComposeDraft{}, nil
	}
	if err != nil {
		return nil, err
	}
	drafts := []models.ComposeDraft{}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".yaml" {
			continue
		}
		draftID := strings.TrimSuffix(entry.Name(), ".yaml")
		draft, err := s.GetDraft(context.Background(), draftID)
		if err != nil {
			return nil, err
		}
		drafts = append(drafts, draft)
	}
	sort.Slice(drafts, func(left, right int) bool {
		return drafts[left].UpdatedAt.After(drafts[right].UpdatedAt)
	})
	return drafts, nil
}

func (s *Store) GetDraft(_ context.Context, draftID string) (models.ComposeDraft, error) {
	path, err := s.draftPath(draftID)
	if err != nil {
		return models.ComposeDraft{}, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return models.ComposeDraft{}, err
	}
	var draft models.ComposeDraft
	if err := yaml.Unmarshal(raw, &draft); err != nil {
		return models.ComposeDraft{}, err
	}
	return normalizeDraft(draft, time.Now().UTC()), nil
}

// MarkSaved flips the draft's status from draft → saved, records
// the created Thought ID, and appends a history entry. It is
// called by the compose module once the new Thought has been
// captured (source=compose) via the capture module.
func (s *Store) MarkSaved(_ context.Context, draftID string, content string, thought models.Thought) (models.ComposeDraft, error) {
	draft, err := s.GetDraft(context.Background(), draftID)
	if err != nil {
		return models.ComposeDraft{}, err
	}
	now := time.Now().UTC()
	draft.Status = models.ComposeStatusSaved
	draft.Content = strings.TrimSpace(content)
	draft.SavedThoughtID = thought.ID
	draft.UpdatedAt = now
	draft.SavedAt = &now
	draft.History = append(draft.History, models.ComposeDraftHistory{
		Status:    models.ComposeStatusSaved,
		Message:   "saved as thought",
		ThoughtID: thought.ID,
		At:        now,
	})
	if err := s.writeDraft(draft); err != nil {
		return models.ComposeDraft{}, err
	}
	return draft, nil
}

// Delete removes a draft file. The HTTP layer exposes this only to
// tests; the Web drawer does not currently surface a delete button.
func (s *Store) Delete(_ context.Context, draftID string) error {
	path, err := s.draftPath(draftID)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (s *Store) writeDraft(draft models.ComposeDraft) error {
	path, err := s.draftPath(draft.ID)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	raw, err := yaml.Marshal(draft)
	if err != nil {
		return err
	}
	tmp := fmt.Sprintf("%s.%d.tmp", path, time.Now().UnixNano())
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	defer func() {
		_ = os.Remove(tmp)
	}()
	return os.Rename(tmp, path)
}

func (s *Store) draftDir() (string, error) {
	if strings.TrimSpace(s.rootPath) == "" {
		return "", errors.New("root path is required")
	}
	path := filepath.Join(s.rootPath, "compose", "drafts")
	if err := workspace.EnsureInside(s.rootPath, path); err != nil {
		return "", err
	}
	return path, nil
}

func (s *Store) draftPath(draftID string) (string, error) {
	draftID = strings.TrimSpace(draftID)
	if draftID == "" {
		return "", errors.New("draft id is required")
	}
	if strings.ContainsAny(draftID, `/\`) {
		return "", fmt.Errorf("invalid draft id %q", draftID)
	}
	dir, err := s.draftDir()
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, draftID+".yaml")
	if err := workspace.EnsureInside(s.rootPath, path); err != nil {
		return "", err
	}
	return path, nil
}

func normalizeDraft(draft models.ComposeDraft, now time.Time) models.ComposeDraft {
	if draft.Status == "" {
		draft.Status = models.ComposeStatusDraft
	}
	if draft.Format == "" {
		draft.Format = models.ComposeFormatSummary
	}
	if draft.CreatedAt.IsZero() {
		draft.CreatedAt = now
	}
	if draft.UpdatedAt.IsZero() {
		draft.UpdatedAt = draft.CreatedAt
	}
	return draft
}
