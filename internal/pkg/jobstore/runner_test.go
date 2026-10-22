package jobstore

import (
	"context"
	"testing"
	"thoughtflow/internal/pkg/models"
	"time"
)

func TestRunnerCoalescesClosesAndRecoversSameJob(t *testing.T) {
	store := New(t.TempDir())
	runner := NewRunner(store)
	started := make(chan struct{})
	stopped := make(chan struct{})
	job, err := runner.Start("refine", "thought", 3, func(ctx context.Context, job models.Job) {
		close(started)
		_, _ = store.MarkRunning(job)
		<-ctx.Done()
		close(stopped)
	})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	duplicate, err := runner.Start("refine", "thought", 3, func(context.Context, models.Job) { t.Error("duplicate worker") })
	if err != nil || duplicate.ID != job.ID {
		t.Fatal("did not coalesce", err)
	}
	runner.Close()
	<-stopped
	next := NewRunner(New(store.rootPath))
	defer next.Close()
	recovered := make(chan models.Job, 1)
	if err := next.Recover("refine", func(_ context.Context, j models.Job) { recovered <- j; _, _ = store.MarkSucceeded(j, "resumed") }); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-recovered:
		if got.ID != job.ID {
			t.Fatal("new job replaced recovery identity")
		}
	case <-time.After(time.Second):
		t.Fatal("recovery never ran")
	}
}

func TestRunnerLatestEventIsNotLostWhileWorkerIsReturning(t *testing.T) {
	runner := NewRunner(New(t.TempDir()))
	defer runner.Close()
	started := make(chan struct{}, 2)
	unblock := make(chan struct{})
	run := func(context.Context, models.Job) { started <- struct{}{}; <-unblock }
	first, err := runner.StartLatest("index", "thought", run)
	if err != nil {
		t.Fatal(err)
	}
	<-started
	for range 10 {
		next, err := runner.StartLatest("index", "thought", run)
		if err != nil || first.ID != next.ID {
			t.Fatal("not merged")
		}
	}
	unblock <- struct{}{}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("last event lost")
	}
	close(unblock)
}

func TestRunContextCancellationStopsAndPreservesAcceptedWork(t *testing.T) {
	store := New(t.TempDir())
	runner := NewRunner(store)
	defer runner.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runner.Bind(ctx)
	started := make(chan struct{})
	stopped := make(chan struct{})
	job, err := runner.Start("refine", "cancel-at-shutdown", 1, func(ctx context.Context, job models.Job) {
		_, _ = store.MarkRunning(job)
		close(started)
		<-ctx.Done()
		close(stopped)
	})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	cancel()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("Run cancellation did not reach worker")
	}
	if _, err = runner.Start("refine", "new-after-shutdown", 1, func(context.Context, models.Job) { t.Error("new work started during shutdown") }); err == nil {
		t.Fatal("accepted new shutdown work")
	}
	persisted, err := store.Get(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Status != models.JobStatusRunning {
		t.Fatalf("unfinished work no longer recoverable: %+v", persisted)
	}
}
