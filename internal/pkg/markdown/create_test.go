package markdown

import (
	"errors"
	"os"
	"sync"
	"testing"

	"thoughtflow/internal/pkg/models"
)

func TestCreateThoughtPublishesExclusively(t *testing.T) {
	root := t.TempDir()
	const id = "20261104-create-only"
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, body := range []string{"First input", "Second input"} {
		wg.Add(1)
		go func(body string) {
			defer wg.Done()
			results <- CreateThought(root, models.Thought{ID: id, UserTitle: body}, models.ThoughtContent{AINotes: body})
		}(body)
	}
	wg.Wait()
	close(results)
	created, exists := 0, 0
	for err := range results {
		if err == nil {
			created++
		} else if errors.Is(err, os.ErrExist) {
			exists++
		} else {
			t.Fatal(err)
		}
	}
	if created != 1 || exists != 1 {
		t.Fatal("competing create replaced an existing file", created, exists)
	}
	thought, content, err := ReadThought(root, id)
	if err != nil || content.AINotes != thought.UserTitle {
		t.Fatal("creation mixed identities or content", err)
	}
	raw, err := ReadThoughtRaw(root, id)
	if err != nil {
		t.Fatal(err)
	}
	if err := CreateThought(root, models.Thought{ID: id, UserTitle: "Overwrite"}, models.ThoughtContent{AINotes: "Overwrite"}); !errors.Is(err, os.ErrExist) {
		t.Fatal(err)
	}
	after, err := ReadThoughtRaw(root, id)
	if err != nil || string(after) != string(raw) {
		t.Fatal("failed create changed existing bytes", err)
	}
}
