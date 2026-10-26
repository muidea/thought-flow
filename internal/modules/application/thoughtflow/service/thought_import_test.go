package service

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http/httptest"
	"strings"
	"testing"

	capturebiz "thoughtflow/internal/modules/capture/biz"
	"thoughtflow/internal/pkg/markdown"
	"thoughtflow/internal/pkg/models"
)

func callThoughtImport(t *testing.T, svc *Service, names []string, raw []byte) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for _, name := range names {
		file, err := writer.CreateFormFile("file", name)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = file.Write(raw)
	}
	_ = writer.Close()
	req := httptest.NewRequest("POST", "/api/thoughts/import", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	res := httptest.NewRecorder()
	svc.handleImportThought(req.Context(), res, req)
	return res
}

func TestThoughtMarkdownImportHTTPPreservesSourceAndDownloads(t *testing.T) {
	ws := &models.Workspace{RootPath: t.TempDir()}
	svc := &Service{workspace: ws, captureService: capturebiz.NewService(ws, nil, nil)}
	source := []byte("\xef\xbb\xbf---\r\ntitle: 完整资料\r\nextra: {keep: true}\r\n---\r\n# Content\r\n\r\n## AI Notes\r\n```\r\n## Original\r\n```\r\n![image](../image.png)\r\n\r\n")
	res := callThoughtImport(t, svc, []string{"资料.md"}, source)
	if res.Code != 201 {
		t.Fatalf("import: %d %s", res.Code, res.Body.String())
	}
	var envelope struct{ Data models.CaptureResult }
	if err := json.Unmarshal(res.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	id := envelope.Data.Thought.ID
	for _, endpoint := range []string{"import-source", "markdown"} {
		req := httptest.NewRequest("GET", "/api/thoughts/"+id+"/"+endpoint, nil)
		got := httptest.NewRecorder()
		if endpoint == "markdown" {
			svc.handleThoughtMarkdown(req.Context(), got, req)
			_, content := markdown.ParseThought(got.Body.Bytes())
			if !bytes.Equal([]byte(content.Original), source) {
				t.Fatal("canonical Thought lost original content")
			}
		} else {
			svc.handleThoughtImportSource(req.Context(), got, req)
			if !bytes.Equal(got.Body.Bytes(), source) {
				t.Fatal("original download changed bytes")
			}
		}
		if got.Code != 200 || !strings.Contains(got.Header().Get("Content-Disposition"), "attachment;") || got.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Fatal("download failed or unsafe headers", got.Code, got.Header())
		}
	}
	if err := svc.captureService.DeleteThought(context.Background(), id, "delete"); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "/api/thoughts/"+id+"/import-source", nil)
	deleted := httptest.NewRecorder()
	svc.handleThoughtImportSource(req.Context(), deleted, req)
	if deleted.Code != 404 {
		t.Fatal("deleted import remains downloadable", deleted.Code)
	}
}

func TestThoughtMarkdownImportHTTPRejectsIncompleteOrInvalidFiles(t *testing.T) {
	ws := &models.Workspace{RootPath: t.TempDir()}
	svc := &Service{workspace: ws, captureService: capturebiz.NewService(ws, nil, nil)}
	for _, tc := range []struct {
		names []string
		raw   []byte
		want  int
	}{
		{nil, []byte("text"), 400}, {[]string{"one.md", "two.md"}, []byte("text"), 400},
		{[]string{"one.html"}, []byte("<script>alert(1)</script>"), 422}, {[]string{"one.md"}, []byte{0xff, 0xfe}, 422},
		{[]string{"one.md"}, bytes.Repeat([]byte("x"), capturebiz.MaxMarkdownImportBytes+1), 413},
	} {
		if res := callThoughtImport(t, svc, tc.names, tc.raw); res.Code != tc.want {
			t.Fatalf("invalid upload status=%d want=%d body=%s", res.Code, tc.want, res.Body.String())
		}
	}
	// A transport cut after the file body must not create a Thought before
	// the trailing multipart boundary has been checked.
	req := httptest.NewRequest("POST", "/api/thoughts/import", strings.NewReader("--cut\r\nContent-Disposition: form-data; name=\"file\"; filename=\"one.md\"\r\n\r\n# Truncated"))
	req.Header.Set("Content-Type", "multipart/form-data; boundary=cut")
	res := httptest.NewRecorder()
	svc.handleImportThought(req.Context(), res, req)
	if res.Code != 400 {
		t.Fatal("incomplete request accepted", res.Code)
	}
	notes, err := svc.captureService.ListThoughts(context.Background())
	if err != nil || len(notes) != 0 {
		t.Fatal("invalid upload saved partial content", err)
	}
}
