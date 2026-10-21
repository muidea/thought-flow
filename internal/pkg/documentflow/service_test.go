package documentflow_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"thoughtflow/assets/documentformats"
	"thoughtflow/internal/pkg/documentflow"
	"thoughtflow/internal/pkg/documentprofile"
	"thoughtflow/internal/pkg/documentstore"
	"thoughtflow/internal/pkg/documentvalidation"
	"thoughtflow/internal/pkg/jobstore"
	"thoughtflow/internal/pkg/markdown"
	"thoughtflow/internal/pkg/models"
)

func setup(t *testing.T, family string) (string, *documentflow.Service, models.Thought, models.ThoughtContent) {
	t.Helper()
	root := t.TempDir()
	registry, e := documentprofile.NewRegistry("", "", documentprofile.Limits{})
	if e != nil {
		t.Fatal(e)
	}
	s := documentflow.Configure(root, registry, documentflow.Options{Enabled: true, AllowManual: true, SemanticRequired: true, MaxRepairAttempts: 2, ReviewTimeout: time.Second})
	raw := documentformats.DesignDocV2
	if family == "research" {
		raw = documentformats.ResearchReportV2
	}
	if family == "article" {
		raw = documentformats.BlogPostV2
	}
	p, e := documentprofile.Parse([]byte(raw), documentprofile.Limits{})
	if e != nil {
		t.Fatal(e)
	}
	params := map[string]string{}
	for _, input := range p.Inputs {
		params[input.Key] = "confirmed"
	}
	params["subtype"] = "opinion"
	sections := map[string]models.DocumentSection{}
	for _, spec := range p.Sections {
		sections[spec.Key] = models.DocumentSection{Content: "G1 D1 T1 Q1 F1 C1: failure and recovery; a limitation, counterexample. Evidence [S1]."}
	}
	body := documentprofile.Render(p, models.DocumentDraft{Title: "Document", Sections: sections, References: []models.DocumentReference{{ID: "S1", SourceLink: "https://example.org/evidence"}}}, params).Content
	thought := models.Thought{ID: "20261021-test-typed", UserTitle: "Document", DocumentProfile: &p.Ref, DocumentParameters: params, DocumentProfileSnapshot: p.Snapshot, DocumentSources: &models.SourceManifest{SchemaVersion: 1, Sources: []models.DocumentSource{{ID: "S1", Type: "external", Locator: "https://example.org/evidence", Version: "1", Hash: "sha256:source", VerificationStatus: "unverified"}}}}
	content := models.ThoughtContent{AINotes: body}
	if e = markdown.WriteThought(root, thought, content); e != nil {
		t.Fatal(e)
	}
	thought, content, e = markdown.ReadThought(root, thought.ID)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(s.Close)
	return root, s, thought, content
}
func check(t *testing.T, s *documentflow.Service, id string) (models.DocumentSnapshot, models.ValidationReport) {
	t.Helper()
	snapshot, e := s.Get(id)
	if e != nil {
		t.Fatal(e)
	}
	report, e := s.Validate(context.Background(), id, snapshot.Head.WorkingRevisionID, snapshot.Head.Generation)
	if e != nil {
		t.Fatal(e)
	}
	if report.Status != "needs_review" {
		t.Fatalf("unexpected report: %+v", report)
	}
	return snapshot, report
}
func request(snapshot models.DocumentSnapshot, r models.ValidationReport, operation string) documentflow.FinalizeRequest {
	req := documentflow.FinalizeRequest{RevisionID: r.RevisionID, ReportID: r.ReportID, OperationID: operation, DeliveryLevel: "proposal"}
	for _, i := range r.Issues {
		if i.HumanConfirmation {
			req.Confirmations = append(req.Confirmations, models.DocumentConfirmation{IssueID: i.ID, Reason: "Inspected the indicated original evidence and contract for this exact revision."})
		}
	}
	return req
}
func TestThreeTypesRevisionCheckFinalizeExactExport(t *testing.T) {
	for _, family := range []string{"design", "research", "article"} {
		t.Run(family, func(t *testing.T) {
			_, s, thought, content := setup(t, family)
			snap, r := check(t, s, thought.ID)
			req := request(snap, r, "finalize-1")
			f, e := s.Finalize(thought.ID, snap.Head.Generation, req)
			if e != nil {
				t.Fatal(e)
			}
			body, e := s.Export(thought.ID, f.RevisionID, "final")
			if e != nil || body != markdown.ExtractDocumentBody(content) {
				t.Fatalf("export differs: %v", e)
			}
			retry, e := s.Finalize(thought.ID, snap.Head.Generation, req)
			if e != nil || retry.FinalizationID != f.FinalizationID {
				t.Fatalf("idempotency failed: %+v %v", retry, e)
			}
			req.DeliveryLevel = "ready_for_use"
			if _, e = s.Finalize(thought.ID, snap.Head.Generation, req); !errors.Is(e, documentstore.ErrConflict) {
				t.Fatalf("changed operation accepted: %v", e)
			}
		})
	}
}
func TestEditsInvalidateReportAndConfirmationButKeepHistoricalFinal(t *testing.T) {
	root, s, thought, content := setup(t, "design")
	snap, r := check(t, s, thought.ID)
	req := request(snap, r, "first")
	f, e := s.Finalize(thought.ID, snap.Head.Generation, req)
	if e != nil {
		t.Fatal(e)
	}
	content.AINotes += "\nA human edit.\n"
	if e = markdown.WriteThought(root, thought, content); e != nil {
		t.Fatal(e)
	}
	current, e := s.Get(thought.ID)
	if e != nil {
		t.Fatal(e)
	}
	if (current.Report != nil && (current.Report.ReportID == r.ReportID || current.Report.Status == "passed")) || current.Head.FinalizedRevisionID != f.RevisionID || current.Head.WorkingRevisionID == f.RevisionID {
		t.Fatalf("old report inherited: %+v", current)
	}
	req.OperationID = "old-report-new-revision"
	req.RevisionID = current.Head.WorkingRevisionID
	if _, e = s.Finalize(thought.ID, current.Head.Generation, req); !errors.Is(e, documentstore.ErrConflict) {
		t.Fatalf("old report accepted: %v", e)
	}
	old, e := s.Export(thought.ID, f.RevisionID, "final")
	if e != nil || strings.Contains(old, "A human edit") {
		t.Fatal("historical final overwritten")
	}
	if _, e = s.Export(thought.ID, current.Head.WorkingRevisionID, "final"); !errors.Is(e, documentflow.ErrRules) {
		t.Fatalf("draft exported as final: %v", e)
	}
}
func TestMetadataOnlyDoesNotChangeRevisionAndStaleWriterCannotWin(t *testing.T) {
	root, s, thought, content := setup(t, "design")
	initial, e := s.Get(thought.ID)
	if e != nil {
		t.Fatal(e)
	}
	thought.AITags = []string{"derived"}
	thought.IndexStatus = "indexed"
	if e = markdown.WriteThought(root, thought, content); e != nil {
		t.Fatal(e)
	}
	same, _ := s.Get(thought.ID)
	if same.Head.Generation != initial.Head.Generation {
		t.Fatal("metadata invalidates body")
	}
	current := thought
	current.UserTitle = "Changed validated title"
	if e = markdown.WriteThought(root, current, content); e != nil {
		t.Fatal(e)
	}
	if e = markdown.WriteThought(root, thought, content); !errors.Is(e, documentstore.ErrConflict) {
		t.Fatalf("stale metadata writer overwrote title: %v", e)
	}
}
func TestExternalEditorCreatesDraftAndRoundTripReservedSections(t *testing.T) {
	root, s, thought, content := setup(t, "design")
	content.AINotes += "\n## Original\nReserved in text\n\n```md\n## Links\n## AI Notes\n  code  \n```\n\nline  \n\n\nlast\n"
	if e := markdown.WriteThought(root, thought, content); e != nil {
		t.Fatal(e)
	}
	parsed, c, e := markdown.ReadThought(root, thought.ID)
	if e != nil || markdown.ExtractDocumentBody(c) != documentstore.Normalize(content.AINotes) {
		t.Fatalf("body truncated: %q %v", c.AINotes, e)
	}
	before, _ := s.Get(thought.ID)
	path := filepath.Join(root, markdown.ThoughtRelativePath(thought.ID))
	raw, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	raw = []byte(strings.Replace(string(raw), "Reserved in text", "Externally edited text", 1))
	raw = []byte(strings.Replace(string(raw), "---\n", "---\nvalid: true\n", 1))
	if e = os.WriteFile(path, raw, 0644); e != nil {
		t.Fatal(e)
	}
	_, c, e = markdown.ReadThought(root, parsed.ID)
	if e != nil || !strings.Contains(c.AINotes, "Externally edited text") {
		t.Fatal(e)
	}
	after, _ := s.Get(thought.ID)
	if after.Head.Generation != before.Head.Generation+1 || (after.Report != nil && after.Report.Status == "passed") {
		t.Fatal("external valid marker trusted")
	}
}

type reviewer struct {
	entered chan struct{}
	gate    chan struct{}
	err     error
}

func (r *reviewer) ReviewDocument(ctx context.Context, d documentstore.RevisionData) ([]models.DocumentIssue, string, error) {
	if r.entered != nil {
		close(r.entered)
	}
	if r.gate != nil {
		select {
		case <-r.gate:
		case <-ctx.Done():
			return nil, "fake", ctx.Err()
		}
	}
	return nil, "fake", r.err
}
func TestLateReviewRemainsHistoricalAndConcurrentReviewRejected(t *testing.T) {
	root, s, thought, content := setup(t, "design")
	review := &reviewer{entered: make(chan struct{}), gate: make(chan struct{})}
	s.Options.Reviewer = review
	initial, _ := s.Get(thought.ID)
	result := make(chan models.ValidationReport, 1)
	failure := make(chan error, 1)
	go func() {
		r, e := s.Validate(context.Background(), thought.ID, initial.Head.WorkingRevisionID, initial.Head.Generation)
		result <- r
		failure <- e
	}()
	<-review.entered
	if _, e := s.Validate(context.Background(), thought.ID, initial.Head.WorkingRevisionID, initial.Head.Generation); !errors.Is(e, documentstore.ErrConflict) {
		t.Fatalf("duplicate paid review started: %v", e)
	}
	content.AINotes += "\nnew working version\n"
	if e := markdown.WriteThought(root, thought, content); e != nil {
		t.Fatal(e)
	}
	close(review.gate)
	old := <-result
	if e := <-failure; e != nil {
		t.Fatal(e)
	}
	latest, _ := s.Get(thought.ID)
	if old.RevisionID == latest.Head.WorkingRevisionID || (latest.Report != nil && latest.Report.ReportID == old.ReportID) {
		t.Fatal("late review replaced draft status")
	}
}
func TestReviewFailureManualPathAndHardErrors(t *testing.T) {
	_, s, thought, _ := setup(t, "design")
	s.Options.Reviewer = &reviewer{err: errors.New("invalid JSON or budget")}
	snap, _ := s.Get(thought.ID)
	r, e := s.Validate(context.Background(), thought.ID, snap.Head.WorkingRevisionID, snap.Head.Generation)
	if e != nil || r.Status != "error" {
		t.Fatalf("review failure claimed success: %v %+v", e, r)
	}
	req := request(snap, r, "manual")
	if _, e = s.Finalize(thought.ID, snap.Head.Generation, req); e != nil {
		t.Fatalf("explicit manual path unavailable: %v", e)
	}
}
func TestProjectionFailureRecoveryFromHEAD(t *testing.T) {
	root, s, thought, _ := setup(t, "design")
	snap, _ := s.Get(thought.ID)
	d, e := s.Store.Revision(thought.ID, snap.Head.WorkingRevisionID)
	if e != nil {
		t.Fatal(e)
	}
	d.Body += "\ncommitted authority before projection\n"
	out, e := s.CreateRevision(thought.ID, snap.Head.Generation, d)
	if e != nil {
		t.Fatal(e)
	}
	if !out.Head.PendingProjection {
		t.Fatal("projection not marked durable pending")
	}
	// Simulate restart after HEAD publication and before Thought write.
	s2 := documentflow.Configure(root, s.Profiles, s.Options)
	defer s2.Close()
	_, c, e := markdown.ReadThought(root, thought.ID)
	if e != nil || !strings.Contains(c.AINotes, "committed authority") {
		t.Fatalf("projection not recovered: %v", e)
	}
	h, _ := s2.Store.Head(thought.ID)
	if h.PendingProjection || h.WorkingRevisionID != out.Head.WorkingRevisionID {
		t.Fatal("recovery created another revision")
	}
}
func TestConcurrentFinalizeAndEditCAS(t *testing.T) {
	_, s, thought, _ := setup(t, "design")
	snap, r := check(t, s, thought.ID)
	d, _ := s.Store.Revision(thought.ID, snap.Head.WorkingRevisionID)
	d.Body += "\nedit\n"
	var wg sync.WaitGroup
	results := make(chan error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, e := s.Finalize(thought.ID, snap.Head.Generation, request(snap, r, "concurrent"))
		results <- e
	}()
	go func() { defer wg.Done(); _, e := s.CreateRevision(thought.ID, snap.Head.Generation, d); results <- e }()
	wg.Wait()
	close(results)
	success, conflict := 0, 0
	for e := range results {
		if e == nil {
			success++
		} else if errors.Is(e, documentstore.ErrConflict) {
			conflict++
		} else {
			t.Fatal(e)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("CAS outcomes %d %d", success, conflict)
	}
}
func TestCanceledReviewAndRestartDoNotReplayModel(t *testing.T) {
	_, s, thought, _ := setup(t, "design")
	s.Options.Jobs = jobstore.New(t.TempDir())
	review := &reviewer{entered: make(chan struct{}), gate: make(chan struct{})}
	s.Options.Reviewer = review
	snap, _ := s.Get(thought.ID)
	done := make(chan error, 1)
	go func() {
		_, e := s.Validate(context.Background(), thought.ID, snap.Head.WorkingRevisionID, snap.Head.Generation)
		done <- e
	}()
	<-review.entered
	s.Close()
	if e := <-done; !errors.Is(e, context.Canceled) {
		t.Fatalf("canceled result published: %v", e)
	}
	jobs, e := s.Options.Jobs.List()
	if e != nil || len(jobs) != 1 || jobs[0].Status != models.JobStatusCanceled {
		t.Fatalf("job not recoverable: %+v %v", jobs, e)
	}
}
func TestRepairScopeIncludesOnlySelectedASTSection(t *testing.T) {
	before := "# Title\n\n## A\nold\n\n## B\nevidence\n"
	after := strings.Replace(before, "old", "corrected", 1)
	if e := documentflow.CheckRepairScope(before, after, []string{"A"}); e != nil {
		t.Fatal(e)
	}
	if e := documentflow.CheckRepairScope(before, strings.Replace(after, "evidence", "invented", 1), []string{"A"}); !errors.Is(e, documentflow.ErrRules) {
		t.Fatal("repair escaped scope")
	}
	if e := documentflow.CheckRepairScope(before, after, []string{"nonexistent"}); !errors.Is(e, documentflow.ErrRules) {
		t.Fatal("unknown section accepted")
	}
}
func TestValidationKeyChangesWithContractAndSources(t *testing.T) {
	_, s, thought, _ := setup(t, "design")
	snap, _ := s.Get(thought.ID)
	d, _ := s.Store.Revision(thought.ID, snap.Head.WorkingRevisionID)
	key := documentvalidation.Key(d)
	d.Contract.Parameters["audience"] = "different"
	out, e := s.CreateRevision(thought.ID, snap.Head.Generation, d)
	if e != nil {
		t.Fatal(e)
	}
	next, _ := s.Store.Revision(thought.ID, out.Head.WorkingRevisionID)
	if documentvalidation.Key(next) == key {
		t.Fatal("contract change reused validation")
	}
}

type repairer struct{ calls int }

func (r *repairer) RepairDocument(_ context.Context, d documentstore.RevisionData, _ []models.DocumentIssue, _ []string) (string, error) {
	r.calls++
	return strings.Replace(d.Body, "## 方案设计\n\n", "## 方案设计\n\nScoped clarification.\n\n", 1), nil
}
func TestRepairBudgetCreatesRevisionsAndRevalidates(t *testing.T) {
	root, s, thought, _ := setup(t, "design")
	opts := s.Options
	opts.Concurrency = 1
	s = documentflow.Configure(root, s.Profiles, opts)
	t.Cleanup(s.Close)
	rpr := &repairer{}
	s.Options.Repairer = rpr
	s.Options.MaxRepairAttempts = 2
	for range 2 {
		snap, r := check(t, s, thought.ID)
		next, e := s.Repair(context.Background(), thought.ID, snap.Head.WorkingRevisionID, r.ReportID, snap.Head.Generation, []string{r.Issues[0].ID}, []string{"proposal"})
		if e != nil {
			t.Fatal(e)
		}
		if next.Revision.RevisionID == snap.Revision.RevisionID || next.Report == nil || next.Report.RevisionID != next.Revision.RevisionID {
			t.Fatal("repair reused old report")
		}
		if _, _, e = markdown.ReadThought(root, thought.ID); e != nil {
			t.Fatal(e)
		}
	}
	snap, r := check(t, s, thought.ID)
	if _, e := s.Repair(context.Background(), thought.ID, snap.Head.WorkingRevisionID, r.ReportID, snap.Head.Generation, []string{r.Issues[0].ID}, []string{"proposal"}); !errors.Is(e, documentflow.ErrRules) {
		t.Fatalf("budget ignored: %v", e)
	}
	if rpr.calls != 2 {
		t.Fatal("unbounded paid repair")
	}
}
func TestPublishedFinalsAreVisibleButOrphanFinalRecordIsNot(t *testing.T) {
	_, s, thought, _ := setup(t, "design")
	snap, r := check(t, s, thought.ID)
	orphan := models.FinalizationRecord{FinalizationID: "f-orphan", RevisionID: r.RevisionID, ReportID: r.ReportID, ValidationKey: r.ValidationKey}
	if e := s.Store.PutRecord(thought.ID, "finalizations", orphan.FinalizationID, orphan); e != nil {
		t.Fatal(e)
	}
	if _, e := s.Export(thought.ID, r.RevisionID, "final"); !errors.Is(e, documentflow.ErrRules) {
		t.Fatalf("unpublished final visible: %v", e)
	}
	if _, e := s.Finalize(thought.ID, snap.Head.Generation, request(snap, r, "publish")); e != nil {
		t.Fatal(e)
	}
}
func TestValidationRetryReusesCompletedOperation(t *testing.T) {
	_, s, thought, _ := setup(t, "design")
	s.Options.Reviewer = &reviewer{}
	snap, _ := s.Get(thought.ID)
	first, e := s.Validate(context.Background(), thought.ID, snap.Head.WorkingRevisionID, snap.Head.Generation, "semantic", "review-op")
	if e != nil {
		t.Fatal(e)
	}
	second, e := s.Validate(context.Background(), thought.ID, snap.Head.WorkingRevisionID, snap.Head.Generation, "semantic", "review-op")
	if e != nil || first.ReportID != second.ReportID {
		t.Fatal("completed step repeated")
	}
	if _, e = s.Validate(context.Background(), thought.ID, snap.Head.WorkingRevisionID, snap.Head.Generation, "manual", "review-op"); !errors.Is(e, documentstore.ErrConflict) {
		t.Fatal("same operation changed review policy")
	}
}
func TestReviewTimeoutReportsErrorAndExplicitManualRetryWorks(t *testing.T) {
	_, s, thought, _ := setup(t, "design")
	s.Options.ReviewTimeout = 5 * time.Millisecond
	s.Options.Reviewer = &reviewer{gate: make(chan struct{})}
	snap, _ := s.Get(thought.ID)
	r, e := s.Validate(context.Background(), thought.ID, snap.Head.WorkingRevisionID, snap.Head.Generation)
	if e != nil || r.Status != "error" {
		t.Fatalf("timeout lost error report: %+v %v", r, e)
	}
	r, e = s.Validate(context.Background(), thought.ID, snap.Head.WorkingRevisionID, snap.Head.Generation, "manual")
	if e != nil || r.Status != "needs_review" {
		t.Fatal("manual fallback unavailable")
	}
}
func TestDisabledFeaturePreservesFinalExports(t *testing.T) {
	_, s, thought, _ := setup(t, "design")
	snap, r := check(t, s, thought.ID)
	f, e := s.Finalize(thought.ID, snap.Head.Generation, request(snap, r, "finish"))
	if e != nil {
		t.Fatal(e)
	}
	s.Options.Enabled = false
	if _, e = s.Export(thought.ID, f.RevisionID, "final"); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Validate(context.Background(), thought.ID, snap.Head.WorkingRevisionID, snap.Head.Generation); !errors.Is(e, documentflow.ErrUnavailable) {
		t.Fatal("feature switch ignored")
	}
}

func TestValidationUsesFrozenProfileWhenRegistryChanges(t *testing.T) {
	_, s, thought, _ := setup(t, "design")
	before, _ := s.Get(thought.ID)
	s.Profiles = nil
	r, e := s.Validate(context.Background(), thought.ID, before.Head.WorkingRevisionID, before.Head.Generation)
	if e != nil || r.ValidationKey != before.Report.ValidationKey {
		t.Fatalf("validation drifted with registry: %+v %v", r, e)
	}
}
func TestSourceUpdatesDoNotRewriteFrozenRevision(t *testing.T) {
	_, s, thought, _ := setup(t, "research")
	initial, _ := s.Get(thought.ID)
	s.Options.SourceHash = func(source models.DocumentSource) (string, error) { return "updated-source", nil }
	d, _ := s.Store.Revision(thought.ID, initial.Head.WorkingRevisionID)
	d.Sources.Sources[0].ResourceID = "original-source"
	next, e := s.CreateRevision(thought.ID, initial.Head.Generation, d)
	if e != nil {
		t.Fatal(e)
	}
	snapshot, e := s.Get(thought.ID)
	if e != nil || snapshot.SourceFreshness != "changed" || snapshot.Revision.RevisionID != next.Revision.RevisionID {
		t.Fatalf("source changes not reported: %+v %v", snapshot, e)
	}
	original, e := s.Store.Revision(thought.ID, initial.Head.WorkingRevisionID)
	if e != nil || original.Sources.Sources[0].Hash != "sha256:source" {
		t.Fatal("historical source snapshot mutated")
	}
}

func TestFinalizationRemainsReadableWhenProjectionCannotComplete(t *testing.T) {
	_, s, thought, _ := setup(t, "design")
	snap, _ := s.Get(thought.ID)
	d, _ := s.Store.Revision(thought.ID, snap.Head.WorkingRevisionID)
	d.Body += "\nAuthority can survive a failed compatibility projection.\n"
	next, e := s.CreateRevision(thought.ID, snap.Head.Generation, d)
	if e != nil {
		t.Fatal(e)
	}
	report, e := s.Validate(context.Background(), thought.ID, next.Revision.RevisionID, next.Head.Generation, "manual")
	if e != nil {
		t.Fatal(e)
	}
	f, e := s.Finalize(thought.ID, next.Head.Generation, request(next, report, "final-before-projection"))
	if e != nil {
		t.Fatal(e)
	}
	body, e := s.Export(thought.ID, f.RevisionID, "final")
	if e != nil || body != documentstore.Normalize(d.Body) {
		t.Fatal("final depends on compatibility projection")
	}
	head, _ := s.Store.Head(thought.ID)
	if !head.PendingProjection {
		t.Fatal("projection failure was not recoverable")
	}
}

func TestFinalizationChecksSourcesOutsideDocumentLock(t *testing.T) {
	_, s, thought, _ := setup(t, "design")
	snap, err := s.Get(thought.ID)
	if err != nil {
		t.Fatal(err)
	}
	d, err := s.Store.Revision(thought.ID, snap.Revision.RevisionID)
	if err != nil {
		t.Fatal(err)
	}
	d.Sources.Sources[0].ResourceID = thought.ID
	if _, err = s.CreateRevision(thought.ID, snap.Head.Generation, d); err != nil {
		t.Fatal(err)
	}
	s.Options.SourceHash = func(source models.DocumentSource) (string, error) {
		unlock := s.Store.Lock(source.ResourceID)
		defer unlock()
		return source.Hash, nil
	}
	snap, report := check(t, s, thought.ID)
	req := request(snap, report, "source-lock-check")
	req.DeliveryLevel = "ready_for_use"
	done := make(chan error, 1)
	go func() { _, e := s.Finalize(thought.ID, snap.Head.Generation, req); done <- e }()
	select {
	case e := <-done:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("finalization held the document lock during source I/O")
	}
}

func TestDisabledWorkflowDoesNotCallRepairOrReview(t *testing.T) {
	for _, disabled := range []string{"feature", "family"} {
		t.Run(disabled, func(t *testing.T) {
			_, s, thought, _ := setup(t, "design")
			snap, report := check(t, s, thought.ID)
			rpr := &repairer{}
			s.Options.Repairer = rpr
			if disabled == "feature" {
				s.Options.Enabled = false
			} else {
				s.Options.Families = []string{"article"}
			}
			if _, e := s.Validate(context.Background(), thought.ID, snap.Head.WorkingRevisionID, snap.Head.Generation); !errors.Is(e, documentflow.ErrUnavailable) {
				t.Fatalf("disabled review accepted: %v", e)
			}
			if _, e := s.Repair(context.Background(), thought.ID, snap.Head.WorkingRevisionID, report.ReportID, snap.Head.Generation, []string{report.Issues[0].ID}, []string{"proposal"}); !errors.Is(e, documentflow.ErrUnavailable) || rpr.calls != 0 {
				t.Fatalf("disabled repair called provider: calls=%d err=%v", rpr.calls, e)
			}
		})
	}
}
