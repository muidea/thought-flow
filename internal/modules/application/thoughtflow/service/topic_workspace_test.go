package service

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"thoughtflow/internal/pkg/markdown"
	"thoughtflow/internal/pkg/models"
	"thoughtflow/internal/pkg/objectlocation"
	"thoughtflow/internal/pkg/topicstore"
)

func workspaceCall(t *testing.T, s *Service, method, path string, body any) (int, []byte) {
	t.Helper()
	raw, e := json.Marshal(body)
	if e != nil {
		t.Fatal(e)
	}
	w := httptest.NewRecorder()
	s.handleTopicWorkspace(context.Background(), w, httptest.NewRequest(method, path, bytes.NewReader(raw)))
	return w.Code, w.Body.Bytes()
}
func TestWorkspaceHTTPContractCASPlacementAndOwnedDraftReplay(t *testing.T) {
	s, _ := documentHTTPFixture(t, "design")
	ctx := context.Background()
	store := topicstore.New(s.workspace.RootPath)
	topic, e := store.Create(ctx, models.TopicCreateRequest{OperationID: "create-collection-http", Name: "Collection HTTP", Mode: "collection"})
	if e != nil {
		t.Fatal(e)
	}
	c, e := store.Catalog(ctx, topic.ID)
	if e != nil {
		t.Fatal(e)
	}
	req := models.CatalogWriteRequest{OperationID: "planned", ExpectedGeneration: c.Generation, Groups: c.Groups, Entries: []models.CatalogEntry{{ID: "design-entry", GroupID: "default", Title: "Design", Required: true, Binding: models.CatalogBinding{Kind: "planned"}}}}
	path := "/api/topics/" + topic.ID + "/catalog"
	status, raw := workspaceCall(t, s, "PUT", path, req)
	if status != 200 {
		t.Fatal(status, string(raw))
	}
	req.OperationID = "stale"
	status, raw = workspaceCall(t, s, "PUT", path, req)
	if status != 409 {
		t.Fatal(status, string(raw))
	}
	p, e := s.documentProfiles.ResolveLatest("builtin.design-doc")
	if e != nil {
		t.Fatal(e)
	}
	c, e = store.Catalog(ctx, topic.ID)
	if e != nil {
		t.Fatal(e)
	}
	draft := map[string]any{"operation_id": "owned-draft", "expected_generation": c.Generation, "profile": p.Ref, "parameters": map[string]string{}, "content": "# Design\n\nInitial draft"}
	draftPath := "/api/topics/" + topic.ID + "/entries/design-entry/drafts"
	for i := 0; i < 2; i++ {
		status, raw = workspaceCall(t, s, "POST", draftPath, draft)
		if status != 200 {
			t.Fatal(status, string(raw))
		}
	}
	view, e := store.CatalogView(ctx, topic.ID)
	if e != nil {
		t.Fatal(e)
	}
	id := view.Catalog.Entries[0].Binding.ThoughtID
	l, e := objectlocation.For(s.workspace.RootPath).Resolve(ctx, id)
	if e != nil || l.OwnerTopicID != topic.ID || l.Placement.ThoughtPath != filepath.ToSlash(filepath.Join("topics", topic.ID, "thoughts", id+".md")) {
		t.Fatal(l, e)
	}
	status, raw = workspaceCall(t, s, "GET", "/api/topics/"+topic.ID+"/entries/design-entry/content?view=final", nil)
	if status != 422 {
		t.Fatal("draft silently presented as final", status, string(raw))
	}
	status, raw = workspaceCall(t, s, "GET", "/api/topics/"+topic.ID+"/entries/design-entry/content?view=draft", nil)
	if status != 200 {
		t.Fatal(status, string(raw))
	}
	status, raw = workspaceCall(t, s, "PATCH", "/api/thoughts/"+id+"/search-policy", models.SearchPolicyRequest{OperationID: "exclude-http", ExpectedGeneration: l.Generation, GlobalSearch: "exclude"})
	if status != 200 {
		t.Fatal(status, string(raw))
	}
	status, raw = workspaceCall(t, s, "PATCH", "/api/thoughts/"+id+"/search-policy", models.SearchPolicyRequest{OperationID: "exclude-stale", ExpectedGeneration: l.Generation, GlobalSearch: "include"})
	if status != 409 {
		t.Fatal(status, string(raw))
	}
}

func TestWorkspaceHTTPRestoreDeletedDocument(t *testing.T) {
	s, id := documentHTTPFixture(t, "design")
	if e := markdown.DeleteThought(s.workspace.RootPath, id); e != nil {
		t.Fatal(e)
	}
	l, e := objectlocation.For(s.workspace.RootPath).Lookup(id)
	if e != nil {
		t.Fatal(e)
	}
	path := "/api/thoughts/" + id + "/restore"
	req := markdown.RestoreRequest{OperationID: "restore-http", ExpectedGeneration: l.Generation}
	code, raw := workspaceCall(t, s, "POST", path, req)
	if code != 200 {
		t.Fatal(code, string(raw))
	}
	if _, _, e = markdown.ReadThought(s.workspace.RootPath, id); e != nil {
		t.Fatal(e)
	}
	code, raw = workspaceCall(t, s, "POST", path, req)
	if code != 200 {
		t.Fatal("replay", code, string(raw))
	}
}
