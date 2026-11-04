package service

import (
	"strings"

	ds "thoughtflow/internal/pkg/documentset"
	"thoughtflow/internal/pkg/scratchpad"
)

// This is a read-only conversation projection. Never use it in place of the
// complete questions/EngineeringIssues when deciding whether a set can run.
func captureSetClarification(plan ds.Plan, runID string) scratchpad.ArchiveClarification {
	out := scratchpad.ArchiveClarification{RunID: runID, Questions: []string{}}
	seen := map[string]bool{}
	for _, q := range plan.Shared.Questions {
		if !q.Blocking || strings.TrimSpace(q.Resolution) != "" {
			continue
		}
		// Empty kind and these IDs are reserved by BuildPlan for deterministic
		// diagnostics. Typed user questions always take precedence over the ID.
		diagnostic := q.Kind == "" && (q.ID == "modules" || strings.HasPrefix(q.ID, "fact-") || strings.HasPrefix(q.ID, "input-") || strings.HasPrefix(q.ID, "condition-") || strings.HasPrefix(q.ID, "gap-"))
		if diagnostic {
			out.DiagnosticCount++
			continue
		}
		text := strings.TrimSpace(q.Message)
		for _, option := range q.Options {
			text += "\n- " + option.Description
			if strings.TrimSpace(option.Impact) != "" {
				text += "（" + option.Impact + "）"
			}
		}
		if len(q.ResolutionCriteria) > 0 {
			text += "\n确认依据：" + strings.Join(q.ResolutionCriteria, "；")
		}
		key := strings.Join(strings.Fields(text), " ")
		if key != "" && !seen[key] {
			out.Questions = append(out.Questions, text)
			seen[key] = true
		}
	}
	return out
}

func captureSetClarificationText(c scratchpad.ArchiveClarification) string {
	if len(c.Questions) == 0 {
		return "归档规划尚未通过完整性校验，尚未生成文件。已有对话已保留，无需逐条确认内部校验项。请核对设计范围后重新要求归档；若范围已经明确，需要检查规划结果。"
	}
	text := "归档尚未开始生成文件，请在对话中补充以下待决事项：\n" + strings.Join(c.Questions, "\n\n") + "\n\n已有对话内容无需重复填写，回复后将自动继续归档。"
	if c.DiagnosticCount > 0 {
		text += "规划中另有完整性校验缺口，将在补充答复后重新核验；本次不会跳过校验生成。"
	}
	return text
}
