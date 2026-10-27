package documentsetflow

import (
	"net/url"
	"os"
	"path"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	ds "thoughtflow/internal/pkg/documentset"
	"thoughtflow/internal/pkg/documentstore"
	"thoughtflow/internal/pkg/objectlocation"
)

type MarkdownFile struct {
	Path    string `json:"path"`
	Member  string `json:"member_key"`
	Content string `json:"content"`
}
type MarkdownPreviewRequest struct {
	OperationID string         `json:"operation_id"`
	SetID       string         `json:"set_id"`
	PlanID      string         `json:"plan_id"`
	PlanHash    string         `json:"plan_hash"`
	Files       []MarkdownFile `json:"files"`
}
type MarkdownMapping struct {
	Path         string `json:"path"`
	Member       string `json:"member_key"`
	DocumentID   string `json:"document_id"`
	Original     string `json:"original"`
	OriginalHash string `json:"original_hash"`
	Derived      string `json:"derived"`
	DerivedHash  string `json:"derived_hash"`
}
type MarkdownPreview struct {
	ID          string            `json:"preview_id"`
	RequestHash string            `json:"request_hash"`
	SetID       string            `json:"set_id"`
	PlanHash    string            `json:"plan_hash"`
	Mappings    []MarkdownMapping `json:"mappings"`
	Warnings    []string          `json:"warnings"`
	Cycles      [][]string        `json:"cycles"`
}

func markdownPath(set, id string) string {
	return documentsetstorePath(set) + "/markdown/" + id + ".json"
}
func documentsetstorePath(set string) string { return ".content-registry/private/document-sets/" + set }

// PreviewMarkdown never imports attachments or reads a client-supplied path.
// Original bytes are immutable; only the derived candidate rewrites links.
func (s *Service) PreviewMarkdown(req MarkdownPreviewRequest) (MarkdownPreview, error) {
	var out MarkdownPreview
	if !documentstore.ValidID(req.OperationID) || !documentstore.ValidID(req.SetID) || len(req.Files) == 0 || len(req.Files) > 50 {
		return out, ds.ErrInvalid
	}
	p, e := s.Store.Plan(req.SetID, req.PlanID)
	if e != nil {
		return out, e
	}
	if p.Hash != req.PlanHash {
		return out, ds.ErrConflict
	}
	release := objectlocation.For(s.Store.Root).LockObject("set-markdown-" + req.OperationID)
	defer release()
	hash := documentstore.HashJSON(req)
	if e = objectlocation.ReadJSON(s.Store.Root, markdownPath(req.SetID, req.OperationID), &out); e == nil {
		if out.RequestHash != hash {
			return out, ds.ErrConflict
		}
		return out, nil
	} else if !os.IsNotExist(e) {
		return out, e
	}
	out = MarkdownPreview{ID: req.OperationID, RequestHash: hash, SetID: p.SetID, PlanHash: p.Hash, Warnings: []string{}, Cycles: [][]string{}}
	members := map[string]ds.PlanMember{}
	for _, m := range p.Members {
		members[m.Key] = m
	}
	paths := map[string]string{}
	used := map[string]bool{}
	total := 0
	for _, f := range req.Files {
		total += len(f.Content)
		if !safeMarkdownPath(f.Path) || !utf8.ValidString(f.Content) || len(f.Content) > 1<<20 || total > 8<<20 || members[f.Member].Key == "" || used[f.Member] || paths[f.Path] != "" {
			return out, ds.ErrInvalid
		}
		paths[f.Path] = f.Member
		used[f.Member] = true
	}
	graph := map[string][]string{}
	for _, f := range req.Files {
		derived := f.Content
		for _, link := range ds.Links(f.Content) {
			u, err := url.Parse(link)
			if err != nil {
				return out, ds.ErrInvalid
			}
			if u.Scheme != "" || u.Host != "" || u.Path == "" {
				continue
			}
			decoded, err := url.PathUnescape(u.Path)
			if err != nil {
				return out, ds.ErrInvalid
			}
			target := path.Clean(path.Join(path.Dir(f.Path), decoded))
			if strings.EqualFold(path.Ext(target), ".md") {
				member := paths[target]
				if member == "" {
					out.Warnings = append(out.Warnings, "Missing document: "+f.Path+" → "+target)
					continue
				}
				graph[f.Member] = append(graph[f.Member], member)
				replacement := "docset://" + p.SetID + "/" + url.PathEscape(member)
				if u.Fragment != "" {
					replacement += "#" + u.EscapedFragment()
				}
				derived = rewriteMarkdownLocator(derived, link, replacement)
			} else {
				out.Warnings = append(out.Warnings, "Attachment not imported; original link retained: "+f.Path+" → "+link)
			}
		}
		out.Mappings = append(out.Mappings, MarkdownMapping{Path: f.Path, Member: f.Member, DocumentID: members[f.Member].DocumentID, Original: f.Content, OriginalHash: documentstore.Hash([]byte(f.Content)), Derived: derived, DerivedHash: documentstore.Hash([]byte(derived))})
	}
	// Strongly connected reference groups, including cycles longer than two,
	// are diagnostic only. They are not the generation scheduling DAG.
	index := 0
	indices := map[string]int{}
	low := map[string]int{}
	onStack := map[string]bool{}
	stack := []string{}
	var visit func(string)
	visit = func(node string) {
		index++
		indices[node] = index
		low[node] = index
		stack = append(stack, node)
		onStack[node] = true
		for _, next := range graph[node] {
			if indices[next] == 0 {
				visit(next)
				if low[next] < low[node] {
					low[node] = low[next]
				}
			} else if onStack[next] && indices[next] < low[node] {
				low[node] = indices[next]
			}
		}
		if low[node] != indices[node] {
			return
		}
		component := []string{}
		for {
			last := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			onStack[last] = false
			component = append(component, last)
			if last == node {
				break
			}
		}
		cycle := len(component) > 1
		for _, next := range graph[node] {
			if next == node {
				cycle = true
			}
		}
		if cycle {
			sort.Strings(component)
			out.Cycles = append(out.Cycles, component)
		}
	}
	keys := []string{}
	for key := range used {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if indices[key] == 0 {
			visit(key)
		}
	}

	e = objectlocation.WriteJSON(s.Store.Root, markdownPath(p.SetID, req.OperationID), out, true)
	return out, e
}
func safeMarkdownPath(s string) bool {
	return s != "" && len(s) < 512 && path.Clean(s) == s && !strings.HasPrefix(s, "/") && !strings.HasPrefix(s, "../") && !strings.ContainsAny(s, "\\\x00\r\n") && strings.EqualFold(path.Ext(s), ".md")
}

var destinationPattern = regexp.MustCompile(`(?m)(\]\(<?|\]:[ \t]*<?)([^\s<>]+)(>?(?:[ \t]+["'][^\n]*["'])?\)?[ \t]*$|>?\))`)

func rewriteMarkdownLocator(body, from, to string) string {
	// Operate only on Markdown destination syntax; never replace prose/code
	// occurrences indiscriminately. Reference definitions are handled too.
	return destinationPattern.ReplaceAllStringFunc(body, func(v string) string {
		i := strings.Index(v, from)
		if i < 0 {
			return v
		}
		end := i + len(from)
		if end < len(v) && !strings.ContainsRune(">) \t", rune(v[end])) {
			return v
		}
		return v[:i] + to + v[end:]
	})
}
func (s *Service) markdownMember(run ds.Run, key string) (*MarkdownMapping, error) {
	if run.MarkdownPreviewID == "" {
		return nil, nil
	}
	var p MarkdownPreview
	if e := objectlocation.ReadJSON(s.Store.Root, markdownPath(run.Plan.SetID, run.MarkdownPreviewID), &p); e != nil {
		return nil, e
	}
	if p.PlanHash != run.Plan.Hash {
		return nil, ds.ErrConflict
	}
	for _, m := range p.Mappings {
		if m.Member == key {
			if m.OriginalHash != documentstore.Hash([]byte(m.Original)) || m.DerivedHash != documentstore.Hash([]byte(m.Derived)) {
				return nil, ds.ErrInvalid
			}
			return &m, nil
		}
	}
	return nil, nil
}

func importedSection(body string) string {
	lines := strings.Split(body, "\n")
	fence := ""
	for i, line := range lines {
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "```") || strings.HasPrefix(trim, "~~~") {
			marker := trim[:3]
			if fence == "" {
				fence = marker
			} else if marker == fence {
				fence = ""
			}
			continue
		}
		if fence != "" {
			continue
		}
		if strings.HasPrefix(trim, "# ") || strings.HasPrefix(trim, "## ") {
			lines[i] = "### " + strings.TrimLeft(trim, "# ")
		}
		if i > 0 && (regexp.MustCompile(`^={3,}$`).MatchString(trim) || regexp.MustCompile(`^-{3,}$`).MatchString(trim)) && strings.TrimSpace(lines[i-1]) != "" {
			lines[i-1] = "### " + lines[i-1]
			lines[i] = ""
		}
	}
	return strings.Join(lines, "\n")
}
