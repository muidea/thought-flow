//go:build !duckdb

package searchdb

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"thoughtflow/internal/pkg/markdown"
	"thoughtflow/internal/pkg/models"
)

type Store struct {
	mu    sync.RWMutex
	items map[string]indexedThought
}

type indexedThought struct {
	thought models.Thought
	content models.ThoughtContent
	text    string
	tags    []string
}

func Open(ctx context.Context, path string) (*Store, error) {
	_ = ctx
	if path == "" {
		return nil, errors.New("index path is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	return &Store{items: map[string]indexedThought{}}, nil
}

func (s *Store) Close() error {
	return nil
}

func (s *Store) Init(ctx context.Context) error {
	_ = ctx
	return nil
}

func (s *Store) IndexThought(ctx context.Context, thought models.Thought, content models.ThoughtContent) error {
	_ = ctx
	if thought.ID == "" {
		return errors.New("thought id is required")
	}
	if thought.DisplayTitle == "" {
		thought.DisplayTitle = firstNonEmpty(thought.UserTitle, thought.ExtractedTitle, thought.ID)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items[thought.ID] = indexedThought{
		thought: thought,
		content: content,
		text:    buildSearchText(thought, content),
		tags:    append(append([]string{}, thought.UserTags...), thought.AITags...),
	}
	return nil
}

func (s *Store) ReindexWorkspace(ctx context.Context, rootPath string) (int, error) {
	_ = ctx
	next := map[string]indexedThought{}
	thoughtsPath := filepath.Join(rootPath, "thoughts")
	count := 0
	err := filepath.WalkDir(thoughtsPath, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || filepath.Ext(path) != ".md" {
			return nil
		}
		thoughtID := strings.TrimSuffix(filepath.Base(path), ".md")
		thought, content, err := markdown.ReadThought(rootPath, thoughtID)
		if err != nil {
			return err
		}
		if thought.DisplayTitle == "" {
			thought.DisplayTitle = firstNonEmpty(thought.UserTitle, thought.ExtractedTitle, thought.ID)
		}
		next[thought.ID] = indexedThought{
			thought: thought,
			content: content,
			text:    buildSearchText(thought, content),
			tags:    append(append([]string{}, thought.UserTags...), thought.AITags...),
		}
		count++
		return nil
	})
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	s.mu.Lock()
	s.items = next
	s.mu.Unlock()
	return count, nil
}

func (s *Store) Search(ctx context.Context, query models.SearchQuery) (models.SearchResponse, error) {
	_ = ctx
	page := query.Page
	if page <= 0 {
		page = 1
	}
	pageSize := query.PageSize
	if pageSize <= 0 || pageSize > 100 {
		pageSize = 20
	}
	q := strings.ToLower(strings.TrimSpace(query.Query))

	s.mu.RLock()
	all := make([]indexedThought, 0, len(s.items))
	for _, item := range s.items {
		if q == "" || strings.Contains(strings.ToLower(item.text), q) || strings.Contains(strings.ToLower(item.thought.DisplayTitle), q) {
			all = append(all, item)
		}
	}
	s.mu.RUnlock()

	sort.Slice(all, func(left, right int) bool {
		return all[left].thought.UpdatedAt.After(all[right].thought.UpdatedAt)
	})
	total := len(all)
	start := (page - 1) * pageSize
	if start > total {
		start = total
	}
	end := start + pageSize
	if end > total {
		end = total
	}

	items := []models.SearchResult{}
	for _, item := range all[start:end] {
		keywordScore := keywordScore(item.text, query.Query)
		items = append(items, models.SearchResult{
			ThoughtID:     item.thought.ID,
			Title:         item.thought.DisplayTitle,
			Snippet:       snippet(item.text, query.Query),
			Score:         keywordScore,
			KeywordScore:  keywordScore,
			SemanticScore: 0,
			RecencyScore:  0,
			Path:          item.thought.Path,
			Topics:        item.thought.TopicIDs,
			Tags:          item.tags,
		})
	}
	return models.SearchResponse{
		Items:    items,
		Page:     page,
		PageSize: pageSize,
		Total:    total,
	}, nil
}

func buildSearchText(thought models.Thought, content models.ThoughtContent) string {
	parts := []string{
		thought.UserTitle,
		thought.ExtractedTitle,
		thought.Summary,
		strings.Join(thought.UserTags, " "),
		strings.Join(thought.AITags, " "),
		content.Original,
		content.ExtractedContent,
		content.AINotes,
	}
	return strings.Join(parts, "\n")
}

func snippet(text string, query string) string {
	text = strings.Join(strings.Fields(text), " ")
	if len(text) <= 240 {
		return text
	}
	lower := strings.ToLower(text)
	q := strings.ToLower(strings.TrimSpace(query))
	if q != "" {
		idx := strings.Index(lower, q)
		if idx > 0 {
			start := idx - 80
			if start < 0 {
				start = 0
			}
			end := start + 240
			if end > len(text) {
				end = len(text)
			}
			return text[start:end]
		}
	}
	return text[:240]
}

func keywordScore(text string, query string) float64 {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return 0.5
	}
	lower := strings.ToLower(text)
	if !strings.Contains(lower, q) {
		return 0
	}
	count := strings.Count(lower, q)
	score := 0.5 + float64(count)*0.1
	if score > 1 {
		return 1
	}
	return score
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
