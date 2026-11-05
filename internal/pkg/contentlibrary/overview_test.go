package contentlibrary

import (
	"testing"
	"time"

	ds "thoughtflow/internal/pkg/documentset"
)

func TestOverviewAttentionKeepsSavedTargetAndOnlyLatestFailedAttempt(t *testing.T) {
	now := time.Now().UTC()
	makeRun := func(set, id, state string, delta time.Duration) ds.Run {
		return ds.Run{ID: id, State: state, UpdatedAt: now.Add(delta), Plan: ds.Plan{SetID: set, Capture: ds.CaptureSnapshot{SessionID: "session-" + set}, Shared: ds.SharedDesign{Inputs: map[string]string{"title": "Design " + set}}}}
	}
	runs := []ds.Run{makeRun("saved", "failure", "failed_terminal", time.Second), makeRun("unsaved", "unsaved-failure", "failed_retryable", 2*time.Second), makeRun("recovered", "old-failure", "failed_terminal", 0), makeRun("recovered", "success", "draft_saved", time.Second)}
	s := &Store{cachedRuns: &runs}
	items := []Item{{ID: "saved", Kind: "document_set", Title: "Saved design", Selection: Selection{"archive", "fixed-archive"}, Questions: []string{"Retention policy?"}, UpdatedAt: now}}
	attention, total := s.attention(items)
	if total != 3 || len(attention) != 3 {
		t.Fatal(attention, total)
	}
	if attention[0].SetID != "unsaved" || attention[0].Selection.ID != "" || attention[0].SessionID != "session-unsaved" {
		t.Fatal(attention[0])
	}
	if attention[1].Kind != "task_failed" || attention[1].Selection.ID != "fixed-archive" || attention[1].RunID != "failure" {
		t.Fatal(attention[1])
	}
	if attention[2].Kind != "questions" || attention[2].QuestionCount != 1 {
		t.Fatal(attention[2])
	}
}
