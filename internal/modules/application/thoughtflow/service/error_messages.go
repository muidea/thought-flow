package service

import (
	"net/http"
	"strings"
)

// localizedErrorMessage is the single policy for API error copy. Error codes
// remain stable for programmatic clients; the user-facing message is selected
// from the request language and never exposes an internal error string.
func localizedErrorMessage(req *http.Request, code string) string {
	english := prefersEnglish(req)
	en, zh := errorMessageForCode(code)
	if english {
		return en
	}
	return zh
}

func prefersEnglish(req *http.Request) bool {
	if req == nil {
		return false
	}
	for _, preference := range strings.Split(req.Header.Get("Accept-Language"), ",") {
		language := strings.TrimSpace(strings.SplitN(preference, ";", 2)[0])
		if strings.HasPrefix(strings.ToLower(language), "en") {
			return true
		}
		if strings.HasPrefix(strings.ToLower(language), "zh") {
			return false
		}
	}
	return false
}

func errorMessageForCode(code string) (english, chinese string) {
	switch {
	case strings.HasSuffix(code, ".invalid_json"):
		return "Request body must be valid JSON.", "请求正文必须是有效的 JSON。"
	case strings.HasSuffix(code, ".invalid_request"):
		if strings.HasPrefix(code, "thoughtflow.topic.") {
			return "Topic name or identifier is required.", "专题名称或标识不能为空。"
		}
		return "The request contains invalid or missing fields.", "请求包含无效或缺失的字段。"
	case strings.HasSuffix(code, ".unavailable"):
		return "This service is not ready. Please try again shortly.", "服务尚未就绪，请稍后重试。"
	case strings.HasSuffix(code, ".not_found"):
		return "The requested resource was not found.", "未找到请求的资源。"
	}

	messages := map[string][2]string{
		"thoughtflow.ui.asset_failed":                    {"Unable to load this interface asset.", "无法加载界面资源。"},
		"thoughtflow.capture.list_failed":                {"Unable to load thoughts.", "无法加载笔记。"},
		"thoughtflow.capture.session_required":           {"A capture session is required.", "需要采集会话。"},
		"thoughtflow.capture.invalid_patch":              {"The thought update is invalid.", "笔记更新内容无效。"},
		"thoughtflow.capture.locked":                     {"This thought is locked by another session.", "该笔记正被其他会话锁定。"},
		"thoughtflow.capture.refining":                   {"This thought is being refined. Please try again shortly.", "该笔记正在整理，请稍后重试。"},
		"thoughtflow.capture.patch_failed":               {"Unable to update the thought.", "无法更新笔记。"},
		"thoughtflow.capture.delete_failed":              {"Unable to delete the thought.", "无法删除笔记。"},
		"thoughtflow.refiner.suggest_failed":             {"Unable to generate suggestions.", "无法生成建议。"},
		"thoughtflow.search.query_failed":                {"Unable to search thoughts.", "无法搜索笔记。"},
		"thoughtflow.search.reindex_failed":              {"Unable to rebuild the search index.", "无法重建搜索索引。"},
		"thoughtflow.compose.generate_failed":            {"Unable to generate the draft.", "无法生成草稿。"},
		"thoughtflow.compose.list_failed":                {"Unable to load drafts.", "无法加载草稿。"},
		"thoughtflow.compose.sources_failed":             {"Unable to update draft sources.", "无法更新草稿来源。"},
		"thoughtflow.compose.delete_failed":              {"Unable to delete the draft.", "无法删除草稿。"},
		"thoughtflow.compose.save_failed":                {"Unable to save the draft.", "无法保存草稿。"},
		"thoughtflow.profile.invalid":                    {"The document profile is invalid.", "文档配置无效。"},
		"thoughtflow.profile.conflict":                   {"The document profile conflicts with the current session.", "文档配置与当前会话冲突。"},
		"thoughtflow.topic.list_failed":                  {"Unable to load topics.", "无法加载专题。"},
		"thoughtflow.topic.update_failed":                {"Unable to update the topic.", "无法更新专题。"},
		"thoughtflow.topic.refresh_failed":               {"Unable to refresh the topic.", "无法刷新专题。"},
		"thoughtflow.topic.weave_preview_failed":         {"Unable to prepare the topic update preview.", "无法生成专题更新预览。"},
		"thoughtflow.topic.weave_accept_failed":          {"Unable to apply the topic update.", "无法应用专题更新。"},
		"thoughtflow.topic.weave_proposals_failed":       {"Unable to load topic update proposals.", "无法加载专题更新建议。"},
		"thoughtflow.topic.weave_proposal_not_found":     {"The topic update proposal was not found.", "未找到专题更新建议。"},
		"thoughtflow.topic.list_candidates_failed":       {"Unable to load topic candidates.", "无法加载专题候选项。"},
		"thoughtflow.system.invalid_request":             {"The request contains invalid or missing fields.", "请求包含无效或缺失的字段。"},
		"thoughtflow.jobs.list_failed":                   {"Unable to load jobs.", "无法加载任务。"},
		"thoughtflow.system.sse_unavailable":             {"Live updates are not available.", "实时更新不可用。"},
		"thoughtflow.system.metrics_failed":              {"Unable to load system metrics.", "无法加载系统指标。"},
		"thoughtflow.capture.scratchpad.invalid_session": {"The capture session is invalid or has expired.", "采集会话无效或已过期。"},
		"thoughtflow.capture.scratchpad.write_failed":    {"Unable to save the capture session.", "无法保存采集会话。"},
		"thoughtflow.capture.diff_required":              {"Select a source thought before using this strategy.", "使用此策略前请先选择源笔记。"},
		"thoughtflow.capture.already_committed":          {"This capture session has already been committed.", "该采集会话已归档。"},
		"thoughtflow.archive.confirmation_required":      {"Confirm the preview before committing.", "归档前请先确认预览。"},
		"thoughtflow.archive.preview_required":           {"Preview the changes before committing.", "归档前请先预览变更。"},
		"thoughtflow.archive.preview_stale":              {"The preview is outdated. Refresh it before committing.", "预览已过期，请刷新后再归档。"},
		"thoughtflow.archive.format_invalid":             {"The capture content does not match the selected document format.", "采集内容不符合所选文档格式。"},
		"thoughtflow.capture.commit_failed":              {"Unable to commit the capture session.", "无法归档采集会话。"},
		"thoughtflow.capture.preview_failed":             {"Unable to prepare the capture preview.", "无法生成采集预览。"},
		"thoughtflow.capture.reopen_failed":              {"Unable to reopen this thought in capture.", "无法在采集中重新打开该笔记。"},
		"thoughtflow.public.disabled":                    {"Public thought sharing is disabled.", "公开笔记分享未启用。"},
		"thoughtflow.public.not_acceptable":              {"The requested response format is not available.", "请求的响应格式不可用。"},
	}
	if message, ok := messages[code]; ok {
		return message[0], message[1]
	}
	return "Request failed. Please try again.", "请求失败，请重试。"
}
