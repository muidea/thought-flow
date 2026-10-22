package documentflow_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"thoughtflow/assets/documentformats"
	"thoughtflow/internal/pkg/documentflow"
	"thoughtflow/internal/pkg/documentprofile"

	"thoughtflow/internal/pkg/documentstore"
	"thoughtflow/internal/pkg/markdown"
	"thoughtflow/internal/pkg/models"
)

func TestExternalIdentityMismatchCannotPublishAnotherDocument(t *testing.T) {
	root, service, a, content := setup(t, "design")
	b := a
	b.ID, b.Document = "20261022-document-b", nil
	b.Path = ""
	content.AINotes += "\nUnique B content\n"
	if err := markdown.WriteThought(root, b, content); err != nil {
		t.Fatal(err)
	}
	beforeA, _ := service.Store.Head(a.ID)
	beforeB, _ := service.Store.Head(b.ID)
	pathA := filepath.Join(root, markdown.ThoughtRelativePath(a.ID))
	raw, err := os.ReadFile(pathA)
	if err != nil {
		t.Fatal(err)
	}
	raw = []byte(strings.Replace(string(raw), `id: "`+a.ID+`"`, `id: "`+b.ID+`"`, 1))
	if err = os.WriteFile(pathA, raw, 0600); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for range 10 {
			if _, _, err := markdown.ReadThought(root, b.ID); err != nil {
				t.Error(err)
			}
		}
	}()
	for range 10 {
		if _, _, err = markdown.ReadThought(root, a.ID); !errors.Is(err, documentstore.ErrInvalid) {
			t.Errorf("mismatched identity accepted: %v", err)
		}
	}
	wg.Wait()
	afterA, _ := service.Store.Head(a.ID)
	afterB, _ := service.Store.Head(b.ID)
	if !reflect.DeepEqual(beforeA, afterA) || !reflect.DeepEqual(beforeB, afterB) {
		t.Fatal("reading A published a revision")
	}
	if actual, err := os.ReadFile(pathA); err != nil || string(actual) != string(raw) {
		t.Fatal("rejected read changed projection", err)
	}
}

func TestCandidateRejectsSensitiveSourcesBeforePublication(t *testing.T) {
	_, service, thought, content := setup(t, "design")
	profile, err := service.Profiles.Resolve(*thought.DocumentProfile)
	if err != nil {
		t.Fatal(err)
	}
	for _, locator := range []string{"https://user:SECRET@example.org/", "https://example.org/?token=SECRET", "https://example.org/\r\nSECRET"} {
		id := documentstore.ID("candidate-")
		_, err := service.Candidate(context.Background(), id, content.AINotes, "Document", profile, thought.DocumentParameters, models.SourceManifest{Sources: []models.DocumentSource{{ID: "S1", Locator: locator}}}, "")
		if !errors.Is(err, documentstore.ErrInvalid) {
			t.Fatalf("sensitive candidate accepted: %v", err)
		}
		if _, err := os.Stat(filepath.Join(service.Store.Root, id)); !os.IsNotExist(err) {
			t.Fatalf("candidate persisted before safety check: %v", err)
		}
	}
}

func TestUnsafeRenderedDestinationsCannotBeFinalized(t *testing.T) {
	for _, node := range []string{"![diagram](https://example.org/?token=SECRET)", "<https://user:SECRET@example.org/>", "### [Heading](https://example.org/?token=SECRET)"} {
		t.Run(node, func(t *testing.T) {
			root, service, thought, content := setup(t, "design")
			content.AINotes += "\n" + node + "\n"
			if err := markdown.WriteThought(root, thought, content); err != nil {
				t.Fatal(err)
			}
			snapshot, err := service.Get(thought.ID)
			if err != nil {
				t.Fatal(err)
			}
			report, err := service.Validate(context.Background(), thought.ID, snapshot.Head.WorkingRevisionID, snapshot.Head.Generation, "manual")
			if err != nil {
				t.Fatal(err)
			}
			if report.Status != "failed" {
				t.Fatalf("unsafe body accepted: %+v", report)
			}
			if _, err = service.Finalize(thought.ID, snapshot.Head.Generation, request(snapshot, report, "unsafe")); err == nil {
				t.Fatal("unsafe body finalized")
			}
		})
	}
}

func TestCandidateRespectsClosedDisabledAndFamilyGates(t *testing.T) {
	for _, mode := range []string{"disabled", "family", "closed"} {
		t.Run(mode, func(t *testing.T) {
			_, s, thought, content := setup(t, "design")
			profile, err := s.Profiles.Resolve(*thought.DocumentProfile)
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "disabled":
				s.Options.Enabled = false
			case "family":
				s.Options.Families = []string{"research"}
			case "closed":
				s.Close()
			}
			if _, err = s.Candidate(context.Background(), "blocked-candidate", content.AINotes, "Document", profile, thought.DocumentParameters, models.SourceManifest{}, ""); err == nil {
				t.Fatal("candidate bypassed feature gate")
			}
			if _, err = s.Store.Head("blocked-candidate"); !os.IsNotExist(err) {
				t.Fatal("disabled candidate was published", err)
			}
		})
	}
}

type levelRepairer struct {
	level    int
	adjacent bool
}

func (r levelRepairer) RepairDocument(_ context.Context, d documentstore.RevisionData, _ []models.DocumentIssue, sections []string) (string, error) {
	heading := strings.Repeat("#", r.level) + " " + sections[0] + "\n\n"
	if r.adjacent {
		return d.Body + "\nChanged outside the selected section.\n", nil
	}
	return strings.Replace(d.Body, heading, heading+"Scoped repair fact 271828.\n\n", 1), nil
}

func TestRepairPublishesNewRevisionForCustomHeadingLevels(t *testing.T) {
	for _, level := range []int{3, 6} {
		t.Run(fmt.Sprintf("H%d", level), func(t *testing.T) {
			root, s, thought, content := setup(t, "design")
			profile, err := documentprofile.Parse([]byte(strings.Replace(documentformats.DesignDocV2, "heading_level: 2", fmt.Sprintf("heading_level: %d", level), 1)), documentprofile.Limits{})
			if err != nil {
				t.Fatal(err)
			}
			thought.Document = nil
			thought.DocumentProfile = &profile.Ref
			thought.DocumentProfileSnapshot = profile.Snapshot
			thought.DocumentParameters = map[string]string{}
			for _, input := range profile.Inputs {
				thought.DocumentParameters[input.Key] = "confirmed"
			}
			base, err := s.Get(thought.ID)
			if err != nil {
				t.Fatal(err)
			}
			revision, err := s.Store.Revision(thought.ID, base.Head.WorkingRevisionID)
			if err != nil {
				t.Fatal(err)
			}
			thought.DocumentSources = &revision.Sources
			content.AINotes = strings.ReplaceAll(content.AINotes, "\n## ", "\n"+strings.Repeat("#", level)+" ")
			thought.ID = fmt.Sprintf("20261022-repair-h%d", level)
			thought.Path = ""
			if err = markdown.WriteThought(root, thought, content); err != nil {
				t.Fatal(err)
			}
			snap, report := check(t, s, thought.ID)
			s.Options.Repairer = levelRepairer{level: level, adjacent: true}
			if _, err = s.Repair(context.Background(), thought.ID, snap.Head.WorkingRevisionID, report.ReportID, snap.Head.Generation, []string{report.Issues[0].ID}, []string{"proposal"}); !errors.Is(err, documentflow.ErrRules) {
				t.Fatalf("adjacent edit accepted: %v", err)
			}
			unchanged, _ := s.Get(thought.ID)
			if unchanged.Head.Generation != snap.Head.Generation {
				t.Fatal("rejected repair advanced HEAD")
			}
			s.Options.Repairer = levelRepairer{level: level}
			next, err := s.Repair(context.Background(), thought.ID, snap.Head.WorkingRevisionID, report.ReportID, snap.Head.Generation, []string{report.Issues[0].ID}, []string{"proposal"})
			if err != nil {
				t.Fatal(err)
			}
			if next.Revision.RevisionID == snap.Revision.RevisionID || !strings.Contains(next.Body, "Scoped repair fact 271828.") || next.Report == nil || next.Report.RevisionID != next.Revision.RevisionID {
				t.Fatalf("repair not published and revalidated: %+v", next)
			}
		})
	}
}
