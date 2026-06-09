package jobstore

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"thoughtflow/internal/pkg/models"
)

func TestRuntimeStatusCreatesWritableJobDirectory(t *testing.T) {
	rootPath := filepath.Join(t.TempDir(), "jobs")
	status := New(rootPath).RuntimeStatus()

	if status.Status != "ready" || !status.Writable || status.JobsPath != rootPath || status.Error != "" {
		t.Fatalf("RuntimeStatus() = %#v", status)
	}
	info, err := os.Stat(rootPath)
	if err != nil {
		t.Fatalf("expected jobs directory: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("expected %s to be directory", rootPath)
	}
}

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

func TestRecentByResourceReturnsNewestJobsFirst(t *testing.T) {
	store := New(t.TempDir())
	base := time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)
	jobs := []models.Job{
		{ID: "job-old", Type: models.JobTypeRefine, ResourceID: "thought-1", Status: models.JobStatusSucceeded, CreatedAt: base},
		{ID: "job-other", Type: models.JobTypeRefine, ResourceID: "thought-2", Status: models.JobStatusSucceeded, CreatedAt: base.Add(1 * time.Minute)},
		{ID: "job-new", Type: models.JobTypeRefine, ResourceID: "thought-1", Status: models.JobStatusRunning, CreatedAt: base.Add(2 * time.Minute)},
		{ID: "job-mid", Type: models.JobTypeIndex, ResourceID: "thought-1", Status: models.JobStatusQueued, CreatedAt: base.Add(1 * time.Minute)},
	}
	for _, job := range jobs {
		if err := store.Save(job); err != nil {
			t.Fatalf("Save(%s) error = %v", job.ID, err)
		}
	}

	recent, err := store.RecentByResource("thought-1", 2)
	if err != nil {
		t.Fatalf("RecentByResource() error = %v", err)
	}
	if len(recent) != 2 {
		t.Fatalf("recent jobs = %#v", recent)
	}
	if recent[0].ID != "job-new" || recent[1].ID != "job-mid" {
		t.Fatalf("recent order = %#v", recent)
	}
}
