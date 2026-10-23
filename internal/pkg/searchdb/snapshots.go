package searchdb

import (
	"context"
	"os"
	"time"

	"thoughtflow/internal/pkg/models"
	"thoughtflow/internal/pkg/objectlocation"
)

// This separate index has no global-thought identity. Refresh from catalog
// facts under the search namespace lease, so unlinked snapshots cannot linger
// until an asynchronous notification or a full reindex happens.
func (s *Store) refreshTopicSnapshots(ctx context.Context, topic string) error {
	r := objectlocation.For(s.root)
	t, err := r.Topic(topic)
	if err != nil {
		return err
	}
	var catalog models.TopicCatalog
	if t.Mode == models.TopicModeCollection {
		if err = objectlocation.ReadJSON(s.root, "topics/"+topic+"/catalog.json", &catalog); err != nil {
			return err
		}
	}
	type record struct {
		entry    models.CatalogEntry
		snapshot models.TopicSnapshot
		body     string
	}
	records := []record{}
	for _, entry := range catalog.Entries {
		if entry.Binding.Kind != "snapshot" {
			continue
		}
		base := "topics/" + topic + "/references/" + entry.Binding.SnapshotID
		var snap models.TopicSnapshot
		if err = objectlocation.ReadJSON(s.root, base+"/snapshot.json", &snap); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		if snap.ID != entry.Binding.SnapshotID || snap.Private {
			return objectlocation.ErrUnsafe
		}
		path, err := objectlocation.SafePath(s.root, base+"/body.md")
		if err != nil {
			return err
		}
		raw, err := objectlocation.ReadFileBounded(path, objectlocation.MaxResourceBytes)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if objectlocation.Hash(raw) != snap.BodyHash {
			return objectlocation.ErrRecovery
		}
		records = append(records, record{entry, snap, string(raw)})
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err = s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS topic_snapshot_contents(topic_id VARCHAR,entry_id VARCHAR,snapshot_id VARCHAR,title VARCHAR,body VARCHAR,revision_id VARCHAR,created_at TIMESTAMP,PRIMARY KEY(topic_id,entry_id))`); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `DELETE FROM topic_snapshot_contents WHERE topic_id=?`, topic); err != nil {
		return err
	}
	for _, row := range records {
		if _, err = tx.ExecContext(ctx, `INSERT INTO topic_snapshot_contents VALUES(?,?,?,?,?,?,?)`, topic, row.entry.ID, row.snapshot.ID, row.entry.Title, row.body, row.snapshot.RevisionID, row.snapshot.CreatedAt); err != nil {
			return err
		}
	}
	return tx.Commit()
}
func (s *Store) searchTopicSnapshots(ctx context.Context, query models.SearchQuery, mode string, vector bool) ([]models.SearchResult, error) {
	// Snapshot tags are empty; tag-constrained searches must not bypass filtering.
	if len(query.Tags) > 0 {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT entry_id,snapshot_id,title,body,revision_id,created_at FROM topic_snapshot_contents WHERE topic_id=?`, query.TopicID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []models.SearchResult{}
	for rows.Next() {
		var hit models.SearchResult
		var body string
		var created time.Time
		if err = rows.Scan(&hit.EntryID, &hit.SnapshotID, &hit.Title, &body, &hit.IndexedRevisionID, &created); err != nil {
			return nil, err
		}
		if !query.From.IsZero() && created.Before(query.From) || !query.To.IsZero() && created.After(query.To) {
			continue
		}
		hit.KeywordScore = keywordScore(hit.Title+"\n"+body, query.Query)
		if query.Query != "" && hit.KeywordScore <= 0 {
			continue
		}
		hit.VersionKind = "snapshot"
		hit.Path = "topics/" + query.TopicID + "/references/" + hit.SnapshotID + "/body.md"
		hit.Topics = []string{query.TopicID}
		hit.Snippet = snippet(body, query.Query)
		hit.RecencyScore = recencyScore(created)
		hit.Score, _ = scoreWithWeights(mode, hit.KeywordScore, 0, hit.RecencyScore, vector, query.Weights)
		result = append(result, hit)
	}
	return result, rows.Err()
}
