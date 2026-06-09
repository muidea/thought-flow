package synthesisstore

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

func (s *Store) SaveDraft(ctx context.Context, draft models.SynthesisDraft) (models.SynthesisDraft, error) {
	_ = ctx
	now := time.Now().UTC()
	draft = normalizeDraft(draft, now)
	if strings.TrimSpace(draft.ID) == "" {
		return models.SynthesisDraft{}, errors.New("draft id is required")
	}
	if len(draft.ThoughtIDs) == 0 {
		return models.SynthesisDraft{}, errors.New("thought_ids is required")
	}
	if strings.TrimSpace(draft.Content) == "" {
		return models.SynthesisDraft{}, errors.New("content is required")
	}
	if len(draft.History) == 0 {
		draft.History = append(draft.History, models.SynthesisDraftHistory{
			Status:  draft.Status,
			Message: "draft created",
			At:      draft.CreatedAt,
		})
	}
	if err := s.writeDraft(draft); err != nil {
		return models.SynthesisDraft{}, err
	}
	return draft, nil
}

func (s *Store) ListDrafts(ctx context.Context) ([]models.SynthesisDraft, error) {
	_ = ctx
	dir, err := s.draftDir()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return []models.SynthesisDraft{}, nil
	}
	if err != nil {
		return nil, err
	}
	drafts := []models.SynthesisDraft{}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".yaml" {
			continue
		}
		draftID := strings.TrimSuffix(entry.Name(), ".yaml")
		draft, err := s.GetDraft(ctx, draftID)
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

func (s *Store) GetDraft(ctx context.Context, draftID string) (models.SynthesisDraft, error) {
	_ = ctx
	path, err := s.draftPath(draftID)
	if err != nil {
		return models.SynthesisDraft{}, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return models.SynthesisDraft{}, err
	}
	var draft models.SynthesisDraft
	if err := yaml.Unmarshal(raw, &draft); err != nil {
		return models.SynthesisDraft{}, err
	}
	return normalizeDraft(draft, time.Now().UTC()), nil
}

func (s *Store) MarkSaved(ctx context.Context, draftID string, content string, thought models.Thought) (models.SynthesisDraft, error) {
	draft, err := s.GetDraft(ctx, draftID)
	if err != nil {
		return models.SynthesisDraft{}, err
	}
	now := time.Now().UTC()
	draft.Status = "saved"
	draft.Content = strings.TrimSpace(content)
	draft.SavedThoughtID = thought.ID
	draft.UpdatedAt = now
	draft.SavedAt = &now
	draft.History = append(draft.History, models.SynthesisDraftHistory{
		Status:    "saved",
		Message:   "saved as thought",
		ThoughtID: thought.ID,
		At:        now,
	})
	if err := s.writeDraft(draft); err != nil {
		return models.SynthesisDraft{}, err
	}
	return draft, nil
}

func (s *Store) writeDraft(draft models.SynthesisDraft) error {
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
	path := filepath.Join(s.rootPath, "synthesis", "drafts")
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

func normalizeDraft(draft models.SynthesisDraft, now time.Time) models.SynthesisDraft {
	if draft.Status == "" {
		draft.Status = "draft"
	}
	if draft.CreatedAt.IsZero() {
		draft.CreatedAt = now
	}
	if draft.UpdatedAt.IsZero() {
		draft.UpdatedAt = draft.CreatedAt
	}
	return draft
}
