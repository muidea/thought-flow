package markdown

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"thoughtflow/internal/pkg/models"
	"thoughtflow/internal/pkg/workspace"
)

func ThoughtRelativePath(thoughtID string) string {
	year := "unknown"
	month := "unknown"
	if len(thoughtID) >= 8 {
		year = thoughtID[:4]
		month = thoughtID[4:6]
	}
	return filepath.Join("thoughts", year, month, thoughtID+".md")
}

func WriteThought(rootPath string, thought models.Thought, content models.ThoughtContent) error {
	if thought.Path == "" {
		thought.Path = ThoughtRelativePath(thought.ID)
	}
	targetPath := filepath.Join(rootPath, filepath.FromSlash(thought.Path))
	if err := workspace.EnsureInside(rootPath, targetPath); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		return err
	}
	unknownFrontMatter, err := readUnknownFrontMatter(targetPath)
	if err != nil {
		return err
	}
	tmpPath := fmt.Sprintf("%s.%d.tmp", targetPath, time.Now().UnixNano())
	if err := os.WriteFile(tmpPath, renderThought(thought, content, unknownFrontMatter), 0o644); err != nil {
		return err
	}
	defer func() {
		_ = os.Remove(tmpPath)
	}()
	return os.Rename(tmpPath, targetPath)
}

func ReadThought(rootPath string, thoughtID string) (models.Thought, models.ThoughtContent, error) {
	relPath := ThoughtRelativePath(thoughtID)
	targetPath := filepath.Join(rootPath, filepath.FromSlash(relPath))
	if err := workspace.EnsureInside(rootPath, targetPath); err != nil {
		return models.Thought{}, models.ThoughtContent{}, err
	}
	raw, err := os.ReadFile(targetPath)
	if err != nil {
		return models.Thought{}, models.ThoughtContent{}, err
	}
	thought, content := ParseThought(raw)
	thought.ID = firstNonEmpty(thought.ID, thoughtID)
	thought.Path = filepath.ToSlash(relPath)
	return thought, content, nil
}

func RenderThought(thought models.Thought, content models.ThoughtContent) []byte {
	return renderThought(thought, content, nil)
}

func renderThought(thought models.Thought, content models.ThoughtContent, unknownFrontMatter []string) []byte {
	var buf bytes.Buffer
	buf.WriteString("---\n")
	writeScalar(&buf, "id", thought.ID)
	writeScalar(&buf, "type", thought.Type)
	writeScalar(&buf, "source", thought.Source)
	writeScalar(&buf, "user_title", thought.UserTitle)
	writeScalar(&buf, "extracted_title", thought.ExtractedTitle)
	writeScalar(&buf, "url", thought.URL)
	writeScalar(&buf, "path", filepath.ToSlash(thought.Path))
	writeTime(&buf, "created_at", thought.CreatedAt)
	writeTime(&buf, "updated_at", thought.UpdatedAt)
	writeScalar(&buf, "content_hash", thought.ContentHash)
	writeList(&buf, "user_tags", thought.UserTags)
	writeList(&buf, "ai_tags", thought.AITags)
	writeList(&buf, "topic_ids", thought.TopicIDs)
	writeScalar(&buf, "summary", thought.Summary)
	writeList(&buf, "key_points", thought.KeyPoints)
	writeScalar(&buf, "capture_status", thought.CaptureStatus)
	writeScalar(&buf, "refine_status", thought.RefineStatus)
	writeScalar(&buf, "index_status", thought.IndexStatus)
	writeScalar(&buf, "topic_status", thought.TopicStatus)
	writeUnknownFrontMatter(&buf, unknownFrontMatter)
	writeErrors(&buf, thought.Errors)
	buf.WriteString("---\n\n")
	writeSection(&buf, "Original", content.Original)
	writeSection(&buf, "Extracted Content", content.ExtractedContent)
	writeSection(&buf, "AI Notes", content.AINotes)
	writeSection(&buf, "Links", content.Links)
	return buf.Bytes()
}

func readUnknownFrontMatter(targetPath string) ([]string, error) {
	raw, err := os.ReadFile(targetPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return unknownFrontMatterLines(raw), nil
}

func ParseThought(raw []byte) (models.Thought, models.ThoughtContent) {
	text := string(raw)
	thought := models.Thought{}
	if frontMatter, ok := frontMatterText(raw); ok {
		parseFrontMatter(frontMatter, &thought)
		normalized := strings.ReplaceAll(text, "\r\n", "\n")
		if strings.HasPrefix(normalized, "---\n") {
			if end := strings.Index(normalized[4:], "\n---"); end >= 0 {
				text = strings.TrimPrefix(normalized[4+end+len("\n---"):], "\n")
			}
		}
	}
	content := models.ThoughtContent{
		Original:         parseSection(text, "Original"),
		ExtractedContent: parseSection(text, "Extracted Content"),
		AINotes:          parseSection(text, "AI Notes"),
		Links:            parseSection(text, "Links"),
	}
	thought.DisplayTitle = displayTitle(thought, content)
	return thought, content
}

func writeScalar(buf *bytes.Buffer, key string, value string) {
	if value == "" {
		return
	}
	_, _ = fmt.Fprintf(buf, "%s: %q\n", key, value)
}

func writeTime(buf *bytes.Buffer, key string, value time.Time) {
	if value.IsZero() {
		return
	}
	_, _ = fmt.Fprintf(buf, "%s: %q\n", key, value.Format(time.RFC3339))
}

func writeList(buf *bytes.Buffer, key string, values []string) {
	if len(values) == 0 {
		return
	}
	copied := append([]string(nil), values...)
	sort.Strings(copied)
	_, _ = fmt.Fprintf(buf, "%s:\n", key)
	for _, value := range copied {
		_, _ = fmt.Fprintf(buf, "  - %q\n", value)
	}
}

func writeUnknownFrontMatter(buf *bytes.Buffer, lines []string) {
	for _, line := range lines {
		_, _ = fmt.Fprintln(buf, strings.TrimRight(line, "\r"))
	}
}

func writeErrors(buf *bytes.Buffer, errors []models.ErrorRef) {
	if len(errors) == 0 {
		buf.WriteString("errors: []\n")
		return
	}
	buf.WriteString("errors:\n")
	for _, errRef := range errors {
		_, _ = fmt.Fprintf(buf, "  - code: %q\n", errRef.Code)
		_, _ = fmt.Fprintf(buf, "    message: %q\n", errRef.Message)
		writeIndentedTime(buf, "    ", "occurred_at", errRef.OccurredAt)
		_, _ = fmt.Fprintf(buf, "    retryable: %t\n", errRef.Retryable)
	}
}

func writeIndentedTime(buf *bytes.Buffer, indent string, key string, value time.Time) {
	if value.IsZero() {
		return
	}
	_, _ = fmt.Fprintf(buf, "%s%s: %q\n", indent, key, value.Format(time.RFC3339))
}

func unknownFrontMatterLines(raw []byte) []string {
	frontMatter, ok := frontMatterText(raw)
	if !ok {
		return nil
	}
	lines := strings.Split(frontMatter, "\n")
	ret := []string{}
	for idx := 0; idx < len(lines); {
		line := strings.TrimRight(lines[idx], "\r")
		key, hasKey := frontMatterKey(line)
		if !hasKey {
			idx++
			continue
		}
		blockEnd := idx + 1
		for blockEnd < len(lines) {
			if _, nextHasKey := frontMatterKey(strings.TrimRight(lines[blockEnd], "\r")); nextHasKey {
				break
			}
			blockEnd++
		}
		if !knownThoughtFrontMatterKey(key) {
			for _, preserved := range lines[idx:blockEnd] {
				ret = append(ret, strings.TrimRight(preserved, "\r"))
			}
		}
		idx = blockEnd
	}
	return ret
}

func frontMatterText(raw []byte) (string, bool) {
	text := strings.ReplaceAll(string(raw), "\r\n", "\n")
	if !strings.HasPrefix(text, "---\n") {
		return "", false
	}
	end := strings.Index(text[4:], "\n---")
	if end < 0 {
		return "", false
	}
	return text[4 : 4+end], true
}

func frontMatterKey(line string) (string, bool) {
	if strings.TrimSpace(line) == "" {
		return "", false
	}
	if line[0] == ' ' || line[0] == '\t' || strings.HasPrefix(strings.TrimSpace(line), "-") {
		return "", false
	}
	parts := strings.SplitN(line, ":", 2)
	if len(parts) != 2 {
		return "", false
	}
	key := strings.TrimSpace(parts[0])
	if key == "" {
		return "", false
	}
	return key, true
}

func knownThoughtFrontMatterKey(key string) bool {
	switch key {
	case "id",
		"type",
		"source",
		"user_title",
		"extracted_title",
		"url",
		"path",
		"created_at",
		"updated_at",
		"content_hash",
		"user_tags",
		"ai_tags",
		"topic_ids",
		"summary",
		"key_points",
		"capture_status",
		"refine_status",
		"index_status",
		"topic_status",
		"errors":
		return true
	default:
		return false
	}
}

func writeSection(buf *bytes.Buffer, title string, value string) {
	if value == "" {
		return
	}
	_, _ = fmt.Fprintf(buf, "## %s\n\n%s\n\n", title, strings.TrimSpace(value))
}

func parseFrontMatter(frontMatter string, thought *models.Thought) {
	lines := strings.Split(frontMatter, "\n")
	for idx := 0; idx < len(lines); idx++ {
		line := lines[idx]
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "-") {
			continue
		}
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		value := strings.Trim(strings.TrimSpace(parts[1]), `"`)
		switch key {
		case "id":
			thought.ID = value
		case "type":
			thought.Type = value
		case "source":
			thought.Source = value
		case "user_title":
			thought.UserTitle = value
		case "extracted_title":
			thought.ExtractedTitle = value
		case "url":
			thought.URL = value
		case "path":
			thought.Path = value
		case "created_at":
			thought.CreatedAt = parseTime(value)
		case "updated_at":
			thought.UpdatedAt = parseTime(value)
		case "content_hash":
			thought.ContentHash = value
		case "user_tags":
			thought.UserTags = parseList(lines, &idx)
		case "ai_tags":
			thought.AITags = parseList(lines, &idx)
		case "topic_ids":
			thought.TopicIDs = parseList(lines, &idx)
		case "summary":
			thought.Summary = value
		case "key_points":
			thought.KeyPoints = parseList(lines, &idx)
		case "capture_status":
			thought.CaptureStatus = value
		case "refine_status":
			thought.RefineStatus = value
		case "index_status":
			thought.IndexStatus = value
		case "topic_status":
			thought.TopicStatus = value
		case "errors":
			thought.Errors = parseErrors(lines, &idx, value)
		}
	}
}

func parseErrors(lines []string, idx *int, value string) []models.ErrorRef {
	if strings.TrimSpace(value) == "[]" {
		return nil
	}
	errors := []models.ErrorRef{}
	for *idx+1 < len(lines) {
		next := strings.TrimSpace(lines[*idx+1])
		if !strings.HasPrefix(next, "-") {
			break
		}
		errRef := models.ErrorRef{}
		first := strings.TrimSpace(strings.TrimPrefix(next, "-"))
		if strings.Contains(first, ":") {
			applyErrorField(&errRef, first)
		}
		*idx = *idx + 1
		for *idx+1 < len(lines) {
			child := lines[*idx+1]
			if strings.TrimSpace(child) == "" {
				*idx = *idx + 1
				continue
			}
			if !strings.HasPrefix(child, " ") && !strings.HasPrefix(child, "\t") {
				break
			}
			trimmed := strings.TrimSpace(child)
			if strings.HasPrefix(trimmed, "-") {
				break
			}
			applyErrorField(&errRef, trimmed)
			*idx = *idx + 1
		}
		errors = append(errors, errRef)
	}
	return errors
}

func applyErrorField(errRef *models.ErrorRef, line string) {
	parts := strings.SplitN(line, ":", 2)
	if len(parts) != 2 {
		return
	}
	key := strings.TrimSpace(parts[0])
	value := strings.Trim(strings.TrimSpace(parts[1]), `"`)
	switch key {
	case "code":
		errRef.Code = value
	case "message":
		errRef.Message = value
	case "occurred_at":
		errRef.OccurredAt = parseTime(value)
	case "retryable":
		errRef.Retryable = strings.EqualFold(value, "true")
	}
}

func parseList(lines []string, idx *int) []string {
	ret := []string{}
	for *idx+1 < len(lines) {
		next := strings.TrimSpace(lines[*idx+1])
		if !strings.HasPrefix(next, "-") {
			break
		}
		value := strings.TrimSpace(strings.TrimPrefix(next, "-"))
		value = strings.Trim(value, `"`)
		if value != "" {
			ret = append(ret, value)
		}
		*idx = *idx + 1
	}
	return ret
}

func parseTime(value string) time.Time {
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}
	}
	return parsed
}

func parseSection(text string, section string) string {
	marker := "## " + section
	start := strings.Index(text, marker)
	if start < 0 {
		return ""
	}
	body := text[start+len(marker):]
	body = strings.TrimPrefix(body, "\r")
	body = strings.TrimPrefix(body, "\n")
	body = strings.TrimPrefix(body, "\r")
	body = strings.TrimPrefix(body, "\n")
	lines := strings.Split(body, "\n")
	for idx, line := range lines {
		if idx == 0 {
			continue
		}
		if isThoughtBodySection(line) {
			body = strings.Join(lines[:idx], "\n")
			break
		}
	}
	return strings.TrimSpace(body)
}

func isThoughtBodySection(line string) bool {
	switch strings.TrimSpace(line) {
	case "## Original",
		"## Extracted Content",
		"## AI Notes",
		"## Links":
		return true
	default:
		return false
	}
}

// AppendAINotes appends a timestamped paragraph to the AI Notes section
// of a thought body. If the section is missing, it is created just
// before the trailing separator (or appended to the end). The returned
// string is the new value for ThoughtContent.AINotes.
func AppendAINotes(existing string, paragraph string, now time.Time) string {
	cleanParagraph := strings.TrimRight(paragraph, "\n")
	header := fmt.Sprintf("\n### %s\n", now.UTC().Format("2006-01-02 15:04:05 UTC"))
	if strings.TrimSpace(existing) == "" {
		return "## AI Notes\n" + header + cleanParagraph + "\n"
	}
	// Insert before any trailing "---" separator so the AI Notes block
	// stays adjacent to the previous content rather than slipping past the
	// front-matter divider. If no separator is found, append at the end.
	lines := strings.Split(existing, "\n")
	insertAt := len(lines)
	for idx := len(lines) - 1; idx >= 0; idx-- {
		if strings.TrimSpace(lines[idx]) == "---" {
			insertAt = idx
			break
		}
	}
	prefix := strings.Join(lines[:insertAt], "\n")
	suffix := strings.Join(lines[insertAt:], "\n")
	if strings.TrimSpace(prefix) == "" {
		return "## AI Notes\n" + header + cleanParagraph + "\n" + suffix
	}
	if !strings.HasSuffix(prefix, "\n") {
		prefix += "\n"
	}
	return prefix + header + cleanParagraph + "\n" + suffix
}

func displayTitle(thought models.Thought, content models.ThoughtContent) string {
	if thought.UserTitle != "" {
		return thought.UserTitle
	}
	if thought.ExtractedTitle != "" {
		return thought.ExtractedTitle
	}
	if content.Original != "" {
		firstLine := strings.TrimSpace(strings.Split(content.Original, "\n")[0])
		if len(firstLine) > 80 {
			return firstLine[:80]
		}
		return firstLine
	}
	return thought.ID
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
