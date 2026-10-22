package topicstore

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"thoughtflow/internal/pkg/markdown"
	"thoughtflow/internal/pkg/models"
)

func TestParallelMembersPreserveDocumentAndBacklinks(t *testing.T) {
	root := t.TempDir()
	a, b := New(root), New(root)
	ctx := context.Background()
	topic, err := a.Create(ctx, models.TopicCreateRequest{Name: "Concurrent"})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "topics", topic.Slug, "index.md")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	authored := string(raw) + "\nManually confirmed paragraph.\n"
	if err = os.WriteFile(path, []byte(authored), 0600); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := range 8 {
		thought := testThought(fmt.Sprintf("20261022-100000-%d", i), fmt.Sprintf("Unique fact %d", i), nil)
		content := models.ThoughtContent{Original: fmt.Sprintf("Unique body %d", i)}
		if err = markdown.WriteThought(root, thought, content); err != nil {
			t.Fatal(err)
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, _, err := b.AddMembership(ctx, topic, thought, content, models.TopicMembership{MatchType: "manual"}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	current, err := a.Get(ctx, topic.ID)
	if err != nil || len(current.Members) != 8 {
		t.Fatalf("lost member: %+v %v", current, err)
	}
	document, _ := a.ReadDocument(ctx, topic.ID)
	if !strings.Contains(document, "Manually confirmed paragraph.") {
		t.Fatal("lost authored prose")
	}
	for _, id := range current.Members {
		thought, _, err := markdown.ReadThought(root, id)
		if err != nil || !contains(thought.TopicIDs, topic.ID) {
			t.Fatalf("lost backlink %s %v", id, err)
		}
	}
	before := document
	_, _, _, err = a.RefreshWithMatcher(ctx, topic.ID, func(context.Context, models.Topic, models.Thought, models.ThoughtContent) (models.TopicMembership, bool) {
		return models.TopicMembership{}, false
	})
	if err != nil {
		t.Fatal(err)
	}
	after, _ := a.ReadDocument(ctx, topic.ID)
	if before != after {
		t.Fatal("rule change rewrote accepted document")
	}
}
