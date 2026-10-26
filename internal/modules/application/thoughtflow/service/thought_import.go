package service

import (
	"context"
	"errors"
	"io"
	"mime"
	"net/http"
	"os"
	"strings"

	capturebiz "thoughtflow/internal/modules/capture/biz"
	"thoughtflow/internal/pkg/markdown"
	"thoughtflow/internal/pkg/models"
)

func (s *Service) handleImportThought(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	if s.captureService == nil {
		writeError(w, r, 503, "thoughtflow.capture.unavailable", "capture service is not ready")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, capturebiz.MaxMarkdownImportBytes+(1<<20))
	reader, err := r.MultipartReader()
	if err != nil {
		writeError(w, r, 400, "thoughtflow.capture.import_invalid", "a multipart Markdown file is required")
		return
	}
	part, err := reader.NextPart()
	if err != nil || part.FormName() != "file" || part.FileName() == "" {
		writeError(w, r, 400, "thoughtflow.capture.import_invalid", "exactly one file field is required")
		return
	}
	name := part.FileName()
	raw, err := io.ReadAll(io.LimitReader(part, capturebiz.MaxMarkdownImportBytes+1))
	if len(raw) > capturebiz.MaxMarkdownImportBytes {
		writeError(w, r, 413, "thoughtflow.capture.import_too_large", "Markdown file exceeds 32 MiB; nothing was imported")
		return
	}
	if err != nil {
		writeError(w, r, 400, "thoughtflow.capture.import_invalid", "could not read the complete Markdown file")
		return
	}
	if _, err = reader.NextPart(); err != io.EOF {
		writeError(w, r, 400, "thoughtflow.capture.import_invalid", "exactly one complete file is required per request")
		return
	}
	if ctx.Err() != nil {
		writeError(w, r, 408, "thoughtflow.capture.import_failed", "import canceled before saving")
		return
	}
	result, err := s.captureService.ImportMarkdown(ctx, name, raw)
	if err != nil {
		if errors.Is(err, capturebiz.ErrMarkdownImport) {
			writeError(w, r, 422, "thoughtflow.capture.import_invalid", "use a non-empty UTF-8 .md or .markdown file without NUL bytes (maximum 32 MiB)")
		} else {
			writeError(w, r, 500, "thoughtflow.capture.import_failed", err.Error())
		}
		return
	}
	writeJSON(w, r, http.StatusCreated, result)
}

func (s *Service) handleThoughtImportSource(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSuffix(pathID(r.URL.Path, "/api/thoughts/"), "/import-source")
	snapshot, err := s.captureService.GetThought(ctx, id)
	if err != nil {
		thoughtDownloadError(w, r, err)
		return
	}
	meta := snapshot.Thought.MarkdownImport
	if meta == nil {
		thoughtDownloadError(w, r, os.ErrNotExist)
		return
	}
	raw := []byte(snapshot.Content.Original)
	if len(raw) != meta.Size || models.ContentHash(string(raw)) != meta.SHA256 {
		writeError(w, r, 409, "thoughtflow.capture.import_source_changed", "the stored original no longer matches the imported file")
		return
	}
	writeMarkdownDownload(w, meta.Filename, raw)
}

func (s *Service) handleThoughtMarkdown(_ context.Context, w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSuffix(pathID(r.URL.Path, "/api/thoughts/"), "/markdown")
	raw, err := markdown.ReadThoughtRaw(s.workspace.RootPath, id)
	if err != nil {
		thoughtDownloadError(w, r, err)
		return
	}
	writeMarkdownDownload(w, id+".md", raw)
}

func thoughtDownloadError(w http.ResponseWriter, r *http.Request, err error) {
	status := 500
	if errors.Is(err, os.ErrNotExist) {
		status = 404
	}
	writeError(w, r, status, "thoughtflow.capture.download_failed", err.Error())
}

func writeMarkdownDownload(w http.ResponseWriter, filename string, raw []byte) {
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": filename}))
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(raw)
}
