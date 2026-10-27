package topicstore

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/yuin/goldmark"
	"html"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"thoughtflow/internal/pkg/documentsetstore"
	"thoughtflow/internal/pkg/documentstore"
	"thoughtflow/internal/pkg/markdown"
	"thoughtflow/internal/pkg/models"
	"thoughtflow/internal/pkg/objectlocation"
)

const MaxPackageBytes = 512 * 1024 * 1024
const MaxPackageFiles = 5000

type ExportRequest struct {
	OperationID    string `json:"operation_id"`
	Kind           string `json:"export_kind"`
	IncludePrivate bool   `json:"include_private_evidence"`
}
type PackageFile struct {
	Path    string `json:"path"`
	Hash    string `json:"hash"`
	Size    int64  `json:"size"`
	Private bool   `json:"private,omitempty"`
}
type PackageRelation struct {
	EntryID   string `json:"entry_id"`
	Kind      string `json:"kind"`
	ThoughtID string `json:"thought_id,omitempty"`
}
type PackageManifest struct {
	OfflineStatus     string                        `json:"offline_status"`
	ResourceIssues    []models.OfflineResourceIssue `json:"resource_issues,omitempty"`
	Relations         []PackageRelation             `json:"relations"`
	SchemaVersion     int                           `json:"schema_version"`
	Kind              string                        `json:"kind"`
	TopicID           string                        `json:"topic_id"`
	TopicGeneration   uint64                        `json:"topic_generation"`
	CatalogGeneration uint64                        `json:"catalog_generation"`
	FrozenAt          time.Time                     `json:"frozen_at"`
	Private           bool                          `json:"contains_private"`
	EvidenceReview    string                        `json:"evidence_review"`
	Files             []PackageFile                 `json:"files"`
	Objects           []models.ObjectLocation       `json:"objects"`
	Selections        []models.CatalogEntryView     `json:"selections,omitempty"`
}
type ExportResult struct {
	ID          string          `json:"id"`
	State       string          `json:"state"`
	Kind        string          `json:"export_kind"`
	Hash        string          `json:"hash"`
	Manifest    PackageManifest `json:"manifest"`
	DownloadURL string          `json:"download_url"`
}

func exportBase(id string) string { return ".content-registry/private/exports/" + id }

// ExportPackage freezes all mutable files under the namespace write lease.
// Remote model work is never part of this critical section. Selection resolves
// latest once; subsequent packaging uses only immutable snapshot bytes.
func (s *Store) ExportPackage(ctx context.Context, id string, req ExportRequest) (ExportResult, error) {
	var result ExportResult
	if req.Kind != "delivery" && req.Kind != "backup" {
		return result, objectlocation.ErrInvalid
	}
	if req.Kind == "delivery" && req.IncludePrivate {
		return result, objectlocation.ErrInvalid
	}
	if !objectlocation.ValidID(req.OperationID) {
		return result, objectlocation.ErrInvalid
	}
	setStore := documentsetstore.New(s.rootPath)
	if _, e := setStore.Head(id); e == nil && req.Kind == "delivery" {
		return result, fmt.Errorf("%w: a managed set requires a fixed set revision and joint delivery gate; use document-set exports", objectlocation.ErrInvalid)
	} else if e != nil && !os.IsNotExist(e) {
		return result, e
	}
	r := objectlocation.For(s.rootPath)
	// Resolve the dynamic view before the write lease (it acquires object leases),
	// then validate its epoch under the lease before freezing any selection.
	epoch := r.Epoch()
	view, e := s.CatalogView(ctx, id)
	if e != nil {
		return result, e
	}
	r.Namespace.Lock()
	defer r.Namespace.Unlock()
	if epoch != r.Epoch() {
		return result, objectlocation.ErrConflict
	}
	unlock := s.lock(id)
	defer unlock()
	op, e := r.Begin(req.OperationID, "export", "", id, req)
	if e != nil {
		return result, e
	}
	if op.State == "completed" {
		e = objectlocation.ReadJSON(s.rootPath, exportBase(op.ID)+".json", &result)
		return result, e
	}
	manifest := PackageManifest{OfflineStatus: "complete", SchemaVersion: 1, Kind: req.Kind, TopicID: id, TopicGeneration: view.Topic.Generation, CatalogGeneration: view.Catalog.Generation, FrozenAt: time.Now().UTC(), Private: req.IncludePrivate, EvidenceReview: "Private evidence is excluded; offline evidence review is not complete.", Files: []PackageFile{}, Objects: []models.ObjectLocation{}, Selections: view.Entries}
	if req.Kind == "backup" {
		manifest.OfflineStatus = "not_evaluated"
	}
	if req.IncludePrivate {
		manifest.EvidenceReview = "Private evidence explicitly included; handle this backup as private."
	}
	for _, v := range view.Entries {
		kind := v.Entry.Binding.Kind
		if kind == "thought" {
			if v.BindingStatus == "missing" || v.Location == nil {
				kind = "missing"
			} else if v.Location.OwnerTopicID == id {
				kind = "owned"
			} else {
				kind = "external_reference"
			}
		}
		manifest.Relations = append(manifest.Relations, PackageRelation{EntryID: v.Entry.ID, Kind: kind, ThoughtID: v.Entry.Binding.ThoughtID})
	}
	files := map[string][]byte{}
	total := int64(0)
	add := func(path string, raw []byte, private bool) error {
		if !validPackagePath(path) {
			return objectlocation.ErrUnsafe
		}
		if _, ok := files[path]; ok {
			return nil
		}
		if len(files) >= MaxPackageFiles || total+int64(len(raw)) > MaxPackageBytes {
			return objectlocation.ErrInvalid
		}
		files[path] = append([]byte{}, raw...)
		total += int64(len(raw))
		manifest.Files = append(manifest.Files, PackageFile{path, objectlocation.Hash(raw), int64(len(raw)), private})
		return nil
	}
	read := func(path string, private bool) error {
		p, e := objectlocation.SafePath(s.rootPath, path)
		if e != nil {
			return e
		}
		info, e := os.Stat(p)
		if e != nil {
			return e
		}
		if !info.Mode().IsRegular() || info.Size() > objectlocation.MaxResourceBytes {
			return objectlocation.ErrUnsafe
		}
		raw, e := os.ReadFile(p)
		if e != nil {
			return e
		}
		return add(path, raw, private)
	}
	if req.Kind == "backup" {
		paths, e := s.TopicPaths(ctx, id)
		if e != nil {
			return result, e
		}
		if _, e := setStore.Head(id); e == nil {
			closure, err := setStore.ContentPaths(id)
			if err != nil {
				return result, err
			}
			for _, p := range closure {
				if p != ".content-registry/schema.json" {
					paths = append(paths, p)
				}
			}
		} else if !os.IsNotExist(e) {
			return result, e
		}
		for _, path := range paths {
			if e = read(path, false); e != nil {
				if strings.HasSuffix(path, "catalog.md") && os.IsNotExist(e) {
					continue
				}
				return result, e
			}
		}
		owned, e := r.ListOwned(ctx, id)
		if e != nil {
			return result, e
		}
		ds := documentstore.New(s.rootPath)
		for _, l := range owned {
			manifest.Objects = append(manifest.Objects, l)
			paths := []string{l.Placement.ThoughtPath, objectlocation.RecordPath(l.ObjectID)}
			if l.ResourceBindingsRef != nil {
				paths = append(paths, l.ResourceBindingsRef.Path)
			}
			var journal []string
			if _, err := ds.Head(l.ObjectID); err == nil {
				journal, e = ds.ContentPaths(l.ObjectID)
				if e != nil {
					return result, e
				}
			} else if !os.IsNotExist(err) {
				return result, err
			} else {
				projection, err := objectlocation.SafePath(s.rootPath, l.Placement.ThoughtPath)
				if err != nil {
					return result, err
				}
				raw, err := objectlocation.ReadFileBounded(projection, objectlocation.MaxResourceBytes)
				if err != nil {
					return result, err
				}
				note, _ := markdown.ParseThought(raw)
				if note.DocumentProfile != nil {
					return result, objectlocation.ErrRecovery
				}
				// A remaining journal without HEAD is damaged formal history,
				// even if an external edit also removed projection metadata.
				journalRoot, err := objectlocation.SafePath(s.rootPath, l.Placement.DocumentRoot)
				if err != nil {
					return result, err
				}
				entries, err := os.ReadDir(journalRoot)
				if err != nil && !os.IsNotExist(err) {
					return result, err
				}
				if len(entries) > 0 {
					return result, objectlocation.ErrRecovery
				}
			}
			paths = append(paths, journal...)
			for _, path := range paths {
				if path == ".content-registry/schema.json" {
					continue
				}
				if e = read(path, false); e != nil {
					return result, e
				}
			}
			bindings, e := r.Bindings(l.ObjectID)
			if e != nil {
				return result, e
			}
			for _, binding := range bindings.Bindings {
				if binding.Private {
					continue
				}
				for _, path := range []string{binding.Path, ".content-registry/resources/" + binding.ResourceID + ".json"} {
					if e = read(path, false); e != nil {
						return result, e
					}
				}
			}
			if req.IncludePrivate && l.Placement.EvidenceRoot != "" {
				keys, e := evidenceFiles(ds, l.ObjectID)
				if e != nil {
					return result, e
				}
				for _, key := range keys {
					if e = read(l.Placement.EvidenceRoot+"/"+key+".txt", true); e != nil {
						return result, e
					}
				}
			}
		}
		// Snapshots are explicit immutable references, never a recursive directory walk.
		for _, entry := range view.Catalog.Entries {
			if entry.Binding.Kind != "snapshot" {
				continue
			}
			base := "topics/" + id + "/references/" + entry.Binding.SnapshotID
			var snap models.TopicSnapshot
			if e = objectlocation.ReadJSON(s.rootPath, base+"/snapshot.json", &snap); e != nil {
				return result, e
			}
			if snap.ID != entry.Binding.SnapshotID || snap.Private {
				return result, objectlocation.ErrUnsafe
			}
			for _, b := range snap.Resources {
				if b.Private || !strings.HasPrefix(b.Path, base+"/assets/") {
					return result, objectlocation.ErrUnsafe
				}
				if e = read(b.Path, false); e != nil {
					return result, e
				}
				if objectlocation.Hash(files[b.Path]) != b.Hash {
					return result, objectlocation.ErrRecovery
				}
			}
			for _, name := range []string{"snapshot.json", "body.md"} {
				if e = read(base+"/"+name, false); e != nil {
					return result, e
				}
			}
		}
		storage, _ := json.MarshalIndent(manifest.Objects, "", "  ")
		if e = add("topics/"+id+"/storage.snapshot.json", storage, false); e != nil {
			return result, e
		}
	} else {
		indexHTML := strings.Builder{}
		fmt.Fprintf(&indexHTML, "<h1>%s</h1><p>%s</p>", html.EscapeString(view.Topic.Name), html.EscapeString(view.Topic.Description))
		index := strings.Builder{}
		fmt.Fprintf(&index, "# %s\n\n%s\n\n", view.Topic.Name, view.Topic.Description)
		for _, g := range view.Catalog.Groups {
			fmt.Fprintf(&index, "## %s\n\n", g.Title)
			fmt.Fprintf(&indexHTML, "<h2>%s</h2><ul>", html.EscapeString(g.Title))
			for _, v := range view.Entries {
				if v.Entry.GroupID != g.ID {
					continue
				}
				entry := v.Entry
				if entry.Binding.Kind == "planned" || v.SelectedRevisionID == "" {
					if entry.Required {
						return result, ErrRevisionUnavailable
					}
					fmt.Fprintf(&index, "- %s (unavailable)\n", entry.Title)
					continue
				}
				if v.Availability != "available" {
					return result, ErrRevisionUnavailable
				}
				var body string
				var bindings []models.ResourceBinding
				if entry.Binding.Kind == "snapshot" {
					var snap models.TopicSnapshot
					base := "topics/" + id + "/references/" + entry.Binding.SnapshotID
					if e = objectlocation.ReadJSON(s.rootPath, base+"/snapshot.json", &snap); e != nil {
						return result, e
					}
					path, e := objectlocation.SafePath(s.rootPath, base+"/body.md")
					if e != nil {
						return result, e
					}
					raw, e := objectlocation.ReadFileBounded(path, objectlocation.MaxResourceBytes)
					if e != nil || snap.ID != entry.Binding.SnapshotID || snap.Private || objectlocation.Hash(raw) != snap.BodyHash {
						return result, objectlocation.ErrRecovery
					}
					body = string(raw)
					bindings = snap.Resources
				} else {
					d, err := dsRevision(s.rootPath, entry.Binding.ThoughtID, v.SelectedRevisionID)
					if err != nil {
						return result, err
					}
					body = d.Body
					bindings = d.Resources.Bindings
				}
				issues := objectlocation.OfflineResourceIssues(body, bindings)
				for _, issue := range issues {
					issue.EntryID = entry.ID
					manifest.ResourceIssues = append(manifest.ResourceIssues, issue)
				}
				if len(issues) > 0 {
					manifest.OfflineStatus = "partial"
				}
				for _, binding := range bindings {
					if binding.Private {
						return result, objectlocation.ErrUnsafe
					}
					var path string
					if entry.Binding.Kind == "snapshot" {
						base := "topics/" + id + "/references/" + entry.Binding.SnapshotID + "/assets/"
						if !strings.HasPrefix(binding.Path, base) {
							return result, objectlocation.ErrUnsafe
						}
						path, e = objectlocation.SafePath(s.rootPath, binding.Path)
					} else {
						path, e = r.BoundResourcePath(entry.Binding.ThoughtID, binding)
					}
					if e != nil {
						return result, e
					}
					raw, err := objectlocation.ReadFileBounded(path, objectlocation.MaxResourceBytes)
					if err != nil || objectlocation.Hash(raw) != binding.Hash {
						return result, objectlocation.ErrRecovery
					}
					if _, err = objectlocation.ValidateResource(filepath.Base(binding.Path), raw); err != nil {
						return result, err
					}
					asset := "assets/" + binding.ResourceID + filepath.Ext(binding.Path)
					if e = add(asset, raw, false); e != nil {
						return result, e
					}
					body = strings.ReplaceAll(body, "resource://"+binding.ResourceID, "../"+asset)
					if binding.OriginalLocator != "" {
						body = strings.ReplaceAll(body, binding.OriginalLocator, "../"+asset)
					}
				}
				path := "documents/" + entry.ID + ".md"
				if e = add(path, []byte(body), false); e != nil {
					return result, e
				}
				var rendered bytes.Buffer
				if e = goldmark.Convert([]byte(body), &rendered); e != nil {
					return result, e
				}
				htmlPath := "documents/" + entry.ID + ".html"
				renderedBody := rendered.String()
				if len(issues) > 0 {
					renderedBody = "<p role=alert>Offline resources incomplete; consult manifest.json for external or missing assets.</p>" + renderedBody
				}
				if e = add(htmlPath, offlineHTML(entry.Title, renderedBody), false); e != nil {
					return result, e
				}
				fmt.Fprintf(&indexHTML, "<li><a href=%q>%s</a></li>", htmlPath, html.EscapeString(entry.Title))
				fmt.Fprintf(&index, "- [%s](%s)\n", entry.Title, path)
			}
			indexHTML.WriteString("</ul>")
		}
		if e = add("index.html", offlineHTML(view.Topic.Name, indexHTML.String()), false); e != nil {
			return result, e
		}
		if e = add("index.md", []byte(index.String()), false); e != nil {
			return result, e
		}
	}
	sort.Slice(manifest.Files, func(i, j int) bool { return manifest.Files[i].Path < manifest.Files[j].Path })
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	manifestRaw, _ := json.MarshalIndent(manifest, "", "  ")
	writer, e := zw.CreateHeader(&zip.FileHeader{Name: "manifest.json", Method: zip.Store})
	if e != nil {
		return result, e
	}
	if _, e = writer.Write(manifestRaw); e != nil {
		return result, e
	}
	for _, file := range manifest.Files {
		writer, e = zw.CreateHeader(&zip.FileHeader{Name: file.Path, Method: zip.Store})
		if e != nil {
			return result, e
		}
		if _, e = writer.Write(files[file.Path]); e != nil {
			return result, e
		}
	}
	if e = zw.Close(); e != nil {
		return result, e
	}
	if buf.Len() > MaxPackageBytes {
		return result, objectlocation.ErrInvalid
	}
	result = ExportResult{ID: op.ID, State: "completed", Kind: req.Kind, Hash: objectlocation.Hash(buf.Bytes()), Manifest: manifest, DownloadURL: "/api/topics/" + id + "/exports/" + op.ID + "?download=1"}
	if e = objectlocation.WriteFile(s.rootPath, exportBase(op.ID)+".zip", buf.Bytes(), 0600); e != nil {
		return result, e
	}
	if e = objectlocation.WriteJSON(s.rootPath, exportBase(op.ID)+".json", result, true); e != nil {
		return result, e
	}
	return result, r.Complete(op)
}
func dsRevision(root, id, rev string) (documentstore.RevisionData, error) {
	d, e := documentstore.New(root).Revision(id, rev)
	if e == nil && d.ResourceError != "" {
		return d, objectlocation.ErrRecovery
	}
	return d, e
}
func evidenceFiles(ds *documentstore.Store, id string) ([]string, error) {
	h, e := ds.Head(id)
	if e != nil {
		if os.IsNotExist(e) {
			return nil, nil
		}
		return nil, e
	}
	seen := map[string]bool{}
	keys := map[string]bool{}
	for _, start := range []string{h.WorkingRevisionID, h.FinalizedRevisionID} {
		for rev := start; rev != "" && !seen[rev]; {
			seen[rev] = true
			d, e := ds.Revision(id, rev)
			if e != nil {
				return nil, e
			}
			for _, source := range d.Sources.Sources {
				if source.EvidenceHash != "" {
					key := strings.TrimPrefix(source.EvidenceHash, "sha256:")
					if len(key) != 64 || !objectlocation.ValidID(key) {
						return nil, objectlocation.ErrInvalid
					}
					keys[key] = true
				}
			}
			rev = d.Revision.ParentRevisionID
		}
	}
	out := []string{}
	for key := range keys {
		out = append(out, key)
	}
	sort.Strings(out)
	return out, nil
}
func validPackagePath(path string) bool {
	if path == "" || strings.ContainsAny(path, "\\\x00") || filepath.IsAbs(path) || filepath.ToSlash(filepath.Clean(path)) != path || strings.HasPrefix(path, "../") || strings.Count(path, "/") > 16 {
		return false
	}
	for _, part := range strings.Split(path, "/") {
		if part == "." || part == ".." || part == "" {
			return false
		}
	}
	return true
}
func (s *Store) ExportResult(topic, id string) (ExportResult, error) {
	var result ExportResult
	if !objectlocation.ValidID(id) {
		return result, objectlocation.ErrInvalid
	}
	e := objectlocation.ReadJSON(s.rootPath, exportBase(id)+".json", &result)
	if e == nil && result.Manifest.TopicID != topic {
		return ExportResult{}, os.ErrNotExist
	}
	return result, e
}
func (s *Store) ExportBytes(topic, id string) ([]byte, error) {
	result, e := s.ExportResult(topic, id)
	if e != nil {
		return nil, e
	}
	path, e := objectlocation.SafePath(s.rootPath, exportBase(id)+".zip")
	if e != nil {
		return nil, e
	}
	raw, e := os.ReadFile(path)
	if e == nil && objectlocation.Hash(raw) != result.Hash {
		return nil, objectlocation.ErrRecovery
	}
	return raw, e
}

// ReadPackage bounds both declared and actual sizes and rejects links, duplicate
// names, extra entries and mismatched content before any live path is written.
func ReadPackage(raw []byte) (PackageManifest, map[string][]byte, error) {
	var manifest PackageManifest
	files := map[string][]byte{}
	if len(raw) > MaxPackageBytes {
		return manifest, nil, objectlocation.ErrInvalid
	}
	zr, e := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if e != nil {
		return manifest, nil, objectlocation.ErrInvalid
	}
	if len(zr.File) > MaxPackageFiles+1 {
		return manifest, nil, objectlocation.ErrInvalid
	}
	total := uint64(0)
	for _, f := range zr.File {
		if !validPackagePath(f.Name) || !f.Mode().IsRegular() || f.UncompressedSize64 > MaxPackageBytes || f.CompressedSize64 == 0 && f.UncompressedSize64 > 0 || f.CompressedSize64 > 0 && f.UncompressedSize64/f.CompressedSize64 > 100 {
			return manifest, nil, objectlocation.ErrUnsafe
		}
		if _, exists := files[f.Name]; exists {
			return manifest, nil, objectlocation.ErrUnsafe
		}
		total += f.UncompressedSize64
		if total > MaxPackageBytes {
			return manifest, nil, objectlocation.ErrInvalid
		}
		reader, e := f.Open()
		if e != nil {
			return manifest, nil, e
		}
		data, e := io.ReadAll(io.LimitReader(reader, int64(f.UncompressedSize64)+1))
		reader.Close()
		if e != nil || uint64(len(data)) != f.UncompressedSize64 {
			return manifest, nil, objectlocation.ErrInvalid
		}
		files[f.Name] = data
	}
	if e = json.Unmarshal(files["manifest.json"], &manifest); e != nil || manifest.SchemaVersion != 1 || manifest.Kind != "backup" || !objectlocation.ValidID(manifest.TopicID) {
		return manifest, nil, objectlocation.ErrInvalid
	}
	delete(files, "manifest.json")
	if len(files) != len(manifest.Files) {
		return manifest, nil, objectlocation.ErrInvalid
	}
	seen := map[string]bool{}
	for _, f := range manifest.Files {
		data, ok := files[f.Path]
		if seen[f.Path] || !ok || int64(len(data)) != f.Size || objectlocation.Hash(data) != f.Hash {
			return manifest, nil, objectlocation.ErrInvalid
		}
		seen[f.Path] = true
	}
	return manifest, files, nil
}

func offlineHTML(title, body string) []byte {
	return []byte(`<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width"><meta http-equiv="Content-Security-Policy" content="default-src 'none'; img-src 'self'; style-src 'unsafe-inline'; base-uri 'none'; form-action 'none'"><title>` + html.EscapeString(title) + `</title><style>body{max-width:70rem;margin:2rem auto;padding:0 1rem;font:18px/1.6 system-ui}img{max-width:100%}pre{overflow:auto}a{overflow-wrap:anywhere}</style></head><body>` + body + `</body></html>`)
}
