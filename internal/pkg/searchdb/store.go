//go:build duckdb

package searchdb

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "github.com/duckdb/duckdb-go/v2"

	"thoughtflow/internal/pkg/markdown"
	"thoughtflow/internal/pkg/models"
)

type Store struct {
	db           *sql.DB
	ftsMu        sync.Mutex
	ftsChecked   bool
	ftsAvailable bool
	ftsDirty     bool
	ftsErr       error
	vssMu        sync.Mutex
	vssChecked   bool
	vssAvailable bool
	vssErr       error
	hnswIndexes  map[int]bool
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
	store := &Store{db: db, ftsDirty: true, hnswIndexes: map[int]bool{}}
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

func execIgnoreIndexExists(ctx context.Context, db *sql.DB, query string) error {
	if _, err := db.ExecContext(ctx, query); err != nil {
		message := strings.ToLower(err.Error())
		if strings.Contains(message, "already exists") || strings.Contains(message, "duplicate") {
			return nil
		}
		return err
	}
	return nil
}

func (s *Store) markFTSDirty() {
	s.ftsMu.Lock()
	s.ftsDirty = true
	s.ftsMu.Unlock()
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
	if err = tx.Commit(); err != nil {
		return err
	}
	s.markFTSDirty()
	return nil
}

func (s *Store) IndexEmbedding(ctx context.Context, record models.EmbeddingRecord) error {
	if record.ThoughtID == "" {
		return errors.New("thought id is required")
	}
	if len(record.Vector) == 0 {
		return errors.New("embedding vector is required")
	}
	if record.Dimension == 0 {
		record.Dimension = len(record.Vector)
	}
	if record.Dimension != len(record.Vector) {
		return errors.New("embedding dimension does not match vector length")
	}
	if record.CreatedAt.IsZero() {
		record.CreatedAt = time.Now().UTC()
	}
	raw, err := json.Marshal(record.Vector)
	if err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM thought_embeddings WHERE thought_id = ? AND model = ?`, record.ThoughtID, record.Model); err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO thought_embeddings (
		thought_id, model, dimension, vector, content_hash, created_at
	) VALUES (?, ?, ?, ?, ?, ?)`, record.ThoughtID, record.Model, record.Dimension, string(raw), record.ContentHash, record.CreatedAt)
	if err != nil {
		return err
	}
	_ = s.indexEmbeddingVector(ctx, record)
	return nil
}

func (s *Store) ReindexWorkspace(ctx context.Context, rootPath string) (int, error) {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM thought_contents`); err != nil {
		return 0, err
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM thoughts`); err != nil {
		return 0, err
	}
	s.markFTSDirty()
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

func (s *Store) ensureFTSIndex(ctx context.Context) bool {
	s.ftsMu.Lock()
	defer s.ftsMu.Unlock()

	if !s.ftsChecked {
		s.ftsChecked = true
		if err := s.loadFTS(ctx); err != nil {
			s.ftsAvailable = false
			s.ftsErr = err
			return false
		}
		s.ftsAvailable = true
		s.ftsErr = nil
		s.ftsDirty = true
	}
	if !s.ftsAvailable {
		return false
	}
	if !s.ftsDirty {
		return true
	}
	if _, err := s.db.ExecContext(ctx, `PRAGMA create_fts_index('thought_contents', 'thought_id', 'search_text', overwrite = 1)`); err != nil {
		s.ftsAvailable = false
		s.ftsErr = err
		return false
	}
	s.ftsDirty = false
	s.ftsErr = nil
	return true
}

func (s *Store) loadFTS(ctx context.Context) error {
	return withNormalizedProxyEnv(func() error {
		s.configureDuckDBProxy(ctx)
		if _, err := s.db.ExecContext(ctx, `LOAD fts`); err == nil {
			return nil
		}
		if _, err := s.db.ExecContext(ctx, `INSTALL fts`); err != nil {
			return err
		}
		_, err := s.db.ExecContext(ctx, `LOAD fts`)
		return err
	})
}

func (s *Store) ensureHNSWIndex(ctx context.Context, dimension int) bool {
	tableName, err := embeddingVectorTableName(dimension)
	if err != nil {
		return false
	}

	s.vssMu.Lock()
	defer s.vssMu.Unlock()

	if !s.vssChecked {
		s.vssChecked = true
		if err := s.loadVSS(ctx); err != nil {
			s.vssAvailable = false
			s.vssErr = err
			return false
		}
		s.vssAvailable = true
		s.vssErr = nil
	}
	if !s.vssAvailable {
		return false
	}
	if s.hnswIndexes == nil {
		s.hnswIndexes = map[int]bool{}
	}
	if s.hnswIndexes[dimension] {
		return true
	}
	indexName := embeddingHNSWIndexName(dimension)
	query := fmt.Sprintf(`CREATE INDEX %s ON %s USING HNSW (vector) WITH (metric = 'cosine')`, indexName, tableName)
	if err := execIgnoreIndexExists(ctx, s.db, query); err != nil {
		s.vssErr = err
		return false
	}
	s.hnswIndexes[dimension] = true
	s.vssErr = nil
	return true
}

func (s *Store) loadVSS(ctx context.Context) error {
	return withNormalizedProxyEnv(func() error {
		s.configureDuckDBProxy(ctx)
		if _, err := s.db.ExecContext(ctx, `LOAD vss`); err == nil {
			_, _ = s.db.ExecContext(ctx, `SET hnsw_enable_experimental_persistence = true`)
			return nil
		}
		if _, err := s.db.ExecContext(ctx, `INSTALL vss`); err != nil {
			return err
		}
		if _, err := s.db.ExecContext(ctx, `LOAD vss`); err != nil {
			return err
		}
		_, _ = s.db.ExecContext(ctx, `SET hnsw_enable_experimental_persistence = true`)
		return nil
	})
}

func (s *Store) configureDuckDBProxy(ctx context.Context) {
	for _, key := range []string{"http_proxy", "HTTP_PROXY", "https_proxy", "HTTPS_PROXY"} {
		value, ok := os.LookupEnv(key)
		if !ok || strings.TrimSpace(value) == "" {
			continue
		}
		if _, err := s.db.ExecContext(ctx, `SET http_proxy = ?`, strings.TrimSuffix(value, "/")); err == nil {
			return
		}
	}
}

func withNormalizedProxyEnv(run func() error) error {
	keys := []string{
		"http_proxy",
		"https_proxy",
		"ftp_proxy",
		"all_proxy",
		"HTTP_PROXY",
		"HTTPS_PROXY",
		"FTP_PROXY",
		"ALL_PROXY",
	}
	type previousValue struct {
		value string
		ok    bool
	}
	previous := map[string]previousValue{}
	for _, key := range keys {
		value, ok := os.LookupEnv(key)
		previous[key] = previousValue{value: value, ok: ok}
		if ok {
			os.Setenv(key, strings.TrimSuffix(value, "/"))
		}
	}
	defer func() {
		for _, key := range keys {
			value := previous[key]
			if value.ok {
				os.Setenv(key, value.value)
			} else {
				os.Unsetenv(key)
			}
		}
	}()
	return run()
}

func (s *Store) indexEmbeddingVector(ctx context.Context, record models.EmbeddingRecord) error {
	tableName, err := embeddingVectorTableName(record.Dimension)
	if err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
		thought_id VARCHAR,
		model VARCHAR,
		vector FLOAT[%d],
		content_hash VARCHAR,
		created_at TIMESTAMP
	)`, tableName, record.Dimension)); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(`DELETE FROM %s WHERE thought_id = ? AND model = ?`, tableName), record.ThoughtID, record.Model); err != nil {
		return err
	}
	args := []any{record.ThoughtID, record.Model}
	for _, value := range record.Vector {
		args = append(args, value)
	}
	args = append(args, record.ContentHash, record.CreatedAt)
	_, err = s.db.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s (
		thought_id, model, vector, content_hash, created_at
	) VALUES (?, ?, %s, ?, ?)`, tableName, arrayValueExpression(record.Dimension)), args...)
	return err
}

func embeddingVectorTableName(dimension int) (string, error) {
	if dimension <= 0 {
		return "", errors.New("embedding dimension is required")
	}
	if dimension > 8192 {
		return "", errors.New("embedding dimension is too large")
	}
	return fmt.Sprintf("thought_embedding_vectors_%d", dimension), nil
}

func embeddingHNSWIndexName(dimension int) string {
	return fmt.Sprintf("thought_embedding_vectors_%d_hnsw_cosine", dimension)
}

func arrayValueExpression(dimension int) string {
	parts := make([]string, dimension)
	for idx := range parts {
		parts[idx] = "?::FLOAT"
	}
	return fmt.Sprintf("array_value(%s)::FLOAT[%d]", strings.Join(parts, ", "), dimension)
}

func (s *Store) keywordScoresFromFTS(ctx context.Context, query string) (map[string]float64, bool) {
	query = strings.TrimSpace(query)
	if query == "" || !s.ensureFTSIndex(ctx) {
		return nil, false
	}
	rows, err := s.db.QueryContext(ctx, `SELECT thought_id, score
		FROM (
			SELECT thought_id, fts_main_thought_contents.match_bm25(thought_id, ?, conjunctive := 1) AS score
			FROM thought_contents
		) sq
		WHERE score IS NOT NULL`, query)
	if err != nil {
		return nil, false
	}
	defer rows.Close()

	scores := map[string]float64{}
	maxScore := 0.0
	for rows.Next() {
		var thoughtID string
		var rawScore float64
		if err := rows.Scan(&thoughtID, &rawScore); err != nil {
			return nil, false
		}
		scores[thoughtID] = rawScore
		if rawScore > maxScore {
			maxScore = rawScore
		}
	}
	if err := rows.Err(); err != nil {
		return nil, false
	}
	if maxScore <= 0 {
		return scores, true
	}
	for thoughtID, rawScore := range scores {
		scores[thoughtID] = rawScore / maxScore
	}
	return scores, true
}

func (s *Store) semanticScoresFromDuckDB(ctx context.Context, queryVector []float64, model string, limit int) (map[string]float64, bool, string) {
	dimension := len(queryVector)
	tableName, err := embeddingVectorTableName(dimension)
	if err != nil {
		return nil, false, ""
	}
	if s.ensureHNSWIndex(ctx, dimension) {
		if scores, ok := s.semanticScoresFromHNSW(ctx, tableName, queryVector, model, limit); ok {
			return scores, true, "duckdb_hnsw"
		}
	}
	args := make([]any, 0, dimension+1)
	for _, value := range queryVector {
		args = append(args, value)
	}
	where := "1=1"
	if strings.TrimSpace(model) != "" {
		where = "model = ?"
		args = append(args, model)
	}
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`SELECT thought_id, array_cosine_similarity(vector, %s) AS score
		FROM %s
		WHERE %s`, arrayValueExpression(dimension), tableName, where), args...)
	if err != nil {
		return nil, false, ""
	}
	defer rows.Close()

	scores := map[string]float64{}
	for rows.Next() {
		var thoughtID string
		var score float64
		if err := rows.Scan(&thoughtID, &score); err != nil {
			return nil, false, ""
		}
		if score < 0 {
			score = 0
		}
		if existing, ok := scores[thoughtID]; !ok || score > existing {
			scores[thoughtID] = score
		}
	}
	if err := rows.Err(); err != nil {
		return nil, false, ""
	}
	return scores, true, "duckdb_array"
}

func (s *Store) semanticScoresFromHNSW(ctx context.Context, tableName string, queryVector []float64, model string, limit int) (map[string]float64, bool) {
	dimension := len(queryVector)
	if limit <= 0 {
		limit = 100
	}
	firstVectorArgs := make([]any, 0, dimension)
	secondVectorArgs := make([]any, 0, dimension)
	for _, value := range queryVector {
		firstVectorArgs = append(firstVectorArgs, value)
		secondVectorArgs = append(secondVectorArgs, value)
	}
	where := "1=1"
	args := []any{}
	args = append(args, firstVectorArgs...)
	if strings.TrimSpace(model) != "" {
		where = "model = ?"
		args = append(args, model)
	}
	args = append(args, secondVectorArgs...)
	args = append(args, limit)
	vectorExpr := arrayValueExpression(dimension)
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`SELECT thought_id, 1 - distance AS score
		FROM (
			SELECT thought_id, array_cosine_distance(vector, %s) AS distance
			FROM %s
			WHERE %s
			ORDER BY array_cosine_distance(vector, %s)
			LIMIT ?
		) ranked`, vectorExpr, tableName, where, vectorExpr), args...)
	if err != nil {
		s.vssErr = err
		return nil, false
	}
	defer rows.Close()

	scores := map[string]float64{}
	for rows.Next() {
		var thoughtID string
		var score float64
		if err := rows.Scan(&thoughtID, &score); err != nil {
			s.vssErr = err
			return nil, false
		}
		if score < 0 {
			score = 0
		}
		if score > 1 {
			score = 1
		}
		if existing, ok := scores[thoughtID]; !ok || score > existing {
			scores[thoughtID] = score
		}
	}
	if err := rows.Err(); err != nil {
		s.vssErr = err
		return nil, false
	}
	return scores, true
}

func semanticCandidateLimit(page int, pageSize int) int {
	if page <= 0 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = 20
	}
	limit := page * pageSize * 4
	if limit < 100 {
		return 100
	}
	if limit > 1000 {
		return 1000
	}
	return limit
}

func searchFilterWhere(query models.SearchQuery) (string, []any) {
	where := "1=1"
	args := []any{}
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
	if !query.From.IsZero() {
		where = where + " AND t.updated_at >= ?"
		args = append(args, query.From)
	}
	if !query.To.IsZero() {
		where = where + " AND t.updated_at <= ?"
		args = append(args, query.To)
	}
	return where, args
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
	mode := strings.ToLower(strings.TrimSpace(query.Mode))
	if mode == "" {
		mode = "hybrid"
	}
	sortMode := normalizedSearchSort(query.Sort)
	useVector := len(query.QueryVector) > 0 && (mode == "semantic" || mode == "hybrid")
	trimmedQuery := strings.TrimSpace(query.Query)
	ftsScores, useFTS := s.keywordScoresFromFTS(ctx, trimmedQuery)
	semanticScores, useDuckDBVector, duckDBSemanticSource := map[string]float64{}, false, ""
	if useVector {
		semanticScores, useDuckDBVector, duckDBSemanticSource = s.semanticScoresFromDuckDB(ctx, query.QueryVector, query.EmbeddingModel, semanticCandidateLimit(page, pageSize))
	}
	keywordSource := "like"
	if useFTS {
		keywordSource = "duckdb_fts"
	}
	semanticSource := "none"
	if useVector {
		semanticSource = "json_cosine"
		if useDuckDBVector {
			semanticSource = duckDBSemanticSource
		}
	}

	where, args := searchFilterWhere(query)
	selectQuery := fmt.Sprintf(`SELECT t.id, t.title, c.search_text, t.path, c.tags, coalesce(t.topic_ids, ''), t.updated_at
		FROM thoughts t JOIN thought_contents c ON t.id = c.thought_id
		WHERE %s
		ORDER BY t.updated_at DESC`, where)
	rows, err := s.db.QueryContext(ctx, selectQuery, args...)
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
		var updatedAt time.Time
		if err := rows.Scan(&item.ThoughtID, &item.Title, &searchText, &item.Path, &tags, &topicIDs, &updatedAt); err != nil {
			return models.SearchResponse{}, err
		}
		item.Snippet = snippet(searchText, query.Query)
		item.KeywordScore = keywordScore(searchText, query.Query)
		if useFTS && trimmedQuery != "" {
			item.KeywordScore = ftsScores[item.ThoughtID]
		}
		if trimmedQuery != "" && !useVector && item.KeywordScore <= 0 {
			continue
		}
		if useDuckDBVector {
			score, ok := semanticScores[item.ThoughtID]
			if ok {
				item.SemanticScore = score
			} else {
				embedding := s.embeddingForThought(ctx, item.ThoughtID, query.EmbeddingModel)
				item.SemanticScore = semanticScore(query.QueryVector, embedding.Vector, query.EmbeddingModel, embedding.Model)
			}
		} else if useVector {
			embedding := s.embeddingForThought(ctx, item.ThoughtID, query.EmbeddingModel)
			item.SemanticScore = semanticScore(query.QueryVector, embedding.Vector, query.EmbeddingModel, embedding.Model)
		}
		item.RecencyScore = recencyScore(updatedAt)
		weights := models.SearchWeights{}
		item.Score, weights = scoreWithWeights(mode, item.KeywordScore, item.SemanticScore, item.RecencyScore, useVector, query.Weights)
		item.Tags = splitCSV(tags)
		item.Topics = splitCSV(topicIDs)
		item.Explain = explainSearchResult(query, mode, sortMode, weights, keywordSource, semanticSource, item)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return models.SearchResponse{}, err
	}
	sortSearchResults(items, sortMode)
	total := len(items)
	start := offset
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

func (s *Store) embeddingForThought(ctx context.Context, thoughtID string, model string) models.EmbeddingRecord {
	record, _ := s.GetEmbedding(ctx, thoughtID, model)
	return record
}

func (s *Store) GetEmbedding(ctx context.Context, thoughtID string, model string) (models.EmbeddingRecord, bool) {
	where := "thought_id = ?"
	args := []any{thoughtID}
	if strings.TrimSpace(model) != "" {
		where += " AND model = ?"
		args = append(args, model)
	}
	row := s.db.QueryRowContext(ctx, `SELECT thought_id, model, dimension, vector, content_hash, created_at
		FROM thought_embeddings WHERE `+where+` ORDER BY created_at DESC LIMIT 1`, args...)
	var record models.EmbeddingRecord
	var raw string
	if err := row.Scan(&record.ThoughtID, &record.Model, &record.Dimension, &raw, &record.ContentHash, &record.CreatedAt); err != nil {
		return models.EmbeddingRecord{}, false
	}
	_ = json.Unmarshal([]byte(raw), &record.Vector)
	if len(record.Vector) == 0 {
		return models.EmbeddingRecord{}, false
	}
	return record, true
}

func (s *Store) SemanticScores(ctx context.Context, queryVector []float64, model string, limit int) (map[string]float64, string, bool) {
	scores, ok, source := s.semanticScoresFromDuckDB(ctx, queryVector, model, limit)
	return scores, source, ok
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
