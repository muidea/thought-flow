package documentsetflow

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"thoughtflow/internal/pkg/ai"
	ds "thoughtflow/internal/pkg/documentset"
	"thoughtflow/internal/pkg/documentsetstore"
	"thoughtflow/internal/pkg/documentstore"
	"thoughtflow/internal/pkg/markdown"
	"thoughtflow/internal/pkg/models"
	"thoughtflow/internal/pkg/objectlocation"
	"thoughtflow/internal/pkg/topicstore"
)

func cloneRevision(t *testing.T, r ds.Revision) ds.Revision {
	t.Helper()
	raw, e := json.Marshal(r)
	if e != nil {
		t.Fatal(e)
	}
	var out ds.Revision
	if e = json.Unmarshal(raw, &out); e != nil {
		t.Fatal(e)
	}
	return out
}
func assertCode(t *testing.T, r ds.Revision, code string) {
	t.Helper()
	for _, i := range ds.Validate(r).Issues {
		if i.Code == code {
			return
		}
	}
	t.Fatalf("missing issue %s: %+v", code, ds.Validate(r).Issues)
}

func TestFinalBodyAndSemanticEvidenceCannotBeFaked(t *testing.T) {
	s, _, p := setup(t, &generator{}, reviewer{})
	start(t, s, p, "quality")
	run := wait(t, s, "quality")
	if run.State != "committed" {
		t.Fatal(run.State, run.Error)
	}
	base := *run.Candidate
	tests := []struct {
		name, code string
		mutate     func(*ds.Revision)
	}{
		{"missing member", "member_missing", func(r *ds.Revision) { r.Members = r.Members[1:] }},
		{"owner duplicate", "duplicate_contract_owner", func(r *ds.Revision) {
			for _, f := range r.Plan.Shared.Facts {
				if f.Kind == "interface" {
					r.Members[0].Body += ds.FactBlock(f)
					r.Members[0].BodyHash = documentstore.Hash([]byte(r.Members[0].Body))
				}
			}
		}},
		{"nullable mismatch", "interface_entity_conflict", func(r *ds.Revision) {
			for i := range r.Plan.Shared.Facts {
				f := &r.Plan.Shared.Facts[i]
				if f.Kind == "interface" {
					f.Fields[0].Nullable = true
				}
			}
			r.Plan.Hash = ds.PlanHash(r.Plan)
		}},
		{"type mismatch", "interface_entity_conflict", func(r *ds.Revision) {
			for i := range r.Plan.Shared.Facts {
				f := &r.Plan.Shared.Facts[i]
				if f.Kind == "interface" {
					f.Fields[0].Type = "integer"
				}
			}
			r.Plan.Hash = ds.PlanHash(r.Plan)
		}},
		{"sidecar true body false", "final_contract_mismatch", func(r *ds.Revision) {
			for i := range r.Members {
				a := &r.Members[i]
				if a.Key == "interfaces" {
					a.Body = strings.Replace(a.Body, "| string |", "| integer |", 1)
					a.BodyHash = documentstore.Hash([]byte(a.Body))
				}
			}
		}},
		{"no verification coverage", "verification_coverage_missing", func(r *ds.Revision) { r.Plan.Shared.Relations = nil; r.Plan.Hash = ds.PlanHash(r.Plan) }},
		{"changed revision", "member_hash", func(r *ds.Revision) { r.Members[0].Body += "Changed without hash" }},
		{"wrong fixed link", "member_link_unresolved", func(r *ds.Revision) {
			r.Members[0].Body += "\n[other](docset://wrong-set/architecture#scope)"
			r.Members[0].BodyHash = documentstore.Hash([]byte(r.Members[0].Body))
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) { r := cloneRevision(t, base); tt.mutate(&r); assertCode(t, r, tt.code) })
	}
	for _, kind := range []string{"missing", "duplicate", "wrong section", "forged status"} {
		t.Run(kind, func(t *testing.T) {
			r := cloneRevision(t, base)
			switch kind {
			case "missing":
				r.Report.Evidence = nil
			case "duplicate":
				r.Report.Evidence = append(r.Report.Evidence, r.Report.Evidence[0])
			case "wrong section":
				r.Report.Evidence[0].Locations[0].Section = "verification"
			case "forged status":
				r.Report.SemanticStatus = "not_run"
			}
			ds.Rehash(&r)
			if ds.Ready(r) == nil {
				t.Fatal("forged gate accepted")
			}
		})
	}
	r := cloneRevision(t, base)
	for i := range r.Plan.Shared.Facts {
		f := &r.Plan.Shared.Facts[i]
		if f.Kind == "interface" {
			f.Fields[0].Classification = "computed"
			f.Fields[0].EntityID = ""
			f.Fields[0].EntityField = ""
			for j := range r.Members {
				if r.Members[j].Key == f.Owner {
					r.Members[j].Body = strings.Replace(r.Members[j].Body, ds.FactBlock(base.Plan.Shared.Facts[i]), ds.FactBlock(*f), 1)
					r.Members[j].BodyHash = documentstore.Hash([]byte(r.Members[j].Body))
				}
			}
		}
	}
	r.Plan.Hash = ds.PlanHash(r.Plan)
	for _, i := range ds.Validate(r).Issues {
		if i.Code == "entity_mapping_missing" {
			t.Fatal("computed field incorrectly required a column")
		}
	}
	// All inspectable copies are authenticated, including a report file not used as the primary manifest.
	path := filepath.Join(s.Store.Root, documentsetstore.Base(p.SetID), "revisions", base.ID, "validation.json")
	if e := os.WriteFile(path, []byte(`{"status":"ready"}`), 0644); e != nil {
		t.Fatal(e)
	}
	if _, e := s.Store.Read(p.SetID, base.ID); e == nil {
		t.Fatal("tampered adjacent report accepted")
	}
}

func TestConservativeBudgetsAndContextCoverage(t *testing.T) {
	s, _, p := setup(t, &generator{}, reviewer{})
	r := ds.Run{ID: "budget", OperationID: "budget", Plan: p, State: "checking"}
	if e := s.Store.SaveRun(r); e != nil {
		t.Fatal(e)
	}
	r.Plan.Profile.Limits.MaxCalls = 1
	if _, e := s.callStart(&r, "test", "", map[string]string{"a": "b"}); e != nil {
		t.Fatal(e)
	}
	if _, e := s.callStart(&r, "test", "", map[string]string{"a": "b"}); !errors.Is(e, ds.ErrBudget) {
		t.Fatal(e)
	}
	r.Calls = nil
	r.Plan.Profile.Limits.MaxInputTokens = 2
	if _, e := s.callStart(&r, "test", "", strings.Repeat("a", 3)); !errors.Is(e, ds.ErrBudget) {
		t.Fatal("token budget", e)
	}
	r.Plan.Profile.Limits.MaxInputTokens = 1000
	r.Plan.Profile.Limits.MaxContextBytes = 2
	if _, e := s.callStart(&r, "test", "", "abcd"); !errors.Is(e, ds.ErrBudget) {
		t.Fatal("context budget", e)
	}
}

func TestMarkdownPreviewRetainsOriginalAndDetectsBoundaries(t *testing.T) {
	s, _, p := setup(t, &generator{}, nil)
	original := "\ufeff# Source\r\n\r\n[Module](modules/order.md#scope)\r\n[missing](absent.md)\r\n![local](image.png)\r\n\r\n```text\r\nDo not lose this field: 123\r\n```\r\n"
	req := MarkdownPreviewRequest{OperationID: "md-preview", SetID: p.SetID, PlanID: p.ID, PlanHash: p.Hash, Files: []MarkdownFile{{"architecture.md", "architecture", original}, {"modules/order.md", "module:MOD-ORDER", "# Scope\n\n[Architecture](../architecture.md#scope)\n\nKeep every word."}}}
	preview, e := s.PreviewMarkdown(req)
	if e != nil {
		t.Fatal(e)
	}
	if preview.Mappings[0].Original != original || preview.Mappings[0].OriginalHash != documentstore.Hash([]byte(original)) || len(preview.Warnings) != 2 || len(preview.Cycles) == 0 {
		t.Fatalf("preview %+v", preview)
	}
	if !strings.Contains(preview.Mappings[0].Derived, "docset://"+p.SetID+"/module:MOD-ORDER#scope") {
		t.Fatal("link not normalized", preview.Mappings[0].Derived)
	}
	again, e := s.PreviewMarkdown(req)
	if e != nil || again.RequestHash != preview.RequestHash {
		t.Fatal(e)
	}
	req.Files[0].Content += "different"
	if _, e = s.PreviewMarkdown(req); !errors.Is(e, ds.ErrConflict) {
		t.Fatal("same operation changed", e)
	}
	for _, bad := range []string{"../secret.md", "/secret.md", "a\\b.md", "a/../b.md"} {
		req.OperationID = documentstore.ID("invalid-")
		req.Files[0].Path = bad
		if _, e = s.PreviewMarkdown(req); e == nil {
			t.Fatal("unsafe path", bad)
		}
	}
	// A second import with resolvable sources is kept as an immutable candidate;
	// Original survives even when a local attachment prevents publication.
	run, e := s.Start(RunRequest{OperationID: "md-run", SetID: p.SetID, PlanID: p.ID, PlanHash: p.Hash, ExpectedGeneration: &p.BaseGeneration, CaptureGeneration: &p.Capture.Generation, MarkdownPreviewID: preview.ID})
	if e != nil {
		t.Fatal(e)
	}
	run = wait(t, s, run.ID)
	if run.Candidate == nil {
		t.Fatal(run.State, run.Error)
	}
	found := false
	for _, a := range run.Candidate.Members {
		if a.Key == "architecture" {
			found = true
			if a.Original != original {
				t.Fatal("source bytes changed")
			}
			d, e := documentstore.New(s.Store.Root).Revision(a.DocumentID, a.RevisionID)
			if e != nil || d.Projection == nil || d.Projection.Content.Original != original {
				t.Fatal("immutable original missing", e)
			}
		}
	}
	if !found {
		t.Fatal("imported candidate missing")
	}
}

func TestNonSoftwareSetUsesSamePublicationAndGate(t *testing.T) {
	root := t.TempDir()
	capture, _ := fixture()
	s := New(root, Options{Enabled: true, Generator: &generator{}, Reviewer: reviewer{}, Snapshot: func(string) (ds.CaptureSnapshot, error) { return capture, nil }})
	defer s.Close()
	shared := ds.SharedDesign{Inputs: map[string]string{"goal": "Study needs", "scope": "Compare alternatives"}, Facts: []ds.Fact{{ID: "REQ-STUDY", Kind: "requirement", Name: "Study", Content: "Compare options", Status: "confirmed", Source: ds.Source{Message: 0, Quote: "Build ordering"}, Owner: "brief"}, {ID: "DEC-ANALYZE", Kind: "decision", Name: "Comparison", Content: "Evaluate evidence", Status: "confirmed", Source: ds.Source{Message: 0, Quote: "Build ordering"}, Owner: "analysis"}, {ID: "TEST-REVIEW", Kind: "verification", Name: "Review", Content: "Verify evidence", Status: "confirmed", Source: ds.Source{Message: 0, Quote: "Build ordering"}, Owner: "recommendations"}}, Relations: []ds.Relation{{ID: "REL-VERIFY", Kind: "verification", From: "REQ-STUDY", To: "TEST-REVIEW"}}}
	profile := ds.Builtins()[1]
	zero := uint64(0)
	p, e := s.Plan(context.Background(), capture.SessionID, PlanRequest{OperationID: "research-plan", ExpectedGeneration: &zero, CaptureGeneration: &capture.Generation, ProfileID: profile.ID, ProfileVersion: profile.Version, ProfileHash: profile.Hash, Shared: &shared})
	if e != nil {
		t.Fatal(e)
	}
	start(t, s, p, "research-run")
	r := wait(t, s, "research-run")
	if r.State != "committed" || len(r.Candidate.Members) != 3 {
		t.Fatal(r.State, r.Error)
	}
}

func TestLegacyTopicBackupContainsRetainedSetClosure(t *testing.T) {
	s, _, p := setup(t, &generator{}, reviewer{})
	start(t, s, p, "legacy-backup")
	r := wait(t, s, "legacy-backup")
	if r.State != "committed" {
		t.Fatal(r.State, r.Error)
	}
	topics := topicstore.New(s.Store.Root)
	result, e := topics.ExportPackage(context.Background(), p.SetID, topicstore.ExportRequest{OperationID: "topic-backup", Kind: "backup"})
	if e != nil {
		t.Fatal(e)
	}
	raw, e := topics.ExportBytes(p.SetID, result.ID)
	if e != nil {
		t.Fatal(e)
	}
	destination := t.TempDir()
	_, e = topicstore.New(destination).ImportPackage(context.Background(), "restore-topic", raw)
	if e != nil {
		t.Fatal(e)
	}
	restored, e := documentsetstore.New(destination).Read(p.SetID, r.Candidate.ID)
	if e != nil || restored.Hash != r.Candidate.Hash {
		t.Fatal("set closure lost", e)
	}
}

// Regression for A committed before projection, followed by B and a manual edit.
func TestOldIntentNeverRollsBackNewHeadAndManualContent(t *testing.T) {
	s, c, p := setup(t, &generator{}, reviewer{})
	s.Store.Fault = func(point string) error {
		if point == "before_projection" {
			return errors.New("crash A projection")
		}
		return nil
	}
	start(t, s, p, "commit-a")
	a := wait(t, s, "commit-a")
	if a.State != "committed" {
		t.Fatal(a.State, a.Error)
	}
	s.Store.Fault = nil
	if e := s.Store.Project(p.SetID); e != nil {
		t.Fatal(e)
	}
	c.Generation++
	c.ContextHash = "context-b"
	c.Messages[0].Text += " Additional implementation details."
	h, _ := s.Store.Head(p.SetID)
	_, shared := fixture()
	shared.Facts[0].Content += " Preserve audit records."
	next, e := s.Plan(context.Background(), c.SessionID, PlanRequest{OperationID: "plan-b", SetID: p.SetID, ExpectedGeneration: &h.Generation, CaptureGeneration: &c.Generation, ProfileID: p.Profile.ID, ProfileVersion: p.Profile.Version, ProfileHash: p.Profile.Hash, Shared: &shared})
	if e != nil {
		t.Fatal(e)
	}
	start(t, s, next, "commit-b")
	b := wait(t, s, "commit-b")
	if b.State != "committed" {
		t.Fatal(b.State, b.Error)
	}
	id := next.Members[0].DocumentID
	if e = markdown.UpdateThought(s.Store.Root, id, func(_ *models.Thought, c *models.ThoughtContent) error {
		c.AINotes += "\nManual text after B."
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	// Re-open A outbox even after B completed it, as a durable replay test.
	intents, e := s.Store.Intents(p.SetID)
	if e != nil {
		t.Fatal(e)
	}
	for _, in := range intents {
		if in.OperationID == "commit-a" {
			in.Done = false
			if e = s.Store.SaveIntent(in); e != nil {
				t.Fatal(e)
			}
		}
	}
	if issues := s.Recover(); len(issues) > 0 {
		t.Fatal(issues)
	}
	h, _ = s.Store.Head(p.SetID)
	if h.Working != b.Candidate.ID {
		t.Fatal("head rolled back")
	}
	_, content, e := markdown.ReadThought(s.Store.Root, id)
	if e != nil || !strings.Contains(content.AINotes, "Manual text after B.") {
		t.Fatal("manual content lost", e)
	}
	// The ancestor set still restores when personal HEADs all point at B/new edits.
	archive, e := s.Store.Export(p.SetID, documentsetstore.ExportRequest{OperationID: "all-history", ExpectedGeneration: &h.Generation, RevisionID: h.Working, Kind: "backup"})
	if e != nil {
		t.Fatal(e)
	}
	restored := documentsetstore.New(t.TempDir())
	if _, e = restored.Import(archive, "all-history-import"); e != nil {
		t.Fatal(e)
	}
	if old, e := restored.Read(p.SetID, a.Candidate.ID); e != nil || old.Hash != a.Candidate.Hash {
		t.Fatal("ancestor lost", e)
	}
}

func TestManagedContractEditCreatesProposal(t *testing.T) {
	s, _, p := setup(t, &generator{}, reviewer{})
	start(t, s, p, "contract-edit")
	r := wait(t, s, "contract-edit")
	if r.State != "committed" {
		t.Fatal(r.State, r.Error)
	}
	id := ""
	for _, a := range r.Candidate.Members {
		if a.Key == "interfaces" {
			id = a.DocumentID
		}
	}
	e := markdown.UpdateThought(s.Store.Root, id, func(_ *models.Thought, c *models.ThoughtContent) error {
		c.AINotes = strings.Replace(c.AINotes, "| string |", "| integer |", 1)
		return nil
	})
	if e == nil {
		t.Fatal("contract silently forked")
	}
	entries, e := os.ReadDir(filepath.Join(s.Store.Root, ".content-registry/private/document-set-proposals", id))
	if e != nil || len(entries) != 1 {
		t.Fatal("proposal not retained", e)
	}
}

type repairableGenerator struct{}

func (g repairableGenerator) GenerateDocument(ctx context.Context, r ai.DocumentGenerationRequest) (models.DocumentDraft, error) {
	draft, e := (&generator{}).GenerateDocument(ctx, r)
	if r.Context.Title == "Architecture" && !strings.Contains(r.Context.Body, "repair-scope") {
		v := draft.Sections["scope"]
		v.Content += " WRONG CONTRACT"
		draft.Sections["scope"] = v
	}
	return draft, e
}

type contractReviewer struct{}

func (contractReviewer) ReviewDocumentSet(ctx context.Context, items []ds.ReviewItem, members []ds.Artifact, shared ds.SharedDesign) (ds.ReviewResponse, string, error) {
	response, model, e := (reviewer{}).ReviewDocumentSet(ctx, items, members, shared)
	for _, m := range members {
		if strings.Contains(m.Body, "WRONG CONTRACT") {
			response.Issues = append(response.Issues, ds.Issue{Code: "repair-scope", Severity: "error", Message: "Scope explanation contradicts the confirmed contract", Locations: []ds.Location{{Member: m.Key, Section: "scope", Evidence: "WRONG CONTRACT"}}})
			break
		}
	}
	return response, model, e
}
func TestSemanticRepairIsBoundedAndPreservesFacts(t *testing.T) {
	s, _, p := setup(t, repairableGenerator{}, contractReviewer{})
	start(t, s, p, "repairable")
	r := wait(t, s, "repairable")
	if r.State != "committed" || r.RepairRounds != 1 {
		t.Fatal(r.State, r.RepairRounds, r.Error)
	}
	if r.Candidate.Plan.Shared.Hash != p.Shared.Hash {
		t.Fatal("repair changed confirmed facts")
	}
	for _, m := range r.Candidate.Members {
		if strings.Contains(m.Body, "WRONG CONTRACT") {
			t.Fatal("wrong explanation published")
		}
	}
}
func TestPreHeadFaultsCannotExposeMembersAndOtherRecoveryContinues(t *testing.T) {
	for _, point := range []string{"set_revision_written", "intent_written"} {
		t.Run(point, func(t *testing.T) {
			s, _, p := setup(t, &generator{}, reviewer{})
			s.Store.Fault = func(step string) error {
				if step == point {
					return errors.New("interrupted before head")
				}
				return nil
			}
			start(t, s, p, "before-head")
			r := wait(t, s, "before-head")
			if r.State == "committed" {
				t.Fatal("unexpected publication")
			}
			if _, e := s.Store.Head(p.SetID); !os.IsNotExist(e) {
				t.Fatal("unpublished head visible", e)
			}
			for _, m := range p.Members {
				if _, e := objectlocation.For(s.Store.Root).Resolve(context.Background(), m.DocumentID); !os.IsNotExist(e) {
					t.Fatal("pending member visible", e)
				}
			}
			s.Store.Fault = nil
			if issues := s.Recover(); len(issues) != 0 {
				t.Fatal(issues)
			}
			// Existing prepared bytes may be retained, but are never activated by recovery.
			for _, m := range p.Members {
				if _, _, e := markdown.ReadThought(s.Store.Root, m.DocumentID); !os.IsNotExist(e) {
					t.Fatal("half set became readable", e)
				}
			}
		})
	}
}
