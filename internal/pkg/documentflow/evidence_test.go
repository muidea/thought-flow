package documentflow

import (
	"errors"
	"testing"
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
