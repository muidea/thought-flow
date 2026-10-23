package objectlocation

import (
	"context"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"thoughtflow/internal/pkg/models"
)

const MaxResourceBytes = 32 * 1024 * 1024

func resourcePath(id string) string { return ".content-registry/resources/" + id + ".json" }
func ResourceID(locator string) (string, bool) {
	if !strings.HasPrefix(locator, "resource://") {
		return "", false
	}
	id := strings.TrimPrefix(locator, "resource://")
	return id, ValidID(id)
}
func sniffResource(name string, raw []byte) (string, error) {
	if len(raw) == 0 || len(raw) > MaxResourceBytes || strings.ContainsAny(name, "/\\\x00\r\n") || name == "." || name == ".." {
		return "", ErrInvalid
	}
	ext := strings.ToLower(filepath.Ext(name))
	mime := strings.Split(http.DetectContentType(raw), ";")[0]
	types := map[string]string{".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".gif": "image/gif", ".webp": "image/webp", ".pdf": "application/pdf", ".txt": "text/plain", ".md": "text/plain"}
	expected, ok := types[ext]
	if !ok || expected != mime {
		return "", ErrUnsafe
	}
	if mime == "text/plain" && (!utf8.Valid(raw) || strings.ContainsRune(string(raw), 0)) {
		return "", ErrUnsafe
	}
	return mime, nil
}
func (r *Registry) Bindings(id string) (models.ResourceBindings, error) {
	l, e := r.Lookup(id)
	if e != nil {
		return models.ResourceBindings{}, e
	}
	return r.bindingsForLocation(l)
}
func (r *Registry) bindingsForLocation(l models.ObjectLocation) (models.ResourceBindings, error) {
	id := l.ObjectID
	if l.ResourceBindingsRef == nil {
		return models.ResourceBindings{ObjectID: id, Bindings: []models.ResourceBinding{}}, nil
	}
	var b models.ResourceBindings
	e := ReadJSON(r.Root, l.ResourceBindingsRef.Path, &b)
	if e != nil {
		return b, e
	}
	if b.ObjectID != id || HashJSON(b) != l.ResourceBindingsRef.Hash {
		return b, ErrRecovery
	}
	for _, binding := range b.Bindings {
		if !ValidBinding(binding) || !r.PublicResourcePath(binding.Path) {
			return b, ErrUnsafe
		}
	}
	return b, nil
}
func (r *Registry) UploadResource(ctx context.Context, topic, id, operation, name string, generation uint64, raw []byte) (models.TopicResource, error) {
	var resource models.TopicResource
	mime, e := sniffResource(name, raw)
	if e != nil {
		return resource, e
	}
	r.Namespace.Lock()
	defer r.Namespace.Unlock()
	unlock := r.LockObject(id)
	defer unlock()
	l, e := r.Resolve(ctx, id)
	if e != nil {
		return resource, e
	}
	t, e := r.Topic(topic)
	if e != nil || t.Mode != models.TopicModeCollection {
		return resource, ErrInvalid
	}
	if l.OwnerTopicID != topic {
		return resource, ErrConflict
	}
	request := struct {
		Topic, Object, Name, Hash string
		Generation                uint64
	}{topic, id, name, Hash(raw), generation}
	op, e := r.Begin(operation, "resource", id, topic, request)
	if e != nil {
		return resource, e
	}
	rid := "res-" + strings.TrimPrefix(Hash([]byte(operation)), "sha256:")[:32]
	if op.State == "completed" {
		e = ReadJSON(r.Root, resourcePath(rid), &resource)
		return resource, e
	}
	if l.LastOperationID == operation && l.Generation == generation+1 {
		e = ReadJSON(r.Root, resourcePath(rid), &resource)
		if e != nil {
			return resource, e
		}
		return resource, r.Complete(op)
	}
	if l.Generation != generation {
		return resource, ErrConflict
	}
	path := "topics/" + topic + "/attachments/" + rid + "/" + name
	if !r.PublicResourcePath(path) {
		return resource, ErrUnsafe
	}
	resource = models.TopicResource{ID: rid, TopicID: topic, Name: name, Path: path, MIME: mime, Hash: Hash(raw), Size: int64(len(raw)), Shareable: true, ObjectIDs: []string{id}}
	if e = WriteFile(r.Root, path, raw, 0644); e != nil {
		return resource, e
	}
	if e = WriteJSON(r.Root, resourcePath(rid), resource, false); e != nil {
		return resource, e
	}
	bindings, e := r.Bindings(id)
	if e != nil {
		return resource, e
	}
	bindings.Bindings = append(bindings.Bindings, models.ResourceBinding{ResourceID: rid, Hash: resource.Hash, Path: path})
	hash := HashJSON(bindings)
	refPath := ".content-registry/bindings/" + id + "/" + strings.TrimPrefix(hash, "sha256:") + ".json"
	if e = WriteJSON(r.Root, refPath, bindings, false); e != nil {
		return resource, e
	}
	l.ResourceBindingsRef = &models.FileDigest{Path: refPath, Hash: hash}
	l.Generation++
	l.LastOperationID = operation
	op.TargetPath = RecordPath(id)
	op.CandidateHash = HashJSON(l)
	op.EventType = "topic.resource_updated"
	if e = r.Prepare(op); e != nil {
		return resource, e
	}
	if e = WriteJSON(r.Root, RecordPath(id), l, false); e != nil {
		return resource, e
	}
	r.BumpEpoch()
	return resource, r.Complete(op)
}

// ReadResource authorizes by object context and exact binding hash. Private
// evidence never enters this registry and cannot be reached by a guessed ID.
func (r *Registry) ReadResource(ctx context.Context, id, object string) (models.TopicResource, []byte, error) {
	var resource models.TopicResource
	if !ValidID(id) || !ValidID(object) {
		return resource, nil, ErrInvalid
	}
	unlock := r.ReadLease(object)
	defer unlock()
	if _, e := r.Resolve(ctx, object); e != nil {
		return resource, nil, e
	}
	b, e := r.Bindings(object)
	if e != nil {
		return resource, nil, e
	}
	found := false
	for _, binding := range b.Bindings {
		if binding.ResourceID == id && !binding.Private {
			if e = ReadJSON(r.Root, resourcePath(id), &resource); e != nil {
				return resource, nil, e
			}
			if resource.ID != id || resource.Hash != binding.Hash || !resource.Shareable {
				return resource, nil, ErrUnsafe
			}
			resource.Path = binding.Path
			found = true
			break
		}
	}
	if !found {
		return resource, nil, os.ErrNotExist
	}
	path, e := SafePath(r.Root, resource.Path)
	if e != nil {
		return resource, nil, e
	}
	info, e := os.Stat(path)
	if e != nil {
		return resource, nil, e
	}
	if !info.Mode().IsRegular() || info.Size() > MaxResourceBytes {
		return resource, nil, ErrUnsafe
	}
	raw, e := ReadFileBounded(path, MaxResourceBytes)
	if e != nil {
		return resource, nil, e
	}
	if Hash(raw) != resource.Hash {
		return resource, nil, fmt.Errorf("resource hash mismatch: %w", ErrRecovery)
	}
	mime, e := ValidateResource(resource.Name, raw)
	if e != nil || mime != resource.MIME || int64(len(raw)) != resource.Size {
		return resource, nil, ErrUnsafe
	}
	return resource, raw, nil
}

func resourceMigrationPath(owner string, b models.ResourceBinding) string {
	base := "attachments"
	if owner != "" {
		base = "topics/" + owner + "/attachments"
	}
	return base + "/" + b.ResourceID + "/" + filepath.Base(b.Path)
}
func bindingRef(b models.ResourceBindings) *models.FileDigest {
	hash := HashJSON(b)
	return &models.FileDigest{Path: ".content-registry/bindings/" + b.ObjectID + "/" + strings.TrimPrefix(hash, "sha256:") + ".json", Hash: hash}
}
func (r *Registry) migratedBindings(source models.ObjectLocation, owner string) (models.ResourceBindings, error) {
	b, e := r.relativeBindings(source)
	if e != nil {
		return b, e
	}
	for i := range b.Bindings {
		b.Bindings[i].Path = resourceMigrationPath(owner, b.Bindings[i])
	}
	return b, nil
}
func (r *Registry) BoundResourcePath(object string, frozen models.ResourceBinding) (string, error) {
	b, e := r.Bindings(object)
	if e != nil {
		return "", e
	}
	for _, current := range b.Bindings {
		if current.ResourceID == frozen.ResourceID && current.Hash == frozen.Hash && !current.Private && !frozen.Private {
			return SafePath(r.Root, current.Path)
		}
	}
	r.AddMetric("resources_missing_total", 1)
	return "", os.ErrNotExist
}

func ValidateResource(name string, raw []byte) (string, error) { return sniffResource(name, raw) }

// ValidBinding excludes all private namespaces even if a record is tampered
// with to claim Private=false. Only passive registered attachment roots qualify.
func ValidBinding(b models.ResourceBinding) bool {
	if b.OriginalLocator != "" {
		locator := html.UnescapeString(b.OriginalLocator)
		u, e := url.Parse(locator)
		if e != nil || u.Scheme != "" || u.Host != "" || strings.HasPrefix(u.Path, "/") || strings.ContainsAny(locator, "\\\x00\r\n") || u.RawQuery != "" {
			return false
		}
	}
	if b.Private || !ValidID(b.ResourceID) || len(b.Hash) != 71 || !strings.HasPrefix(b.Hash, "sha256:") {
		return false
	}
	p := strings.Split(b.Path, "/")
	if len(p) == 3 && p[0] == "attachments" && p[1] == b.ResourceID {
		return validResourceName(p[2])
	}
	if len(p) == 5 && p[0] == "topics" && ValidID(p[1]) && p[2] == "attachments" && p[3] == b.ResourceID {
		return validResourceName(p[4])
	}
	// Snapshot bindings are validated separately against the exact snapshot root.
	return false
}
func validResourceName(name string) bool {
	return len(name) <= 240 && name != "" && name != "." && name != ".." && !strings.ContainsAny(name, "/\\\x00\r\n")
}

func ValidResourceName(name string) bool { return validResourceName(name) }
