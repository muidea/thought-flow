package service

import (
	"reflect"
	"strings"
	"testing"

	ds "thoughtflow/internal/pkg/documentset"
)

func TestCaptureSetClarificationSeparatesDecisionsFromDiagnostics(t *testing.T) {
	questions := []ds.Question{
		{ID: "interface", Kind: "question", Blocking: true, Message: "是否需要对外接口？", Facts: []string{"CON-INTERFACE"}},
		{ID: "duplicate", Kind: "question", Blocking: true, Message: " 是否需要对外接口？ "},
		{ID: "persistence", Kind: "question", Blocking: true, Message: "请明确持久化范围及生命周期。"},
		{ID: "acceptance", Kind: "question", Blocking: true, Message: "请确认验收证据及验证方式。"},
		{ID: "delivery", Kind: "question", Blocking: true, Message: "请确认交付文件及格式。"},
		{ID: "closed", Kind: "question", Blocking: true, Message: "CLOSED", Resolution: "已确认"},
		{ID: "risk", Kind: "question", Message: "NONBLOCKING"},
	}
	for _, id := range []string{"fact-REQ-1", "fact-REQ-2", "condition-has_interfaces", "condition-has_persistence", "modules", "gap-a", "gap-b", "input-goal"} {
		questions = append(questions, ds.Question{ID: id, Blocking: true, Message: "INTERNAL DIAGNOSTIC"})
	}
	plan := ds.Plan{Shared: ds.SharedDesign{Questions: questions}}
	before := append([]ds.Question(nil), questions...)
	c := captureSetClarification(plan, "run")
	if len(c.Questions) != 4 || c.DiagnosticCount != 8 || c.RunID != "run" {
		t.Fatal(c)
	}
	text := captureSetClarificationText(c)
	if strings.Contains(text, "INTERNAL") || strings.Contains(text, "CLOSED") || strings.Contains(text, "NONBLOCKING") || strings.Count(text, "是否需要对外接口？") != 1 || !strings.Contains(text, "不会跳过校验") {
		t.Fatal(text)
	}
	if !reflect.DeepEqual(before, plan.Shared.Questions) {
		t.Fatal("presentation changed the authoritative blockers")
	}
}

func TestCaptureSetClarificationDoesNotCallPlannerDefectsUserDecisions(t *testing.T) {
	plan := ds.Plan{Shared: ds.SharedDesign{Questions: []ds.Question{{ID: "gap-1", Blocking: true, Message: "Required role instance has no defined content"}}}}
	c := captureSetClarification(plan, "run")
	if len(c.Questions) != 0 || !strings.Contains(captureSetClarificationText(c), "需要检查规划结果") {
		t.Fatal(c)
	}
	// Typed questions are not hidden by reserved-looking IDs. Options and
	// decision criteria must remain available, including legacy untyped questions.
	plan.Shared.Questions = []ds.Question{{ID: "fact-choice", Kind: "conflict", Blocking: true, Message: "选择恢复方式", Options: []ds.QuestionOption{{ID: "keep", Description: "保留", Impact: "可恢复"}}, ResolutionCriteria: []string{"保留用户输入"}}, {ID: "legacy", Blocking: true, Message: "旧版待决事项"}}
	c = captureSetClarification(plan, "run")
	if len(c.Questions) != 2 || !strings.Contains(c.Questions[0], "可恢复") || !strings.Contains(c.Questions[0], "保留用户输入") {
		t.Fatal(c)
	}
}
