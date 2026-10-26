package biz

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
	"gopkg.in/yaml.v3"

	"thoughtflow/internal/pkg/models"
)

const MaxMarkdownImportBytes = 32 << 20

var ErrMarkdownImport = errors.New("invalid Markdown import")

// ImportMarkdown never adopts identity, filesystem paths, status or document
// contracts supplied by the source. Metadata extraction is best effort; the
// complete original bytes, including unrecognized front matter, remain intact.
func (s *Service) ImportMarkdown(ctx context.Context, filename string, raw []byte) (models.CaptureResult, error) {
	ext := strings.ToLower(filepath.Ext(filename))
	if (ext != ".md" && ext != ".markdown") || len(filename) > 255 || strings.ContainsAny(filename, `/\`) ||
		strings.IndexFunc(filename, unicode.IsControl) >= 0 || len(raw) == 0 || len(raw) > MaxMarkdownImportBytes ||
		!utf8.Valid(raw) || bytes.IndexByte(raw, 0) >= 0 || len(bytes.TrimSpace(bytes.TrimPrefix(raw, []byte("\xef\xbb\xbf")))) == 0 {
		return models.CaptureResult{}, ErrMarkdownImport
	}
	title, tags := markdownImportMetadata(raw)
	if title == "" {
		title = strings.TrimSuffix(filename, filepath.Ext(filename))
	}
	return s.Capture(ctx, models.CaptureCommand{
		Type: models.ThoughtTypeText, Content: string(raw), Title: title, Tags: tags, Source: models.ThoughtSourceImport,
		MarkdownImport: &models.MarkdownImport{Filename: filename, SHA256: models.ContentHash(string(raw)), Size: len(raw)},
	})
}

func markdownImportMetadata(raw []byte) (string, []string) {
	body := bytes.TrimPrefix(raw, []byte("\xef\xbb\xbf"))
	normalized := strings.ReplaceAll(string(body), "\r\n", "\n")
	title := ""
	var tags []string
	if strings.HasPrefix(normalized, "---\n") {
		lines := strings.Split(normalized, "\n")
		for i := 1; i < len(lines); i++ {
			if lines[i] != "---" && lines[i] != "..." {
				continue
			}
			var metadata map[string]any
			if yaml.Unmarshal([]byte(strings.Join(lines[1:i], "\n")), &metadata) == nil {
				for _, key := range []string{"title", "user_title", "extracted_title"} {
					if value, ok := metadata[key].(string); ok && strings.TrimSpace(value) != "" {
						title = strings.TrimSpace(value)
						break
					}
				}
				for _, key := range []string{"tags", "user_tags"} {
					switch v := metadata[key].(type) {
					case string:
						tags = append(tags, strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == '，' })...)
					case []any:
						for _, tag := range v {
							if value, ok := tag.(string); ok {
								tags = append(tags, value)
							}
						}
					}
				}
			}
			body = []byte(strings.Join(lines[i+1:], "\n"))
			break
		}
	}
	if title == "" {
		_ = ast.Walk(goldmark.DefaultParser().Parse(text.NewReader(body)), func(n ast.Node, entering bool) (ast.WalkStatus, error) {
			if heading, ok := n.(*ast.Heading); ok && entering {
				title = strings.TrimSpace(string(heading.Text(body)))
				return ast.WalkStop, nil
			}
			return ast.WalkContinue, nil
		})
	}
	return title, normalizeList(tags)
}
