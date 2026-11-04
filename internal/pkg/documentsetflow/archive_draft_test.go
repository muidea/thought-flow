package documentsetflow

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	ds "thoughtflow/internal/pkg/documentset"
	"thoughtflow/internal/pkg/documentsetstore"
	"thoughtflow/internal/pkg/documentstore"
	"thoughtflow/internal/pkg/markdown"
	"thoughtflow/internal/pkg/objectlocation"
	"thoughtflow/internal/testutil/designfixture"
)

func proposedArchivePlan(t *testing.T, g *engineeringGenerator) (*Service, ds.Plan) {
	t.Helper()
	s, p, c := engineeringSetup(t, g)
	_, shared := designfixture.Capture()
	c.Messages = append(c.Messages, ds.Message{Role: "ai", Text: "Assistant design: preserve execution records, define API error contracts, and evaluate requirement-level and goal-level evidence. PRIVATE-PROPOSAL-SOURCE."})
	for i := range shared.Facts {
		if shared.Facts[i].Kind != "requirement" {
			shared.Facts[i].Status = "proposed"
			shared.Facts[i].Source = ds.Source{Message: 1, Quote: "Assistant design"}
			for j := range shared.Facts[i].Provenance {
				shared.Facts[i].Provenance[j].Kind = "model_proposal"
			}
		}
	}
	for k, v := range shared.Conditions {
		v.Status = "proposed"
		v.Source = ds.Source{Message: 1, Quote: "Assistant design"}
		shared.Conditions[k] = v
	}
	s.opts.Snapshot = func(string) (ds.CaptureSnapshot, error) { return *c, nil }
	zero := uint64(0)
	plan, e := s.Plan(context.Background(), c.SessionID, PlanRequest{OperationID: "proposed-plan", SetID: p.SetID, ExpectedGeneration: &zero, CaptureGeneration: &c.Generation, ProfileID: p.Profile.ID, ProfileVersion: p.Profile.Version, ProfileHash: p.Profile.Hash, Shared: &shared})
	if e != nil {
		t.Fatal(e)
	}
	return s, plan
}

func TestCaptureArchiveDraftExtractsAssistantDesignWithoutApprovingIt(t *testing.T) {
	g := &engineeringGenerator{}
	s, p := proposedArchivePlan(t, g)
	strict := start(t, s, p, "strict")
	if strict.State != "needs_clarification" || len(g.requests) != 0 {
		t.Fatal("formal path lost its confirmation gate")
	}
	req := RunRequest{ArchiveDraft: true, OperationID: "archive-draft", SetID: p.SetID, PlanID: p.ID, PlanHash: p.Hash, ExpectedGeneration: &p.BaseGeneration, CaptureGeneration: &p.Capture.Generation}
	if _, e := s.Start(req); e != nil {
		t.Fatal(e)
	}
	run := wait(t, s, req.OperationID)
	if run.State != "draft_saved" || run.Candidate != nil || run.Draft == nil || !run.Draft.NotesReady || run.Draft.NotesPending {
		t.Fatal(run.State, run.Error)
	}
	roles := map[string]bool{}
	for _, m := range run.Draft.Members {
		note, content, err := markdown.ReadThought(s.Store.Root, m.ThoughtID)
		if err != nil || content.AINotes != m.Body || note.DesignDraft == nil || note.DesignDraft.BodyHash != m.Hash || note.DesignDraft.RunID != run.ID || !note.CaptureDraft || note.Document != nil || note.DocumentProfile != nil {
			t.Fatal("archive did not create an exact unapproved Thought", m.Key, err)
		}
		if _, err := documentstore.New(s.Store.Root).Head(m.ThoughtID); !os.IsNotExist(err) {
			t.Fatal("unapproved note acquired a document HEAD", err)
		}
		roles[strings.Split(m.Key, ":")[0]] = true
		raw, e := os.ReadFile(filepath.Join(s.Store.Root, documentsetstore.Private(p.SetID), "drafts", run.ID, m.Filename))
		if e != nil || string(raw) != m.Body || documentstore.Hash(raw) != m.Hash {
			t.Fatal("missing exact private markdown", m.Key, e)
		}
		info, _ := os.Stat(filepath.Join(s.Store.Root, documentsetstore.Private(p.SetID), "drafts", run.ID, m.Filename))
		if info.Mode().Perm()&0077 != 0 {
			t.Fatal("draft is not private")
		}
		if m.Key == "archive-source" && !strings.Contains(m.Body, "PRIVATE-PROPOSAL-SOURCE") {
			t.Fatal("original conversation lost")
		}
	}
	for _, key := range []string{"architecture", "module", "interfaces", "storage", "verification", "delivery", "archive-source"} {
		if !roles[key] {
			t.Fatal("unknown/proposed scope silently omitted", key)
		}
	}
	if ids, err := markdown.ListThoughtIDs(s.Store.Root); err != nil || len(ids) != 7 {
		t.Fatal("archive missing from ordinary notes", ids, err)
	}
	if len(g.requests) != 6 {
		t.Fatal("wrong draft call count", len(g.requests))
	}
	for _, request := range g.requests {
		if request.Profile.Ref.ProfileID == "builtin.engineering-module" && (!strings.Contains(request.Context.Body, `"member_key":"interfaces"`) || !strings.Contains(request.Context.Body, `"member_key":"storage"`)) {
			t.Fatal("module draft missed fixed upstream contracts")
		}
		if !request.UnapprovedDraft || !strings.Contains(request.Context.Body, "PRIVATE-PROPOSAL-SOURCE") || !strings.Contains(request.Context.Body, "model_proposal") {
			t.Fatal("generation did not receive complete classified conversation")
		}
	}
	for _, f := range run.Plan.Shared.Facts {
		if f.Kind != "requirement" && f.Status != "proposed" {
			t.Fatal("proposal was auto-approved")
		}
	}
	h, e := s.Store.HeadOrEmpty(p.SetID)
	if e != nil || h.Generation != 0 || h.Working != "" || h.Finalized != "" {
		t.Fatal("draft published a SetHead", e)
	}
	if len(ds.EngineeringIssues(run.Plan)) == 0 {
		t.Fatal("formal blockers were erased")
	}
	if _, e = s.Store.Export(p.SetID, documentsetstore.ExportRequest{OperationID: "draft-delivery", ExpectedGeneration: &p.BaseGeneration, RevisionID: run.ID, Kind: "delivery"}); e == nil {
		t.Fatal("unapproved draft exported as formal delivery")
	}
	if _, e = s.Finalize(p.SetID, FinalizeRequest{OperationID: "draft-finalize", ExpectedGeneration: &p.BaseGeneration, RevisionID: run.ID, RevisionHash: run.Draft.SourceHash, ApprovedBy: "user", DeliveryLevel: "ready_for_use"}); e == nil {
		t.Fatal("unapproved draft finalized")
	}
	before := len(g.requests)
	if _, e = s.Start(req); e != nil {
		t.Fatal(e)
	}
	if len(g.requests) != before {
		t.Fatal("idempotent archive replayed LLM")
	}
	restarted := New(s.Store.Root, Options{Enabled: true})
	defer restarted.Close()
	if issues := restarted.Recover(); len(issues) > 0 {
		t.Fatal(issues)
	}
	restored, e := restarted.Store.Run(run.ID)
	if e != nil || restored.State != "draft_saved" || restored.Draft.SourceHash != run.Draft.SourceHash {
		t.Fatal("restart lost draft", e)
	}
}

func TestArchiveDraftDoesNotHideEmptyGenerationOrOverwriteEditedFiles(t *testing.T) {
	for _, empty := range []bool{true, false} {
		t.Run(map[bool]string{true: "empty-output", false: "edited-replay"}[empty], func(t *testing.T) {
			g := &engineeringGenerator{empty: empty}
			s, p := proposedArchivePlan(t, g)
			req := RunRequest{ArchiveDraft: true, OperationID: "draft-negative", SetID: p.SetID, PlanID: p.ID, PlanHash: p.Hash, ExpectedGeneration: &p.BaseGeneration, CaptureGeneration: &p.Capture.Generation}
			if _, e := s.Start(req); e != nil {
				t.Fatal(e)
			}
			run := wait(t, s, req.OperationID)
			if empty {
				if run.State != "failed_terminal" || run.Error == "" {
					t.Fatal("empty draft was disguised as success", run.State)
				}
				return
			}
			m := run.Draft.Members[0]
			path := filepath.Join(s.Store.Root, documentsetstore.Private(p.SetID), "drafts", run.ID, m.Filename)
			if e := os.WriteFile(path, []byte("MANUAL INPUT"), 0600); e != nil {
				t.Fatal(e)
			}
			if e := s.Store.SaveArchiveDraftMember(p.SetID, run.ID, m); e == nil {
				t.Fatal("manual edit overwritten")
			}
			raw, _ := os.ReadFile(path)
			if string(raw) != "MANUAL INPUT" {
				t.Fatal("manual input lost")
			}
		})
	}
}

func TestArchiveDraftPreservesUnknownRolesAndRejectsChangedSource(t *testing.T) {
	g := &engineeringGenerator{}
	s, p := proposedArchivePlan(t, g)
	for _, which := range []string{"unknown", "suggested-exclusion", "confirmed-exclusion"} {
		next := p
		next.Shared.Conditions = map[string]ds.Condition{}
		condition := p.Shared.Conditions["has_interfaces"]
		no := false
		switch which {
		case "unknown":
			condition.Value = nil
			condition.Status = "unresolved"
		case "suggested-exclusion":
			condition.Value = &no
			condition.Status = "proposed"
		case "confirmed-exclusion":
			condition.Value = &no
			condition.Status = "confirmed"
		}
		next.Shared.Conditions["has_interfaces"] = condition
		members, e := archiveDraftMembers(next)
		if e != nil {
			t.Fatal(e)
		}
		found := false
		for _, m := range members {
			if m.Role == "interfaces" {
				found = true
			}
		}
		if found == (which == "confirmed-exclusion") {
			t.Fatal("draft applicability silently changed scope", which)
		}
	}
	path := documentsetstore.Private(p.SetID) + "/captures/" + p.ID + ".json"
	var captured ds.CaptureSnapshot
	if e := objectlocation.ReadJSON(s.Store.Root, path, &captured); e != nil {
		t.Fatal(e)
	}
	captured.Messages[1].Text = "TAMPERED PRIVATE SOURCE"
	if e := objectlocation.WriteJSON(s.Store.Root, path, captured, true); e != nil {
		t.Fatal(e)
	}
	req := RunRequest{ArchiveDraft: true, OperationID: "tampered-source", SetID: p.SetID, PlanID: p.ID, PlanHash: p.Hash, ExpectedGeneration: &p.BaseGeneration, CaptureGeneration: &p.Capture.Generation}
	if _, e := s.Start(req); e != nil {
		t.Fatal(e)
	}
	run := wait(t, s, req.OperationID)
	if run.State != "stale" || len(g.requests) != 0 {
		t.Fatal("altered frozen source accepted", run.State)
	}
}
