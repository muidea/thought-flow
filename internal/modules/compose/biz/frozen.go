package biz

import (
	"encoding/json"
	"os"
	"path/filepath"

	"thoughtflow/internal/pkg/documentstore"
	"thoughtflow/internal/pkg/models"
	"thoughtflow/internal/pkg/workspace"
)

// Inputs are private runtime data, never part of the document Git allowlist.
func (s *Service) saveFrozenInputs(id string, snapshots []models.ThoughtSnapshot) (string, error) {
	path := filepath.Join(s.workspace.RuntimePath, "compose-inputs", id+".json")
	if e := workspace.EnsureNoSymlinks(s.workspace.RuntimePath, path); e != nil {
		return "", e
	}
	if e := os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		return "", e
	}
	raw, e := json.Marshal(snapshots)
	if e != nil {
		return "", e
	}
	f, e := os.CreateTemp(filepath.Dir(path), ".input-")
	if e != nil {
		return "", e
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, e = f.Write(raw); e == nil {
		e = f.Sync()
	}
	closeErr := f.Close()
	if e != nil {
		return "", e
	}
	if closeErr != nil {
		return "", closeErr
	}
	if e = os.Rename(tmp, path); e != nil {
		return "", e
	}
	return documentstore.Hash(raw), nil
}
func (s *Service) readFrozenInputs(id, hash string) ([]models.ThoughtSnapshot, error) {
	if !documentstore.ValidID(id) {
		return nil, documentstore.ErrInvalid
	}
	path := filepath.Join(s.workspace.RuntimePath, "compose-inputs", id+".json")
	if e := workspace.EnsureNoSymlinks(s.workspace.RuntimePath, path); e != nil {
		return nil, e
	}
	raw, e := os.ReadFile(path)
	if e != nil {
		return nil, e
	}
	if documentstore.Hash(raw) != hash {
		return nil, documentstore.ErrInvalid
	}
	var snapshots []models.ThoughtSnapshot
	e = json.Unmarshal(raw, &snapshots)
	return snapshots, e
}
