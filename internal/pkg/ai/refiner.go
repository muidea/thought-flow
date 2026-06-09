package ai

import (
	"context"
	"regexp"
	"sort"
	"strings"
	"time"

	"thoughtflow/internal/pkg/models"
)

type RefineRequest struct {
	Thought models.Thought
	Content models.ThoughtContent
}

type RefineProvider interface {
	Refine(ctx context.Context, req RefineRequest) (models.ThoughtRefinement, error)
}

type LocalRefineProvider struct{}

func NewLocalRefineProvider() *LocalRefineProvider {
	return &LocalRefineProvider{}
}

func (p *LocalRefineProvider) Refine(ctx context.Context, req RefineRequest) (models.ThoughtRefinement, error) {
	_ = ctx
	text := strings.TrimSpace(req.Content.ExtractedContent)
	if text == "" {
		text = strings.TrimSpace(req.Content.Original)
	}
	return models.ThoughtRefinement{
		ThoughtID:   req.Thought.ID,
		Status:      models.RefineStatusRefined,
		Summary:     summarize(text),
		KeyPoints:   keyPoints(text),
		AITags:      inferTags(text),
		Model:       "local-rule",
		InputHash:   models.ContentHash(text),
		GeneratedAt: time.Now().UTC(),
	}, nil
}

func summarize(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	if len(text) <= 240 {
		return text
	}
	return text[:240]
}

func keyPoints(text string) []string {
	splitter := regexp.MustCompile(`[。.!?\n]+`)
	raw := splitter.Split(text, -1)
	ret := []string{}
	for _, item := range raw {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if len(item) > 160 {
			item = item[:160]
		}
		ret = append(ret, item)
		if len(ret) >= 3 {
			break
		}
	}
	return ret
}

func inferTags(text string) []string {
	lower := strings.ToLower(text)
	candidates := map[string][]string{
		"ai":                   {"ai", "llm", "embedding", "model", "openai", "deepseek"},
		"knowledge-management": {"knowledge", "note", "markdown", "obsidian", "logseq", "search"},
		"engineering":          {"go", "golang", "api", "http", "duckdb", "git"},
		"research":             {"paper", "study", "analysis", "summary"},
	}
	ret := []string{}
	for tag, words := range candidates {
		for _, word := range words {
			if strings.Contains(lower, word) {
				ret = append(ret, tag)
				break
			}
		}
	}
	sort.Strings(ret)
	return ret
}
