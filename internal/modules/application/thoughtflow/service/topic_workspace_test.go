package service

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

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

func TestPublicRawReadKeepsLeaseUntilMigrationSafe(t *testing.T) {
	s, id := documentHTTPFixture(t, "design")
	s.config.Server.PublicThoughtsEnabled = true
	ctx := context.Background()
	r := objectlocation.For(s.workspace.RootPath)
	topic, err := topicstore.New(s.workspace.RootPath).Create(ctx, models.TopicCreateRequest{OperationID: "create", Name: "Raw reading", Mode: "collection"})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := r.PreviewMigration(ctx, id, topic.ID)
	if err != nil {
		t.Fatal(err)
	}
	var reads atomic.Int32
	resolved, resume := make(chan struct{}), make(chan struct{})
	r.Fault = func(phase string) error {
		if phase == "thought_raw_resolved" && reads.Add(1) == 2 {
			close(resolved)
			<-resume
		}
		return nil
	}
	type reply struct {
		status int
		body   string
	}
	done := make(chan reply, 1)
	go func() {
		w := httptest.NewRecorder()
		s.handlePublicThought(ctx, w, httptest.NewRequest("GET", "/p/thoughts/"+id, nil))
		done <- reply{w.Code, w.Body.String()}
	}()
	select {
	case <-resolved:
	case <-time.After(3 * time.Second):
		t.Fatal("public raw read not reached")
	}
	moved := make(chan error, 1)
	go func() {
		_, err := r.ExecuteMigration(ctx, models.MigrationRequest{OperationID: "move", PreviewID: plan.ID, PlanHash: plan.PlanHash, ExpectedObjectGeneration: plan.Source.Generation})
		moved <- err
	}()
	deadline := time.Now().Add(3 * time.Second)
	for r.Namespace.TryRLock() {
		r.Namespace.RUnlock()
		if time.Now().After(deadline) {
			close(resume)
			t.Fatal("migration was not blocked by raw read")
		}
		time.Sleep(time.Millisecond)
	}
	close(resume)
	select {
	case reply := <-done:
		if reply.status != 200 || len(reply.body) == 0 {
			t.Fatal(reply)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("public read deadlocked")
	}
	select {
	case err := <-moved:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("migration deadlocked")
	}
	r.Fault = nil
}
