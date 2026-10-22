package documentflow

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"thoughtflow/internal/pkg/documentstore"
	"thoughtflow/internal/pkg/models"
)

func TestSourceFreshnessKeepsMostSevereStatusAcrossSources(t *testing.T) {
	s := &Service{Options: Options{SourceHash: func(source models.DocumentSource) (string, error) {
		if source.ResourceID == "missing" {
			return "", errors.New("missing")
		}
		return "current", nil
	}}}
	for _, tc := range []struct{ name, resource, want string }{
		{"missing_then_unverified", "missing", "unavailable"},
		{"changed_then_unverified", "changed", "changed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sources := []models.DocumentSource{
				{ID: "S1", ResourceID: tc.resource, Hash: "old", VerificationStatus: "verified"},
				{ID: "S2", Hash: "frozen", VerificationStatus: "unverified"},
			}
			for i := 0; i < 2; i++ {
				if got := s.sourceFreshness("document", models.SourceManifest{Sources: sources}); got != tc.want {
					t.Fatalf("freshness = %s, want %s", got, tc.want)
				}
				sources[0], sources[1] = sources[1], sources[0]
			}
		})
	}
}

func TestEvidenceAllowsSymlinkAboveRuntimeRoot(t *testing.T) {
	base := t.TempDir()
	realParent := filepath.Join(base, "real-parent")
	if err := os.Mkdir(realParent, 0700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(base, "parent-alias")
	if err := os.Symlink(realParent, alias); err != nil {
		t.Fatal(err)
	}
	s := &Service{Store: documentstore.New(t.TempDir()), Options: Options{RuntimePath: filepath.Join(alias, "runtime")}}
	manifest := models.SourceManifest{Sources: []models.DocumentSource{{ID: "S1"}}}
	const excerpt = "private source excerpt"
	if err := s.SaveEvidence("doc", &manifest, map[string]string{"S1": excerpt}); err != nil {
		t.Fatal(err)
	}
	if actual, err := s.evidence("doc", manifest); err != nil || actual["S1"] != excerpt {
		t.Fatalf("evidence through runtime parent alias: %v, %v", actual, err)
	}
	path, err := s.evidencePath("doc", manifest.Sources[0].EvidenceHash)
	if err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("private evidence permissions: %v, %v", info, err)
	}
}

func TestEvidenceRejectsSymlinksAtAndInsideRuntimeRoot(t *testing.T) {
	const excerpt = "private source excerpt"
	hash := documentstore.Hash([]byte(excerpt))
	filename := hash[len("sha256:"):] + ".txt"
	for _, relative := range []string{".", "document-evidence", "document-evidence/doc", "document-evidence/doc/" + filename} {
		t.Run(relative, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "runtime")
			link := filepath.Join(root, filepath.FromSlash(relative))
			if err := os.MkdirAll(filepath.Dir(link), 0700); err != nil {
				t.Fatal(err)
			}
			outside := t.TempDir()
			if filepath.Ext(relative) == ".txt" {
				outside = filepath.Join(outside, filename)
				if err := os.WriteFile(outside, []byte(excerpt), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Symlink(outside, link); err != nil {
				t.Fatal(err)
			}
			s := &Service{Store: documentstore.New(t.TempDir()), Options: Options{RuntimePath: root}}
			manifest := models.SourceManifest{Sources: []models.DocumentSource{{ID: "S1", EvidenceHash: hash}}}
			if err := s.SaveEvidence("doc", &manifest, map[string]string{"S1": excerpt}); !errors.Is(err, os.ErrPermission) {
				t.Fatalf("evidence write through linked path %s accepted: %v", relative, err)
			}
			if _, err := s.evidence("doc", manifest); !errors.Is(err, os.ErrPermission) {
				t.Fatalf("evidence read through linked path %s accepted: %v", relative, err)
			}
		})
	}
}
