// Package documentsetstore publishes an entire frozen set through one durable
// HEAD and keeps operation intents independently of the current HEAD.
package documentsetstore

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	ds "thoughtflow/internal/pkg/documentset"
	"thoughtflow/internal/pkg/documentstore"
	"thoughtflow/internal/pkg/markdown"
	"thoughtflow/internal/pkg/models"
	"thoughtflow/internal/pkg/objectlocation"
)

type Store struct {
	Root  string
	Fault func(string) error
}
type Intent struct {
	Kind           string   `json:"kind,omitempty"`
	FinalizationID string   `json:"finalization_id,omitempty"`
	OperationID    string   `json:"operation_id"`
	RequestHash    string   `json:"request_hash"`
	SetID          string   `json:"set_id"`
	RevisionID     string   `json:"revision_id"`
	RevisionHash   string   `json:"revision_hash"`
	Expected       uint64   `json:"expected_generation"`
	Committed      bool     `json:"committed"`
	Done           bool     `json:"done"`
	Error          string   `json:"error,omitempty"`
	Events         []string `json:"events"`
}

func New(root string) *Store   { return &Store{Root: root} }
func Base(id string) string    { return "topics/" + id + "/document-set" }
func Private(id string) string { return ".content-registry/private/document-sets/" + id }
func valid(ids ...string) error {
	for _, id := range ids {
		if !documentstore.ValidID(id) {
			return ds.ErrInvalid
		}
	}
	return nil
}
func (s *Store) checkpoint(p string) error {
	if s.Fault != nil {
		return s.Fault(p)
	}
	return nil
}
func (s *Store) Head(id string) (ds.Head, error) {
	var h ds.Head
	if e := valid(id); e != nil {
		return h, e
	}
	e := objectlocation.ReadJSON(s.Root, Base(id)+"/head.json", &h)
	if e == nil && (h.SchemaVersion != 1 || h.Generation == 0) {
		e = ds.ErrInvalid
	}
	if e == nil {
		seen := map[string]string{}
		for _, f := range h.Finalizations {
			if !documentstore.ValidID(f.ID) || !documentstore.ValidID(f.OperationID) || !documentstore.ValidID(f.RevisionID) || seen[f.ID] != "" || f.RevisionHash == "" || f.ReportHash == "" || strings.TrimSpace(f.ApprovedBy) == "" || f.At.IsZero() || (f.DeliveryLevel != "proposal" && f.DeliveryLevel != "ready_for_use") {
				return h, ds.ErrInvalid
			}
			seen[f.ID] = documentstore.HashJSON(f)
			var saved ds.Finalization
			if e = objectlocation.ReadJSON(s.Root, Base(id)+"/finalizations/"+f.ID+".json", &saved); e != nil {
				return h, e
			}
			if documentstore.HashJSON(saved) != documentstore.HashJSON(f) {
				return h, ds.ErrInvalid
			}
		}
		if (h.Finalized == "") != (h.Finalization == nil) {
			return h, ds.ErrInvalid
		}
		if h.Finalization != nil && (seen[h.Finalization.ID] != documentstore.HashJSON(h.Finalization) || h.Finalization.RevisionID != h.Finalized) {
			return h, ds.ErrInvalid
		}
	}
	return h, e
}
func (s *Store) HeadOrEmpty(id string) (ds.Head, error) {
	h, e := s.Head(id)
	if os.IsNotExist(e) {
		return ds.Head{SchemaVersion: 1}, nil
	}
	return h, e
}
func (s *Store) SavePlan(p ds.Plan) error {
	if e := ds.ValidatePlan(p); e != nil {
		return e
	}
	return s.immutable(Private(p.SetID)+"/plans/"+p.ID+".json", p, true)
}
func (s *Store) Plan(set, id string) (ds.Plan, error) {
	var p ds.Plan
	if e := valid(set, id); e != nil {
		return p, e
	}
	e := objectlocation.ReadJSON(s.Root, Private(set)+"/plans/"+id+".json", &p)
	if e == nil {
		e = ds.ValidatePlan(p)
	}
	return p, e
}
func (s *Store) SaveRun(r ds.Run) error {
	if e := valid(r.ID, r.OperationID, r.Plan.SetID); e != nil {
		return e
	}
	r.UpdatedAt = time.Now().UTC()
	return objectlocation.WriteJSON(s.Root, ".content-registry/private/document-set-runs/"+r.ID+".json", r, true)
}
func (s *Store) Run(id string) (ds.Run, error) {
	var r ds.Run
	if e := valid(id); e != nil {
		return r, e
	}
	e := objectlocation.ReadJSON(s.Root, ".content-registry/private/document-set-runs/"+id+".json", &r)
	if e == nil && (r.ID != id || r.Plan.SetID == "") {
		e = ds.ErrInvalid
	}
	return r, e
}
func (s *Store) Runs() ([]ds.Run, error) {
	dir, e := objectlocation.SafePath(s.Root, ".content-registry/private/document-set-runs")
	if e != nil {
		return nil, e
	}
	entries, e := os.ReadDir(dir)
	if os.IsNotExist(e) {
		return []ds.Run{}, nil
	}
	if e != nil {
		return nil, e
	}
	out := []ds.Run{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		r, e := s.Run(strings.TrimSuffix(entry.Name(), ".json"))
		if e != nil {
			out = append(out, ds.Run{ID: strings.TrimSuffix(entry.Name(), ".json"), State: "failed_terminal", Error: e.Error()})
			continue
		}
		out = append(out, r)
	}
	return out, nil
}
func (s *Store) immutable(path string, v any, private bool) error {
	target, e := objectlocation.SafePath(s.Root, path)
	if e != nil {
		return e
	}
	raw, e := json.MarshalIndent(v, "", "  ")
	if e != nil {
		return e
	}
	raw = append(raw, '\n')
	if existing, e := objectlocation.ReadFileBounded(target, 32<<20); e == nil {
		if string(existing) == string(raw) {
			return nil
		}
		return ds.ErrConflict
	} else if !os.IsNotExist(e) {
		return e
	}
	mode := os.FileMode(0755)
	if private {
		mode = 0700
	}
	if e = os.MkdirAll(filepath.Dir(target), mode); e != nil {
		return e
	}
	f, e := os.CreateTemp(filepath.Dir(target), ".immutable-")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	if e = f.Chmod(0600); e == nil {
		_, e = f.Write(raw)
	}
	if e == nil {
		e = f.Sync()
	}
	c := f.Close()
	if e == nil {
		e = c
	}
	if e != nil {
		return e
	}
	if e = os.Link(f.Name(), target); e != nil {
		if os.IsExist(e) {
			return s.immutable(path, v, private)
		}
		return e
	}
	return objectlocation.SyncDir(filepath.Dir(target))
}
func (s *Store) WriteRevision(r ds.Revision) error {
	if e := valid(r.ID, r.Plan.SetID, r.OperationID); e != nil {
		return e
	}
	if r.SchemaVersion != 1 || r.Hash != ds.RevisionHash(r) || r.Report.Hash != ds.ReportHash(r.Report) || r.Report.CandidateHash != ds.CandidateHash(r) {
		return ds.ErrInvalid
	}
	for _, a := range r.Members {
		if a.BodyHash != documentstore.Hash([]byte(a.Body)) {
			return ds.ErrInvalid
		}
		v, e := documentstore.New(s.Root).FrozenRevision(a.DocumentID, a.RevisionID)
		if e != nil {
			return e
		}
		if !artifactMatches(a, v) {
			return ds.ErrInvalid
		}
	}
	path := Base(r.Plan.SetID) + "/revisions/" + r.ID
	// Manifest is self-contained. The adjacent files are inspectable projections;
	// all reads and recovery authenticate them against the immutable manifest.
	for name, value := range map[string]any{"shared-design.json": r.Plan.Shared, "plan.json": r.Plan, "validation.json": r.Report} {
		if e := s.immutable(path+"/"+name, value, false); e != nil {
			return e
		}
	}
	return s.immutable(path+"/manifest.json", r, false)
}
func (s *Store) Revision(id, rev string) (ds.Revision, error) {
	var r ds.Revision
	if e := valid(id, rev); e != nil {
		return r, e
	}
	if e := objectlocation.For(s.Root).CheckTopicActive(id); e != nil {
		return r, e
	}
	e := objectlocation.ReadJSON(s.Root, Base(id)+"/revisions/"+rev+"/manifest.json", &r)
	if e != nil {
		return r, e
	}
	if r.ID != rev || r.Plan.SetID != id || r.SchemaVersion != 1 || r.Hash != ds.RevisionHash(r) || r.Report.Hash != ds.ReportHash(r.Report) || r.Report.CandidateHash != ds.CandidateHash(r) {
		return r, ds.ErrInvalid
	}
	for name, expected := range map[string]any{"plan.json": r.Plan, "shared-design.json": r.Plan.Shared, "validation.json": r.Report} {
		var actual json.RawMessage
		if e := objectlocation.ReadJSON(s.Root, Base(id)+"/revisions/"+rev+"/"+name, &actual); e != nil {
			return r, e
		}
		var decoded any
		if e := json.Unmarshal(actual, &decoded); e != nil {
			return r, ds.ErrInvalid
		}
		raw, _ := json.Marshal(expected)
		var wanted any
		if json.Unmarshal(raw, &wanted) != nil || documentstore.HashJSON(decoded) != documentstore.HashJSON(wanted) {
			return r, ds.ErrInvalid
		}
	}
	for _, a := range r.Members {
		if a.BodyHash != documentstore.Hash([]byte(a.Body)) {
			return r, ds.ErrInvalid
		}
		d, e := documentstore.New(s.Root).FrozenRevision(a.DocumentID, a.RevisionID)
		if e != nil {
			return r, e
		}
		if !artifactMatches(a, d) {
			return r, ds.ErrInvalid
		}
	}
	return r, nil
}

// A member authenticates all frozen readable bytes, including import originals.
func artifactMatches(a ds.Artifact, d documentstore.RevisionData) bool {
	if d.ResourceError != "" || d.Body != a.Body || documentstore.HashJSON(d.Contract) != documentstore.HashJSON(a.Contract) || d.Profile != a.ProfileSnapshot || documentstore.HashJSON(d.Sources) != documentstore.HashJSON(a.Sources) || d.Revision.ResourceBindingsHash != a.ResourceHash || documentstore.HashJSON(d.Resources) != documentstore.HashJSON(a.Resources) {
		return false
	}
	if a.OriginalHash != "" {
		return d.Projection != nil && d.Projection.Content.Original == a.Original && documentstore.Hash([]byte(a.Original)) == a.OriginalHash
	}
	return a.Original == "" && (d.Projection == nil || d.Projection.Content.Original == "")
}
func retainedRoots(h ds.Head) []string {
	roots := []string{h.Working, h.Finalized}
	for _, f := range h.Finalizations {
		roots = append(roots, f.RevisionID)
	}
	return roots
}
func (s *Store) Reachable(id, rev string) bool {
	h, e := s.Head(id)
	if e != nil {
		return false
	}
	seen := map[string]bool{}
	for _, start := range retainedRoots(h) {
		for v := start; v != "" && !seen[v]; {
			if v == rev {
				return true
			}
			seen[v] = true
			r, e := s.Revision(id, v)
			if e != nil {
				return false
			}
			v = r.Parent
		}
	}
	return false
}
func (s *Store) Read(id, rev string) (ds.Revision, error) {
	if !s.Reachable(id, rev) {
		return ds.Revision{}, os.ErrNotExist
	}
	return s.Revision(id, rev)
}
func (s *Store) Intents(id string) ([]Intent, error) {
	path, e := objectlocation.SafePath(s.Root, Private(id)+"/intents")
	if e != nil {
		return nil, e
	}
	entries, e := os.ReadDir(path)
	if os.IsNotExist(e) {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	out := []Intent{}
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".json") {
			var in Intent
			if e = objectlocation.ReadJSON(s.Root, Private(id)+"/intents/"+entry.Name(), &in); e != nil {
				return nil, e
			}
			out = append(out, in)
		}
	}
	return out, nil
}
func (s *Store) SaveIntent(in Intent) error {
	if e := valid(in.SetID, in.OperationID, in.RevisionID); e != nil {
		return e
	}
	return objectlocation.WriteJSON(s.Root, Private(in.SetID)+"/intents/"+in.OperationID+".json", in, true)
}
func (s *Store) Commit(r ds.Revision, expected uint64, requestHash string, check func() error) error {
	if ds.Ready(r) != nil {
		return ds.ErrRules
	}
	registry := objectlocation.For(s.Root)
	registry.Namespace.Lock()
	defer registry.Namespace.Unlock()
	h, e := s.HeadOrEmpty(r.Plan.SetID)
	if e != nil {
		return e
	}
	intents, e := s.Intents(r.Plan.SetID)
	if e != nil {
		return e
	}
	for _, in := range intents {
		if in.OperationID == r.OperationID {
			if in.RequestHash != requestHash {
				return ds.ErrConflict
			}
			if in.Committed || s.Reachable(r.Plan.SetID, in.RevisionID) {
				return nil
			}
		}
	}
	if h.Generation != expected || r.Parent != h.Working {
		return ds.ErrConflict
	}
	if check != nil {
		if e = check(); e != nil {
			return e
		}
	}
	if e = registry.RequireSetWriter(); e != nil {
		return e
	}
	if e = s.WriteRevision(r); e != nil {
		return e
	}
	if e = s.checkpoint("set_revision_written"); e != nil {
		return e
	}
	intent := Intent{OperationID: r.OperationID, RequestHash: requestHash, SetID: r.Plan.SetID, RevisionID: r.ID, RevisionHash: r.Hash, Expected: expected, Events: []string{"document_set.committed"}}
	if e = s.SaveIntent(intent); e != nil {
		return e
	}
	if e = s.checkpoint("intent_written"); e != nil {
		return e
	}
	h.SchemaVersion = 1
	h.Generation++
	h.Working = r.ID
	h.OperationID = r.OperationID
	if e = objectlocation.WriteJSON(s.Root, Base(r.Plan.SetID)+"/head.json", h, false); e != nil {
		return e
	}
	if e = s.checkpoint("set_head_published"); e != nil {
		return e
	}
	intent.Committed = true
	return s.SaveIntent(intent)
}

// Project reconciles the CURRENT set, never the old intent's member list. A
// personal working HEAD or raw bytes changed after the frozen baseline wins.
func (s *Store) Project(id string) error {
	registry := objectlocation.For(s.Root)
	registry.Namespace.Lock()
	defer registry.Namespace.Unlock()
	h, e := s.Head(id)
	if e != nil {
		return e
	}
	r, e := s.Revision(id, h.Working)
	if e != nil {
		return e
	}
	var catalog models.TopicCatalog
	if e = objectlocation.ReadJSON(s.Root, "topics/"+id+"/catalog.json", &catalog); e != nil {
		return e
	}
	entries := []models.CatalogEntry{}
	for _, entry := range catalog.Entries {
		l, e := registry.Lookup(entry.Binding.ThoughtID)
		if e == nil && l.ManagedSetID == id {
			continue
		}
		entries = append(entries, entry)
	}
	for _, a := range r.Members {
		l, e := registry.Lookup(a.DocumentID)
		if e != nil {
			return e
		}
		if l.Lifecycle != "active" {
			l.Lifecycle = "active"
			if e = registry.CommitLocation(l, "document_set.activated"); e != nil {
				return e
			}
		}
		head, e := documentstore.New(s.Root).Head(a.DocumentID)
		if e != nil && !os.IsNotExist(e) {
			return e
		}
		expected := r.Plan.Baselines[a.Key]
		current := WorkingHash(s.Root, a.DocumentID)
		if current == expected || head.WorkingRevisionID == "" || (head.PendingProjection && head.WorkingRevisionID == a.RevisionID) {
			if e = s.checkpoint("before_projection"); e != nil {
				return e
			}
			if head.WorkingRevisionID != a.RevisionID {
				head.Generation++
				head.WorkingRevisionID = a.RevisionID
				head.PendingProjection = true
				if e = documentstore.New(s.Root).SaveHead(a.DocumentID, head); e != nil {
					return e
				}
			}
			now := r.CreatedAt
			t := models.Thought{CaptureStatus: models.CaptureStatusCaptured, RefineStatus: models.RefineStatusRefined, IndexStatus: models.IndexStatusPending, TopicStatus: models.TopicStatusUpdated, ID: a.DocumentID, Type: models.ThoughtTypeText, UserTitle: a.Contract.Title, OwnerTopicID: id, Path: l.Placement.ThoughtPath, CreatedAt: now, UpdatedAt: now, Document: &models.DocumentLink{DocumentID: a.DocumentID, RevisionID: a.RevisionID, Generation: head.Generation}, DocumentProfile: &a.Contract.ProfileRef}
			content := models.ThoughtContent{Original: a.Original, AINotes: a.Body}
			if a.OriginalHash != "" {
				t.MarkdownImport = &models.MarkdownImport{SHA256: a.OriginalHash, Size: len(a.Original), Filename: a.Key + ".md"}
			}
			raw := markdown.RenderThought(t, content)
			if e = objectlocation.WriteFile(s.Root, l.Placement.ThoughtPath, raw, 0644); e != nil {
				return e
			}
			head.PendingProjection = false
			if e = documentstore.New(s.Root).SaveHead(a.DocumentID, head); e != nil {
				return e
			}
		}
		entries = append(entries, models.CatalogEntry{ID: a.DocumentID, GroupID: "document-set", Title: a.Contract.Title, Role: r.Plan.Members[indexOf(r.Plan.Members, a.Key)].Role, Required: true, Binding: models.CatalogBinding{Kind: "thought", ThoughtID: a.DocumentID, VersionPolicy: "latest_finalized"}})
	}
	if catalog.LastOperationID != h.OperationID {
		found := false
		for _, g := range catalog.Groups {
			if g.ID == "document-set" {
				found = true
			}
		}
		if !found {
			catalog.Groups = append(catalog.Groups, models.CatalogGroup{ID: "document-set", Title: "Document set"})
		}
		catalog.Entries = entries
		catalog.Generation++
		catalog.LastOperationID = h.OperationID
		if e = objectlocation.WriteJSON(s.Root, "topics/"+id+"/catalog.json", catalog, false); e != nil {
			return e
		}
	}
	if h.ProjectedWorking != h.Working {
		h.ProjectedWorking = h.Working
		if e = objectlocation.WriteJSON(s.Root, Base(id)+"/head.json", h, false); e != nil {
			return e
		}
	}
	registry.BumpEpoch()
	return nil
}
func indexOf(m []ds.PlanMember, key string) int {
	for i, v := range m {
		if v.Key == key {
			return i
		}
	}
	return 0
}

// WorkingHash includes unparsed on-disk edits as well as the personal HEAD.
func WorkingHash(root, id string) string {
	l, e := objectlocation.For(root).Lookup(id)
	if e != nil {
		return ""
	}
	p, e := objectlocation.SafePath(root, l.Placement.ThoughtPath)
	if e != nil {
		return "invalid"
	}
	raw, e := objectlocation.ReadFileBounded(p, 32<<20)
	if e != nil && !os.IsNotExist(e) {
		return "invalid"
	}
	h, e := documentstore.New(root).Head(id)
	if e != nil && !os.IsNotExist(e) {
		return "invalid"
	}
	if len(raw) == 0 && h.WorkingRevisionID == "" {
		return ""
	}
	return documentstore.HashJSON([]string{documentstore.Hash(raw), fmt.Sprint(h.Generation), h.WorkingRevisionID})
}
func (s *Store) Recover(emit func(Intent) error) []error {
	return s.recover("", emit)
}

// RecoverSet repairs only derived publication state; it never invokes a model.
func (s *Store) RecoverSet(id string, emit func(Intent) error) []error {
	if e := valid(id); e != nil {
		return []error{e}
	}
	return s.recover(id, emit)
}

func (s *Store) recover(only string, emit func(Intent) error) []error {
	root, e := objectlocation.SafePath(s.Root, ".content-registry/private/document-sets")
	if e != nil {
		return []error{e}
	}
	entries, e := os.ReadDir(root)
	if os.IsNotExist(e) {
		return nil
	}
	if e != nil {
		return []error{e}
	}
	issues := []error{}
	for _, entry := range entries {
		if !entry.IsDir() || !documentstore.ValidID(entry.Name()) || (only != "" && entry.Name() != only) {
			continue
		}
		intents, e := s.Intents(entry.Name())
		if e != nil {
			issues = append(issues, e)
			continue
		}
		for _, in := range intents {
			if in.Done {
				continue
			}
			committed := s.Reachable(in.SetID, in.RevisionID)
			if in.Kind == "finalization" {
				committed = false
				h, err := s.Head(in.SetID)
				if err != nil {
					issues = append(issues, err)
					continue
				}
				for _, f := range h.Finalizations {
					if f.ID == in.FinalizationID && f.OperationID == in.OperationID && f.RevisionID == in.RevisionID {
						committed = true
						break
					}
				}
			}
			if !committed {
				if in.Committed {
					in.Error = "committed revision not reachable"
					s.SaveIntent(in)
					issues = append(issues, ds.ErrInvalid)
				}
				continue
			}
			r, e := s.Revision(in.SetID, in.RevisionID)
			if e == nil && r.Hash != in.RevisionHash {
				e = ds.ErrInvalid
			}
			if e == nil {
				e = s.Project(in.SetID)
			}
			if e == nil && emit != nil {
				e = emit(in)
			}
			if e != nil {
				in.Error = e.Error()
				s.SaveIntent(in)
				issues = append(issues, e)
				continue
			}
			in.Committed = true
			in.Done = true
			in.Error = ""
			if e = s.SaveIntent(in); e != nil {
				issues = append(issues, e)
			}
		}
	}
	return issues
}
func (s *Store) Member(id, rev, key string) (ds.Artifact, error) {
	r, e := s.Read(id, rev)
	if e != nil {
		return ds.Artifact{}, e
	}
	for _, a := range r.Members {
		if a.Key == key {
			l, err := objectlocation.For(s.Root).Lookup(a.DocumentID)
			if err != nil {
				return a, err
			}
			if l.ManagedSetID != id || l.ManagedMemberKey != key || l.DeletedAt != nil || l.Lifecycle == "deleted" {
				return a, os.ErrNotExist
			}
			return a, nil
		}
	}
	return ds.Artifact{}, os.ErrNotExist
}

// PublishProfile preserves immutable custom profile versions.
func (s *Store) PublishProfile(p ds.Profile) error {
	return s.immutable(fmt.Sprintf(".content-registry/document-set-profiles/%s/v%d.json", p.ID, p.Version), p, false)
}

// ContentPaths follows the retained set parent chains in addition to individual
// document HEADs. Old set-selected revisions remain recoverable indefinitely.
func (s *Store) ContentPaths(id string) ([]string, error) {
	h, e := s.Head(id)
	if e != nil {
		return nil, e
	}
	out := []string{Base(id) + "/head.json", "topics/" + id + "/topic.yaml", "topics/" + id + "/catalog.json", ".content-registry/schema.json"}
	for _, f := range h.Finalizations {
		out = append(out, Base(id)+"/finalizations/"+f.ID+".json")
	}
	seen := map[string]bool{}
	for _, start := range retainedRoots(h) {
		for rev := start; rev != "" && !seen[rev]; {
			seen[rev] = true
			r, e := s.Revision(id, rev)
			if e != nil {
				return nil, e
			}
			for _, name := range []string{"manifest.json", "plan.json", "shared-design.json", "validation.json"} {
				out = append(out, Base(id)+"/revisions/"+rev+"/"+name)
			}
			for _, a := range r.Members {
				l, e := objectlocation.For(s.Root).Lookup(a.DocumentID)
				if e != nil {
					return nil, e
				}
				out = append(out, objectlocation.RecordPath(a.DocumentID))
				for _, name := range []string{"manifest.json", "contract.json", "source-manifest.json", "profile.snapshot.md", "body.md"} {
					out = append(out, l.Placement.DocumentRoot+"/revisions/"+a.RevisionID+"/"+name)
				}
				d, e := documentstore.New(s.Root).FrozenRevision(a.DocumentID, a.RevisionID)
				if e != nil {
					return nil, e
				}
				if d.Revision.ResourceBindingsHash != "" {
					out = append(out, l.Placement.DocumentRoot+"/revisions/"+a.RevisionID+"/resources.json")
				}
				for _, b := range d.Resources.Bindings {
					if b.Private {
						return nil, ds.ErrRules
					}
					out = append(out, b.Path, ".content-registry/resources/"+b.ResourceID+".json")
				}
				if d.Revision.ProjectionHash != "" {
					out = append(out, l.Placement.DocumentRoot+"/revisions/"+a.RevisionID+"/projection.json")
				}
			}
			rev = r.Parent
		}
	}
	return out, nil
}

func (s *Store) WriteFinalization(id string, f ds.Finalization) error {
	if e := valid(id, f.ID, f.OperationID, f.RevisionID); e != nil {
		return e
	}
	return s.immutable(Base(id)+"/finalizations/"+f.ID+".json", f, false)
}

// Resource reads a public asset from an exact reachable set member snapshot.
func (s *Store) Resource(set, revision, member, resourceID string) ([]byte, string, error) {
	registry := objectlocation.For(s.Root)
	registry.Namespace.RLock()
	defer registry.Namespace.RUnlock()
	a, e := s.Member(set, revision, member)
	if e != nil {
		return nil, "", e
	}
	for _, b := range a.Resources.Bindings {
		if b.ResourceID != resourceID || b.Private {
			continue
		}
		p, e := registry.FrozenResourcePath(a.DocumentID, b)
		if e != nil {
			return nil, "", e
		}
		raw, e := objectlocation.ReadFileBounded(p, objectlocation.MaxResourceBytes)
		if e != nil {
			return nil, "", e
		}
		if objectlocation.Hash(raw) != b.Hash {
			return nil, "", ds.ErrInvalid
		}
		mime, e := objectlocation.ValidateResource(filepath.Base(b.Path), raw)
		return raw, mime, e
	}
	return nil, "", os.ErrNotExist
}
