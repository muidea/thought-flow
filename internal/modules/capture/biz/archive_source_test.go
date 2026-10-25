package biz

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"thoughtflow/internal/pkg/ai"
	"thoughtflow/internal/pkg/documentprofile"
	"thoughtflow/internal/pkg/models"
	"thoughtflow/internal/pkg/scratchpad"
)

type reorganizedArchiveGenerator struct{ calls int }

func (g *reorganizedArchiveGenerator) GenerateDocument(ctx context.Context, req ai.DocumentGenerationRequest) (models.DocumentDraft, error) {
	g.calls++
	return (automaticResearchGenerator{}).GenerateDocument(ctx, req)
}

func longArchiveSource() string {
	var source strings.Builder
	for i := 0; i < 18; i++ {
		fmt.Fprintf(&source, "## 方案材料 %d\n\n第 %d 项接口约定：企业计划向制造运营下达产品任务，仓储确认物料批次，设备反馈执行状态；该约定必须保留原始编号、数量 %d 和计量单位，不能用一句泛化概括替换。\n\n", i, i, 1000+i)
	}
	return source.String()
}

func TestReorganizedArchivePreservesSourceWithoutAnotherModelCall(t *testing.T) {
	svc, store, capture := archiveFixture(t, "builtin.research-report", 2)
	source := longArchiveSource()
	generator := &reorganizedArchiveGenerator{}
	svc.documentGenerator = generator
	_, err := store.Update("s", func(sp *scratchpad.Scratchpad) error {
		sp.Content = "根据以上内容归档"
		sp.SessionContext.CandidateBody = source
		sp.SessionContext.DocumentParameters = inferredResearchParameters()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	initial, err := svc.BeginPreview("s", true)
	if err != nil {
		t.Fatal(err)
	}
	if err = svc.PreviewAndArchive(context.Background(), initial); err != nil {
		t.Fatal(err)
	}
	sp, err := store.Get("s")
	if err != nil || sp.Operation.State != "saved" || sp.CommittedThoughtID == "" || generator.calls != 1 {
		t.Fatalf("archive did not finish in one generation: %+v calls=%d err=%v", sp, generator.calls, err)
	}
	thought, err := capture.GetThought(context.Background(), sp.CommittedThoughtID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(thought.Content.AINotes, "### 会话素材摘录") != 1 {
		t.Fatal("source material missing or duplicated")
	}
	for _, line := range strings.Split(source, "\n") {
		if strings.HasPrefix(line, "第 ") && !strings.Contains(thought.Content.AINotes, line) {
			t.Fatalf("source detail lost: %s", line)
		}
	}
	if !strings.Contains(thought.Content.AINotes, "不表示已取得外部证据") {
		t.Fatal("source copy treated as verified evidence")
	}
	result, err := svc.Commit(context.Background(), "s")
	if err != nil || result.Thought.ID != sp.CommittedThoughtID || generator.calls != 1 {
		t.Fatal("save replay generated again")
	}
}

func TestSourceRetentionStillRejectsUnsafeLinksAndSizeOverflow(t *testing.T) {
	for _, tc := range []struct {
		name   string
		unsafe bool
	}{
		{name: "size overflow"},
		{name: "unsafe link", unsafe: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, store, capture := archiveFixture(t, "builtin.research-report", 2)
			source := longArchiveSource()
			if tc.unsafe {
				source += "\n[private](https://example.org/resource?token=SECRET)"
			} else {
				source = strings.Repeat(source, 60000/len([]rune(source))+1)
			}
			svc.documentGenerator = &reorganizedArchiveGenerator{}
			_, err := store.Update("s", func(sp *scratchpad.Scratchpad) error {
				sp.SessionContext.CandidateBody = source
				sp.SessionContext.DocumentParameters = inferredResearchParameters()
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			sp, err := svc.BeginPreview("s", true)
			if err != nil {
				t.Fatal(err)
			}
			err = svc.PreviewAndArchive(context.Background(), sp)
			sp, readErr := store.Get("s")
			if readErr != nil || sp.CommittedThoughtID != "" || sp.Operation.State == "saved" {
				t.Fatalf("unsafe/oversized source saved: %+v %v", sp, err)
			}
			notes, e := capture.ListThoughts(context.Background())
			if e != nil || len(notes) != 0 {
				t.Fatal("failed content written")
			}
			found := false
			if sp.ArchivePreview != nil {
				for _, issue := range sp.ArchivePreview.Validation.Issues {
					if tc.unsafe {
						found = found || issue.Code == "unsafe_link"
					} else {
						found = found || issue.Code == "thoughtflow.profile.body_too_long" || issue.Code == "body_length"
					}
				}
			}
			if !found {
				t.Fatalf("%s validation lost: source chars=%d err=%v", tc.name, len([]rune(source)), err)
			}
		})
	}
}

func TestSourceRetentionRequiresDeclaredSectionAndRemainsIdempotent(t *testing.T) {
	source := longArchiveSource()
	request := ai.DocumentGenerationRequest{Context: ai.DocumentSourceContext{Body: source}}
	draft := models.DocumentDraft{Sections: map[string]models.DocumentSection{"unknown": {Content: "Reorganized prose"}}}
	if preserveArchiveSource(request, &draft) {
		t.Fatal("created an undeclared appendix")
	}
	request.Profile.Sections = []documentprofile.SectionSpec{{Key: "findings"}}
	draft.Sections = map[string]models.DocumentSection{"findings": {Content: "Reorganized prose"}}
	if archiveSourceCoverageIssue(source, draft.Sections["findings"].Content) == nil {
		t.Fatal("test does not cover failed original coverage")
	}
	if !preserveArchiveSource(request, &draft) || archiveSourceCoverageIssue(source, draft.Sections["findings"].Content) != nil {
		t.Fatal("retained source still fails coverage")
	}
	before := draft.Sections["findings"].Content
	if preserveArchiveSource(request, &draft) || before != draft.Sections["findings"].Content {
		t.Fatal("retained source duplicated")
	}
}

func TestArchiveWarningsDoNotRequestMissingInputs(t *testing.T) {
	for _, tc := range []struct {
		severity string
		want     bool
	}{{"warning", false}, {models.ValidationSeverityError, true}} {
		v := models.ArchiveValidation{Issues: []models.ValidationIssue{{Code: "research_evidence_missing", Severity: tc.severity}, {Code: "thoughtflow.profile.source_content_missing", Severity: models.ValidationSeverityError}}}
		if archiveNeedsInput(v) != tc.want {
			t.Fatalf("severity=%s input state mismatch", tc.severity)
		}
	}
}
