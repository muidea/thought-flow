package markdown

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"thoughtflow/internal/pkg/documentflow"
	"thoughtflow/internal/pkg/documentstore"
	"thoughtflow/internal/pkg/models"
	"thoughtflow/internal/pkg/objectlocation"
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
	registry := objectlocation.For(rootPath)
	location, e := registry.Reserve(thought.ID, thought.OwnerTopicID, "create-"+thought.ID)
	if e != nil {
		return e
	}
	if thought.Path != "" && filepath.ToSlash(thought.Path) != location.Placement.ThoughtPath {
		return objectlocation.ErrUnsafe
	}
	thought.Path = location.Placement.ThoughtPath
	flow := documentflow.For(rootPath)
	unlock := flow.Store.Lock(thought.ID)
	err := func() error {
		defer unlock()
		if thought.ContentExpectedHash != "" {
			_, current, err := readThoughtLocked(rootPath, thought.ID)
			if err != nil {
				return err
			}
			if documentstore.HashJSON(current) != thought.ContentExpectedHash {
				return documentstore.ErrConflict
			}
		}
		if _, err := flow.SyncProjectionLocked(&thought, &content, false); err != nil {
			return err
		}
		return writeThoughtProjection(rootPath, thought, content)
	}()
	if err != nil {
		return err
	}
	if location.Lifecycle == "reserved" {
		return registry.Activate(thought.ID)
	}
	return nil
}

func writeThoughtProjection(rootPath string, thought models.Thought, content models.ThoughtContent) error {
	location, e := objectlocation.For(rootPath).Lookup(thought.ID)
	if e != nil {
		return e
	}
	thought.Path = location.Placement.ThoughtPath
	targetPath := filepath.Join(rootPath, filepath.FromSlash(thought.Path))
	if err := workspace.EnsureNoSymlinks(rootPath, targetPath); err != nil {
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
	if err := writeProjectionSync(tmpPath, renderThought(thought, content, unknownFrontMatter)); err != nil {
		return err
	}
	defer func() {
		_ = os.Remove(tmpPath)
	}()
	if err := os.Rename(tmpPath, targetPath); err != nil {
		return err
	}
	dir, e := os.Open(filepath.Dir(targetPath))
	if e != nil {
		return e
	}
	e = dir.Sync()
	dir.Close()
	if e != nil {
		return e
	}
	objectlocation.For(rootPath).BumpEpoch()
	if thought.Document != nil {
		return documentflow.For(rootPath).ProjectionCommittedLocked(thought.ID, thought.Document.RevisionID)
	}
	return nil
}

// CleanupOrphanThoughtTempFiles removes any *.tmp leftovers from a
// previous crash of WriteThought. The happy path of WriteThought deletes
// its temp file via defer, but if the process dies between os.WriteFile
// and os.Rename, the temp file remains on disk. The lock is in-memory
// only, so the file state and the process state stay consistent across
// restarts — the only "stale" thing left behind is a temp file that no
// reader will ever pick up. Cleaning it up at startup keeps the
// thoughts directory tidy and prevents disk pressure on long-running
// services.
//
// Safe to call when the directory does not exist (returns nil).
func CleanupOrphanThoughtTempFiles(thoughtsPath string) error {
	if strings.TrimSpace(thoughtsPath) == "" {
		return nil
	}
	if _, err := os.Stat(thoughtsPath); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var firstErr error
	err := filepath.WalkDir(thoughtsPath, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil || d.IsDir() {
			return walkErr
		}
		if !strings.HasSuffix(d.Name(), ".tmp") {
			return nil
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) && firstErr == nil {
			firstErr = err
		}
		return nil
	})
	if err != nil && firstErr == nil {
		return err
	}
	return firstErr
}

func ReadThought(rootPath string, thoughtID string) (models.Thought, models.ThoughtContent, error) {
	flow := documentflow.For(rootPath)
	unlock := flow.Store.Lock(thoughtID)
	thought, content, err := readThoughtLocked(rootPath, thoughtID)
	unlock()
	if err == nil {
		if l, e := objectlocation.For(rootPath).Lookup(thoughtID); e == nil && l.Lifecycle == "reserved" {
			err = objectlocation.For(rootPath).Activate(thoughtID)
		}
	}
	return thought, content, err
}

// UpdateThought serializes a read-modify-write with projections, deletion and
// document HEAD publication. Metadata writers must merge into this latest value.
func UpdateThought(rootPath, thoughtID string, update func(*models.Thought, *models.ThoughtContent) error) error {
	flow := documentflow.For(rootPath)
	unlock := flow.Store.Lock(thoughtID)
	defer unlock()
	return updateThoughtLocked(rootPath, thoughtID, update)
}
func updateThoughtLocked(rootPath, thoughtID string, update func(*models.Thought, *models.ThoughtContent) error) error {
	flow := documentflow.For(rootPath)
	thought, content, err := readThoughtLocked(rootPath, thoughtID)
	if err != nil {
		return err
	}
	if err = update(&thought, &content); err != nil {
		return err
	}
	if thought.ID != thoughtID {
		return documentstore.ErrInvalid
	}
	if _, err = flow.SyncProjectionLocked(&thought, &content, false); err != nil {
		return err
	}
	return writeThoughtProjection(rootPath, thought, content)
}

func readThoughtLocked(rootPath, thoughtID string) (models.Thought, models.ThoughtContent, error) {
	flow := documentflow.For(rootPath)
	raw, err := ReadThoughtRawUnderLease(rootPath, thoughtID)
	location, locationErr := objectlocation.For(rootPath).Lookup(thoughtID)
	if locationErr != nil {
		return models.Thought{}, models.ThoughtContent{}, locationErr
	}
	if location.DeletedAt != nil {
		return models.Thought{}, models.ThoughtContent{}, os.ErrNotExist
	}
	relPath := location.Placement.ThoughtPath
	var thought models.Thought
	var content models.ThoughtContent
	if os.IsNotExist(err) {
		h, headErr := flow.Store.ActiveHead(thoughtID)
		if headErr != nil {
			return thought, content, headErr
		}
		if !h.PendingProjection {
			return thought, content, err
		}
		d, e := flow.Store.Revision(thoughtID, h.WorkingRevisionID)
		if e != nil {
			return thought, content, e
		}
		if d.Projection == nil {
			return thought, content, documentflow.ErrUnavailable
		}
		thought, content = d.Projection.Thought, d.Projection.Content
	} else if err != nil {
		return thought, content, err
	} else {
		if err = validateThoughtEncoding(raw); err != nil {
			return thought, content, err
		}
		thought, content = ParseThought(raw)
	}
	if thought.ID != "" && thought.ID != thoughtID {
		return models.Thought{}, models.ThoughtContent{}, fmt.Errorf("%w: Markdown identity does not match its path", documentstore.ErrInvalid)
	}
	thought.ID = thoughtID
	thought.OwnerTopicID = location.OwnerTopicID
	thought.GlobalSearch = location.GlobalSearch
	thought.Path = filepath.ToSlash(relPath)
	recover, err := flow.SyncProjectionLocked(&thought, &content, true)
	if err != nil {
		return models.Thought{}, models.ThoughtContent{}, err
	}
	thought.DisplayTitle = displayTitle(thought, content)
	if recover {
		if err = writeThoughtProjection(rootPath, thought, content); err != nil {
			return models.Thought{}, models.ThoughtContent{}, err
		}
	}
	return thought, content, nil
}

// ReadThoughtRaw returns the exact Markdown document stored for thoughtID.
// It performs the same workspace-boundary validation as ReadThought so
// callers that need a lossless representation do not reconstruct paths.
func ReadThoughtRaw(rootPath string, thoughtID string) ([]byte, error) {
	release := objectlocation.For(rootPath).ReadLease(thoughtID)
	defer release()
	return ReadThoughtRawUnderLease(rootPath, thoughtID)
}

// ReadThoughtRawUnderLease requires an existing namespace lease. The atomic
// projection can also be inspected without locking a second source object.
func ReadThoughtRawUnderLease(rootPath string, thoughtID string) ([]byte, error) {
	if err := documentflow.For(rootPath).Store.CheckActive(thoughtID); err != nil {
		return nil, err
	}
	location, e := objectlocation.For(rootPath).Lookup(thoughtID)
	if e != nil {
		return nil, e
	}
	if e = objectlocation.For(rootPath).Checkpoint("thought_raw_resolved"); e != nil {
		return nil, e
	}
	if location.DeletedAt != nil {
		return nil, os.ErrNotExist
	}
	relPath := location.Placement.ThoughtPath
	targetPath := filepath.Join(rootPath, filepath.FromSlash(relPath))
	if err := workspace.EnsureNoSymlinks(rootPath, targetPath); err != nil {
		return nil, err
	}
	return os.ReadFile(targetPath)
}

func RenderThought(thought models.Thought, content models.ThoughtContent) []byte {
	return renderThought(thought, content, nil)
}

func renderThought(thought models.Thought, content models.ThoughtContent, unknownFrontMatter []string) []byte {
	var buf bytes.Buffer
	buf.WriteString("---\n")
	writeScalar(&buf, "id", thought.ID)
	if thought.Document != nil {
		writeScalar(&buf, "document_id", thought.Document.DocumentID)
		writeScalar(&buf, "document_revision_id", thought.Document.RevisionID)
	}
	boundary := ""
	encoding := "sections-v2"
	if thought.MarkdownImport != nil {
		encoding = "sections-v3"
		raw, _ := json.Marshal(thought.MarkdownImport)
		writeScalar(&buf, "markdown_import", string(raw))
	}
	if thought.DocumentProfile != nil || thought.Document != nil || thought.MarkdownImport != nil {
		boundary = documentstore.HashJSON(content)
		writeScalar(&buf, "body_encoding", encoding)
		writeScalar(&buf, "body_boundary", boundary)
	}
	writeScalar(&buf, "type", thought.Type)
	writeScalar(&buf, "source", thought.Source)
	writeScalar(&buf, "user_title", thought.UserTitle)
	writeScalar(&buf, "extracted_title", thought.ExtractedTitle)
	writeScalar(&buf, "url", thought.URL)
	writeScalar(&buf, "path", filepath.ToSlash(thought.Path))
	writeTime(&buf, "created_at", thought.CreatedAt)
	writeTime(&buf, "updated_at", thought.UpdatedAt)
	writeScalar(&buf, "content_hash", thought.ContentHash)
	writeScalar(&buf, "last_refined_input_hash", thought.LastRefinedInputHash)
	writeList(&buf, "user_tags", thought.UserTags)
	writeList(&buf, "ai_tags", thought.AITags)
	writeList(&buf, "topic_ids", thought.TopicIDs)
	writeScalar(&buf, "summary", thought.Summary)
	writeList(&buf, "key_points", thought.KeyPoints)
	writeList(&buf, "related_thought_ids", thought.RelatedThoughtIDs)
	writeList(&buf, "suggested_topic_ids", thought.SuggestedTopicIDs)
	writeURLFollowups(&buf, "url_followups", thought.URLFollowups)
	writeBlockScalar(&buf, "expansion_plan", thought.ExpansionPlan)
	writeDocumentProfile(&buf, thought.DocumentProfile)
	if thought.Document == nil {
		if thought.DocumentParameters != nil {
			raw, _ := json.Marshal(thought.DocumentParameters)
			writeScalar(&buf, "document_parameters", string(raw))
		}
		if thought.DocumentSources != nil {
			raw, _ := json.Marshal(thought.DocumentSources)
			writeScalar(&buf, "document_sources", string(raw))
		}
	}
	writeScalar(&buf, "capture_draft", strconv.FormatBool(thought.CaptureDraft))
	writeScalar(&buf, "capture_status", thought.CaptureStatus)
	writeScalar(&buf, "refine_status", thought.RefineStatus)
	writeScalar(&buf, "index_status", thought.IndexStatus)
	writeScalar(&buf, "topic_status", thought.TopicStatus)
	writeUnknownFrontMatter(&buf, unknownFrontMatter)
	writeErrors(&buf, thought.Errors)
	buf.WriteString("---\n\n")
	if boundary != "" {
		for _, section := range []struct{ name, value string }{{"Original", content.Original}, {"Extracted Content", content.ExtractedContent}, {"AI Notes", content.AINotes}, {"Links", content.Links}} {
			value := documentstore.Normalize(section.value)
			if encoding == "sections-v3" {
				// The extra LF belongs to the envelope, not the source. Preserve
				// BOM, CRLF, indentation and trailing whitespace byte for byte.
				value = section.value + "\n"
			}
			fmt.Fprintf(&buf, "<!-- thoughtflow:%s:%s:start -->\n## %s\n\n%s<!-- thoughtflow:%s:%s:end -->\n\n", boundary, section.name, section.name, value, boundary, section.name)
		}
	} else {
		writeSection(&buf, "Original", content.Original)
		writeSection(&buf, "Extracted Content", content.ExtractedContent)
		writeSection(&buf, "AI Notes", content.AINotes)
		writeSection(&buf, "Links", content.Links)
	}
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
		// Strip only the envelope; normalizing the entire file would also
		// modify an imported document embedded in a lossless section.
		for offset := strings.IndexByte(text, '\n') + 1; offset > 0 && offset < len(text); {
			end := strings.IndexByte(text[offset:], '\n')
			if end < 0 {
				break
			}
			end += offset
			if strings.TrimSuffix(text[offset:end], "\r") == "---" {
				text = text[end+1:]
				break
			}
			offset = end + 1
		}
	}
	content := models.ThoughtContent{
		Original:         parseSection(text, "Original"),
		ExtractedContent: parseSection(text, "Extracted Content"),
		AINotes:          parseSection(text, "AI Notes"),
		Links:            parseSection(text, "Links"),
	}
	if fm, ok := frontMatterText(raw); ok {
		boundary := ""
		encoding := ""
		for _, line := range strings.Split(fm, "\n") {
			if strings.HasPrefix(line, "body_boundary:") {
				boundary = parseScalar(strings.TrimSpace(strings.TrimPrefix(line, "body_boundary:")))
			}
			if strings.HasPrefix(line, "body_encoding:") {
				encoding = parseScalar(strings.TrimSpace(strings.TrimPrefix(line, "body_encoding:")))
			}
		}
		if (encoding == "sections-v2" || encoding == "sections-v3") && boundary != "" {
			values := []*string{&content.Original, &content.ExtractedContent, &content.AINotes, &content.Links}
			for i, name := range []string{"Original", "Extracted Content", "AI Notes", "Links"} {
				start := "<!-- thoughtflow:" + boundary + ":" + name + ":start -->\n## " + name + "\n\n"
				end := "<!-- thoughtflow:" + boundary + ":" + name + ":end -->"
				if encoding == "sections-v3" {
					end = "\n" + end
				}
				a := strings.Index(text, start)
				if a >= 0 {
					tail := text[a+len(start):]
					b := strings.Index(tail, end)
					if b >= 0 {
						*values[i] = tail[:b]
						if encoding != "sections-v3" && strings.TrimSpace(*values[i]) == "" {
							*values[i] = ""
						}
					}
				}
			}
		}
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
	// RFC3339Nano keeps sub-second precision so list ordering by updated_at
	// remains stable when multiple thoughts are captured within the same second.
	_, _ = fmt.Fprintf(buf, "%s: %q\n", key, value.UTC().Format(time.RFC3339Nano))
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

// writeBlockScalar emits a YAML block scalar (|) for multi-line text
// such as the LLM expansion plan. Indented lines stay verbatim and
// trailing newlines are stripped. An empty value is a no-op so the
// thought file doesn't accumulate empty keys.
func writeBlockScalar(buf *bytes.Buffer, key string, value string) {
	if strings.TrimSpace(value) == "" {
		return
	}
	_, _ = fmt.Fprintf(buf, "%s: |\n", key)
	for _, line := range strings.Split(strings.TrimRight(value, "\n"), "\n") {
		_, _ = fmt.Fprintf(buf, "  %s\n", line)
	}
}

// writeURLFollowups emits the url_followups list as a sequence of
// inline maps. Sorted by URL for deterministic file output.
func writeURLFollowups(buf *bytes.Buffer, key string, values []models.URLFollowup) {
	if len(values) == 0 {
		return
	}
	copied := append([]models.URLFollowup(nil), values...)
	sort.Slice(copied, func(i, j int) bool { return copied[i].URL < copied[j].URL })
	_, _ = fmt.Fprintf(buf, "%s:\n", key)
	for _, v := range copied {
		_, _ = fmt.Fprintf(buf, "  - url: %q\n", v.URL)
		if v.Title != "" {
			_, _ = fmt.Fprintf(buf, "    title: %q\n", v.Title)
		}
		if v.Snippet != "" {
			_, _ = fmt.Fprintf(buf, "    snippet: %q\n", v.Snippet)
		}
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
	_, _ = fmt.Fprintf(buf, "%s%s: %q\n", indent, key, value.UTC().Format(time.RFC3339Nano))
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
	case "markdown_import", "document_id", "document_revision_id", "body_encoding", "body_boundary", "id",
		"type",
		"source",
		"user_title",
		"extracted_title",
		"url",
		"path",
		"created_at",
		"updated_at",
		"content_hash",
		"last_refined_input_hash",
		"user_tags",
		"ai_tags",
		"topic_ids",
		"summary",
		"key_points",
		"related_thought_ids",
		"suggested_topic_ids",
		"url_followups",
		"expansion_plan",
		"document_profile",
		"capture_draft",
		"document_parameters",
		"document_sources",
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
		value := parseScalar(parts[1])
		switch key {
		case "markdown_import":
			_ = json.Unmarshal([]byte(value), &thought.MarkdownImport)
		case "document_id":
			if thought.Document == nil {
				thought.Document = &models.DocumentLink{}
			}
			thought.Document.DocumentID = parseScalar(parts[1])
		case "document_revision_id":
			if thought.Document == nil {
				thought.Document = &models.DocumentLink{}
			}
			thought.Document.RevisionID = parseScalar(parts[1])
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
		case "last_refined_input_hash":
			thought.LastRefinedInputHash = value
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
		case "related_thought_ids":
			thought.RelatedThoughtIDs = parseList(lines, &idx)
		case "suggested_topic_ids":
			thought.SuggestedTopicIDs = parseList(lines, &idx)
		case "url_followups":
			thought.URLFollowups = parseURLFollowups(lines, &idx)
		case "expansion_plan":
			thought.ExpansionPlan = parseBlockScalar(lines, &idx)
		case "document_profile":
			thought.DocumentProfile = parseDocumentProfile(lines, &idx)
		case "document_parameters":
			_ = json.Unmarshal([]byte(value), &thought.DocumentParameters)
		case "document_sources":
			_ = json.Unmarshal([]byte(value), &thought.DocumentSources)
		case "capture_draft":
			thought.CaptureDraft = value == "true"
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

func writeDocumentProfile(buf *bytes.Buffer, ref *models.DocumentProfileRef) {
	if ref == nil || strings.TrimSpace(ref.ProfileID) == "" {
		return
	}
	buf.WriteString("document_profile:\n")
	_, _ = fmt.Fprintf(buf, "  family: %q\n", ref.Family)
	_, _ = fmt.Fprintf(buf, "  profile_id: %q\n", ref.ProfileID)
	_, _ = fmt.Fprintf(buf, "  version: %d\n", ref.Version)
	_, _ = fmt.Fprintf(buf, "  content_hash: %q\n", ref.ContentHash)
}

func parseDocumentProfile(lines []string, idx *int) *models.DocumentProfileRef {
	ref := &models.DocumentProfileRef{}
	for j := *idx + 1; j < len(lines); j++ {
		raw := lines[j]
		if strings.TrimSpace(raw) == "" {
			continue
		}
		if !strings.HasPrefix(raw, "  ") && !strings.HasPrefix(raw, "\t") {
			break
		}
		key, value, ok := splitKV(strings.TrimSpace(raw))
		if !ok {
			continue
		}
		switch key {
		case "family":
			ref.Family = unquote(value)
		case "profile_id":
			ref.ProfileID = unquote(value)
		case "version":
			ref.Version, _ = strconv.Atoi(unquote(value))
		case "content_hash":
			ref.ContentHash = unquote(value)
		}
		*idx = j
	}
	if strings.TrimSpace(ref.ProfileID) == "" {
		return nil
	}
	return ref
}

// parseBlockScalar reads a YAML `key: |` block scalar. The header line
// itself is the current one; subsequent indented (>=2 spaces) lines are
// accumulated verbatim until the indent drops to zero.
func parseBlockScalar(lines []string, idx *int) string {
	var buf strings.Builder
	for j := *idx + 1; j < len(lines); j++ {
		raw := lines[j]
		if raw == "" {
			buf.WriteString("\n")
			continue
		}
		// A non-indented line ends the block. parseFrontMatter's loop
		// sees the same line and will dispatch on the new key, so we
		// must not consume it.
		if !strings.HasPrefix(raw, "  ") && !strings.HasPrefix(raw, "\t") {
			break
		}
		buf.WriteString(strings.TrimPrefix(raw, "  "))
		buf.WriteString("\n")
		*idx = j
	}
	return strings.TrimRight(buf.String(), "\n")
}

// parseURLFollowups reads a sequence of inline maps written by
// writeURLFollowups. Each item starts with `- url: ...` followed by
// optional `title:` / `snippet:` lines indented to the same column.
func parseURLFollowups(lines []string, idx *int) []models.URLFollowup {
	out := []models.URLFollowup{}
	for j := *idx + 1; j < len(lines); j++ {
		raw := strings.TrimSpace(lines[j])
		if raw == "" {
			continue
		}
		if !strings.HasPrefix(raw, "-") {
			break
		}
		entry := models.URLFollowup{}
		rest := strings.TrimSpace(strings.TrimPrefix(raw, "-"))
		if key, val, ok := splitKV(rest); ok && key == "url" {
			entry.URL = unquote(val)
		}
		for k := j + 1; k < len(lines); k++ {
			cont := lines[k]
			stripped := strings.TrimSpace(cont)
			if stripped == "" {
				continue
			}
			if !strings.HasPrefix(cont, "    ") {
				break
			}
			if key, val, ok := splitKV(stripped); ok {
				switch key {
				case "title":
					entry.Title = unquote(val)
				case "snippet":
					entry.Snippet = unquote(val)
				}
			}
			j = k
		}
		out = append(out, entry)
		*idx = j
	}
	return out
}

func splitKV(s string) (string, string, bool) {
	parts := strings.SplitN(s, ":", 2)
	if len(parts) != 2 {
		return "", "", false
	}
	return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]), true
}

func unquote(s string) string {
	return strings.Trim(strings.TrimSpace(s), `"`)
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
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}
	}
	// Prefer nanosecond precision; fall back to second-precision timestamps
	// written by older ThoughtFlow versions.
	if parsed, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return parsed.UTC()
	}
	if parsed, err := time.Parse(time.RFC3339, value); err == nil {
		return parsed.UTC()
	}
	return time.Time{}
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
	if content.AINotes != "" {
		firstLine := strings.TrimSpace(strings.Split(content.AINotes, "\n")[0])
		if len(firstLine) > 80 {
			return firstLine[:80]
		}
		return firstLine
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

// ExtractDocumentBody is the sole adapter from a Thought wrapper to delivery bytes.
func ExtractDocumentBody(content models.ThoughtContent) string {
	return documentflow.ExtractBody(content)
}

func parseScalar(value string) string {
	value = strings.TrimSpace(value)
	if unquoted, err := strconv.Unquote(value); err == nil {
		return unquoted
	}
	return value
}

func writeProjectionSync(path string, raw []byte) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err = file.Write(raw); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	return closeErr
}

func validateThoughtEncoding(raw []byte) error {
	fm, ok := frontMatterText(raw)
	if !ok {
		return nil
	}
	encoding, boundary := "", ""
	for _, line := range strings.Split(fm, "\n") {
		if strings.HasPrefix(line, "body_encoding:") {
			encoding = parseScalar(strings.TrimPrefix(line, "body_encoding:"))
		}
		if strings.HasPrefix(line, "body_boundary:") {
			boundary = parseScalar(strings.TrimPrefix(line, "body_boundary:"))
		}
	}
	if encoding == "" {
		return nil
	}
	if (encoding != "sections-v2" && encoding != "sections-v3") || boundary == "" {
		return documentstore.ErrInvalid
	}
	text := string(raw)
	if encoding == "sections-v2" {
		text = strings.ReplaceAll(text, "\r\n", "\n")
	}
	last := 0
	for _, name := range []string{"Original", "Extracted Content", "AI Notes", "Links"} {
		start := "<!-- thoughtflow:" + boundary + ":" + name + ":start -->\n## " + name + "\n\n"
		end := "<!-- thoughtflow:" + boundary + ":" + name + ":end -->"
		if encoding == "sections-v3" {
			end = "\n" + end
		}
		a, b := strings.Index(text, start), strings.Index(text, end)
		if strings.Count(text, start) != 1 || strings.Count(text, end) != 1 || a < last || b < a+len(start) {
			return documentstore.ErrInvalid
		}
		last = b + len(end)
	}
	return nil
}
