//go:build duckdb

package searchdb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/duckdb/duckdb-go/v2"

	"thoughtflow/internal/pkg/markdown"
	"thoughtflow/internal/pkg/models"
)

type Store struct {
	db *sql.DB
}

func Open(ctx context.Context, path string) (*Store, error) {
	if path == "" {
		return nil, errors.New("duckdb path is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("duckdb", path)
	if err != nil {
		return nil, err
	}
	store := &Store{db: db}
	if err := store.Init(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Store) Init(ctx context.Context) error {
	queries := []string{
		`CREATE TABLE IF NOT EXISTS thoughts (
			id VARCHAR PRIMARY KEY,
			path VARCHAR,
			title VARCHAR,
			type VARCHAR,
			url VARCHAR,
			created_at TIMESTAMP,
			updated_at TIMESTAMP,
			content_hash VARCHAR,
			capture_status VARCHAR,
			refine_status VARCHAR,
			index_status VARCHAR,
			topic_status VARCHAR,
			topic_ids VARCHAR
		)`,
		`CREATE TABLE IF NOT EXISTS thought_contents (
			thought_id VARCHAR PRIMARY KEY,
			search_text VARCHAR,
			summary VARCHAR,
			tags VARCHAR,
			updated_at TIMESTAMP
		)`,
		`CREATE TABLE IF NOT EXISTS thought_embeddings (
			thought_id VARCHAR,
			model VARCHAR,
			dimension INTEGER,
			vector VARCHAR,
			content_hash VARCHAR,
			created_at TIMESTAMP
		)`,
	}
	for _, query := range queries {
		if _, err := s.db.ExecContext(ctx, query); err != nil {
			return err
		}
	}
	if err := execIgnoreColumnExists(ctx, s.db, `ALTER TABLE thoughts ADD COLUMN topic_ids VARCHAR`); err != nil {
		return err
	}
	return nil
}

func execIgnoreColumnExists(ctx context.Context, db *sql.DB, query string) error {
	if _, err := db.ExecContext(ctx, query); err != nil {
		message := strings.ToLower(err.Error())
		if strings.Contains(message, "already exists") || strings.Contains(message, "duplicate") {
			return nil
		}
		return err
	}
	return nil
}

func (s *Store) IndexThought(ctx context.Context, thought models.Thought, content models.ThoughtContent) error {
	if thought.ID == "" {
		return errors.New("thought id is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	_, err = tx.ExecContext(ctx, `DELETE FROM thoughts WHERE id = ?`, thought.ID)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `DELETE FROM thought_contents WHERE thought_id = ?`, thought.ID)
	if err != nil {
		return err
	}
	title := thought.DisplayTitle
	if title == "" {
		title = firstNonEmpty(thought.UserTitle, thought.ExtractedTitle, thought.ID)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO thoughts (
		id, path, title, type, url, created_at, updated_at, content_hash,
		capture_status, refine_status, index_status, topic_status, topic_ids
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		thought.ID, thought.Path, title, thought.Type, thought.URL, thought.CreatedAt, thought.UpdatedAt,
		thought.ContentHash, thought.CaptureStatus, thought.RefineStatus, models.IndexStatusIndexed, thought.TopicStatus,
		strings.Join(thought.TopicIDs, ","),
	)
	if err != nil {
		return err
	}
	searchText := buildSearchText(thought, content)
	tags := strings.Join(append(append([]string{}, thought.UserTags...), thought.AITags...), ",")
	_, err = tx.ExecContext(ctx, `INSERT INTO thought_contents (
		thought_id, search_text, summary, tags, updated_at
	) VALUES (?, ?, ?, ?, ?)`, thought.ID, searchText, thought.Summary, tags, time.Now().UTC())
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ReindexWorkspace(ctx context.Context, rootPath string) (int, error) {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM thought_contents`); err != nil {
		return 0, err
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM thoughts`); err != nil {
		return 0, err
	}
	count := 0
	thoughtsPath := filepath.Join(rootPath, "thoughts")
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
		if err := s.IndexThought(ctx, thought, content); err != nil {
			return err
		}
		count++
		return nil
	})
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	return count, err
}

func (s *Store) Search(ctx context.Context, query models.SearchQuery) (models.SearchResponse, error) {
	page := query.Page
	if page <= 0 {
		page = 1
	}
	pageSize := query.PageSize
	if pageSize <= 0 || pageSize > 100 {
		pageSize = 20
	}
	offset := (page - 1) * pageSize
	pattern := "%" + strings.ToLower(strings.TrimSpace(query.Query)) + "%"

	where := "1=1"
	args := []any{}
	if strings.TrimSpace(query.Query) != "" {
		where = "(lower(c.search_text) LIKE ? OR lower(t.title) LIKE ?)"
		args = append(args, pattern, pattern)
	}
	if strings.TrimSpace(query.TopicID) != "" {
		where = where + " AND lower(coalesce(t.topic_ids, '')) LIKE ?"
		args = append(args, "%"+strings.ToLower(strings.TrimSpace(query.TopicID))+"%")
	}
	if len(query.Tags) > 0 {
		tagClauses := []string{}
		for _, tag := range query.Tags {
			tag = strings.TrimSpace(tag)
			if tag == "" {
				continue
			}
			tagClauses = append(tagClauses, "lower(c.tags) LIKE ?")
			args = append(args, "%"+strings.ToLower(tag)+"%")
		}
		if len(tagClauses) > 0 {
			where = where + " AND (" + strings.Join(tagClauses, " OR ") + ")"
		}
	}

	countQuery := fmt.Sprintf(`SELECT count(*) FROM thoughts t JOIN thought_contents c ON t.id = c.thought_id WHERE %s`, where)
	var total int
	if err := s.db.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		return models.SearchResponse{}, err
	}

	selectQuery := fmt.Sprintf(`SELECT t.id, t.title, c.search_text, t.path, c.tags, coalesce(t.topic_ids, '')
		FROM thoughts t JOIN thought_contents c ON t.id = c.thought_id
		WHERE %s
		ORDER BY t.updated_at DESC
		LIMIT ? OFFSET ?`, where)
	rows, err := s.db.QueryContext(ctx, selectQuery, append(args, pageSize, offset)...)
	if err != nil {
		return models.SearchResponse{}, err
	}
	defer rows.Close()

	items := []models.SearchResult{}
	for rows.Next() {
		var item models.SearchResult
		var searchText string
		var tags string
		var topicIDs string
		if err := rows.Scan(&item.ThoughtID, &item.Title, &searchText, &item.Path, &tags, &topicIDs); err != nil {
			return models.SearchResponse{}, err
		}
		item.Snippet = snippet(searchText, query.Query)
		item.KeywordScore = keywordScore(searchText, query.Query)
		item.SemanticScore = 0
		item.RecencyScore = 0
		item.Score = item.KeywordScore
		item.Tags = splitCSV(tags)
		item.Topics = splitCSV(topicIDs)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return models.SearchResponse{}, err
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
		strings.Join(thought.TopicIDs, " "),
		content.Original,
		content.ExtractedContent,
		content.AINotes,
		content.Links,
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

func splitCSV(value string) []string {
	ret := []string{}
	for _, item := range strings.Split(value, ",") {
		item = strings.TrimSpace(item)
		if item != "" {
			ret = append(ret, item)
		}
	}
	return ret
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
