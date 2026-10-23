package objectlocation

import (
	"html"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
	"thoughtflow/internal/pkg/models"
)

// Discover relative attachments from every actual Markdown body. All copied
// resources are constrained to the content root and permitted passive types.
// A mapping preserves historical link text without rewriting immutable bodies.
func (r *Registry) relativeBindings(source models.ObjectLocation) (models.ResourceBindings, error) {
	bindings, e := r.Bindings(source.ObjectID)
	if e != nil {
		return bindings, e
	}
	known := map[string]bool{}
	for _, b := range bindings.Bindings {
		known[b.OriginalLocator] = true
	}
	paths := []string{source.Placement.ThoughtPath}
	dir, e := SafePath(r.Root, source.Placement.DocumentRoot+"/revisions")
	if e != nil {
		return bindings, e
	}
	entries, e := os.ReadDir(dir)
	if e != nil && !os.IsNotExist(e) {
		return bindings, e
	}
	for _, entry := range entries {
		if entry.IsDir() && !strings.HasPrefix(entry.Name(), ".") {
			paths = append(paths, source.Placement.DocumentRoot+"/revisions/"+entry.Name()+"/body.md")
		}
	}
	for _, relative := range paths {
		path, e := SafePath(r.Root, relative)
		if e != nil {
			return bindings, e
		}
		raw, e := os.ReadFile(path)
		if e != nil {
			return bindings, e
		}
		tree := goldmark.DefaultParser().Parse(text.NewReader(raw))
		var failure error
		ast.Walk(tree, func(n ast.Node, enter bool) (ast.WalkStatus, error) {
			if !enter {
				return ast.WalkContinue, nil
			}
			destination := ""
			switch node := n.(type) {
			case *ast.Link:
				destination = string(node.Destination)
			case *ast.Image:
				destination = string(node.Destination)
			}
			destination = html.UnescapeString(destination)
			if destination == "" || known[destination] || strings.HasPrefix(destination, "#") {
				return ast.WalkContinue, nil
			}
			u, e := url.Parse(destination)
			if e != nil {
				failure = ErrUnsafe
				return ast.WalkStop, nil
			}
			if u.Scheme != "" {
				return ast.WalkContinue, nil
			}
			if u.Host != "" || u.RawQuery != "" || strings.HasPrefix(u.Path, "/") {
				failure = ErrUnsafe
				return ast.WalkStop, nil
			}
			rel := filepath.ToSlash(filepath.Clean(filepath.Join(filepath.Dir(source.Placement.ThoughtPath), filepath.FromSlash(u.Path))))
			if strings.Contains(rel, "/.private/") || strings.HasPrefix(rel, ".content-registry/") {
				failure = ErrUnsafe
				return ast.WalkStop, nil
			}
			path, e := SafePath(r.Root, rel)
			if e != nil {
				failure = e
				return ast.WalkStop, nil
			}
			info, e := os.Stat(path)
			if e != nil || !info.Mode().IsRegular() || info.Size() > MaxResourceBytes {
				failure = ErrRecovery
				return ast.WalkStop, nil
			}
			data, e := os.ReadFile(path)
			if e != nil {
				failure = e
				return ast.WalkStop, nil
			}
			if _, e = sniffResource(filepath.Base(path), data); e != nil {
				failure = e
				return ast.WalkStop, nil
			}
			hash := Hash(data)
			id := "res-" + strings.TrimPrefix(hash, "sha256:")[:32]
			bindings.Bindings = append(bindings.Bindings, models.ResourceBinding{ResourceID: id, Hash: hash, Path: rel, OriginalBase: filepath.ToSlash(filepath.Dir(source.Placement.ThoughtPath)), OriginalLocator: destination})
			known[destination] = true
			return ast.WalkContinue, nil
		})
		if failure != nil {
			return bindings, failure
		}
	}
	return bindings, nil
}
