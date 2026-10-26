package biz

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"thoughtflow/internal/pkg/markdown"
	"thoughtflow/internal/pkg/models"
)

func TestMarkdownImportPreservesAllSourceBytesAcrossReadAndEdit(t *testing.T) {
	for _, source := range []string{
		"\xef\xbb\xbf---\r\ntitle: 'A quoted title'\r\ntags: [research, original]\r\nid: do-not-adopt\r\npath: ../../escape.md\r\ncustom: {nested: [one, two]}\r\n---\r\n\r\n# Heading\r\n\r\n## Original\r\n\r\nText  \r\n\r\n## AI Notes\r\n\r\n```md\r\n## Links\r\n---\r\n```\r\n![local](./images/x.png)\r\n\r\n\t  ",
		"# No front matter\n\nA table:\n\n| A | B |\n|---|---|\n| 1 | 2 |\n\n<!-- Keep comment -->\n[^one]: full footnote\n",
		"---\ntitle: [malformed: yaml\n---\n# Still importable\n\nDo not discard unknown metadata.\n",
		"\n\n    code only, no heading\n\t\t\n",
	} {
		t.Run(models.ContentHash(source)[7:15], func(t *testing.T) {
			root := t.TempDir()
			ws := &models.Workspace{ID: "test", RootPath: root}
			svc := NewService(ws, nil, nil)
			result, err := svc.ImportMarkdown(context.Background(), "source.md", []byte(source))
			if err != nil {
				t.Fatal(err)
			}
			id := result.Thought.ID
			if id == "do-not-adopt" || result.Thought.Source != models.ThoughtSourceImport || !strings.HasPrefix(result.Thought.Path, "thoughts/") {
				t.Fatal("source identity or path became authoritative", result.Thought)
			}
			for i := 0; i < 3; i++ {
				got, err := NewService(ws, nil, nil).GetThought(context.Background(), id)
				if err != nil || got.Content.Original != source || got.Content.AINotes != source || got.Thought.MarkdownImport.SHA256 != models.ContentHash(source) {
					t.Fatalf("source changed at read %d: %v", i, err)
				}
				if err = markdown.WriteThought(root, got.Thought, got.Content); err != nil {
					t.Fatal(err)
				}
			}
			body := "Edited working body"
			patch, _ := json.Marshal(map[string]string{"body": body})
			got, err := svc.PatchThought(context.Background(), id, "edit", models.ThoughtPatchRequest{Body: &body}, patch)
			if err != nil || got.Content.Original != source || got.Content.AINotes != body {
				t.Fatalf("editing destroyed original: %v", err)
			}
			raw, err := os.ReadFile(filepath.Join(root, result.Thought.Path))
			if err != nil {
				t.Fatal(err)
			}
			_, decoded := markdown.ParseThought(raw)
			if decoded.Original != source {
				t.Fatal("saved Thought does not contain complete source")
			}
		})
	}
}

func TestMarkdownImportMetadataAndIdentity(t *testing.T) {
	svc := NewService(&models.Workspace{RootPath: t.TempDir()}, nil, nil)
	for _, tc := range []struct{ source, title string }{
		{"---\ntitle: Metadata title\ntags: [one, two]\n---\n# Heading\n", "Metadata title"},
		{"```md\n# Fake heading\n```\n\n# Real heading\n", "Real heading"},
		{"Content without heading.", "filename"},
		{"---\nid: original\nuser_title: From Thought\nuser_tags:\n - one\n---\n## Original\nA\n## AI Notes\nB", "From Thought"},
	} {
		result, err := svc.ImportMarkdown(context.Background(), "filename.md", []byte(tc.source))
		if err != nil || result.Thought.UserTitle != tc.title {
			t.Fatalf("metadata conversion: %+v %v", result, err)
		}
		second, err := svc.ImportMarkdown(context.Background(), "filename.md", []byte(tc.source))
		if err != nil || second.Thought.ID == result.Thought.ID {
			t.Fatal("import overwrote an existing Thought", err)
		}
	}
}

func TestMarkdownImportRejectsInvalidInputWithoutSaving(t *testing.T) {
	svc := NewService(&models.Workspace{RootPath: t.TempDir()}, nil, nil)
	for _, tc := range []struct{ name, source string }{
		{"bad.txt", "text"}, {"../bad.md", "text"}, {"bad.md", ""}, {"bad.md", "\xef\xbb\xbf \r\n"},
		{"bad.md", "\xff\xfe"}, {"bad.md", "a\x00b"}, {"bad.md", strings.Repeat("a", MaxMarkdownImportBytes+1)},
	} {
		if _, err := svc.ImportMarkdown(context.Background(), tc.name, []byte(tc.source)); !errors.Is(err, ErrMarkdownImport) {
			t.Fatal("invalid source accepted", tc.name, err)
		}
	}
	notes, err := svc.ListThoughts(context.Background())
	if err != nil || len(notes) != 0 {
		t.Fatal("invalid input was partially saved", err)
	}
}
