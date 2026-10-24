package biz

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"thoughtflow/internal/pkg/documentvalidation"

	"thoughtflow/internal/pkg/documentprofile"
	"thoughtflow/internal/pkg/models"
	"thoughtflow/internal/pkg/scratchpad"
)

var ErrCaptureParameters = errors.New("invalid capture parameters")

func reconcileParameters(old scratchpad.SessionContext, next map[string]string) map[string]string {
	values := mergeStringMaps(old.DocumentParameters, next)
	for key, p := range old.ParameterValues {
		if p.Confirmed {
			values[key] = p.Value
		}
	}
	return values
}

func contextSourceLinks(old scratchpad.SessionContext, next []string) []string {
	if old.SourceLinksConfirmed {
		return append([]string(nil), old.SourceLinks...)
	}
	return mergeContextStrings(old.SourceLinks, next)
}

func archiveSources(links []string) *models.SourceManifest {
	sources := &models.SourceManifest{SchemaVersion: 1, Sources: []models.DocumentSource{}}
	for i, link := range links {
		sources.Sources = append(sources.Sources, models.DocumentSource{ID: fmt.Sprintf("S%d", i+1), Type: "external", Locator: link, VerificationStatus: "unverified"})
	}
	return sources
}

func mergeIssues(old []models.CaptureIssue, next []models.CaptureIssue, legacy []string) []models.CaptureIssue {
	out := append([]models.CaptureIssue(nil), old...)
	for _, text := range legacy {
		kind := "contradiction"
		lower := strings.ToLower(text)
		if strings.Contains(lower, "response_format") || strings.Contains(lower, "conversion_unsupported") {
			kind = "diagnostic"
		}
		next = append(next, models.CaptureIssue{Kind: kind, Message: text})
	}
	for _, item := range next {
		item.Message = strings.TrimSpace(item.Message)
		if item.Message == "" {
			continue
		}
		switch item.Kind {
		case "limitation", "diagnostic":
			item.Blocking = false
		default:
			item.Kind = "contradiction"
			item.Blocking = true
		}
		item.ID = models.ContentHash(item.Kind + ":" + item.Message)[7:23]
		item.Status = "open" // Only the user resolution endpoint can resolve an item.
		found := false
		for _, prev := range out {
			if prev.ID == item.ID {
				found = true
				break
			}
		}
		if !found {
			out = append(out, item)
		}
	}
	return out
}

func activeConflicts(issues []models.CaptureIssue) []string {
	var out []string
	for _, i := range issues {
		if i.Blocking && i.Status != "resolved" {
			out = append(out, i.Message)
		}
	}
	return out
}

func (s *ScratchpadService) normalizeProfileContext(ctx *scratchpad.SessionContext) {
	if s.profiles == nil {
		return
	}
	p, err := s.resolveArchiveProfile(scratchpad.Scratchpad{SessionContext: *ctx}, nil)
	if err != nil {
		ctx.ArchiveReadiness = "needs_input"
		return
	}
	ctx.FrozenProfile = p.Snapshot
	ctx.DocumentParameters = profileParameters(p, ctx.DocumentParameters)
	if ctx.ParameterValues == nil {
		ctx.ParameterValues = map[string]scratchpad.ParameterValue{}
	}
	s.completeContextParameters(p, ctx)
	ctx.MissingProfileInputs = nil
	for _, input := range p.Inputs {
		value := ctx.DocumentParameters[input.Key]
		pv, ok := ctx.ParameterValues[input.Key]
		if (!ok || (!pv.Confirmed && pv.Value != value)) && value != "" {
			pv = scratchpad.ParameterValue{Value: value, Source: "extracted"}
			if value == input.Default {
				pv.Source = "default"
				pv.Confirmed = true
			}
			ctx.ParameterValues[input.Key] = pv
		}
		if input.Required && value == "" {
			ctx.MissingProfileInputs = append(ctx.MissingProfileInputs, input.Key)
		}
	}
	ctx.Issues = mergeIssues(ctx.Issues, nil, ctx.Conflicts)
	ctx.Conflicts = activeConflicts(ctx.Issues)
	ctx.ArchiveReadiness = "ready"
	if len(ctx.MissingProfileInputs) > 0 {
		ctx.ArchiveReadiness = "needs_input"
	} else if len(ctx.Conflicts) > 0 {
		ctx.ArchiveReadiness = "blocked"
	}
}

// Fill writing choices from the conversation, never factual gaps such as a
// standard edition, a measured result or a custom domain-specific input. Keep
// the provenance as assumed and honor even an explicitly cleared user value.
func (s *ScratchpadService) completeContextParameters(p documentprofile.DocumentProfile, ctx *scratchpad.SessionContext) {
	topic := firstNonEmptyString(ctx.Topic, ctx.CandidateTitle)
	if topic == "" || (strings.TrimSpace(ctx.Goal) == "" && strings.TrimSpace(ctx.Topic) == "") {
		return
	}
	values := map[string]string{
		"audience": "本次会话读者（按一般技术背景编写）",
		"purpose":  firstNonEmptyString(ctx.Goal, "整理并留存本次关于"+topic+"的讨论，供个人回顾与后续核验"),
	}
	switch p.Ref.ProfileID {
	case "builtin.research-report":
		values["research_question"] = firstNonEmptyString(ctx.Goal, topic)
		values["scope"] = "以本次会话关于" + topic + "的已讨论内容为范围"
		values["cutoff"] = s.now().UTC().Format("2006-01-02") + "（会话材料整理日期，不代表已核验最新资料）"
		comparisonText := strings.ToLower(topic + " " + ctx.Goal + " " + ctx.CandidateBody)
		if !containsAnyString(comparisonText, "对比", "比较", "compare", "comparison", "versus", " vs ", " vs. ") {
			values["comparison"] = "不适用（本次未进行对比）"
		}
	case "builtin.design-doc":
		values["problem"] = firstNonEmptyString(ctx.Goal, topic)
		values["goals"] = firstNonEmptyString(ctx.Goal, topic)
		values["context"] = topic
		values["non_goals"] = "本次会话未涉及的扩展需求不纳入本版设计范围"
	case "builtin.blog-post":
		values["core_message"] = firstNonEmptyString(ctx.Goal, ctx.CandidateSummary, topic)
		values["subtype"] = "opinion"
		if containsAnyString(strings.ToLower(topic+" "+ctx.Goal), "教程", "tutorial") {
			values["subtype"] = "tutorial"
		} else if containsAnyString(strings.ToLower(topic+" "+ctx.Goal), "案例", "case study") {
			values["subtype"] = "case"
		}
	}
	for _, input := range p.Inputs {
		if strings.TrimSpace(ctx.DocumentParameters[input.Key]) != "" || ctx.ParameterValues[input.Key].Confirmed {
			continue
		}
		if value := values[input.Key]; value != "" {
			ctx.DocumentParameters[input.Key] = value
			ctx.ParameterValues[input.Key] = scratchpad.ParameterValue{Value: value, Source: "assumed"}
		}
	}
}

func containsAnyString(text string, terms ...string) bool {
	for _, term := range terms {
		if strings.Contains(text, term) {
			return true
		}
	}
	return false
}

// PatchParameters is a field-level CAS API. A model result can suggest values,
// but cannot override these explicitly confirmed user decisions.
func (s *ScratchpadService) PatchParameters(id string, generation uint64, values map[string]string, resolutions map[string]string, sources ...[]string) (scratchpad.Scratchpad, error) {
	return s.store.Update(id, func(sp *scratchpad.Scratchpad) error {
		if sp.SessionGeneration != generation {
			return scratchpad.ErrConflict
		}
		p, err := s.resolveArchiveProfile(*sp, nil)
		if err != nil {
			return err
		}
		allowed := map[string]bool{}
		for _, in := range p.Inputs {
			allowed[in.Key] = true
		}
		if sp.SessionContext.DocumentParameters == nil {
			sp.SessionContext.DocumentParameters = map[string]string{}
		}
		if sp.SessionContext.ParameterValues == nil {
			sp.SessionContext.ParameterValues = map[string]scratchpad.ParameterValue{}
		}
		for key, value := range values {
			if !allowed[key] {
				return fmt.Errorf("%w: unknown profile input: %s", ErrCaptureParameters, key)
			}
			value = strings.TrimSpace(value)
			sp.SessionContext.DocumentParameters[key] = value
			sp.SessionContext.ParameterValues[key] = scratchpad.ParameterValue{Value: value, Source: "user", Confirmed: true}
		}
		if len(sources) > 0 {
			for _, link := range sources[0] {
				u, e := url.Parse(link)
				if e != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || documentvalidation.SensitiveLocator(link) {
					return fmt.Errorf("%w: unsafe source link", ErrCaptureParameters)
				}
			}
			sp.SessionContext.SourceLinks = trimNonEmpty(sources[0])
			sp.SessionContext.SourceLinksConfirmed = true
		}
		sp.SessionContext.Issues = mergeIssues(sp.SessionContext.Issues, nil, sp.SessionContext.Conflicts)
		for key, reason := range resolutions {
			if strings.TrimSpace(reason) == "" {
				return fmt.Errorf("%w: resolution reason is required", ErrCaptureParameters)
			}
			found := false
			for i := range sp.SessionContext.Issues {
				if sp.SessionContext.Issues[i].ID == key {
					sp.SessionContext.Issues[i].Status = "resolved"
					sp.SessionContext.Issues[i].Resolution = reason
					found = true
				}
			}
			if !found {
				return fmt.Errorf("%w: unknown issue: %s", ErrCaptureParameters, key)
			}
		}
		sp.SessionContext.Conflicts = activeConflicts(sp.SessionContext.Issues)
		s.normalizeProfileContext(&sp.SessionContext)
		sp.ArchivePreview = nil
		sp.Operation = nil
		// Editing inputs is not a fresh authorization to auto-archive a new generation.
		sp.ArchiveIntent = scratchpad.ArchiveIntentMenu
		sp.SessionContext.ArchiveIntent = scratchpad.ArchiveIntentMenu
		return nil
	})
}

func archivePreflight(p documentprofile.DocumentProfile, parameters map[string]string) []models.ValidationIssue {
	var issues []models.ValidationIssue
	for _, input := range p.Inputs {
		if input.Required && strings.TrimSpace(parameters[input.Key]) == "" {
			issues = append(issues, models.ValidationIssue{Code: "input_required", Severity: "error", Section: input.Key, Message: "Context is missing required input: " + input.Label})
		}
	}
	return issues
}

func foldArchiveAssumptions(draft *models.DocumentDraft, profile documentprofile.DocumentProfile) {
	if profile.SchemaVersion != 2 || len(draft.Assumptions) == 0 || len(profile.Sections) == 0 {
		return
	}
	if draft.Sections == nil {
		draft.Sections = map[string]models.DocumentSection{}
	}
	key := profile.Sections[len(profile.Sections)-1].Key
	for _, preferred := range []string{"limitations", "open_questions", "body"} {
		found := false
		for _, spec := range profile.Sections {
			if spec.Key == preferred {
				key, found = preferred, true
				break
			}
		}
		if found {
			break
		}
	}
	section := draft.Sections[key]
	section.Content += "\n\n**编写假设（并非用户确认的事实）**\n"
	for _, assumption := range draft.Assumptions {
		section.Content += "\n- " + assumption
	}
	draft.Sections[key] = section
	draft.Assumptions = nil // The v2 renderer must not append an undeclared H2.
}

// Capture persists a working document. Evidence quality still blocks formal
// finalization in documentvalidation, but missing external sources alone must
// not prevent archiving a useful, explicitly unverified conversation document.
// Never relax fabricated references, unsafe locators, or structural failures.
func captureEvidenceWarning(issue models.DocumentIssue, profile documentprofile.DocumentProfile, sources *models.SourceManifest) bool {
	return profile.Ref.Family == models.DocumentFamilyResearch && sources != nil && len(sources.Sources) == 0 &&
		(issue.Code == "research_evidence_missing" || issue.Code == "research_traceability")
}

const unverifiedResearchNotice = "本文依据本次会话材料整理，尚无外部来源支撑，未核验标准原文、版本或条款；仅作为个人知识工作版本，不代表已完成事实核验或正式定稿。"

func discloseUnverifiedResearch(draft *models.DocumentDraft, profile documentprofile.DocumentProfile, sources *models.SourceManifest) {
	if profile.Ref.Family != models.DocumentFamilyResearch || sources == nil || len(sources.Sources) != 0 {
		return
	}
	if draft.Sections == nil {
		draft.Sections = map[string]models.DocumentSection{}
	}
	// Only use a section declared by the exact profile; custom profiles may use
	// another layout, in which case the document summary carries the disclosure.
	for _, spec := range profile.Sections {
		if spec.Key == "limitations" {
			section := draft.Sections[spec.Key]
			if !strings.Contains(section.Content, unverifiedResearchNotice) {
				section.Content = strings.TrimSpace(section.Content) + "\n\n" + unverifiedResearchNotice
				draft.Sections[spec.Key] = section
			}
			return
		}
	}
	if !strings.Contains(draft.Summary, unverifiedResearchNotice) {
		draft.Summary = strings.TrimSpace(draft.Summary) + "\n\n" + unverifiedResearchNotice
	}
}
