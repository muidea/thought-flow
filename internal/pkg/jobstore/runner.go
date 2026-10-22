package jobstore

import (
	"context"
	"errors"
	"sync"

	"thoughtflow/internal/pkg/models"
)

// Runner owns one active job per resource and joins its workers on shutdown.
// Nonterminal jobs remain durable when the process stops and are resumed at Run.
type Runner struct {
	dirty  map[string]bool
	mu     sync.Mutex
	ctx    context.Context
	cancel context.CancelFunc
	store  *Store
	active map[string]models.Job
	wg     sync.WaitGroup
	closed bool
}

func NewRunner(store *Store) *Runner {
	ctx, cancel := context.WithCancel(context.Background())
	return &Runner{ctx: ctx, cancel: cancel, store: store, active: map[string]models.Job{}, dirty: map[string]bool{}}
}

func (r *Runner) Start(kind, id string, attempts int, run func(context.Context, models.Job)) (models.Job, error) {
	return r.StartResource(kind, models.ResourceTypeThought, id, attempts, run)
}
func (r *Runner) StartLatest(kind, id string, run func(context.Context, models.Job)) (models.Job, error) {
	return r.startResource(kind, models.ResourceTypeThought, id, 1, true, run)
}
func (r *Runner) StartResource(kind, resourceType, id string, attempts int, run func(context.Context, models.Job)) (models.Job, error) {
	return r.startResource(kind, resourceType, id, attempts, false, run)
}
func (r *Runner) startResource(kind, resourceType, id string, attempts int, latest bool, run func(context.Context, models.Job)) (models.Job, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return models.Job{}, errors.New("job runner is closed")
	}
	if job, ok := r.active[kind+":"+id]; ok {
		if latest {
			r.dirty[kind+":"+id] = true
		}
		return job, nil
	}
	job, err := r.store.CreateWithMaxAttempts(kind, resourceType, id, kind+" queued", attempts)
	if err != nil {
		return models.Job{}, err
	}
	r.launchLocked(job, run)
	return job, nil
}

func (r *Runner) launchLocked(job models.Job, run func(context.Context, models.Job)) {
	r.active[job.Type+":"+job.ResourceID] = job
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		key := job.Type + ":" + job.ResourceID
		for {
			run(r.ctx, job)
			r.mu.Lock()
			if r.dirty[key] && !r.closed {
				delete(r.dirty, key)
				r.mu.Unlock()
				continue
			}
			delete(r.active, key)
			delete(r.dirty, key)
			r.mu.Unlock()
			return
		}
	}()

}

func (r *Runner) Recover(kind string, run func(context.Context, models.Job)) error {
	jobs, err := r.store.List()
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return errors.New("job runner is closed")
	}
	for _, job := range jobs {
		if job.Type != kind {
			continue
		}
		switch job.Status {
		case models.JobStatusQueued, models.JobStatusRunning, models.JobStatusRetrying:
			if _, ok := r.active[job.Type+":"+job.ResourceID]; ok {
				_, _ = r.store.MarkCanceled(job, "merged with active recovery job")
				continue
			}
			job.Status = models.JobStatusQueued
			job.Attempt = 0
			job.FinishedAt = nil
			if err := r.store.Save(job); err != nil {
				return err
			}
			r.launchLocked(job, run)
		}
	}
	return nil
}

func (r *Runner) Close() {
	r.Stop()
	r.wg.Wait()
}

// Bind stops accepting work as soon as the process Run context is canceled.
// Close still joins workers before their module releases dependencies.
func (r *Runner) Bind(ctx context.Context) {
	go func() {
		select {
		case <-ctx.Done():
			r.Stop()
		case <-r.ctx.Done():
		}
	}()
}
func (r *Runner) Stop() { r.mu.Lock(); r.closed = true; r.cancel(); r.mu.Unlock() }
