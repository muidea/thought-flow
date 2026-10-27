package documentsetstore

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	ds "thoughtflow/internal/pkg/documentset"
	"thoughtflow/internal/pkg/documentstore"
	"thoughtflow/internal/pkg/models"
	"thoughtflow/internal/pkg/objectlocation"
)

const MaxPackageBytes = 128 << 20
const MaxPackageFiles = 10000

type ExportRequest struct {
	OperationID        string  `json:"operation_id"`
	ExpectedGeneration *uint64 `json:"expected_generation"`
	RevisionID         string  `json:"set_revision_id"`
	Kind               string  `json:"kind"` // delivery, diagnostic, backup
}
type PackageFile struct {
	Path string `json:"path"`
	Hash string `json:"hash"`
	Size int    `json:"size"`
}
type PackageMember struct {
	Key          string `json:"member_key"`
	Revision     string `json:"revision_id"`
	BodyHash     string `json:"body_hash"`
	MarkdownHash string `json:"derived_markdown_hash"`
	HTMLHash     string `json:"derived_html_hash"`
	Filename     string `json:"filename"`
}
type PackageManifest struct {
	Schema       int             `json:"schema_version"`
	Kind         string          `json:"kind"`
	SetID        string          `json:"set_id"`
	Revision     string          `json:"set_revision_id"`
	RevisionHash string          `json:"set_revision_hash"`
	Status       string          `json:"status"`
	Members      []PackageMember `json:"members"`
	Files        []PackageFile   `json:"files"`
}

func (s *Store) Export(id string, req ExportRequest) ([]byte, error) {
	if req.ExpectedGeneration == nil || req.RevisionID == "" {
		return nil, ds.ErrPrecondition
	}
	if !documentstore.ValidID(req.OperationID) || (req.Kind != "backup" && req.Kind != "delivery" && req.Kind != "diagnostic") {
		return nil, ds.ErrInvalid
	}
	registry := objectlocation.For(s.Root)
	registry.Namespace.Lock()
	defer registry.Namespace.Unlock()
	h, e := s.Head(id)
	if e != nil {
		return nil, e
	}
	if h.Generation != *req.ExpectedGeneration {
		return nil, ds.ErrConflict
	}
	r, e := s.Read(id, req.RevisionID)
	if e != nil {
		return nil, e
	}
	if req.Kind == "delivery" && (h.Finalized != r.ID || h.Finalization == nil || h.Finalization.DeliveryLevel != "ready_for_use" || h.Finalization.RevisionHash != r.Hash || h.Finalization.ReportHash != r.Report.Hash || ds.Ready(r) != nil) {
		return nil, ds.ErrRules
	}
	manifest := PackageManifest{Schema: 1, Kind: req.Kind, SetID: id, Revision: r.ID, RevisionHash: r.Hash, Status: "complete", Members: []PackageMember{}}
	files := map[string][]byte{}
	total := 0
	add := func(name string, raw []byte) error {
		if len(files) >= MaxPackageFiles || total+len(raw) > MaxPackageBytes {
			return ds.ErrBudget
		}
		if _, ok := files[name]; ok {
			return nil
		}
		total += len(raw)
		files[name] = raw
		return nil
	}
	read := func(name string) error {
		path, e := objectlocation.SafePath(s.Root, name)
		if e != nil {
			return e
		}
		raw, e := objectlocation.ReadFileBounded(path, MaxPackageBytes)
		if e != nil {
			return e
		}
		return add(name, raw)
	}
	if req.Kind == "backup" {
		paths, e := s.ContentPaths(id)
		if e != nil {
			return nil, e
		}
		locations := map[string]models.ObjectLocation{}
		for _, path := range paths {
			if e = read(path); e != nil {
				return nil, e
			}
		}
		revisions, e := s.Revisions(id)
		if e != nil {
			return nil, e
		}
		for _, v := range revisions {
			for _, a := range v.Members {
				l, e := registry.Lookup(a.DocumentID)
				if e != nil {
					return nil, e
				}
				locations[a.DocumentID] = l
			}
		}
		owned, e := registry.ListOwned(context.Background(), id)
		if e != nil {
			return nil, e
		}
		for _, l := range owned {
			locations[l.ObjectID] = l
		}
		for _, l := range locations {
			paths, e := documentstore.New(s.Root).ContentPaths(l.ObjectID)
			if os.IsNotExist(e) {
				// A plain note without a journal is valid. A damaged typed
				// journal must never masquerade as a plain note in a backup.
				if _, err := documentstore.New(s.Root).Head(l.ObjectID); err == nil {
					return nil, e
				}
				p, err := objectlocation.SafePath(s.Root, l.Placement.DocumentRoot)
				if err != nil {
					return nil, err
				}
				entries, err := os.ReadDir(p)
				if err != nil && !os.IsNotExist(err) {
					return nil, err
				}
				if len(entries) > 0 {
					return nil, e
				}
				paths = nil
			} else if e != nil {
				return nil, e
			}
			paths = append(paths, l.Placement.ThoughtPath, objectlocation.RecordPath(l.ObjectID))
			if l.ResourceBindingsRef != nil {
				paths = append(paths, l.ResourceBindingsRef.Path)
			}
			for _, path := range paths {
				if e = read(path); e != nil {
					return nil, e
				}
			}
			bindings, e := registry.Bindings(l.ObjectID)
			if e != nil {
				return nil, e
			}
			for _, binding := range bindings.Bindings {
				if binding.Private {
					return nil, ds.ErrRules
				}
				for _, path := range []string{binding.Path, ".content-registry/resources/" + binding.ResourceID + ".json"} {
					if e = read(path); e != nil {
						return nil, e
					}
				}
			}
		}
		// Snapshots are owned immutable references, not editable copies of an
		// external thought. Preserve their exact selected bytes and assets.
		refRoot := "topics/" + id + "/references"
		absolute, e := objectlocation.SafePath(s.Root, refRoot)
		if e != nil {
			return nil, e
		}
		e = filepath.WalkDir(absolute, func(p string, d os.DirEntry, err error) error {
			if os.IsNotExist(err) {
				return nil
			}
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			if d.Type()&os.ModeSymlink != 0 {
				return ds.ErrInvalid
			}
			rel, err := filepath.Rel(s.Root, p)
			if err != nil {
				return err
			}
			return read(filepath.ToSlash(rel))
		})
		if e != nil {
			return nil, e
		}
	} else {
		names := map[string]string{}
		for _, m := range r.Plan.Members {
			names[m.Key] = m.Filename
		}
		index := "# Document set\n\nVersion: " + r.ID + "\n\n"
		md := goldmark.New(goldmark.WithExtensions(extension.GFM), goldmark.WithParserOptions(parser.WithAutoHeadingID()))
		for _, a := range r.Members {
			body := a.Body
			for _, link := range ds.Links(body) {
				u, e := url.Parse(link)
				if e != nil {
					return nil, e
				}
				if u.Scheme == "docset" && u.Host == id {
					key, _ := url.PathUnescape(strings.TrimPrefix(u.Path, "/"))
					filename, ok := names[key]
					if !ok {
						return nil, ds.ErrRules
					}
					replacement := filename
					if u.Fragment != "" {
						replacement += "#" + u.Fragment
					}
					body = strings.ReplaceAll(body, link, replacement)
				} else if u.Scheme == "thought" || u.Scheme == "topic" {
					manifest.Status = "partial"
				}
			}
			resources := a.Resources
			for _, issue := range objectlocation.OfflineResourceIssues(a.Body, resources.Bindings) {
				u, _ := url.Parse(issue.Locator)
				if u != nil && u.Scheme == "docset" {
					continue
				}
				manifest.Status = "partial"
			}
			for _, binding := range resources.Bindings {
				if binding.Private {
					continue
				}
				resourcePath, e := registry.FrozenResourcePath(a.DocumentID, binding)
				if e != nil {
					return nil, e
				}
				raw, e := objectlocation.ReadFileBounded(resourcePath, objectlocation.MaxResourceBytes)
				if e != nil || objectlocation.Hash(raw) != binding.Hash {
					return nil, ds.ErrRules
				}
				target := "assets/" + binding.ResourceID + "/" + path.Base(binding.Path)
				if e = add(target, raw); e != nil {
					return nil, e
				}
				body = strings.ReplaceAll(body, "resource://"+binding.ResourceID, target)
			}
			name := names[a.Key]
			var rendered bytes.Buffer
			if e = md.Convert([]byte(body), &rendered, parser.WithContext(parser.NewContext(parser.WithIDs(headingIDs{})))); e != nil {
				return nil, e
			}
			htmlBody := rendered.String()
			for _, filename := range names {
				htmlBody = strings.ReplaceAll(htmlBody, `href="`+filename, `href="`+strings.TrimSuffix(filename, ".md")+".html")
			}
			page := []byte(`<!doctype html><meta charset="utf-8"><meta name="viewport" content="width=device-width"><meta http-equiv="Content-Security-Policy" content="default-src 'none'; img-src 'self'; style-src 'unsafe-inline'"><title>` + html.EscapeString(a.Contract.Title) + `</title><style>body{max-width:960px;margin:2em auto;padding:1em;line-height:1.7}pre{overflow:auto}table{border-collapse:collapse}td,th{border:1px solid #aaa;padding:.4em}</style>` + htmlBody)
			if e = add(name, []byte(body)); e != nil {
				return nil, e
			}
			if e = add(strings.TrimSuffix(name, ".md")+".html", page); e != nil {
				return nil, e
			}
			manifest.Members = append(manifest.Members, PackageMember{Key: a.Key, Revision: a.RevisionID, BodyHash: a.BodyHash, MarkdownHash: documentstore.Hash([]byte(body)), HTMLHash: documentstore.Hash(page), Filename: name})
			index += "- [" + a.Contract.Title + "](" + name + ")\n"
		}
		if req.Kind == "diagnostic" {
			manifest.Status = "incomplete"
		}
		if req.Kind == "delivery" && manifest.Status != "complete" {
			return nil, ds.ErrRules
		}
		if e = add("index.md", []byte(index)); e != nil {
			return nil, e
		}
		review, _ := json.MarshalIndent(struct {
			Revision string    `json:"set_revision_id"`
			Report   ds.Report `json:"validation"`
		}{r.ID, r.Report}, "", "  ")
		if e = add("review.json", review); e != nil {
			return nil, e
		}
	}
	return zipFiles(files, manifest)
}
func zipFiles(files map[string][]byte, manifest PackageManifest) ([]byte, error) {
	names := []string{}
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		raw := files[name]
		manifest.Files = append(manifest.Files, PackageFile{Path: name, Hash: documentstore.Hash(raw), Size: len(raw)})
	}
	raw, e := json.MarshalIndent(manifest, "", "  ")
	if e != nil {
		return nil, e
	}
	files["package.json"] = raw
	names = append(names, "package.json")
	var b bytes.Buffer
	writer := zip.NewWriter(&b)
	for _, name := range names {
		f, e := writer.Create(name)
		if e != nil {
			return nil, e
		}
		if _, e = f.Write(files[name]); e != nil {
			return nil, e
		}
	}
	if e = writer.Close(); e != nil {
		return nil, e
	}
	if b.Len() > MaxPackageBytes {
		return nil, ds.ErrBudget
	}
	return b.Bytes(), nil
}
func (s *Store) Revisions(id string) ([]ds.Revision, error) {
	h, e := s.Head(id)
	if e != nil {
		return nil, e
	}
	out := []ds.Revision{}
	seen := map[string]bool{}
	for _, start := range retainedRoots(h) {
		for rev := start; rev != "" && !seen[rev]; {
			seen[rev] = true
			r, e := s.Read(id, rev)
			if e != nil {
				return nil, e
			}
			out = append(out, r)
			rev = r.Parent
		}
	}
	return out, nil
}

type ImportState struct {
	OperationID string        `json:"operation_id"`
	RequestHash string        `json:"request_hash"`
	SetID       string        `json:"set_id"`
	State       string        `json:"state"`
	Error       string        `json:"error,omitempty"`
	Files       []PackageFile `json:"files"`
	Head        ds.Head       `json:"head"`
}

func (s *Store) Import(raw []byte, operation string) (ImportState, error) {
	var state ImportState
	if !documentstore.ValidID(operation) || len(raw) > MaxPackageBytes {
		return state, ds.ErrInvalid
	}
	release := objectlocation.For(s.Root).LockObject("set-import-" + operation)
	defer release()
	path := ".content-registry/private/document-set-imports/" + operation
	if e := objectlocation.ReadJSON(s.Root, path+"/state.json", &state); e == nil {
		if state.RequestHash != documentstore.Hash(raw) {
			return state, ds.ErrConflict
		}
		return s.resumeImport(state)
	} else if !os.IsNotExist(e) {
		return state, e
	}
	reader, e := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if e != nil || len(reader.File) > MaxPackageFiles {
		return state, ds.ErrInvalid
	}
	files := map[string][]byte{}
	total := 0
	for _, f := range reader.File {
		if !f.Mode().IsRegular() || f.UncompressedSize64 > MaxPackageBytes || files[f.Name] != nil {
			return state, ds.ErrInvalid
		}
		if _, e = objectlocation.SafePath(s.Root, f.Name); e != nil {
			return state, e
		}
		r, e := f.Open()
		if e != nil {
			return state, e
		}
		data, e := io.ReadAll(io.LimitReader(r, MaxPackageBytes+1))
		r.Close()
		total += len(data)
		if e != nil || total > MaxPackageBytes {
			return state, ds.ErrBudget
		}
		files[f.Name] = data
	}
	var manifest PackageManifest
	if e = ds.StrictJSON(files["package.json"], &manifest); e != nil || manifest.Schema != 1 || manifest.Kind != "backup" || !documentstore.ValidID(manifest.SetID) || len(files) != len(manifest.Files)+1 {
		return state, ds.ErrInvalid
	}
	if len(manifest.Files) == 0 {
		return state, ds.ErrInvalid
	}
	objects, err := validateImportObjects(files, manifest.SetID)
	if err != nil {
		return state, err
	}
	seen := map[string]bool{}
	staging := path + "/staging"
	for _, f := range manifest.Files {
		if seen[f.Path] || documentstore.Hash(files[f.Path]) != f.Hash || len(files[f.Path]) != f.Size {
			return state, ds.ErrInvalid
		}
		seen[f.Path] = true
		if !allowedPath(f.Path, manifest.SetID, objects) {
			return state, ds.ErrInvalid
		}
		if e = objectlocation.WriteFile(s.Root, staging+"/"+f.Path, files[f.Path], 0600); e != nil {
			return state, e
		}
	}
	stageRoot, e := objectlocation.SafePath(s.Root, staging)
	if e != nil {
		return state, e
	}
	staged := New(stageRoot)
	if e = validateStagedObjects(stageRoot, manifest.SetID, files, objects); e != nil {
		return state, e
	}
	var h ds.Head
	if e = objectlocation.ReadJSON(stageRoot, Base(manifest.SetID)+"/head.json", &h); e != nil {
		return state, e
	}
	// Validate complete retained closure in isolation before reserving targets.
	revisions, e := staged.Revisions(manifest.SetID)
	if e != nil {
		return state, e
	}
	if len(revisions) == 0 {
		return state, ds.ErrInvalid
	}
	for _, r := range revisions {
		if ds.Ready(r) != nil {
			return state, ds.ErrRules
		}
	}
	registry := objectlocation.For(s.Root)
	registry.Namespace.Lock()
	defer registry.Namespace.Unlock()
	if _, e = registry.Topic(manifest.SetID); e == nil {
		return state, ds.ErrConflict
	} else if !os.IsNotExist(e) {
		return state, e
	}
	for _, f := range manifest.Files {
		if f.Path == ".content-registry/schema.json" {
			continue
		}
		p, e := objectlocation.SafePath(s.Root, f.Path)
		if e != nil {
			return state, e
		}
		if _, e = os.Stat(p); e == nil {
			return state, ds.ErrConflict
		} else if !os.IsNotExist(e) {
			return state, e
		}
	}
	state = ImportState{OperationID: operation, RequestHash: documentstore.Hash(raw), SetID: manifest.SetID, State: "validated", Files: manifest.Files, Head: h}
	if e = objectlocation.WriteJSON(s.Root, path+"/state.json", state, true); e != nil {
		return state, e
	}
	if e = s.checkpoint("import_validated"); e != nil {
		return state, e
	}
	return s.resumeImportLocked(state)
}
func (s *Store) resumeImport(state ImportState) (ImportState, error) {
	r := objectlocation.For(s.Root)
	r.Namespace.Lock()
	defer r.Namespace.Unlock()
	return s.resumeImportLocked(state)
}
func (s *Store) resumeImportLocked(state ImportState) (ImportState, error) {
	if state.State == "completed" {
		return state, nil
	}
	if state.State == "conflicted" {
		return state, ds.ErrConflict
	}
	base := ".content-registry/private/document-set-imports/" + state.OperationID
	// Preflight every destination on EVERY resume, before writing any bytes.
	for _, file := range state.Files {
		if file.Path == ".content-registry/schema.json" {
			continue
		}
		path, e := objectlocation.SafePath(s.Root, file.Path)
		if e != nil {
			return state, e
		}
		raw, e := objectlocation.ReadFileBounded(path, MaxPackageBytes)
		if e == nil && documentstore.Hash(raw) != file.Hash {
			state.State = "conflicted"
			state.Error = "Destination occupied by different content: " + file.Path
			if e = objectlocation.WriteJSON(s.Root, base+"/state.json", state, true); e != nil {
				return state, e
			}
			return state, ds.ErrConflict
		}
		if e != nil && !os.IsNotExist(e) {
			return state, e
		}
	}
	// Object locations remain gated until the one imported HEAD is published.
	if e := objectlocation.For(s.Root).RequireSetWriter(); e != nil {
		return state, e
	}
	if e := objectlocation.WriteJSON(s.Root, ".content-registry/private/document-set-activations/"+state.SetID+".json", map[string]string{"operation_id": state.OperationID}, true); e != nil {
		return state, e
	}
	for _, file := range state.Files {
		if file.Path == ".content-registry/schema.json" || file.Path == Base(state.SetID)+"/head.json" {
			continue
		}
		p, e := objectlocation.SafePath(s.Root, base+"/staging/"+file.Path)
		if e != nil {
			return state, e
		}
		raw, e := objectlocation.ReadFileBounded(p, MaxPackageBytes)
		if e != nil || documentstore.Hash(raw) != file.Hash {
			return state, ds.ErrInvalid
		}
		if e = objectlocation.WriteFile(s.Root, file.Path, raw, 0644); e != nil {
			return state, e
		}
	}
	if e := s.checkpoint("import_prepared"); e != nil {
		return state, e
	}
	if e := objectlocation.For(s.Root).RequireSetWriter(); e != nil {
		return state, e
	}
	if e := objectlocation.WriteJSON(s.Root, Base(state.SetID)+"/head.json", state.Head, false); e != nil {
		return state, e
	}
	state.State = "completed"
	if e := objectlocation.WriteJSON(s.Root, base+"/state.json", state, true); e != nil {
		return state, e
	}
	return state, nil
}
func (s *Store) RecoverImports() []error {
	path, e := objectlocation.SafePath(s.Root, ".content-registry/private/document-set-imports")
	if e != nil {
		return []error{e}
	}
	entries, e := os.ReadDir(path)
	if os.IsNotExist(e) {
		return nil
	}
	if e != nil {
		return []error{e}
	}
	issues := []error{}
	for _, entry := range entries {
		if !entry.IsDir() || !documentstore.ValidID(entry.Name()) {
			continue
		}
		var state ImportState
		if e = objectlocation.ReadJSON(s.Root, ".content-registry/private/document-set-imports/"+entry.Name()+"/state.json", &state); e != nil {
			issues = append(issues, e)
			continue
		}
		if state.State == "completed" || state.State == "conflicted" {
			continue
		}
		updated, e := s.resumeImport(state)
		if e != nil && updated.State != "conflicted" {
			issues = append(issues, e)
		}
	}
	return issues
}

var _ = fmt.Sprint
