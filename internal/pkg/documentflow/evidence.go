package documentflow

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"thoughtflow/internal/pkg/documentstore"
	"thoughtflow/internal/pkg/models"
)

// Evidence excerpts stay in runtime storage with 0600 permissions. Back up this
// directory together with documents if independent support review is required.
func (s *Service) SaveEvidence(id string, manifest *models.SourceManifest, excerpts map[string]string) error {
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
	for cursor := path; cursor != filepath.Dir(cursor); cursor = filepath.Dir(cursor) {
		if info, e := os.Lstat(cursor); e == nil && info.Mode()&os.ModeSymlink != 0 {
			return "", os.ErrPermission
		} else if e != nil && !os.IsNotExist(e) {
			return "", e
		}
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
	status := "snapshot"
	if _, e := s.evidence(id, sources); e != nil {
		return "unavailable"
	}
	for _, source := range sources.Sources {
		if source.VerificationStatus != "verified" && status == "snapshot" {
			status = "unverified"
		}
		if s.Options.SourceHash != nil && source.ResourceID != "" {
			hash, e := s.Options.SourceHash(source)
			if e != nil {
				status = "unavailable"
			} else if hash != source.Hash && status != "unavailable" {
				status = "changed"
			}
		}
	}
	return status
}
