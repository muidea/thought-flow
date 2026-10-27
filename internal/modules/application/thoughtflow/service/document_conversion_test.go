package service

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"thoughtflow/internal/pkg/documentstore"
	"thoughtflow/internal/pkg/markdown"
	"thoughtflow/internal/pkg/models"
)

func TestPlainThoughtConversionPreservesOriginalAndRequiresCAS(t *testing.T) {
	s, _ := documentHTTPFixture(t, "design")
	original := "\ufeff# Imported\r\n\r\nKeep every field and source line.\r\n"
	captured, e := s.captureService.Capture(context.Background(), models.CaptureCommand{Type: "text", Content: original, Title: "Plain source"})
	if e != nil {
		t.Fatal(e)
	}
	id := captured.Thought.ID
	_, before, e := markdown.ReadThought(s.workspace.RootPath, id)
	if e != nil {
		t.Fatal(e)
	}
	profile, e := s.documentProfiles.ResolveLatest("builtin.design-doc")
	if e != nil {
		t.Fatal(e)
	}
	params := map[string]string{}
	for _, input := range profile.Inputs {
		params[input.Key] = "confirmed"
	}
	base := "/api/thoughts/" + id + "/document-conversions"
	call := func(method string, body any) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(body)
		r := httptest.NewRequest(method, base, bytes.NewReader(raw))
		w := httptest.NewRecorder()
		s.handleDocumentConversion(r.Context(), w, r)
		return w
	}
	get := call("GET", nil)
	if get.Code != 200 {
		t.Fatal(get.Code, get.Body.String())
	}
	req := map[string]any{"operation_id": "convert-source", "expected_content_hash": "stale", "profile_ref": profile.Ref, "parameters": params}
	stale := call("POST", req)
	if stale.Code != 409 {
		t.Fatal(stale.Code, stale.Body.String())
	}
	req["operation_id"] = "convert-source-valid"
	req["expected_content_hash"] = documentstore.HashJSON(before)
	converted := call("POST", req)
	if converted.Code != 201 {
		t.Fatal(converted.Code, converted.Body.String())
	}
	after, c, e := markdown.ReadThought(s.workspace.RootPath, id)
	if e != nil {
		t.Fatal(e)
	}
	if c.Original != before.Original || c.ExtractedContent != before.ExtractedContent || c.Links != before.Links || after.Document == nil || after.DocumentProfile.ProfileID != profile.Ref.ProfileID {
		t.Fatal("conversion lost source or identity", after, c)
	}
	if replay := call("POST", req); replay.Code != 200 {
		t.Fatal(replay.Code, replay.Body.String())
	}
	req["parameters"] = map[string]string{"goal": "different"}
	if conflict := call("POST", req); conflict.Code != 409 {
		t.Fatal(conflict.Code, conflict.Body.String())
	}
}
