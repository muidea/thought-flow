package service

import (
	"context"
	"net/http"
	"os"
	"strings"

	ds "thoughtflow/internal/pkg/documentset"
	"thoughtflow/internal/pkg/documentstore"
	"thoughtflow/internal/pkg/markdown"
	"thoughtflow/internal/pkg/models"
	"thoughtflow/internal/pkg/objectlocation"
)

func (s *Service) handleDocumentConversion(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	if s.documents == nil || s.documentProfiles == nil {
		setError(w, r, ds.ErrUnavailable)
		return
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) != 4 {
		setError(w, r, ds.ErrInvalid)
		return
	}
	id := parts[2]
	if r.Method == "GET" {
		t, c, e := markdown.ReadThought(s.workspace.RootPath, id)
		if e != nil {
			setError(w, r, e)
			return
		}
		if t.DocumentProfile != nil || t.Document != nil {
			setError(w, r, ds.ErrConflict)
			return
		}
		writeJSON(w, r, 200, map[string]any{"expected_content_hash": documentstore.HashJSON(c), "profiles": s.documentProfiles.ListProfiles()})
		return
	}
	var req struct {
		OperationID  string                    `json:"operation_id"`
		ExpectedHash string                    `json:"expected_content_hash"`
		Profile      models.DocumentProfileRef `json:"profile_ref"`
		Parameters   map[string]string         `json:"parameters"`
	}
	if e := decodeWorkspace(r, &req); e != nil {
		setError(w, r, e)
		return
	}
	if req.ExpectedHash == "" || req.Profile.ContentHash == "" || req.Profile.Version == 0 {
		setError(w, r, ds.ErrPrecondition)
		return
	}
	if !documentstore.ValidID(req.OperationID) || !documentstore.ValidID(id) {
		setError(w, r, ds.ErrInvalid)
		return
	}
	profile, e := s.documentProfiles.Resolve(req.Profile)
	if e != nil {
		setError(w, r, e)
		return
	}
	root := s.workspace.RootPath
	release := objectlocation.For(root).LockObject("conversion-" + id)
	defer release()
	key := ".content-registry/private/document-conversions/" + req.OperationID + ".json"
	hash := documentstore.HashJSON(req)
	var prior map[string]string
	if e = objectlocation.ReadJSON(root, key, &prior); e == nil {
		if prior["request_hash"] != hash || prior["document_id"] != id {
			setError(w, r, ds.ErrConflict)
			return
		}
		if out, err := s.documents.Get(id); err == nil {
			writeJSON(w, r, 200, out)
			return
		}
	} else if !os.IsNotExist(e) {
		setError(w, r, e)
		return
	}
	if e = objectlocation.WriteJSON(root, key, map[string]string{"request_hash": hash, "document_id": id}, true); e != nil {
		setError(w, r, e)
		return
	}
	e = markdown.UpdateThought(root, id, func(t *models.Thought, c *models.ThoughtContent) error {
		if t.DocumentProfile != nil || t.Document != nil || documentstore.HashJSON(*c) != req.ExpectedHash {
			return ds.ErrConflict
		}
		// Original and source metadata are retained byte-for-byte. Conversion
		// selects a contract; it does not imply validation or finalization.
		t.DocumentProfile = &profile.Ref
		t.DocumentProfileSnapshot = profile.Snapshot
		t.DocumentParameters = req.Parameters
		zero := uint64(0)
		t.DocumentExpectedGeneration = &zero
		if c.AINotes == "" {
			c.AINotes = c.ExtractedContent
			if c.AINotes == "" {
				c.AINotes = c.Original
			}
		}
		return nil
	})
	if e != nil {
		setError(w, r, e)
		return
	}
	out, e := s.documents.Get(id)
	if e != nil {
		setError(w, r, e)
		return
	}
	s.workspaceChanged(ctx, "", id, "thought.patched", nil)
	writeJSON(w, r, 201, out)
}
