package jobstore

import (
	"testing"

	"thoughtflow/internal/pkg/models"
)

func TestStoreJobStateTransitions(t *testing.T) {
	store := New(t.TempDir())
	job, err := store.CreateWithMaxAttempts(models.JobTypeRefine, models.ResourceTypeThought, "thought-1", "refine queued", 3)
	if err != nil {
		t.Fatalf("CreateWithMaxAttempts() error = %v", err)
	}
	if job.Status != models.JobStatusQueued || job.MaxAttempts != 3 {
		t.Fatalf("created job = %#v", job)
	}

	job, err = store.MarkRunning(job)
	if err != nil {
		t.Fatalf("MarkRunning(first) error = %v", err)
	}
	if job.Status != models.JobStatusRunning || job.Attempt != 1 || job.StartedAt == nil || job.Progress != 0.1 {
		t.Fatalf("running job = %#v", job)
	}
	firstStartedAt := *job.StartedAt

	job, err = store.UpdateProgress(job, 1.8, "almost done")
	if err != nil {
		t.Fatalf("UpdateProgress() error = %v", err)
	}
	if job.Progress != 1 || job.Message != "almost done" {
		t.Fatalf("progress job = %#v", job)
	}

	errRef := models.NewErrorRef("thoughtflow.test.retry", "temporary failure", true)
	job, err = store.MarkRetrying(job, errRef, "retrying after temporary failure")
	if err != nil {
		t.Fatalf("MarkRetrying() error = %v", err)
	}
	if job.Status != models.JobStatusRetrying || job.Error == nil || job.Error.Code != errRef.Code || job.Progress != 0 {
		t.Fatalf("retrying job = %#v", job)
	}

	job, err = store.MarkRunning(job)
	if err != nil {
		t.Fatalf("MarkRunning(second) error = %v", err)
	}
	if job.Attempt != 2 || job.StartedAt == nil || !job.StartedAt.Equal(firstStartedAt) || job.Error != nil {
		t.Fatalf("second running job = %#v", job)
	}

	job, err = store.MarkSucceeded(job, "refine succeeded")
	if err != nil {
		t.Fatalf("MarkSucceeded() error = %v", err)
	}
	if job.Status != models.JobStatusSucceeded || job.Progress != 1 || job.FinishedAt == nil || job.Error != nil {
		t.Fatalf("succeeded job = %#v", job)
	}

	loaded, err := store.Get(job.ID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if loaded.Status != models.JobStatusSucceeded || loaded.Attempt != 2 || loaded.MaxAttempts != 3 {
		t.Fatalf("loaded job = %#v", loaded)
	}
}

func TestStoreCanCancelQueuedJob(t *testing.T) {
	store := New(t.TempDir())
	job, err := store.Create(models.JobTypeReindex, models.ResourceTypeWorkspace, "local", "reindex queued")
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	job, err = store.MarkCanceled(job, "user canceled")
	if err != nil {
		t.Fatalf("MarkCanceled() error = %v", err)
	}
	if job.Status != models.JobStatusCanceled || job.Message != "user canceled" || job.FinishedAt == nil {
		t.Fatalf("canceled job = %#v", job)
	}
}
