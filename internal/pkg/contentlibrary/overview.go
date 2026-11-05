package contentlibrary

import (
	"sort"
	"strings"
	"time"

	ds "thoughtflow/internal/pkg/documentset"
)

type Attention struct {
	Kind          string    `json:"kind"`
	SetID         string    `json:"set_id"`
	Title         string    `json:"title"`
	Selection     Selection `json:"selection"`
	QuestionCount int       `json:"question_count,omitempty"`
	RunID         string    `json:"run_id,omitempty"`
	SessionID     string    `json:"session_id,omitempty"`
	TaskState     string    `json:"task_state,omitempty"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// attention uses the complete saved inventory and latest attempts, before
// pagination. An unsuccessful update never replaces an existing saved target.
func (s *Store) attention(items []Item) ([]Attention, int) {
	out := []Attention{}
	saved := map[string]Item{}
	for _, item := range items {
		if item.Kind != "document_set" {
			continue
		}
		saved[item.ID] = item
		if len(item.Questions) > 0 {
			out = append(out, Attention{Kind: "questions", SetID: item.ID, Title: item.Title, Selection: item.Selection, QuestionCount: len(item.Questions), UpdatedAt: item.UpdatedAt})
		}
	}
	latest := map[string]ds.Run{}
	for _, run := range *s.cachedRuns {
		old, exists := latest[run.Plan.SetID]
		if !exists || run.UpdatedAt.After(old.UpdatedAt) || (run.UpdatedAt.Equal(old.UpdatedAt) && run.ID > old.ID) {
			latest[run.Plan.SetID] = run
		}
	}
	for id, run := range latest {
		if id == "" || !strings.HasPrefix(run.State, "failed") {
			continue
		}
		item, exists := saved[id]
		a := Attention{Kind: "task_failed", SetID: id, Title: ds.DisplayTitle(run.Plan.Shared, "", run.Plan.Profile.Name), RunID: run.ID, SessionID: run.Plan.Capture.SessionID, TaskState: run.State, UpdatedAt: run.UpdatedAt}
		if exists {
			a.Title, a.Selection = item.Title, item.Selection
		}
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].UpdatedAt.Equal(out[j].UpdatedAt) {
			return out[i].SetID+out[i].Kind < out[j].SetID+out[j].Kind
		}
		return out[i].UpdatedAt.After(out[j].UpdatedAt)
	})
	total := len(out)
	if total > 8 {
		out = out[:8]
	}
	return out, total
}
