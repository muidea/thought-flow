package markdown

import (
	"bytes"
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
	tmpPath := fmt.Sprintf("%s.%d.tmp", targetPath, time.Now().UnixNano())
	if err := os.WriteFile(tmpPath, RenderThought(thought, content), 0o644); err != nil {
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
	buf.WriteString("errors: []\n")
	buf.WriteString("---\n\n")
	writeSection(&buf, "Original", content.Original)
	writeSection(&buf, "Extracted Content", content.ExtractedContent)
	writeSection(&buf, "AI Notes", content.AINotes)
	writeSection(&buf, "Links", content.Links)
	return buf.Bytes()
}

func ParseThought(raw []byte) (models.Thought, models.ThoughtContent) {
	text := string(raw)
	thought := models.Thought{}
	if strings.HasPrefix(text, "---\n") {
		if end := strings.Index(text[4:], "\n---"); end >= 0 {
			frontMatter := text[4 : 4+end]
			parseFrontMatter(frontMatter, &thought)
			text = strings.TrimPrefix(text[4+end+len("\n---"):], "\n")
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
		}
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
	next := strings.Index(body, "\n## ")
	if next >= 0 {
		body = body[:next]
	}
	return strings.TrimSpace(body)
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
