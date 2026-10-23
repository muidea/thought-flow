package documentflow

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"thoughtflow/internal/pkg/documentstore"
	"thoughtflow/internal/pkg/models"
	"thoughtflow/internal/pkg/objectlocation"
	"thoughtflow/internal/pkg/workspace"
)

// Evidence excerpts stay in runtime storage with 0600 permissions. Back up this
// directory together with documents if independent support review is required.
func (s *Service) EvidenceAvailable(id string, sources models.SourceManifest) bool {
	_, e := s.evidence(id, sources)
	return e == nil
}
func (s *Service) SaveEvidence(id string, manifest *models.SourceManifest, excerpts map[string]string) error {
	unlock := s.Store.Lock(id)
	defer unlock()
	if e := s.Store.CheckActive(id); e != nil {
		return e
	}
	if len(excerpts) == 0 {
		return nil
	}
	if s.Options.RuntimePath == "" {
		return ErrUnavailable
	}
	if !documentstore.ValidID(id) || manifest == nil {
		return documentstore.ErrInvalid
	}
	for key, excerpt := range excerpts {
		if len(excerpt) > 64*1024 {
			return documentstore.ErrInvalid
		}
		found := false
		for i := range manifest.Sources {
			source := &manifest.Sources[i]
			if source.ID != key {
				continue
			}
			found = true
			hash := documentstore.Hash([]byte(excerpt))
			path, e := s.evidencePath(id, hash)
			if e != nil {
				return e
			}
			if e = os.MkdirAll(filepath.Dir(path), 0700); e != nil {
				return e
			}
			if existing, e := os.ReadFile(path); e == nil {
				if string(existing) != excerpt {
					return documentstore.ErrInvalid
				}
			} else if os.IsNotExist(e) {
				file, e := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0600)
				if e != nil {
					return e
				}
				_, e = file.WriteString(excerpt)
				if e == nil {
					e = file.Sync()
				}
				closeErr := file.Close()
				if e != nil {
					return e
				}
				if closeErr != nil {
					return closeErr
				}
			} else {
				return e
			}
			source.EvidenceHash = hash
			if source.Hash == "" {
				source.Hash = hash
			}
			if source.Version == "" {
				source.Version = hash
			}
			if source.RetrievedAt.IsZero() {
				source.RetrievedAt = time.Now().UTC()
			}
		}
		if !found {
			return documentstore.ErrInvalid
		}
	}
	return nil
}
func (s *Service) evidencePath(id, hash string) (string, error) {
	value := strings.TrimPrefix(hash, "sha256:")
	if len(value) != 64 || !documentstore.ValidID(value) || !documentstore.ValidID(id) {
		return "", documentstore.ErrInvalid
	}
	path := filepath.Join(s.Options.RuntimePath, "document-evidence", id, value+".txt")
	root := filepath.Dir(s.Store.Root)
	l, lookupErr := objectlocation.For(root).Lookup(id)
	if lookupErr != nil && !os.IsNotExist(lookupErr) {
		return "", lookupErr
	}
	if lookupErr == nil && l.Placement.EvidenceRoot != "" {
		return objectlocation.SafePath(root, l.Placement.EvidenceRoot+"/"+value+".txt")
	}
	// Runtime storage has its own boundary, independent of the content root.
	if e := workspace.EnsureNoSymlinks(s.Options.RuntimePath, path); e != nil {
		return "", e
	}
	return path, nil
}
func (s *Service) evidence(id string, sources models.SourceManifest) (map[string]string, error) {
	out := map[string]string{}
	for _, source := range sources.Sources {
		if source.EvidenceHash == "" {
			continue
		}
		path, e := s.evidencePath(id, source.EvidenceHash)
		if e != nil {
			return nil, e
		}
		raw, e := os.ReadFile(path)
		if e != nil {
			return nil, e
		}
		if documentstore.Hash(raw) != source.EvidenceHash {
			return nil, documentstore.ErrInvalid
		}
		out[source.ID] = string(raw)
	}
	return out, nil
}
func (s *Service) sourceFreshness(id string, sources models.SourceManifest) string {
	return s.sourceFreshnessWithHasher(id, sources, s.Options.SourceHash)
}
func (s *Service) sourceFreshnessWithHasher(id string, sources models.SourceManifest, hasher func(models.DocumentSource) (string, error)) string {
	status := "snapshot"
	if _, e := s.evidence(id, sources); e != nil {
		return "unavailable"
	}
	for _, source := range sources.Sources {
		if source.VerificationStatus != "verified" && status == "snapshot" {
			status = "unverified"
		}
		if hasher != nil && source.ResourceID != "" {
			hash, e := hasher(source)
			if e != nil {
				status = "unavailable"
			} else if hash != source.Hash && status != "unavailable" {
				status = "changed"
			}
		}
	}
	return status
}
