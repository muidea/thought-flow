package service

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	topicbiz "thoughtflow/internal/modules/topic/biz"
	"thoughtflow/internal/pkg/contentlibrary"
	ds "thoughtflow/internal/pkg/documentset"
	"thoughtflow/internal/pkg/documentsetstore"
	"thoughtflow/internal/pkg/markdown"
	"thoughtflow/internal/pkg/models"
	"thoughtflow/internal/pkg/objectlocation"
	"thoughtflow/internal/pkg/scratchpad"
	"thoughtflow/internal/pkg/topicstore"
	"thoughtflow/internal/testutil/designfixture"
)

func TestOverviewUsesFullInventoryForCountsAttentionAndSavedConversations(t *testing.T) {
	s, run := savedLibraryFixture(t)
	ctx := context.Background()
	baseline, err := contentlibrary.New(s.workspace.RootPath).List(ctx, contentlibrary.Query{})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 7; i++ {
		if err := markdown.CreateThought(s.workspace.RootPath, models.Thought{ID: fmt.Sprintf("recent-note-%d", i), Type: "text", UserTitle: "Recent independent note", UpdatedAt: time.Now().Add(time.Hour)}, models.ThoughtContent{Original: "Keep the note readable"}); err != nil {
			t.Fatal(err)
		}
	}
	// A later failing attempt has no effect on the selected saved archive.
	failed := ds.Run{ID: "overview-failed-update", OperationID: "overview-failed-update", Plan: run.Plan, State: "failed_terminal", CreatedAt: run.CreatedAt.Add(-time.Hour), UpdatedAt: run.UpdatedAt.Add(-time.Hour)}
	// It remains the latest attempt for a different set, without any saved content.
	failed.Plan.SetID = "set-unsaved-overview"
	failed.Plan.Capture.SessionID = "unsaved-conversation"
	if err := s.documentSets.Store.SaveRun(failed); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	s.handleContentLibrary(ctx, rec, httptest.NewRequest("GET", "/api/content-library?page_size=1&overview=1", nil))
	var response struct {
		Data contentlibrary.Result `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil || rec.Code != 200 {
		t.Fatal(rec.Code, rec.Body.String(), err)
	}
	result := response.Data
	if len(result.Items) != 1 || result.Items[0].Kind != "thought" || result.Counts.Sets != 1 || result.Counts.Notes != baseline.Counts.Notes+7 {
		t.Fatal(result)
	}
	if len(result.ArchivedSessions) != 1 || result.ArchivedSessions[0] != run.Plan.Capture.SessionID {
		t.Fatal(result.ArchivedSessions)
	}
	foundQuestions, foundUnsaved := false, false
	for _, a := range result.Attention {
		foundQuestions = foundQuestions || (a.SetID == run.Plan.SetID && a.Kind == "questions" && a.Selection.ID == run.ID)
		foundUnsaved = foundUnsaved || (a.Kind == "task_failed" && a.SessionID == "unsaved-conversation" && a.Selection.ID == "")
	}
	if !foundQuestions || !foundUnsaved {
		t.Fatal("attention was truncated with the recent content page", result.Attention)
	}
	drafts, err := contentlibrary.New(s.workspace.RootPath).List(ctx, contentlibrary.Query{Kind: "document_set", State: "draft"})
	if err != nil || drafts.Total != 1 || drafts.Items[0].ID != run.Plan.SetID || drafts.Counts.Notes != baseline.Counts.Notes+7 {
		t.Fatal(drafts, err)
	}
}

func TestLegacyAutomaticTopicTitlesAreReadableWithoutRewritingMetadata(t *testing.T) {
	s, run := savedLibraryFixture(t)
	ctx := context.Background()
	root, id := s.workspace.RootPath, run.Plan.SetID
	store := topicstore.New(root)
	s.topicService = topicbiz.NewService(s.workspace, nil, store, nil, nil, nil, nil, nil)
	topic, err := store.Get(ctx, id)
	if err != nil || topic.Name == id {
		t.Fatal("new archive did not persist a readable title", topic, err)
	}
	// Simulate metadata written by the old automatic-archive path.
	topic.Name = id
	path := "topics/" + id + "/topic.yaml"
	if err = objectlocation.WriteJSON(root, path, topic, false); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
	if err != nil {
		t.Fatal(err)
	}
	detail, err := contentlibrary.New(root).Detail(ctx, id, contentlibrary.Selection{})
	if err != nil {
		t.Fatal(err)
	}
	for _, route := range []string{"/api/topics", "/api/topics/" + id, "/api/topics/" + id + "/catalog"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", route, nil)
		switch route {
		case "/api/topics":
			s.handleListTopics(ctx, rec, req)
		case "/api/topics/" + id:
			s.handleGetTopic(ctx, rec, req)
		default:
			s.handleTopicWorkspace(ctx, rec, req)
		}
		var response struct {
			Data json.RawMessage `json:"data"`
		}
		if err = json.Unmarshal(rec.Body.Bytes(), &response); err != nil || rec.Code != 200 {
			t.Fatal(route, rec.Code, rec.Body.String(), err)
		}
		var got models.Topic
		if route == "/api/topics" {
			var topics []models.Topic
			if err = json.Unmarshal(response.Data, &topics); err != nil || len(topics) != 1 {
				t.Fatal(route, string(response.Data), err)
			}
			got = topics[0]
		} else {
			var value struct {
				Topic models.Topic `json:"topic"`
			}
			if err = json.Unmarshal(response.Data, &value); err != nil {
				t.Fatal(err)
			}
			got = value.Topic
		}
		if got.ID != id || got.Name != detail.Title || got.Generation != topic.Generation {
			t.Fatal(route, got)
		}
	}
	after, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("read rewrote authoritative metadata", err)
	}
	topic.Name = "用户自定义订单专题"
	if err = objectlocation.WriteJSON(root, path, topic, false); err != nil {
		t.Fatal(err)
	}
	if got := s.topicDisplay(ctx, topic).(models.Topic); got.Name != topic.Name {
		t.Fatal("custom name overridden", got)
	}
	// An incomplete legacy plan still gets its stored readable description.
	orphan := models.Topic{ID: "set-021eed33daea96cca6e3eb16", Name: "set-021eed33daea96cca6e3eb16", Mode: models.TopicModeCollection, Description: "订单恢复专题"}
	if got := s.topicDisplay(ctx, orphan).(models.Topic); got.Name != orphan.Description || got.ID != orphan.ID {
		t.Fatal("legacy fallback", got)
	}
}

func savedLibraryFixture(t *testing.T) (*Service, ds.Run) {
	t.Helper()
	s, store := automaticSetFixture(t, &captureArchivePlanner{clarify: true, incompleteFirst: true})
	capture, _ := designfixture.Capture()
	sendAutomaticCapture(t, s, capture.Messages[0].Text)
	awaitAutomaticCapture(t, store, func(sp scratchpad.Scratchpad) bool { return sp.SessionContext.FrozenSetProfile != nil })
	sendAutomaticCapture(t, s, "请将以上讨论归档")
	sp := awaitAutomaticCapture(t, store, func(sp scratchpad.Scratchpad) bool {
		if sp.DocumentSetRunID == "" {
			return false
		}
		r, e := s.documentSets.Store.Run(sp.DocumentSetRunID)
		return e == nil && (r.State == "draft_saved" || strings.HasPrefix(r.State, "failed"))
	})
	run, e := s.documentSets.Store.Run(sp.DocumentSetRunID)
	if e != nil || run.State != "draft_saved" {
		t.Fatal(run.State, run.Error, e)
	}
	return s, run
}

func TestContentLibraryFreezesNoteForComposeAndPreview(t *testing.T) {
	s, _ := savedLibraryFixture(t)
	ctx := context.Background()
	id := "source-selection-note"
	if err := markdown.CreateThought(s.workspace.RootPath, models.Thought{ID: id, Type: "text", UserTitle: "Selected note"}, models.ThoughtContent{Original: "Selected original bytes"}); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	s.handleContentLibrary(ctx, rec, httptest.NewRequest("POST", "/api/content-library/sources", strings.NewReader(`{"thought_id":"source-selection-note"}`)))
	var response struct {
		Data []models.ComposeSource `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil || rec.Code != 200 || len(response.Data) != 1 {
		t.Fatal(rec.Code, rec.Body.String(), err)
	}
	if err := markdown.UpdateThought(s.workspace.RootPath, id, func(_ *models.Thought, content *models.ThoughtContent) error {
		content.Original = "Later edit"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	s.handleContentLibrary(ctx, rec, httptest.NewRequest("GET", response.Data[0].SourceLink, nil))
	var preview struct {
		Data models.ThoughtSnapshot `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &preview); err != nil || rec.Code != 200 || preview.Data.Content.Original != "Selected original bytes" || preview.Data.Thought.ID != response.Data[0].SourceID {
		t.Fatal("frozen preview drifted", rec.Code, rec.Body.String(), err)
	}
}
func TestContentLibraryGroupsSavedDraftBeforePaginationAndKeepsSourcesSeparate(t *testing.T) {
	s, run := savedLibraryFixture(t)
	ctx := context.Background()
	lib := contentlibrary.New(s.workspace.RootPath)
	// Topic names have a broader ID contract than document sets. An ordinary
	// Chinese topic must not prevent every note and saved archive from loading.
	if _, e := topicstore.New(s.workspace.RootPath).Create(ctx, models.TopicCreateRequest{Name: "权限认证"}); e != nil {
		t.Fatal(e)
	}
	grouped, e := lib.List(ctx, contentlibrary.Query{Size: 1})
	if e != nil {
		t.Fatal(e)
	}
	if grouped.Counts.Sets != 1 || grouped.Total != grouped.Counts.Notes+1 {
		t.Fatal(grouped.Counts, grouped.Total)
	}
	detail, e := lib.Detail(ctx, run.Plan.SetID, contentlibrary.Selection{})
	if e != nil {
		t.Fatal(e)
	}
	if detail.State != "draft" || detail.Selection.ID != run.ID || len(detail.Members) != len(run.Draft.Members)-1 || len(detail.Sources) != 1 {
		t.Fatal(detail)
	}
	flat, e := lib.List(ctx, contentlibrary.Query{Layout: "flat", Kind: "document_set", Size: 100})
	if e != nil {
		t.Fatal(e)
	}
	if flat.Total != len(detail.Members) {
		t.Fatal("source counted as body", flat.Total, len(detail.Members))
	}
	for _, row := range flat.Items {
		if row.MemberKey == "archive-source" || row.Selection.ID != run.ID {
			t.Fatal(row)
		}
	}
	filtered, e := lib.List(ctx, contentlibrary.Query{Kind: "document_set", Search: detail.Members[0].Title, Size: 1})
	if e != nil || filtered.Total != 1 {
		t.Fatal(filtered, e)
	}
	if _, e = lib.Detail(ctx, run.Plan.SetID, contentlibrary.Selection{Kind: "archive", ID: "unpublished-run"}); !os.IsNotExist(e) {
		t.Fatal("unpublished run readable", e)
	}
	download, err := lib.Markdown(ctx, run.Plan.SetID, detail.Selection)
	if err != nil {
		t.Fatal(err)
	}
	archive, err := zip.NewReader(bytes.NewReader(download), int64(len(download)))
	if err != nil {
		t.Fatal(err)
	}
	bodies := map[string]string{}
	for _, file := range archive.File {
		reader, e := file.Open()
		if e != nil {
			t.Fatal(e)
		}
		raw, e := io.ReadAll(reader)
		reader.Close()
		if e != nil {
			t.Fatal(e)
		}
		bodies[file.Name] = string(raw)
	}
	if len(bodies) != len(run.Draft.Members)+1 {
		t.Fatal("incomplete reading package", len(bodies))
	}
	for _, member := range run.Draft.Members {
		prefix := "documents/"
		if member.Key == "archive-source" {
			prefix = "sources/"
		}
		if bodies[prefix+member.Filename] != member.Body {
			t.Fatal("download changed original", member.Key)
		}
	}
	contexts, err := lib.Contexts(ctx, []string{detail.Members[0].ThoughtID})
	if err != nil || contexts[detail.Members[0].ThoughtID].Version != run.ID {
		t.Fatal(contexts, err)
	}
	for _, request := range []struct {
		path        string
		status      int
		contentType string
	}{
		{"/api/content-library?kind=document_set&page_size=1", 200, "application/json"},
		{"/api/content-library?kind=invalid", 422, "application/json"},
		{"/api/content-library/sets/" + run.Plan.SetID + "?kind=archive&version=unpublished", 404, "application/json"},
		{"/api/content-library/sets/" + run.Plan.SetID + "/markdown?kind=archive&version=" + run.ID, 200, "application/zip"},
	} {
		rec := httptest.NewRecorder()
		s.handleContentLibrary(ctx, rec, httptest.NewRequest(http.MethodGet, request.path, nil))
		if rec.Code != request.status || !strings.HasPrefix(rec.Header().Get("Content-Type"), request.contentType) {
			t.Fatal(request.path, rec.Code, rec.Body.String())
		}
	}
	rec := httptest.NewRecorder()
	s.handleGetThought(ctx, rec, httptest.NewRequest(http.MethodGet, "/api/thoughts/"+detail.Members[0].ThoughtID, nil))
	var envelope struct {
		Data models.ThoughtSnapshot `json:"data"`
	}
	if err = json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil || envelope.Data.ContentSet == nil || envelope.Data.ContentSet.Version != run.ID {
		t.Fatal("note copy lost original link", rec.Body.String(), err)
	}
	// A subsequent failed task changes task state, never the saved content identity.
	failed := run
	failed.ID = "later-failed-run"
	failed.State = "failed_terminal"
	failed.UpdatedAt = run.UpdatedAt.Add(1e9)
	if e = s.documentSets.Store.SaveRun(failed); e != nil {
		t.Fatal(e)
	}
	d, e := lib.Detail(ctx, run.Plan.SetID, contentlibrary.Selection{})
	if e != nil || d.Selection.ID != run.ID || d.TaskState != "failed_terminal" {
		t.Fatal(d.Selection, d.TaskState, e)
	}
	// Editing a note copy does not change the frozen archive.
	m := run.Draft.Members[0]
	l, e := objectlocation.For(s.workspace.RootPath).Lookup(m.ThoughtID)
	if e != nil {
		t.Fatal(e)
	}
	path := filepath.Join(s.workspace.RootPath, l.Placement.ThoughtPath)
	raw, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(path, append(raw, []byte("\nLOCAL_EDIT\n")...), 0644); e != nil {
		t.Fatal(e)
	}
	d, e = lib.Detail(ctx, run.Plan.SetID, contentlibrary.Selection{})
	if e != nil {
		t.Fatal(e)
	}
	for _, member := range append(d.Members, d.Sources...) {
		if strings.Contains(member.Body, "LOCAL_EDIT") {
			t.Fatal("mutable copy replaced original")
		}
	}
	// Adding a whole set freezes body members only. A changed ordinary note
	// projection must not replace the selected archive's source bytes.
	fixed, e := lib.Sources(ctx, contentlibrary.SourceRequest{SetID: d.ID, Selection: d.Selection})
	if e != nil || len(fixed) != len(d.Members) {
		t.Fatal("whole-set sources", len(fixed), e)
	}
	for i, source := range fixed {
		snapshot, err := lib.ResolveSource(ctx, source)
		if err != nil || snapshot.Content.AINotes != d.Members[i].Body || snapshot.Thought.ID != source.SourceID {
			t.Fatal("fixed member changed", source, snapshot, err)
		}
	}
	original, e := lib.Sources(ctx, contentlibrary.SourceRequest{SetID: d.ID, Selection: d.Selection, Member: d.Sources[0].Key})
	if e != nil || len(original) != 1 {
		t.Fatal("explicit original selection", e)
	}
	tampered := fixed[0]
	tampered.Metadata = map[string]string{"set_id": d.ID, "kind": "archive", "version": "unpublished"}
	if _, e = lib.ResolveSource(ctx, tampered); e == nil {
		t.Fatal("tampered fixed identity accepted")
	}
	// Tampering with frozen bytes rejects the read instead of falling back to copies.
	private := filepath.Join(s.workspace.RootPath, documentsetstore.Private(run.Plan.SetID), "drafts", run.ID, m.Filename)
	if e = os.WriteFile(private, []byte("TAMPERED"), 0644); e != nil {
		t.Fatal(e)
	}
	if _, e = lib.Detail(ctx, run.Plan.SetID, contentlibrary.Selection{}); !errors.Is(e, ds.ErrConflict) {
		t.Fatal("tampered archive readable", e)
	}
}
func TestTopicSetReferencesPersistCASAndDoNotSilentlyChangePolicy(t *testing.T) {
	s, run := savedLibraryFixture(t)
	ctx := context.Background()
	root := s.workspace.RootPath
	lib := contentlibrary.New(root)
	topic, e := topicstore.New(root).Create(ctx, models.TopicCreateRequest{OperationID: "library-topic-create", Mode: "collection", Name: "Library references"})
	if e != nil {
		t.Fatal(e)
	}
	historic := models.Thought{ID: "historical-draft-copy", UserTitle: "Old member", DesignDraft: &models.DesignDraftLink{SetID: run.Plan.SetID, RunID: "historical-run", MemberKey: "removed-member"}}
	historicBody := models.ThoughtContent{AINotes: "Retained previous note copy"}
	if e = markdown.WriteThought(root, historic, historicBody); e != nil {
		t.Fatal(e)
	}
	aggregate, e := topicstore.New(root).Create(ctx, models.TopicCreateRequest{OperationID: "library-aggregate", Name: "Historical membership"})
	if e != nil {
		t.Fatal(e)
	}
	if _, _, e = topicstore.New(root).AddMembership(ctx, aggregate, historic, historicBody, models.TopicMembership{ThoughtID: historic.ID}); e != nil {
		t.Fatal(e)
	}
	inferred, e := lib.Topic(ctx, aggregate.ID)
	if e != nil || len(inferred.Sets) != 1 || inferred.Sets[0].Item.ID != run.Plan.SetID {
		t.Fatal("historical member lost stable set", inferred, e)
	}
	zero := uint64(0)
	req := contentlibrary.LinkRequest{OperationID: "pin-draft", Expected: &zero, Entries: []contentlibrary.Link{{SetID: run.Plan.SetID, Policy: "pinned", Selection: contentlibrary.Selection{Kind: "archive", ID: run.ID}}}}
	next, e := lib.WriteLinks(ctx, topic.ID, req)
	if e != nil || next.Generation != 1 {
		t.Fatal(next, e)
	}
	replay, e := lib.WriteLinks(ctx, topic.ID, req)
	if e != nil || replay.Generation != 1 {
		t.Fatal(replay, e)
	}
	req.OperationID = "stale-write"
	if _, e = lib.WriteLinks(ctx, topic.ID, req); !errors.Is(e, ds.ErrConflict) {
		t.Fatal("stale reference accepted", e)
	}
	req.Expected = &next.Generation
	req.OperationID = "no-finalized"
	req.Entries[0] = contentlibrary.Link{SetID: run.Plan.SetID, Policy: "latest_finalized"}
	if _, e = lib.WriteLinks(ctx, topic.ID, req); !os.IsNotExist(e) {
		t.Fatal("policy fell back to draft", e)
	}
	restored := contentlibrary.New(root)
	content, e := restored.Topic(ctx, topic.ID)
	if e != nil || len(content.Sets) != 1 || content.Sets[0].Item.Selection.ID != run.ID {
		t.Fatal(content, e)
	}
	paths, e := topicstore.New(root).TopicPaths(ctx, topic.ID)
	if e != nil {
		t.Fatal(e)
	}
	found := false
	for _, p := range paths {
		found = found || p == contentlibrary.LinksPath(topic.ID)
	}
	if !found {
		t.Fatal("reference not in backup/Git closure", paths)
	}
	// Missing target stays a visible broken reference, never silently disappears.
	if e = os.Remove(filepath.Join(root, ".content-registry/private/document-set-runs", run.ID+".json")); e != nil {
		t.Fatal(e)
	}
	content, e = restored.Topic(ctx, topic.ID)
	if e != nil || len(content.Sets) != 1 || !content.Sets[0].Item.Unavailable {
		t.Fatal(content, e)
	}
}
