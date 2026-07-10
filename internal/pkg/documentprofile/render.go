package documentprofile

import (
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"thoughtflow/internal/pkg/models"
)

var citationPattern = regexp.MustCompile(`\[(S[0-9]+)\]`)

type RenderResult struct {
	Content    string                   `json:"content"`
	Validation models.ArchiveValidation `json:"validation"`
}

func Render(profile DocumentProfile, draft models.DocumentDraft, parameters map[string]string) RenderResult {
	issues := validateDraft(profile, draft)
	lines := strings.Split(strings.TrimRight(profile.Template, "\n"), "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if match := tokenPattern.FindStringSubmatch(trimmed); len(match) == 2 && match[0] == trimmed && strings.HasPrefix(match[1], "section:") {
			parts := strings.Split(match[1], "|")
			key := strings.TrimPrefix(parts[0], "section:")
			content := strings.TrimSpace(draft.Sections[key].Content)
			optional := len(parts) == 2 && parts[1] == "optional"
			if content == "" && optional {
				out = removeTrailingHeading(out)
				continue
			}
			out = append(out, strings.Split(content, "\n")...)
			continue
		}
		replaced := tokenPattern.ReplaceAllStringFunc(line, func(raw string) string {
			token := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(raw, "{{"), "}}"))
			switch {
			case token == "title":
				return strings.TrimSpace(draft.Title)
			case token == "summary":
				return strings.TrimSpace(draft.Summary)
			case token == "references":
				return renderReferences(draft.References)
			case strings.HasPrefix(token, "parameter:"):
				return strings.TrimSpace(parameters[strings.TrimPrefix(token, "parameter:")])
			default:
				return raw
			}
		})
		out = append(out, replaced)
	}
	content := normalizeRendered(strings.Join(out, "\n"))
	issues = append(issues, validateRendered(profile, content, draft.References)...)
	status := models.ArchiveValidationValid
	for _, issue := range issues {
		if issue.Severity == models.ValidationSeverityError {
			status = models.ArchiveValidationInvalid
			break
		}
	}
	return RenderResult{
		Content: content,
		Validation: models.ArchiveValidation{
			Status:      status,
			Issues:      issues,
			ValidatedAt: time.Now().UTC(),
		},
	}
}

func validateDraft(profile DocumentProfile, draft models.DocumentDraft) []models.ValidationIssue {
	issues := []models.ValidationIssue{}
	if strings.TrimSpace(draft.Title) == "" {
		issues = append(issues, issue("thoughtflow.profile.title_required", "", "document title is required"))
	}
	declared := map[string]SectionSpec{}
	for _, section := range profile.Sections {
		declared[section.Key] = section
		if section.Required && strings.TrimSpace(draft.Sections[section.Key].Content) == "" {
			issues = append(issues, issue("thoughtflow.profile.section_required", section.Key, fmt.Sprintf("required section %q is empty", section.Key)))
		}
	}
	if !profile.Validation.AllowUnknownSections {
		for _, key := range sortedMapKeys(draft.Sections) {
			if _, ok := declared[key]; !ok {
				issues = append(issues, issue("thoughtflow.profile.section_unknown", key, fmt.Sprintf("section %q is not declared by the profile", key)))
			}
		}
	}
	return issues
}

func validateRendered(profile DocumentProfile, content string, references []models.DocumentReference) []models.ValidationIssue {
	issues := []models.ValidationIssue{}
	if tokenPattern.MatchString(content) {
		issues = append(issues, issue("thoughtflow.profile.placeholder_remaining", "", "rendered document contains unresolved placeholders"))
	}
	chars := utf8.RuneCountInString(strings.TrimSpace(content))
	if profile.Validation.MinimumBodyChars > 0 && chars < profile.Validation.MinimumBodyChars {
		issues = append(issues, issue("thoughtflow.profile.body_too_short", "", fmt.Sprintf("document has %d characters; minimum is %d", chars, profile.Validation.MinimumBodyChars)))
	}
	if profile.Validation.MaximumBodyChars > 0 && chars > profile.Validation.MaximumBodyChars {
		issues = append(issues, issue("thoughtflow.profile.body_too_long", "", fmt.Sprintf("document has %d characters; maximum is %d", chars, profile.Validation.MaximumBodyChars)))
	}
	knownRefs := make(map[string]struct{}, len(references))
	for index, ref := range references {
		id := strings.TrimSpace(ref.ID)
		if id == "" {
			id = fmt.Sprintf("S%d", index+1)
		}
		knownRefs[id] = struct{}{}
	}
	for _, id := range extractReferenceIDs(content) {
		if _, ok := knownRefs[id]; !ok {
			issues = append(issues, issue("thoughtflow.profile.reference_unknown", "", fmt.Sprintf("citation [%s] does not match a structured reference", id)))
		}
	}
	return issues
}

func renderReferences(refs []models.DocumentReference) string {
	lines := []string{}
	for idx, ref := range refs {
		link := strings.TrimSpace(ref.SourceLink)
		if link == "" {
			continue
		}
		id := strings.TrimSpace(ref.ID)
		if id == "" {
			id = fmt.Sprintf("S%d", idx+1)
		}
		label := strings.TrimSpace(ref.Title)
		if label == "" {
			label = link
		}
		lines = append(lines, fmt.Sprintf("- [%s] %s: %s", id, label, link))
	}
	return strings.Join(lines, "\n")
}

func removeTrailingHeading(lines []string) []string {
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) > 0 && strings.HasPrefix(strings.TrimSpace(lines[len(lines)-1]), "#") {
		lines = lines[:len(lines)-1]
	}
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

func normalizeRendered(value string) string {
	lines := strings.Split(strings.TrimSpace(value), "\n")
	out := []string{}
	blank := false
	for _, line := range lines {
		isBlank := strings.TrimSpace(line) == ""
		if isBlank && blank {
			continue
		}
		out = append(out, strings.TrimRight(line, " \t"))
		blank = isBlank
	}
	return strings.TrimSpace(strings.Join(out, "\n")) + "\n"
}

func extractReferenceIDs(content string) []string {
	matches := citationPattern.FindAllStringSubmatch(content, -1)
	out := make([]string, 0, len(matches))
	seen := map[string]struct{}{}
	for _, match := range matches {
		if len(match) != 2 {
			continue
		}
		if _, ok := seen[match[1]]; ok {
			continue
		}
		seen[match[1]] = struct{}{}
		out = append(out, match[1])
	}
	return out
}

func issue(code, section, message string) models.ValidationIssue {
	return models.ValidationIssue{Code: code, Severity: models.ValidationSeverityError, Section: section, Message: message}
}
