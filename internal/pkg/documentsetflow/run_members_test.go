package documentsetflow

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	ds "thoughtflow/internal/pkg/documentset"
	"thoughtflow/internal/pkg/documentstore"
)

func TestExecuteRejectsInvalidMemberProgress(t *testing.T) {
	for _, which := range []string{"missing", "truncated", "reordered", "duplicate", "extra"} {
		t.Run(which, func(t *testing.T) {
			generator := &recordingGenerator{}
			s, _, plan := setup(t, generator, reviewer{})
			run := ds.Run{ID: "invalid-members", OperationID: "invalid-members", Plan: plan, State: "queued"}
			for _, member := range plan.Members {
				run.Members = append(run.Members, ds.MemberProgress{Key: member.Key, State: "queued"})
			}
			switch which {
			case "missing":
				run.Members = nil
			case "truncated":
				run.Members = run.Members[:1]
			case "reordered":
				run.Members[0], run.Members[1] = run.Members[1], run.Members[0]
			case "duplicate":
				run.Members[1].Key = run.Members[0].Key
			case "extra":
				run.Members = append(run.Members, ds.MemberProgress{Key: "unexpected", State: "queued"})
			}
			if e := s.Store.SaveRun(run); e != nil {
				t.Fatal(e)
			}
			// Direct execution makes an unchecked index deterministic, independent of
			// whether test cleanup would otherwise cancel the background worker.
			s.execute(context.Background(), run.ID)
			stored, e := s.Store.Run(run.ID)
			if e != nil || stored.State != "failed_terminal" || !strings.Contains(stored.Error, "member progress") {
				t.Fatalf("invalid progress did not fail safely: %+v, %v", stored, e)
			}
			if len(generator.requests) != 0 || len(stored.Calls) != 0 || stored.Candidate != nil {
				t.Fatal("invalid task started generation")
			}
			if _, e = s.Store.Head(plan.SetID); !os.IsNotExist(e) {
				t.Fatal("invalid task published a set", e)
			}
		})
	}
}

func TestRetryRejectsInvalidMemberProgressBeforeBinding(t *testing.T) {
	for _, replay := range []bool{false, true} {
		t.Run(map[bool]string{false: "old-attempt", true: "queued-replay"}[replay], func(t *testing.T) {
			s, _, plan := setup(t, &recordingGenerator{}, reviewer{})
			old := ds.Run{ID: "old-members", OperationID: "old-members", ModelHash: "fixture", Plan: plan, State: "failed_terminal"}
			req := RetryRequest{OperationID: "new-members", ExpectedGeneration: &plan.BaseGeneration}
			if replay {
				for _, member := range plan.Members {
					old.Members = append(old.Members, ds.MemberProgress{Key: member.Key, State: "failed"})
				}
				next := ds.Run{ID: req.OperationID, OperationID: req.OperationID, Plan: plan, State: "queued"}
				next.RequestHash = documentstore.HashJSON(struct {
					ID  string
					Req RetryRequest
				}{old.ID, req})
				if e := s.Store.SaveRun(next); e != nil {
					t.Fatal(e)
				}
			}
			if e := s.Store.SaveRun(old); e != nil {
				t.Fatal(e)
			}
			bound := false
			_, e := s.RetryWithBinding(old.ID, req, func(ds.Run) error { bound = true; return nil })
			if !errors.Is(e, ds.ErrInvalid) || bound {
				t.Fatal("invalid task rebound or launched", e, bound)
			}
			if !replay {
				if _, e = s.Store.Run(req.OperationID); !os.IsNotExist(e) {
					t.Fatal("invalid retry created an attempt", e)
				}
			}
			s.mu.Lock()
			launched := len(s.running) != 0
			s.mu.Unlock()
			if launched {
				t.Fatal("invalid retry launched a worker")
			}
		})
	}
}
