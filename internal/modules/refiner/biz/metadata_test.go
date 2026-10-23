package biz

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/muidea/magicCommon/event"
	capturebiz "thoughtflow/internal/modules/capture/biz"
	"thoughtflow/internal/pkg/ai"
	"thoughtflow/internal/pkg/jobstore"
	"thoughtflow/internal/pkg/markdown"
	"thoughtflow/internal/pkg/models"
	"thoughtflow/internal/pkg/thoughtlock"
)

type invalidThenValidMetadata struct {
	calls int
	root  string
	t     *testing.T
}

func (p *invalidThenValidMetadata) Refine(_ context.Context, req ai.RefineRequest) (models.ThoughtRefinement, error) {
	p.calls++
	r := models.ThoughtRefinement{ThoughtID: req.Thought.ID, Status: models.RefineStatusRefined, Summary: "Corrected summary", AITags: []string{"DuckDB"}}
	if p.calls == 1 {
		r.Summary = strings.Repeat("错", models.MetadataSummaryMaxRunes+1)
	} else {
		current, _, err := markdown.ReadThought(p.root, req.Thought.ID)
		if err != nil || current.Summary != "Previous valid summary" || current.RefineStatus != models.RefineStatusFailed {
			p.t.Errorf("invalid metadata overwritten last successful summary: %+v %v", current, err)
		}
	}
	return r, nil
}

func TestInvalidMetadataRetriesWithoutPersistingInvalidSummary(t *testing.T) {
	root := t.TempDir()
	ws := &models.Workspace{RootPath: root, JobsPath: filepath.Join(root, "runtime/jobs")}
	thought := models.Thought{ID: "20261023-100000-retry", UserTitle: "Note", Summary: "Previous valid summary"}
	if err := markdown.WriteThought(root, thought, models.ThoughtContent{AINotes: "New source content"}); err != nil {
		t.Fatal(err)
	}
	jobs := jobstore.New(ws.JobsPath)
	provider := &invalidThenValidMetadata{root: root, t: t}
	service := NewService(ws, jobs, nil, nil, provider, nil)
	defer service.Close()
	job, err := jobs.CreateWithMaxAttempts(models.JobTypeRefine, models.ResourceTypeThought, thought.ID, "queued", refineMaxAttempts)
	if err != nil {
		t.Fatal(err)
	}
	service.refineJob(context.Background(), job, false)
	stored, err := jobs.Get(job.ID)
	if err != nil || stored.Status != models.JobStatusSucceeded || stored.Attempt != 2 || provider.calls != 2 {
		t.Fatalf("invalid metadata was not retried: %+v, calls=%d, %v", stored, provider.calls, err)
	}
	current, _, err := markdown.ReadThought(root, thought.ID)
	if err != nil || current.Summary != "Corrected summary" {
		t.Fatalf("corrected metadata not saved: %+v %v", current, err)
	}
}

func TestBodyPatchRegeneratesMetadataFromCurrentContent(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	ws := &models.Workspace{RootPath: root, JobsPath: filepath.Join(root, "runtime/jobs")}
	hub := event.NewHub(2)
	jobs := jobstore.New(ws.JobsPath)
	locker := thoughtlock.New(time.Second)
	service := NewService(ws, jobs, hub, nil, ai.NewLocalRefineProvider(), nil, WithLocker(locker))
	defer func() { hub.Terminate(ctx); service.Close() }()
	hub.Subscribe(models.EventThoughtPatched, service)
	thought := models.Thought{ID: "20261023-100000-edit", UserTitle: "Note"}
	if err := markdown.WriteThought(root, thought, models.ThoughtContent{AINotes: "Old DuckDB content"}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.RefineNow(ctx, thought.ID); err != nil {
		t.Fatal(err)
	}
	capture := capturebiz.NewService(ws, jobs, hub, capturebiz.WithLocker(locker))
	body := "New PostgreSQL design with explicit concurrency constraints."
	if _, err := capture.PatchThought(ctx, thought.ID, "editor", models.ThoughtPatchRequest{Body: &body}, []byte(`{"body":"New PostgreSQL design with explicit concurrency constraints."}`)); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		current, c, err := markdown.ReadThought(root, thought.ID)
		if err != nil {
			t.Fatal(err)
		}
		if current.Summary == body && current.LastRefinedInputHash == models.ContentHash(ai.RefinementText(current, c)) {
			if contains(current.AITags, "engineering") {
				t.Fatal("old DuckDB tag survived unrelated content edit")
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("metadata stayed stale after edit: %+v", current)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
