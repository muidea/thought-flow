//go:build !duckdb

package searchdb

import (
	"context"
	"errors"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"thoughtflow/internal/pkg/markdown"
	"thoughtflow/internal/pkg/models"
)

type Store struct {
	mu         sync.RWMutex
	items      map[string]indexedThought
	embeddings map[string]models.EmbeddingRecord
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
	return &Store{items: map[string]indexedThought{}, embeddings: map[string]models.EmbeddingRecord{}}, nil
}

func (s *Store) Close() error {
	return nil
}

func (s *Store) IndexEmbedding(ctx context.Context, record models.EmbeddingRecord) error {
	_ = ctx
	if record.ThoughtID == "" {
		return errors.New("thought id is required")
	}
	if len(record.Vector) == 0 {
		return errors.New("embedding vector is required")
	}
	if record.Dimension == 0 {
		record.Dimension = len(record.Vector)
	}
	if record.CreatedAt.IsZero() {
		record.CreatedAt = time.Now().UTC()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.embeddings[record.ThoughtID] = record
	return nil
}

func (s *Store) GetEmbedding(ctx context.Context, thoughtID string, model string) (models.EmbeddingRecord, bool) {
	_ = ctx
	s.mu.RLock()
	defer s.mu.RUnlock()
	record, ok := s.embeddings[thoughtID]
	if !ok || len(record.Vector) == 0 {
		return models.EmbeddingRecord{}, false
	}
	if strings.TrimSpace(model) != "" && record.Model != "" && record.Model != model {
		return models.EmbeddingRecord{}, false
	}
	return record, true
}

func (s *Store) SemanticScores(ctx context.Context, queryVector []float64, model string, limit int) (map[string]float64, string, bool) {
	_ = ctx
	s.mu.RLock()
	defer s.mu.RUnlock()
	scores := map[string]float64{}
	for thoughtID, embedding := range s.embeddings {
		score := semanticScore(queryVector, embedding.Vector, model, embedding.Model)
		if score > 0 {
			scores[thoughtID] = score
		}
	}
	if limit <= 0 || len(scores) <= limit {
		return scores, "memory_cosine", true
	}
	trimmed := map[string]float64{}
	for _, thoughtID := range topSemanticThoughtIDs(scores, limit) {
		trimmed[thoughtID] = scores[thoughtID]
	}
	return trimmed, "memory_cosine", true
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
	mode := strings.ToLower(strings.TrimSpace(query.Mode))
	if mode == "" {
		mode = "hybrid"
	}
	sortMode := normalizedSearchSort(query.Sort)
	useVector := len(query.QueryVector) > 0 && (mode == "semantic" || mode == "hybrid")

	s.mu.RLock()
	all := make([]indexedThought, 0, len(s.items))
	for _, item := range s.items {
		if query.TopicID != "" && !containsFold(item.thought.TopicIDs, query.TopicID) {
			continue
		}
		if len(query.Tags) > 0 && !hasAnyFold(item.tags, query.Tags) {
			continue
		}
		if !query.From.IsZero() && item.thought.UpdatedAt.Before(query.From) {
			continue
		}
		if !query.To.IsZero() && item.thought.UpdatedAt.After(query.To) {
			continue
		}
		matchesKeyword := q == "" || strings.Contains(strings.ToLower(item.text), q) || strings.Contains(strings.ToLower(item.thought.DisplayTitle), q)
		if useVector || matchesKeyword {
			all = append(all, item)
		}
	}

	items := []models.SearchResult{}
	for _, item := range all {
		embedding := s.embeddings[item.thought.ID]
		keywordScore := keywordScore(item.text, query.Query)
		semanticScore := semanticScore(query.QueryVector, embedding.Vector, query.EmbeddingModel, embedding.Model)
		recencyScore := recencyScore(item.thought.UpdatedAt)
		score, weights := scoreWithWeights(mode, keywordScore, semanticScore, recencyScore, useVector, query.Weights)
		result := models.SearchResult{
			ThoughtID:     item.thought.ID,
			Title:         item.thought.DisplayTitle,
			Snippet:       snippet(item.text, query.Query),
			Score:         score,
			KeywordScore:  keywordScore,
			SemanticScore: semanticScore,
			RecencyScore:  recencyScore,
			Path:          item.thought.Path,
			Topics:        item.thought.TopicIDs,
			Tags:          item.tags,
		}
		semanticSource := "none"
		if useVector {
			semanticSource = "memory_cosine"
		}
		result.Explain = explainSearchResult(query, mode, sortMode, weights, "memory_contains", semanticSource, result)
		items = append(items, result)
	}
	s.mu.RUnlock()

	sortSearchResults(items, sortMode)
	total := len(items)
	start := (page - 1) * pageSize
	if start > total {
		start = total
	}
	end := start + pageSize
	if end > total {
		end = total
	}
	items = items[start:end]
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
		strings.Join(thought.TopicIDs, " "),
		content.Original,
		content.ExtractedContent,
		content.AINotes,
		content.Links,
	}
	return strings.Join(parts, "\n")
}

func containsFold(values []string, expected string) bool {
	for _, value := range values {
		if strings.EqualFold(value, expected) {
			return true
		}
	}
	return false
}

func hasAnyFold(values []string, expected []string) bool {
	for _, item := range expected {
		if containsFold(values, item) {
			return true
		}
	}
	return false
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

func semanticScore(queryVector []float64, thoughtVector []float64, queryModel string, thoughtModel string) float64 {
	if len(queryVector) == 0 || len(thoughtVector) == 0 {
		return 0
	}
	if queryModel != "" && thoughtModel != "" && queryModel != thoughtModel {
		return 0
	}
	score := cosine(queryVector, thoughtVector)
	if score < 0 {
		return 0
	}
	return score
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
	return dot / (math.Sqrt(leftNorm) * math.Sqrt(rightNorm))
}

func recencyScore(updatedAt time.Time) float64 {
	if updatedAt.IsZero() {
		return 0
	}
	age := time.Since(updatedAt)
	if age < 0 {
		return 1
	}
	return 1 / (1 + age.Hours()/24/30)
}

func combinedScore(mode string, keyword float64, semantic float64, recency float64, useVector bool) float64 {
	switch mode {
	case "semantic":
		return semantic*0.9 + recency*0.1
	case "hybrid":
		if useVector {
			return keyword*0.45 + semantic*0.45 + recency*0.10
		}
		return keyword
	default:
		return keyword
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
