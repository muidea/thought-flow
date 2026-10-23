// Package documentstore implements a single-writer, immutable document journal.
// HEAD publication is the visibility commit point; Thought projection is retried.
package documentstore

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"thoughtflow/internal/pkg/models"
	"thoughtflow/internal/pkg/objectlocation"
	"thoughtflow/internal/pkg/workspace"
)

const NormalizationVersion = "utf8-lf-eof-v1"

var ErrConflict = errors.New("document generation or operation conflict")
var ErrInvalid = errors.New("invalid document record")
var idPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,159}$`)

type Store struct {
	Root  string
	Fault func(string) error
}
type RevisionData struct {
	Resources           models.ResourceBindings
	ResourceError       string `json:"-"`
	ResourceMappingHash string `json:"-"`
	Projection          *models.ThoughtSnapshot
	Evidence            map[string]string `json:"-"`
	Revision            models.DocumentRevision
	Contract            models.DocumentContract
	Sources             models.SourceManifest
	Profile             string
	Body                string
}

func New(root string) *Store { return &Store{Root: filepath.Join(root, "documents")} }
func ID(prefix string) string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return prefix + hex.EncodeToString(b[:])
}
func Normalize(body string) string {
	return strings.TrimRight(strings.ReplaceAll(body, "\r\n", "\n"), "\n") + "\n"
}
func Hash(raw []byte) string {
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// encoding/json sorts map keys, with fixed struct field order. No float values enter contracts.
func HashJSON(v any) string {
	raw, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return Hash(raw)
}
func (s *Store) Lock(id string) func() {
	return objectlocation.For(filepath.Dir(s.Root)).ReadLease(id)
}
func (s *Store) path(id string, parts ...string) (string, error) {
	if !idPattern.MatchString(id) {
		return "", ErrInvalid
	}
	l, e := objectlocation.For(filepath.Dir(s.Root)).Lookup(id)
	if e != nil {
		if os.IsNotExist(e) {
			if check := workspace.EnsureNoSymlinks(filepath.Dir(s.Root), filepath.Join(append([]string{s.Root, id}, parts...)...)); check != nil {
				return "", check
			}
		}
		return "", e
	}
	p := filepath.Join(append([]string{filepath.Dir(s.Root), filepath.FromSlash(l.Placement.DocumentRoot)}, parts...)...)
	// Match the Markdown boundary: reject links at or inside the workspace,
	// including missing descendants, while allowing OS aliases above it
	// (for example macOS /var -> /private/var).
	if err := workspace.EnsureNoSymlinks(filepath.Dir(s.Root), p); err != nil {
		return "", err
	}
	return p, nil
}
func ValidID(id string) bool { return idPattern.MatchString(id) }
func (s *Store) Head(id string) (models.DocumentHead, error) {
	var h models.DocumentHead
	p, e := s.path(id, "head.json")
	if e != nil {
		return h, e
	}
	e = readJSON(p, &h)
	return h, e
}

// ActiveHead is the public lifecycle gate. A missing projection is recoverable;
// a deleted document is deliberately unavailable even when its journal remains.
func (s *Store) ActiveHead(id string) (models.DocumentHead, error) {
	h, e := s.Head(id)
	if e == nil {
		e = s.checkLocationActive(id)
	}
	if e == nil && h.DeletedAt != nil {
		e = os.ErrNotExist
	}
	return h, e
}

// CheckActive also permits a new, unpublished document.
func (s *Store) CheckActive(id string) error {
	if _, e := objectlocation.For(filepath.Dir(s.Root)).Lookup(id); e == nil {
		if e = s.checkLocationActive(id); e != nil {
			return e
		}
	} else if !os.IsNotExist(e) {
		return e
	}
	h, e := s.Head(id)
	if os.IsNotExist(e) {
		return nil
	}
	if e == nil && h.DeletedAt != nil {
		return os.ErrNotExist
	}
	if e == nil {
		return s.checkLocationActive(id)
	}
	return e
}

func (s *Store) checkLocationActive(id string) error {
	r := objectlocation.For(filepath.Dir(s.Root))
	l, e := r.Lookup(id)
	if e != nil {
		return e
	}
	if l.DeletedAt != nil || l.Lifecycle == "deleted" || l.Lifecycle == "import_pending" {
		return os.ErrNotExist
	}
	if l.ImportOperationID != "" {
		op, e := r.Operation(l.ImportOperationID)
		if e != nil || op.State != "completed" {
			return os.ErrNotExist
		}
	}
	return nil
}

// DeleteLocked publishes revocation before the projection is removed. The
// caller holds Lock(id) through both steps; a retry can finish file cleanup.
func (s *Store) DeleteLocked(id string) (models.DocumentHead, error) {
	h, e := s.Head(id)
	if e != nil && !os.IsNotExist(e) {
		return h, e
	}
	if h.DeletedAt != nil {
		return h, nil
	}
	now := time.Now().UTC()
	h.DeletedAt = &now
	h.PendingProjection = false
	h.Generation++
	if e = s.SaveHead(id, h); e != nil {
		return h, e
	}
	if s.Fault != nil {
		e = s.Fault("deletion_published")
	}
	return h, e
}
func (s *Store) Revision(id, rev string) (RevisionData, error) {
	var d RevisionData
	if !ValidID(rev) {
		return d, ErrInvalid
	}
	p, e := s.path(id, "revisions", rev)
	if e != nil {
		return d, e
	}
	if e = readJSON(filepath.Join(p, "manifest.json"), &d.Revision); e != nil {
		return d, e
	}
	if e = readJSON(filepath.Join(p, "contract.json"), &d.Contract); e != nil {
		return d, e
	}
	if e = readJSON(filepath.Join(p, "source-manifest.json"), &d.Sources); e != nil {
		return d, e
	}
	b, e := safeRead(filepath.Join(p, "body.md"))
	if e != nil {
		return d, e
	}
	d.Body = string(b)
	b, e = safeRead(filepath.Join(p, "profile.snapshot.md"))
	if e != nil {
		return d, e
	}
	d.Profile = string(b)
	currentBindings, bindErr := objectlocation.For(filepath.Dir(s.Root)).Bindings(id)
	if bindErr != nil {
		return d, bindErr
	}
	r := d.Revision
	if r.ResourceBindingsHash != "" {
		if e = readJSON(filepath.Join(p, "resources.json"), &d.Resources); e != nil {
			return d, e
		}
		if HashJSON(d.Resources) != r.ResourceBindingsHash || d.Resources.ObjectID != id {
			return d, ErrInvalid
		}
		for _, b := range d.Resources.Bindings {
			resource, e := objectlocation.For(filepath.Dir(s.Root)).BoundResourcePath(id, b)
			if e != nil {
				d.ResourceError = e.Error()
				continue
			}
			hash, _, e := objectlocation.FileHash(resource)
			if e != nil || hash != b.Hash || b.Private {
				d.ResourceError = "resource_incomplete"
			}
		}
	}
	knownOriginal := map[string]bool{}
	for _, b := range d.Resources.Bindings {
		knownOriginal[b.OriginalLocator] = true
	}
	for _, b := range currentBindings.Bindings {
		if b.OriginalLocator != "" && !knownOriginal[b.OriginalLocator] {
			d.Resources.Bindings = append(d.Resources.Bindings, b)
			path, err := objectlocation.For(filepath.Dir(s.Root)).BoundResourcePath(id, b)
			if err != nil {
				d.ResourceError = "resource_incomplete"
			} else if hash, _, err := objectlocation.FileHash(path); err != nil || hash != b.Hash {
				d.ResourceError = "resource_incomplete"
			}
		}
	}
	mapping := []string{}
	for _, b := range d.Resources.Bindings {
		if b.OriginalLocator != "" {
			mapping = append(mapping, b.OriginalLocator, b.ResourceID, b.Hash)
		}
	}
	if len(mapping) > 0 {
		d.ResourceMappingHash = HashJSON(mapping)
	}
	if r.ProjectionHash != "" {
		if e = readJSON(filepath.Join(p, "projection.json"), &d.Projection); e != nil {
			return d, e
		}
		if d.Projection == nil || HashJSON(d.Projection) != r.ProjectionHash {
			return d, fmt.Errorf("%w: projection hash mismatch", ErrInvalid)
		}
	}
	if r.DocumentID != id || r.RevisionID != rev || Hash([]byte(d.Body)) != r.BodyHash || HashJSON(d.Contract) != r.ContractHash || HashJSON(d.Sources) != r.SourceManifestHash || (d.Profile != "" && Hash([]byte(Normalize(strings.TrimSpace(d.Profile)))) != r.ProfileHash) {
		return d, fmt.Errorf("%w: revision hash mismatch", ErrInvalid)
	}
	return d, nil
}

// Publish must be called while holding Lock(id). Unreferenced revision directories
// after a crash are harmless: readers only discover versions through HEAD.
func (s *Store) Publish(id string, expected uint64, d RevisionData) (models.DocumentHead, error) {
	if _, e := objectlocation.For(filepath.Dir(s.Root)).Reserve(id, "", "document-create"); e != nil {
		return models.DocumentHead{}, e
	}
	h, err := s.Head(id)
	if err != nil && !os.IsNotExist(err) {
		return h, err
	}
	if h.DeletedAt != nil {
		return h, os.ErrNotExist
	}
	if h.Generation != expected {
		return h, ErrConflict
	}
	if !utf8.ValidString(d.Body) || len(d.Body) > 1024*1024 || len(d.Sources.Sources) > 100 {
		return h, ErrInvalid
	}
	d.Body = Normalize(d.Body)
	r := &d.Revision
	r.DocumentID = id
	r.ParentRevisionID = h.WorkingRevisionID
	r.RevisionID = ID("r-")
	r.BodyHash = Hash([]byte(d.Body))
	r.ContractHash = HashJSON(d.Contract)
	r.SourceManifestHash = HashJSON(d.Sources)
	bindings, e := objectlocation.For(filepath.Dir(s.Root)).Bindings(id)
	if e != nil {
		return h, e
	}
	d.Resources = bindings
	r.ResourceBindingsHash = ""
	if len(bindings.Bindings) > 0 {
		r.ResourceBindingsHash = HashJSON(bindings)
	}
	r.ProjectionHash = ""
	if d.Projection != nil {
		r.ProjectionHash = HashJSON(d.Projection)
	}
	r.ProfileHash = d.Contract.ProfileRef.ContentHash
	r.CreatedAt = time.Now().UTC()
	r.NormalizationVersion = NormalizationVersion
	dir, e := s.path(id, "revisions")
	if e != nil {
		return h, e
	}
	if e = mkdir(dir); e != nil {
		return h, e
	}
	tmp, e := os.MkdirTemp(dir, ".stage-")
	if e != nil {
		return h, e
	}
	defer os.RemoveAll(tmp)
	if d.Projection != nil {
		b, e := json.MarshalIndent(d.Projection, "", "  ")
		if e != nil {
			return h, e
		}
		if e = writeSync(filepath.Join(tmp, "projection.json"), b); e != nil {
			return h, e
		}
	}
	records := map[string]any{"manifest.json": r, "contract.json": d.Contract, "source-manifest.json": d.Sources}
	if r.ResourceBindingsHash != "" {
		records["resources.json"] = d.Resources
	}
	for name, v := range records {
		b, e := json.MarshalIndent(v, "", "  ")
		if e != nil {
			return h, e
		}
		if e = writeSync(filepath.Join(tmp, name), b); e != nil {
			return h, e
		}
	}
	for name, b := range map[string]string{"body.md": d.Body, "profile.snapshot.md": d.Profile} {
		if e = writeSync(filepath.Join(tmp, name), []byte(b)); e != nil {
			return h, e
		}
	}
	if e = syncDir(tmp); e != nil {
		return h, e
	}
	if e = os.Rename(tmp, filepath.Join(dir, r.RevisionID)); e != nil {
		return h, e
	}
	if e = syncDir(dir); e != nil {
		return h, e
	}
	if s.Fault != nil {
		if e = s.Fault("revision_written"); e != nil {
			return h, e
		}
	}
	h.Generation++
	h.WorkingRevisionID = r.RevisionID
	h.PendingProjection = true
	if e = s.SaveHead(id, h); e != nil {
		return h, e
	}
	if s.Fault != nil {
		if e = s.Fault("head_published"); e != nil {
			return h, e
		}
	}
	return h, nil
}
func (s *Store) SaveHead(id string, h models.DocumentHead) error {
	if _, e := objectlocation.For(filepath.Dir(s.Root)).Reserve(id, "", "document-create"); e != nil {
		return e
	}
	p, e := s.path(id, "head.json")
	if e != nil {
		return e
	}
	b, e := json.MarshalIndent(h, "", "  ")
	if e != nil {
		return e
	}
	if e = atomicWrite(p, b); e == nil {
		objectlocation.For(filepath.Dir(s.Root)).BumpEpoch()
	}
	return e
}
func (s *Store) ProjectionDone(id, rev string) error {
	unlock := s.Lock(id)
	defer unlock()
	return s.ProjectionDoneLocked(id, rev)
}
func (s *Store) ProjectionDoneLocked(id, rev string) error {
	h, e := s.ActiveHead(id)
	if e != nil {
		return e
	}
	if h.WorkingRevisionID != rev {
		return ErrConflict
	}
	h.PendingProjection = false
	return s.SaveHead(id, h)
}

func (s *Store) PutRecord(id, kind, key string, v any) error {
	if !ValidID(key) || (kind != "reports" && kind != "finalizations") {
		return ErrInvalid
	}
	if _, e := objectlocation.For(filepath.Dir(s.Root)).Reserve(id, "", "document-create"); e != nil {
		return e
	}
	p, e := s.path(id, kind, key+".json")
	if e != nil {
		return e
	}
	b, e := json.MarshalIndent(v, "", "  ")
	if e != nil {
		return e
	}
	if len(b) > 1024*1024 {
		return ErrInvalid
	}
	// Exclusive creation preserves immutability. tmp+link avoids exposing partial JSON.
	if e = mkdir(filepath.Dir(p)); e != nil {
		return e
	}
	tmp, e := os.CreateTemp(filepath.Dir(p), ".record-")
	if e != nil {
		return e
	}
	name := tmp.Name()
	tmp.Close()
	defer os.Remove(name)
	if e = writeSync(name, b); e != nil {
		return e
	}
	if e = os.Link(name, p); e != nil {
		return e
	}
	return syncDir(filepath.Dir(p))
}
func (s *Store) Report(id, key string) (models.ValidationReport, error) {
	var r models.ValidationReport
	if !ValidID(key) {
		return r, ErrInvalid
	}
	p, e := s.path(id, "reports", key+".json")
	if e == nil {
		e = readJSON(p, &r)
	}
	if e == nil && (r.ReportID != key || r.DocumentID != id) {
		return r, ErrInvalid
	}
	return r, e
}
func (s *Store) Finalization(id, key string) (models.FinalizationRecord, error) {
	var r models.FinalizationRecord
	if !ValidID(key) {
		return r, ErrInvalid
	}
	p, e := s.path(id, "finalizations", key+".json")
	if e == nil {
		e = readJSON(p, &r)
	}
	if e == nil && r.FinalizationID != key {
		return r, ErrInvalid
	}
	return r, e
}
func (s *Store) Records(id, kind string) ([]string, error) {
	if kind != "reports" && kind != "finalizations" {
		return nil, ErrInvalid
	}
	p, e := s.path(id, kind)
	if e != nil {
		return nil, e
	}
	entries, e := os.ReadDir(p)
	if os.IsNotExist(e) {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	out := []string{}
	for _, v := range entries {
		if !v.IsDir() && strings.HasSuffix(v.Name(), ".json") {
			out = append(out, strings.TrimSuffix(v.Name(), ".json"))
		}
	}
	return out, nil
}
func readJSON(p string, v any) error {
	b, e := safeRead(p)
	if e != nil {
		return e
	}
	if len(b) > 2*1024*1024 {
		return ErrInvalid
	}
	return json.Unmarshal(b, v)
}
func mkdir(p string) error {
	if e := os.MkdirAll(p, 0o755); e != nil {
		return e
	}
	return syncDir(filepath.Dir(p))
}
func writeSync(p string, b []byte) error {
	f, e := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if e != nil {
		return e
	}
	if _, e = f.Write(b); e == nil {
		e = f.Sync()
	}
	c := f.Close()
	if e == nil {
		e = c
	}
	return e
}
func atomicWrite(p string, b []byte) error {
	if e := mkdir(filepath.Dir(p)); e != nil {
		return e
	}
	f, e := os.CreateTemp(filepath.Dir(p), ".head-")
	if e != nil {
		return e
	}
	tmp := f.Name()
	f.Close()
	defer os.Remove(tmp)
	if e = writeSync(tmp, b); e != nil {
		return e
	}
	if e = os.Rename(tmp, p); e != nil {
		return e
	}
	return syncDir(filepath.Dir(p))
}
func syncDir(p string) error {
	f, e := os.Open(p)
	if e != nil {
		return e
	}
	defer f.Close()
	return f.Sync()
}

func safeRead(path string) ([]byte, error) {
	info, e := os.Lstat(path)
	if e != nil {
		return nil, e
	}
	if !info.Mode().IsRegular() {
		return nil, os.ErrPermission
	}
	if info.Size() > 2*1024*1024 {
		return nil, ErrInvalid
	}
	return os.ReadFile(path)
}
