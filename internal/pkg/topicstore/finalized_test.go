package topicstore

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"thoughtflow/internal/pkg/documentflow"
	"thoughtflow/internal/pkg/documentprofile"
	"thoughtflow/internal/pkg/markdown"
	"thoughtflow/internal/pkg/models"
	"thoughtflow/internal/pkg/objectlocation"
)

func finalizedFixture(t *testing.T) (string, *Store, *documentflow.Service, string, models.FinalizationRecord) {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	store := New(root)
	_, e := store.Create(ctx, models.TopicCreateRequest{OperationID: "create", Name: "Finalized", Mode: "collection"})
	if e != nil {
		t.Fatal(e)
	}
	profiles, e := documentprofile.NewRegistry("", "", documentprofile.Limits{})
	if e != nil {
		t.Fatal(e)
	}
	flow := documentflow.Configure(root, profiles, documentflow.Options{Enabled: true, AllowManual: true, SemanticRequired: true})
	t.Cleanup(flow.Close)
	profile, e := profiles.ResolveLatest("builtin.design-doc")
	if e != nil {
		t.Fatal(e)
	}
	params := map[string]string{}
	sections := map[string]models.DocumentSection{}
	for _, input := range profile.Inputs {
		params[input.Key] = "confirmed"
	}
	for _, section := range profile.Sections {
		sections[section.Key] = models.DocumentSection{Content: "G1 D1 T1: failure and recovery. Evidence [S1]. A counterexample and limitation."}
	}
	body := documentprofile.Render(profile, models.DocumentDraft{Title: "Final document", Sections: sections, References: []models.DocumentReference{{ID: "S1", SourceLink: "https://example.org/evidence"}}}, params).Content
	id := "20261023-finalized"
	if e = markdown.WriteThought(root, models.Thought{ID: id, OwnerTopicID: "finalized", UserTitle: "Final document", DocumentProfile: &profile.Ref, DocumentParameters: params, DocumentProfileSnapshot: profile.Snapshot}, models.ThoughtContent{AINotes: body}); e != nil {
		t.Fatal(e)
	}
	return root, store, flow, id, finalizeFixture(t, flow, id, "final-one")
}
func finalizeFixture(t *testing.T, flow *documentflow.Service, id, op string) models.FinalizationRecord {
	t.Helper()
	snap, e := flow.Get(id)
	if e != nil {
		t.Fatal(e)
	}
	report, e := flow.Validate(context.Background(), id, snap.Revision.RevisionID, snap.Head.Generation)
	if e != nil {
		t.Fatal(e)
	}
	req := documentflow.FinalizeRequest{OperationID: op, RevisionID: report.RevisionID, ReportID: report.ReportID, DeliveryLevel: "proposal"}
	for _, issue := range report.Issues {
		if issue.HumanConfirmation {
			req.Confirmations = append(req.Confirmations, models.DocumentConfirmation{IssueID: issue.ID, Reason: "Reviewed the exact revision and original evidence."})
		} else if issue.Severity == "error" {
			t.Fatalf("fixture hard issue: %+v", issue)
		}
	}
	snap, e = flow.Get(id)
	if e != nil {
		t.Fatal(e)
	}
	f, e := flow.Finalize(id, snap.Head.Generation, req)
	if e != nil {
		t.Fatal(e)
	}
	return f
}
func TestPinnedFinalizedVersionNewDraftDeliveryAndSnapshot(t *testing.T) {
	root, store, flow, id, first := finalizedFixture(t)
	ctx := context.Background()
	c, e := store.Catalog(ctx, "finalized")
	if e != nil {
		t.Fatal(e)
	}
	entry := models.CatalogEntry{ID: "design", GroupID: "default", Title: "Design", Required: true, Binding: models.CatalogBinding{Kind: "thought", ThoughtID: id, VersionPolicy: "pinned_finalized", RevisionID: first.RevisionID, FinalizationID: first.FinalizationID}}
	view, e := store.WriteCatalog(ctx, "finalized", models.CatalogWriteRequest{OperationID: "pin", ExpectedGeneration: c.Generation, RequiredDeliveryLevel: "proposal", Groups: c.Groups, Entries: []models.CatalogEntry{entry}})
	if e != nil || view.DeliveredCount != 1 {
		t.Fatal(view, e)
	}
	before, e := store.EntryContent(ctx, "finalized", "design", "final", "")
	if e != nil {
		t.Fatal(e)
	}
	if e = markdown.UpdateThought(root, id, func(_ *models.Thought, content *models.ThoughtContent) error {
		content.AINotes += "\nA later working draft."
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	view, e = store.CatalogView(ctx, "finalized")
	if e != nil || !view.Entries[0].HasNewDraft || view.Entries[0].SelectedRevisionID != first.RevisionID || view.DeliveredCount != 1 {
		t.Fatal(view, e)
	}
	_ = finalizeFixture(t, flow, id, "final-two")
	after, e := store.EntryContent(ctx, "finalized", "design", "final", "")
	if e != nil || after.Body != before.Body || after.Revision.RevisionID != first.RevisionID {
		t.Fatal("pin drifted", after, e)
	}
	snapshot, e := store.CreateSnapshot(ctx, "finalized", SnapshotRequest{OperationID: "snapshot", ThoughtID: id, RevisionID: first.RevisionID, FinalizationID: first.FinalizationID})
	if e != nil || snapshot.BodyHash != objectlocation.Hash([]byte(before.Body)) {
		t.Fatal(snapshot, e)
	}
	result, e := store.ExportPackage(ctx, "finalized", ExportRequest{OperationID: "delivery", Kind: "delivery"})
	if e != nil || result.Manifest.Private || len(result.Manifest.Selections) != 1 {
		t.Fatal(result, e)
	}
	raw, e := store.ExportBytes("finalized", result.ID)
	if e != nil || len(raw) == 0 {
		t.Fatal(e)
	}
	l, e := objectlocation.For(root).Resolve(ctx, id)
	if e != nil {
		t.Fatal(e)
	}
	revisionBytes, e := os.ReadFile(filepath.Join(root, l.Placement.DocumentRoot, "revisions", first.RevisionID, "body.md"))
	if e != nil {
		t.Fatal(e)
	}
	plan, e := objectlocation.For(root).PreviewMigration(ctx, id, "")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = objectlocation.For(root).ExecuteMigration(ctx, models.MigrationRequest{OperationID: "move-typed", PreviewID: plan.ID, PlanHash: plan.PlanHash, ExpectedObjectGeneration: plan.Source.Generation}); e != nil {
		t.Fatal(e)
	}
	moved, e := flow.Store.Revision(id, first.RevisionID)
	if e != nil || moved.Body != string(revisionBytes) {
		t.Fatal("revision changed on migration", e)
	}
}

func TestRestoreFinalizedOwnedDocumentRetainsItsExactVersionChain(t *testing.T) {
	root, store, flow, id, final := finalizedFixture(t)
	ctx := context.Background()
	before, e := flow.Store.Revision(id, final.RevisionID)
	if e != nil {
		t.Fatal(e)
	}
	if e = markdown.DeleteThought(root, id); e != nil {
		t.Fatal(e)
	}
	r := objectlocation.For(root)
	l, e := r.Lookup(id)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = markdown.RestoreThought(ctx, root, id, markdown.RestoreRequest{OperationID: "restore-typed", ExpectedGeneration: l.Generation}); e != nil {
		t.Fatal(e)
	}
	after, e := flow.Store.Revision(id, final.RevisionID)
	if e != nil || after.Revision != before.Revision || after.Body != before.Body {
		t.Fatal("restore changed immutable revision", e)
	}
	if exported, e := flow.Export(id, final.RevisionID, "final"); e != nil || exported != before.Body {
		t.Fatal("finalization chain not restored", e)
	}
	view, e := store.CatalogView(ctx, "finalized")
	if e != nil || len(view.Uncatalogued) != 1 || len(view.Deleted) != 0 {
		t.Fatal(view, e)
	}
}
