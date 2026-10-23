package objectlocation

import (
	"html"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
	"thoughtflow/internal/pkg/models"
)

// OfflineResourceIssues inspects actual rendered asset destinations. It never
// downloads remote assets or treats an ordinary web citation as an attachment.
func OfflineResourceIssues(body string, bindings []models.ResourceBinding) []models.OfflineResourceIssue {
	known := map[string]bool{}
	for _, b := range bindings {
		if b.Private {
			continue
		}
		known["resource://"+b.ResourceID] = true
		if b.OriginalLocator != "" {
			known[b.OriginalLocator] = true
		}
	}
	issues := []models.OfflineResourceIssue{}
	seen := map[string]bool{}
	ast.Walk(goldmark.DefaultParser().Parse(text.NewReader([]byte(body))), func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		var destination string
		image := false
		switch node := n.(type) {
		case *ast.Image:
			destination, image = string(node.Destination), true
		case *ast.Link:
			destination = string(node.Destination)
		default:
			return ast.WalkContinue, nil
		}
		destination = html.UnescapeString(destination)
		if destination == "" || strings.HasPrefix(destination, "#") || known[destination] || seen[destination] {
			return ast.WalkContinue, nil
		}
		remote := strings.HasPrefix(destination, "https://") || strings.HasPrefix(destination, "http://") || strings.HasPrefix(destination, "//")
		if remote && !image {
			return ast.WalkContinue, nil
		}
		reason := "missing_resource"
		if remote {
			reason = "external_image"
		}
		seen[destination] = true
		issues = append(issues, models.OfflineResourceIssue{Locator: destination, Reason: reason})
		return ast.WalkContinue, nil
	})
	return issues
}
