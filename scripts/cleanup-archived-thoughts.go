// Command cleanup-archived-thoughts performs the one-time, idempotent
// convergence of archive documents that predate the strict preview/profile
// contract. It defaults to dry-run; pass -apply to write atomically through
// the production Markdown writer.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"thoughtflow/internal/pkg/markdown"
	"thoughtflow/internal/pkg/models"
)

const canonicalWorkOrchID = "20260718-025056-3edd4f"

type cleanupSpec struct {
	id         string
	mode       string
	tailMarker string
}

var cleanupSpecs = []cleanupSpec{
	{id: "20260622-063416-bb94c2", mode: "truncate-history"},
	{id: "20260624-015226-5a23be", mode: "truncate-history"},
	{id: "20260714-012613-60543b", mode: "unwrap-profile", tailMarker: "## 备选方案与取舍"},
	{id: "20260716-020102-3c656a", mode: "unwrap-profile", tailMarker: "## 对比与分析"},
	{id: "20260721-124041-a90161", mode: "unwrap-profile", tailMarker: "## 备选方案与取舍"},
	{id: "20260722-024350-ae088f", mode: "unwrap-profile", tailMarker: "## 备选方案与取舍"},
	{id: canonicalWorkOrchID, mode: "normalize-headings"},
	{id: "20260717-035101-13a253", mode: "normalize-headings"},
	{id: "20260718-031333-aae5c4", mode: "redirect"},
	{id: "20260718-035138-9a43b4", mode: "redirect"},
}

func main() {
	workspace := flag.String("workspace", "thoughtflow-workspace", "ThoughtFlow workspace root")
	apply := flag.Bool("apply", false, "write changes; default is dry-run")
	flag.Parse()

	root, err := filepath.Abs(*workspace)
	if err != nil {
		fatal(err)
	}
	changed := 0
	for _, spec := range cleanupSpecs {
		thought, content, err := markdown.ReadThought(root, spec.id)
		if err != nil {
			fatal(fmt.Errorf("read %s: %w", spec.id, err))
		}
		before := strings.TrimSpace(content.AINotes)
		after, err := cleanupBody(spec, thought, before)
		if err != nil {
			fatal(fmt.Errorf("clean %s: %w", spec.id, err))
		}
		after = normalizeSingleTitle(thought, after)
		if after == before {
			fmt.Printf("unchanged %s\n", spec.id)
			continue
		}
		changed++
		fmt.Printf("%s %s lines=%d->%d hash=%s->%s\n", action(*apply), spec.id,
			lineCount(before), lineCount(after), models.ContentHash(before), models.ContentHash(after))
		if !*apply {
			continue
		}
		content.AINotes = after
		thought.ContentHash = models.ContentHash(after)
		thought.UpdatedAt = time.Now().UTC()
		thought.RefineStatus = models.RefineStatusPending
		thought.IndexStatus = models.IndexStatusPending
		if spec.mode == "redirect" {
			thought.Summary = "重复内容已收口至 Thought " + canonicalWorkOrchID + "。"
			thought.KeyPoints = nil
			thought.ExpansionPlan = ""
			thought.RelatedThoughtIDs = appendUnique(thought.RelatedThoughtIDs, canonicalWorkOrchID)
			content.Links = appendLink(content.Links, canonicalWorkOrchID)
		}
		if err := markdown.WriteThought(root, thought, content); err != nil {
			fatal(fmt.Errorf("write %s: %w", spec.id, err))
		}
	}
	if !*apply {
		fmt.Printf("dry-run complete: %d file(s) would change; rerun with -apply\n", changed)
	} else {
		fmt.Printf("apply complete: %d file(s) changed\n", changed)
	}
}

func cleanupBody(spec cleanupSpec, thought models.Thought, body string) (string, error) {
	switch spec.mode {
	case "truncate-history":
		const marker = "\n\n## 补充整理信息"
		if idx := strings.Index(body, marker); idx >= 0 {
			return strings.TrimSpace(body[:idx]), nil
		}
		return body, nil
	case "unwrap-profile":
		return unwrapProfileDocument(body, spec.tailMarker)
	case "redirect":
		title := strings.TrimSpace(thought.UserTitle)
		if title == "" {
			title = strings.TrimSpace(thought.DisplayTitle)
		}
		return fmt.Sprintf("# %s\n\n> 本 Thought 的正文与主版本重复，已保留原 ID 并收口为主版本引用。\n\n## 主版本\n\n- [[%s.md|%s]]", title, canonicalWorkOrchID, canonicalWorkOrchID), nil
	case "normalize-headings":
		return body, nil
	default:
		return "", fmt.Errorf("unknown cleanup mode %q", spec.mode)
	}
}

func unwrapProfileDocument(body, tailMarker string) (string, error) {
	lines := strings.Split(strings.TrimSpace(body), "\n")
	headings := h1Indexes(lines)
	if len(headings) < 2 {
		return strings.TrimSpace(body), nil
	}
	firstTitle := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(lines[headings[0]]), "# "))
	start := headings[len(headings)-1]
	end := len(lines)
	for idx := start + 1; idx < len(lines); idx++ {
		if strings.TrimSpace(lines[idx]) == tailMarker {
			end = idx
			break
		}
	}
	kept := append([]string(nil), lines[start:end]...)
	if len(kept) == 0 {
		return "", fmt.Errorf("nested profile document is empty")
	}
	kept[0] = "# " + firstTitle
	return strings.TrimSpace(strings.Join(kept, "\n")), nil
}

func normalizeSingleTitle(thought models.Thought, body string) string {
	lines := strings.Split(strings.TrimSpace(body), "\n")
	headings := h1Indexes(lines)
	if len(headings) == 0 {
		title := strings.TrimSpace(thought.UserTitle)
		if title == "" {
			title = strings.TrimSpace(thought.DisplayTitle)
		}
		if title != "" {
			return "# " + title + "\n\n" + strings.TrimSpace(body)
		}
		return strings.TrimSpace(body)
	}
	for _, idx := range headings[1:] {
		lines[idx] = "### " + strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(lines[idx]), "# "))
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func h1Indexes(lines []string) []int {
	out := []int{}
	inFence := false
	for idx, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			inFence = !inFence
			continue
		}
		if !inFence && strings.HasPrefix(trimmed, "# ") {
			out = append(out, idx)
		}
	}
	return out
}

func appendLink(existing, thoughtID string) string {
	link := fmt.Sprintf("- [[%s.md|%s]]", thoughtID, thoughtID)
	if strings.Contains(existing, link) {
		return existing
	}
	if strings.TrimSpace(existing) == "" {
		return link
	}
	return strings.TrimSpace(existing) + "\n" + link
}

func appendUnique(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func lineCount(value string) int {
	if strings.TrimSpace(value) == "" {
		return 0
	}
	return len(strings.Split(value, "\n"))
}

func action(apply bool) string {
	if apply {
		return "update"
	}
	return "would-update"
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
