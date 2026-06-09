package jobstore

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	"thoughtflow/internal/pkg/models"
)

type Store struct {
	rootPath string
	mu       sync.RWMutex
}

func New(rootPath string) *Store {
	return &Store{rootPath: rootPath}
}

func (s *Store) Create(jobType, resourceType, resourceID, message string) (models.Job, error) {
	now := time.Now().UTC()
	job := models.Job{
		ID:           models.NewJobID(jobType, now),
		Type:         jobType,
		ResourceType: resourceType,
		ResourceID:   resourceID,
		Status:       models.JobStatusQueued,
		MaxAttempts:  1,
		Message:      message,
		CreatedAt:    now,
	}
	return job, s.Save(job)
}

func (s *Store) Save(job models.Job) error {
	if err := os.MkdirAll(s.rootPath, 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(job, "", "  ")
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return os.WriteFile(filepath.Join(s.rootPath, job.ID+".json"), raw, 0o644)
}

func (s *Store) Get(jobID string) (models.Job, error) {
	if jobID == "" {
		return models.Job{}, errors.New("job id is required")
	}
	s.mu.RLock()
	raw, err := os.ReadFile(filepath.Join(s.rootPath, jobID+".json"))
	s.mu.RUnlock()
	if err != nil {
		return models.Job{}, err
	}
	var job models.Job
	if err := json.Unmarshal(raw, &job); err != nil {
		return models.Job{}, err
	}
	return job, nil
}

func (s *Store) MarkRunning(job models.Job) (models.Job, error) {
	now := time.Now().UTC()
	job.Status = models.JobStatusRunning
	job.StartedAt = &now
	job.Attempt++
	job.Progress = 0.1
	return job, s.Save(job)
}

func (s *Store) MarkSucceeded(job models.Job, message string) (models.Job, error) {
	now := time.Now().UTC()
	job.Status = models.JobStatusSucceeded
	job.Message = message
	job.Progress = 1
	job.FinishedAt = &now
	return job, s.Save(job)
}

func (s *Store) MarkFailed(job models.Job, errRef models.ErrorRef) (models.Job, error) {
	now := time.Now().UTC()
	job.Status = models.JobStatusFailed
	job.Error = &errRef
	job.FinishedAt = &now
	return job, s.Save(job)
}
