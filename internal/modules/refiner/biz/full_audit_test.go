package biz

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"thoughtflow/internal/pkg/ai"
	"thoughtflow/internal/pkg/appconfig"
	"thoughtflow/internal/pkg/jobstore"
	"thoughtflow/internal/pkg/markdown"
	"thoughtflow/internal/pkg/models"
	"thoughtflow/internal/pkg/webfetch"
	"time"
)

func TestURLRefinesFetchedFactsAndKeepsAnnotations(t *testing.T) {
	fact := "UNIQUEFETCHEDFACT is demonstrated by this page. "
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("<html><title>Page</title><article><p>" + strings.Repeat(fact, 30) + "</p></article></html>"))
	}))
	defer origin.Close()
	root := t.TempDir()
	ws := &models.Workspace{RootPath: root, JobsPath: filepath.Join(root, "runtime/jobs")}
	thought := models.Thought{ID: "20261022-url-content", Type: models.ThoughtTypeURL, URL: origin.URL}
	content := models.ThoughtContent{AINotes: origin.URL + "\nUser annotation UNIQUEANNOTATION"}
	if err := markdown.WriteThought(root, thought, content); err != nil {
		t.Fatal(err)
	}
	service := NewService(ws, jobstore.New(ws.JobsPath), nil, nil, ai.NewLocalRefineProvider(), webfetch.NewFromConfig(appconfig.ReaderConfig{Enabled: false, Timeout: time.Second}))
	result, err := service.RefineNow(context.Background(), thought.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Summary, "UNIQUEFETCHEDFACT") {
		t.Fatalf("summarized URL instead of page: %s", result.Summary)
	}
	current, c, err := markdown.ReadThought(root, thought.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ai.RefinementText(current, c), "UNIQUEANNOTATION") {
		t.Fatal("lost annotation")
	}
	if current.LastRefinedInputHash != models.ContentHash(ai.RefinementText(current, c)) {
		t.Fatal("refined input hash not persisted")
	}
	c.AINotes += "\nNew annotation"
	current.ContentHash = models.ContentHash(c.AINotes)
	if _, unchanged := unchangedRefinement(current, c); unchanged {
		t.Fatal("content hash hid changed refinement input")
	}
}
