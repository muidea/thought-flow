package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	mcevent "github.com/muidea/magicCommon/event"

	"thoughtflow/internal/pkg/markdown"
	"thoughtflow/internal/pkg/models"
	"thoughtflow/internal/pkg/objectlocation"
	"thoughtflow/internal/pkg/topicstore"
)

func (s *Service) registerTopicWorkspaceRoutes() {
	for _, route := range [][2]string{
		{"GET", "/api/workspace/operations"}, {"GET", "/api/topic-imports/:id"}, {"POST", "/api/topic-imports"}, {"POST", "/api/topics/:id/exports"}, {"GET", "/api/topics/:id/exports/:export_id"}, {"POST", "/api/topics/:id/snapshots"}, {"POST", "/api/topics/:id/mode-conversions/preview"}, {"POST", "/api/topics/:id/mode-conversions"}, {"POST", "/api/topics/:id/resources"}, {"GET", "/api/resources/:id/content"}, {"GET", "/api/topics/:id/catalog"}, {"PUT", "/api/topics/:id/catalog"}, {"POST", "/api/topics/:id/entries"}, {"DELETE", "/api/topics/:id/entries/:entry_id"}, {"GET", "/api/topics/:id/entries/:entry_id/content"}, {"POST", "/api/topics/:id/entries/:entry_id/drafts"},
		{"PATCH", "/api/topics/:id/settings"}, {"POST", "/api/topics/:id/index/rebuild"}, {"POST", "/api/topics/:id/migrations/preview"}, {"POST", "/api/topics/:id/migrations"}, {"GET", "/api/topics/:id/activity"}, {"DELETE", "/api/topics/:id"},
		{"POST", "/api/thoughts/:id/restore"}, {"GET", "/api/thoughts/:id/placement"}, {"PATCH", "/api/thoughts/:id/search-policy"}, {"GET", "/api/operations/:id"}, {"POST", "/api/operations/:id/retry"}, {"POST", "/api/objects/:id/migrations/reverse-preview"},
	} {
		s.registry.AddHandler(route[1], route[0], s.handleTopicWorkspace)
	}
}
func workspaceError(w http.ResponseWriter, r *http.Request, e error) {
	status, code := 500, "workspace_failed"
	switch {
	case errors.Is(e, objectlocation.ErrConflict):
		status, code = 409, "generation_conflict"
	case errors.Is(e, objectlocation.ErrOperationConflict):
		status, code = 409, "operation_conflict"
	case errors.Is(e, objectlocation.ErrAmbiguous):
		status, code = 409, "location_ambiguous"
	case errors.Is(e, objectlocation.ErrRecovery):
		status, code = 503, "recovery_required"
	case errors.Is(e, objectlocation.ErrUnsafe):
		status, code = 422, "unsafe_path"
	case errors.Is(e, objectlocation.ErrInvalid), errors.Is(e, topicstore.ErrCollectionOnly), errors.Is(e, topicstore.ErrAggregateOnly):
		status, code = 400, "invalid_binding"
	case errors.Is(e, topicstore.ErrRevisionUnavailable):
		status, code = 422, "required_revision_unavailable"
	case errors.Is(e, os.ErrNotExist):
		status, code = 404, "object_not_found"
	}
	writeError(w, r, status, code, e.Error())
}
func decodeWorkspace(r *http.Request, v any) error {
	d := json.NewDecoder(io.LimitReader(r.Body, 2*1024*1024))
	d.DisallowUnknownFields()
	if e := d.Decode(v); e != nil {
		return objectlocation.ErrInvalid
	}
	var tail any
	if d.Decode(&tail) != io.EOF {
		return objectlocation.ErrInvalid
	}
	return nil
}
func (s *Service) workspaceChanged(ctx context.Context, topic, id, kind string, paths []string) {
	if s.events == nil {
		return
	}
	payload := map[string]any{"topic_id": topic, "object_id": id}
	if topic != "" {
		if t, e := topicstore.New(s.workspace.RootPath).Get(ctx, topic); e == nil {
			payload["topic"] = t
			payload["generation"] = t.Generation
			payload["operation_id"] = t.LastOperationID
			if c, e := topicstore.New(s.workspace.RootPath).Catalog(ctx, topic); e == nil {
				payload["catalog_generation"] = c.Generation
				payload["operation_id"] = c.LastOperationID
			}
		}
	}
	if id != "" {
		if l, e := objectlocation.For(s.workspace.RootPath).Lookup(id); e == nil {
			payload["generation"] = l.Generation
			payload["operation_id"] = l.LastOperationID
		}
	}
	resourceType, resourceID := models.ResourceTypeTopic, topic
	if strings.HasPrefix(kind, "thought.") || topic == "" {
		resourceType = models.ResourceTypeThought
		resourceID = id
	}
	e := models.DomainEvent{EventID: objectlocation.ID("evt-"), EventType: kind, SourceUnit: "topic_workspace", OccurredAt: time.Now().UTC(), WorkspaceID: s.workspace.ID, ResourceType: resourceType, ResourceID: resourceID, PayloadVersion: 1, Payload: payload}
	s.events.Post(mcevent.NewEvent(kind, "topic_workspace", "#", mcevent.NewHeader(), e))
	if kind == "thought.ownership_changed" {
		for _, alias := range []string{"object.placement_changed", "topic.migration_completed"} {
			copy := e
			copy.EventID = objectlocation.ID("evt-")
			copy.EventType = alias
			if alias == "topic.migration_completed" {
				copy.ResourceType = models.ResourceTypeTopic
				copy.ResourceID = topic
			}
			s.events.Post(mcevent.NewEvent(alias, "topic_workspace", "#", mcevent.NewHeader(), copy))
		}
	}
	if len(paths) > 0 {
		e.EventType = models.EventGitCommitRequested
		e.Payload = models.GitCommitRequestedPayload{Paths: paths, Reason: kind, ResourceIDs: []string{topic, id}}
		s.events.Post(mcevent.NewEvent(e.EventType, "topic_workspace", "#", mcevent.NewHeader(), e))
	}
}
func (s *Service) handleTopicWorkspace(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	if s.workspace == nil {
		workspaceError(w, r, objectlocation.ErrRecovery)
		return
	}
	registry := objectlocation.For(s.workspace.RootPath)
	store := topicstore.New(s.workspace.RootPath)
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) == 2 && parts[1] == "topic-imports" {
		raw, e := io.ReadAll(io.LimitReader(r.Body, topicstore.MaxPackageBytes+1))
		if e != nil || len(raw) > topicstore.MaxPackageBytes {
			workspaceError(w, r, objectlocation.ErrInvalid)
			return
		}
		value, e := store.ImportPackage(ctx, r.URL.Query().Get("operation_id"), raw)
		if e != nil {
			workspaceError(w, r, e)
			return
		}
		paths := []string{".content-registry/schema.json"}
		for _, file := range value.Manifest.Files {
			if !file.Private {
				paths = append(paths, file.Path)
			}
		}
		s.workspaceChanged(ctx, value.TopicID, "", "topic.created", paths)
		for _, l := range value.Manifest.Objects {
			s.workspaceChanged(ctx, value.TopicID, l.ObjectID, models.EventThoughtCaptured, nil)
		}
		writeJSON(w, r, 200, value)
		return
	}
	if len(parts) < 3 {
		workspaceError(w, r, objectlocation.ErrInvalid)
		return
	}
	id := parts[2]
	if !objectlocation.ValidID(id) {
		workspaceError(w, r, objectlocation.ErrInvalid)
		return
	}
	var value any
	var err error
	topic := ""
	kind := ""
	paths := []string{}
	switch parts[1] {
	case "workspace":
		if id != "operations" {
			err = objectlocation.ErrInvalid
		} else {
			value, err = registry.Operations()
		}
	case "topic-imports":
		value, err = store.ImportStatus(id)
	case "thoughts":
		if len(parts) != 4 {
			err = objectlocation.ErrInvalid
			break
		}
		if parts[3] == "restore" && r.Method == "POST" {
			var req markdown.RestoreRequest
			if err = decodeWorkspace(r, &req); err == nil {
				value, err = markdown.RestoreThought(ctx, s.workspace.RootPath, id, req)
				kind = "thought.restored"
				paths = append(paths, markdown.GitPaths(s.workspace.RootPath, id)...)
			}
		} else if parts[3] == "placement" && r.Method == "GET" {
			value, err = registry.Resolve(ctx, id)
		} else if parts[3] == "search-policy" && r.Method == "PATCH" {
			var req models.SearchPolicyRequest
			if err = decodeWorkspace(r, &req); err == nil {
				value, err = registry.SetSearchPolicy(id, req)
				paths = []string{objectlocation.RecordPath(id)}
				kind = "thought.search_policy_updated"
			}
		} else {
			err = objectlocation.ErrInvalid
		}
	case "resources":
		if len(parts) != 4 || parts[3] != "content" {
			err = objectlocation.ErrInvalid
			break
		}
		var resource models.TopicResource
		var raw []byte
		if set := r.URL.Query().Get("set_id"); set != "" {
			if s.documentSets == nil {
				err = objectlocation.ErrInvalid
			} else {
				raw, resource.MIME, err = s.documentSets.Store.Resource(set, r.URL.Query().Get("set_revision"), r.URL.Query().Get("member_key"), id)
			}
		} else if snapshot := r.URL.Query().Get("snapshot_id"); snapshot != "" {
			var path string
			raw, path, err = store.ReadSnapshotResource(ctx, r.URL.Query().Get("topic_id"), snapshot, id)
			resource.MIME = mime.TypeByExtension(filepath.Ext(path))
		} else {
			resource, raw, err = registry.ReadResource(ctx, id, r.URL.Query().Get("object_id"))
		}
		if err == nil {
			w.Header().Set("Content-Type", resource.MIME)
			w.Header().Set("X-Content-Type-Options", "nosniff")
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(200)
			_, _ = w.Write(raw)
			return
		}
	case "operations":
		value, err = registry.Operation(id)
		if err == nil && r.Method == "GET" && value.(models.WorkspaceOperation).Kind == "import" {
			value, err = store.ImportStatus(id)
		}
		if len(parts) == 4 && parts[3] == "retry" && r.Method == "POST" && err == nil {
			op := value.(models.WorkspaceOperation)
			switch op.Kind {
			case "migration":
				value, err = registry.RetryMigration(id)
			case "import":
				value, err = store.RetryImport(ctx, id)
			default:
				err = objectlocation.ErrInvalid
			}
		}
	case "objects":
		if len(parts) == 5 && parts[3] == "migrations" && parts[4] == "reverse-preview" {
			var req struct {
				TargetTopicID string `json:"target_topic_id"`
			}
			if err = decodeWorkspace(r, &req); err == nil {
				value, err = registry.PreviewMigration(ctx, id, req.TargetTopicID)
			}
		} else {
			err = objectlocation.ErrInvalid
		}
	case "topics":
		topic = id
		if len(parts) == 3 && r.Method == "DELETE" {
			var req topicstore.DeleteCollectionRequest
			if err = decodeWorkspace(r, &req); err == nil {
				err = store.DeleteCollectionCAS(ctx, id, req)
			}
			paths = append(paths, ".content-registry/topic-deletions/"+id+".json", "topics/"+id+"/topic.yaml", "topics/"+id+"/catalog.json", "topics/"+id+"/catalog.md", "topics/"+id+"/index.md", "topics/"+id+"/storage.snapshot.json")
			value = map[string]bool{"deleted": err == nil}
			kind = "topic.deleted"
			break
		}
		if len(parts) < 4 {
			err = objectlocation.ErrInvalid
			break
		}
		switch parts[3] {
		case "exports":
			if r.Method == "GET" && len(parts) == 5 {
				if r.URL.Query().Get("download") == "1" {
					var raw []byte
					raw, err = store.ExportBytes(id, parts[4])
					if err == nil {
						w.Header().Set("Content-Type", "application/zip")
						w.Header().Set("Content-Disposition", "attachment; filename=thoughtflow-"+parts[4]+".zip")
						w.Header().Set("Cache-Control", "no-store")
						_, _ = w.Write(raw)
						return
					}
				} else {
					value, err = store.ExportResult(id, parts[4])
				}
			} else {
				var req topicstore.ExportRequest
				if err = decodeWorkspace(r, &req); err == nil {
					value, err = store.ExportPackage(ctx, id, req)
				}
			}
		case "mode-conversions":
			if len(parts) == 5 && parts[4] == "preview" {
				var req struct {
					Mode string `json:"mode"`
				}
				if err = decodeWorkspace(r, &req); err == nil {
					value, err = store.PreviewConversion(ctx, id, req.Mode)
				}
			} else {
				var req topicstore.ModeConversionRequest
				if err = decodeWorkspace(r, &req); err == nil {
					value, err = store.ConvertMode(ctx, id, req)
					kind = "topic.mode_changed"
				}
			}
		case "snapshots":
			var req topicstore.SnapshotRequest
			if err = decodeWorkspace(r, &req); err == nil {
				value, err = store.CreateSnapshot(ctx, id, req)
				kind = "topic.snapshot_created"
			}
		case "resources":
			var req struct {
				OperationID        string `json:"operation_id"`
				ObjectID           string `json:"object_id"`
				ExpectedGeneration uint64 `json:"expected_generation"`
				Name               string `json:"name"`
				Content            []byte `json:"content"`
			}
			d := json.NewDecoder(io.LimitReader(r.Body, 45*1024*1024))
			if e := d.Decode(&req); e != nil {
				err = objectlocation.ErrInvalid
				break
			}
			var resource models.TopicResource
			resource, err = registry.UploadResource(ctx, id, req.ObjectID, req.OperationID, req.Name, req.ExpectedGeneration, req.Content)
			value = resource
			kind = "topic.resource_updated"
			if err == nil {
				paths = append(paths, resource.Path, ".content-registry/resources/"+resource.ID+".json", objectlocation.RecordPath(req.ObjectID))
				if b, e := registry.Lookup(req.ObjectID); e == nil && b.ResourceBindingsRef != nil {
					paths = append(paths, b.ResourceBindingsRef.Path)
				}
			}
		case "catalog":
			if r.Method == "GET" {
				value, err = store.CatalogView(ctx, id)
			} else {
				var req models.CatalogWriteRequest
				if err = decodeWorkspace(r, &req); err == nil {
					value, err = store.WriteCatalog(ctx, id, req)
					kind = "topic.catalog_updated"
				}
			}
		case "settings":
			var req models.TopicSettingsRequest
			if err = decodeWorkspace(r, &req); err == nil {
				value, err = store.Settings(ctx, id, req)
				kind = "topic.search_policy_updated"
			}
		case "activity":
			value, err = registry.Activities(id)
		case "index":
			value, err = store.RebuildCatalog(ctx, id)
			kind = "topic.catalog_updated"
		case "migrations":
			if len(parts) == 5 && parts[4] == "preview" {
				var req struct {
					ObjectID      string  `json:"object_id"`
					TargetTopicID *string `json:"target_topic_id,omitempty"`
				}
				if err = decodeWorkspace(r, &req); err == nil {
					target := id
					if req.TargetTopicID != nil {
						target = *req.TargetTopicID
					}
					value, err = registry.PreviewMigration(ctx, req.ObjectID, target)
				}
			} else {
				var req models.MigrationRequest
				if err = decodeWorkspace(r, &req); err == nil {
					var op models.WorkspaceOperation
					op, err = registry.ExecuteMigration(ctx, req)
					value = op
					kind = "thought.ownership_changed"
					if err == nil {
						paths = registry.MigrationGitPaths(op.ID)
					}
				}
			}
		case "entries":
			if len(parts) == 6 && parts[5] == "content" {
				value, err = store.EntryContent(ctx, id, parts[4], r.URL.Query().Get("view"), r.URL.Query().Get("revision"))
				break
			}
			if len(parts) == 6 && parts[5] == "drafts" {
				value, err = s.createCatalogDraft(ctx, r, store, id, parts[4])
				kind = "topic.catalog_updated"
				break
			}
			var req struct {
				OperationID        string              `json:"operation_id"`
				ExpectedGeneration uint64              `json:"expected_generation"`
				Entry              models.CatalogEntry `json:"entry"`
			}
			if err = decodeWorkspace(r, &req); err != nil {
				break
			}
			var c models.TopicCatalog
			c, err = store.Catalog(ctx, id)
			if err != nil {
				break
			}
			entries := []models.CatalogEntry{}
			for _, entry := range c.Entries {
				if len(parts) == 5 && entry.ID == parts[4] && r.Method == "DELETE" {
					continue
				}
				entries = append(entries, entry)
			}
			if r.Method == "POST" {
				entries = append(entries, req.Entry)
			}
			value, err = store.WriteCatalog(ctx, id, models.CatalogWriteRequest{OperationID: req.OperationID, ExpectedGeneration: req.ExpectedGeneration, Groups: c.Groups, Entries: entries})
			kind = "topic.catalog_updated"
		default:
			err = objectlocation.ErrInvalid
		}
	default:
		err = objectlocation.ErrInvalid
	}
	if err != nil {
		if op, ok := value.(models.WorkspaceOperation); ok && op.Kind == "migration" && op.State == "failed" && s.events != nil {
			failure := models.DomainEvent{EventID: objectlocation.ID("evt-"), EventType: "topic.migration_failed", SourceUnit: "topic_workspace", OccurredAt: time.Now().UTC(), WorkspaceID: s.workspace.ID, ResourceType: models.ResourceTypeTopic, ResourceID: topic, PayloadVersion: 1, Payload: map[string]any{"operation_id": op.ID, "object_id": op.ObjectID, "topic_id": topic, "state": "failed", "generation": op.BeforeGeneration}}
			s.events.Post(mcevent.NewEvent(failure.EventType, "topic_workspace", "#", mcevent.NewHeader(), failure))
		}
		workspaceError(w, r, err)
		return
	}
	if snapshot, ok := value.(models.TopicSnapshot); ok {
		base := "topics/" + topic + "/references/" + snapshot.ID
		paths = append(paths, base+"/snapshot.json", base+"/body.md")
		for _, b := range snapshot.Resources {
			if !b.Private {
				paths = append(paths, b.Path)
			}
		}
	}
	if kind != "" {
		if topic != "" {
			if p, e := store.TopicPaths(ctx, topic); e == nil {
				paths = append(paths, p...)
			}
		}
		objectID := ""
		switch v := value.(type) {
		case models.ObjectLocation:
			objectID = v.ObjectID
		case models.WorkspaceOperation:
			objectID = v.ObjectID
		}
		s.workspaceChanged(ctx, topic, objectID, kind, paths)
	}
	writeJSON(w, r, 200, value)
}
func (s *Service) createCatalogDraft(ctx context.Context, r *http.Request, store *topicstore.Store, topic, entryID string) (any, error) {
	var req struct {
		OperationID        string                    `json:"operation_id"`
		ExpectedGeneration uint64                    `json:"expected_generation"`
		Profile            models.DocumentProfileRef `json:"profile"`
		Parameters         map[string]string         `json:"parameters"`
		Content            string                    `json:"content"`
	}
	if e := decodeWorkspace(r, &req); e != nil {
		return nil, e
	}
	registry := objectlocation.For(s.workspace.RootPath)
	// Persist the reserved thought identity before capture; a failed catalog CAS
	// leaves an owned, discoverable uncatalogued object rather than an orphan.
	registry.Namespace.Lock()
	thoughtID := "draft-" + strings.TrimPrefix(objectlocation.Hash([]byte(req.OperationID)), "sha256:")[:32]
	op, e := registry.Begin(req.OperationID, "entry_draft", thoughtID, topic, struct {
		EntryID string
		Request any
	}{entryID, req})
	if e != nil {
		registry.Namespace.Unlock()
		return nil, e
	}
	c, e := store.Catalog(ctx, topic)
	if e != nil {
		registry.Namespace.Unlock()
		return nil, e
	}
	var entry *models.CatalogEntry
	for i := range c.Entries {
		if c.Entries[i].ID == entryID {
			entry = &c.Entries[i]
		}
	}
	if entry == nil {
		registry.Namespace.Unlock()
		return nil, os.ErrNotExist
	}
	if op.State == "completed" {
		registry.Namespace.Unlock()
		return store.CatalogView(ctx, topic)
	}
	if entry.Binding.Kind == "thought" && entry.Binding.ThoughtID == thoughtID {
		registry.Namespace.Unlock()
		if e = registry.Complete(op); e != nil {
			return nil, e
		}
		return store.CatalogView(ctx, topic)
	}
	if op.State != "completed" {
		if c.Generation != req.ExpectedGeneration || entry.Binding.Kind != "planned" {
			registry.Namespace.Unlock()
			return nil, objectlocation.ErrConflict
		}
		if e = registry.Prepare(op); e != nil {
			registry.Namespace.Unlock()
			return nil, e
		}
	}
	registry.Namespace.Unlock()
	if s.captureService == nil {
		return nil, objectlocation.ErrRecovery
	}
	_, e = s.captureService.Capture(ctx, models.CaptureCommand{ThoughtID: op.ObjectID, OwnerTopicID: topic, Source: models.ThoughtSourceCompose, Type: models.ThoughtTypeText, Title: entry.Title, Content: req.Content, DocumentProfile: &req.Profile, DocumentParameters: req.Parameters})
	if e != nil {
		return nil, e
	}
	entry.Binding = models.CatalogBinding{Kind: "thought", ThoughtID: op.ObjectID, VersionPolicy: "latest_finalized"}
	view, e := store.WriteCatalog(ctx, topic, models.CatalogWriteRequest{OperationID: "catalog-" + strings.TrimPrefix(objectlocation.Hash([]byte(req.OperationID)), "sha256:")[:32], ExpectedGeneration: req.ExpectedGeneration, RequiredDeliveryLevel: c.RequiredDeliveryLevel, Groups: c.Groups, Entries: c.Entries})
	if e != nil {
		return nil, e
	}
	return view, registry.Complete(op)
}

func (s *Service) replayWorkspaceOutbox() {
	if s.stream == nil || s.workspace == nil {
		return
	}
	ops, e := objectlocation.For(s.workspace.RootPath).Activities("*")
	if e != nil {
		return
	}
	for _, op := range ops {
		if op.Kind == "capture_save" && op.State == "completed" && s.captureService != nil {
			_ = s.captureService.ReplayCaptureSave(op.ID)
			continue
		}
		if op.EventType == "" || op.EventType == "object.reserved" {
			continue
		}
		resourceType, resourceID := models.ResourceTypeTopic, op.TopicID
		if op.ObjectID != "" {
			resourceType = models.ResourceTypeThought
			resourceID = op.ObjectID
		}
		s.stream.Publish(models.DomainEvent{EventID: op.ID + "-" + op.State, EventType: op.EventType, SourceUnit: "workspace_recovery", OccurredAt: op.UpdatedAt, WorkspaceID: s.workspace.ID, ResourceType: resourceType, ResourceID: resourceID, PayloadVersion: 1, Payload: map[string]any{"operation_id": op.ID, "topic_id": op.TopicID, "object_id": op.ObjectID, "generation": op.AfterGeneration, "state": op.State}})
		if op.State != "completed" || s.events == nil {
			continue
		}
		paths := []string{".content-registry/schema.json"}
		if op.TargetPath != "" {
			paths = append(paths, op.TargetPath)
		}
		store := topicstore.New(s.workspace.RootPath)
		if op.TopicID != "" {
			if p, e := store.TopicPaths(context.Background(), op.TopicID); e == nil {
				paths = append(paths, p...)
			}
		}
		if op.ObjectID != "" {
			paths = append(paths, markdown.GitPaths(s.workspace.RootPath, op.ObjectID)...)
		}
		if op.Kind == "migration" {
			paths = append(paths, objectlocation.For(s.workspace.RootPath).MigrationGitPaths(op.ID)...)
		}
		if op.Kind == "import" {
			if result, e := store.ImportStatus(op.ID); e == nil {
				for _, file := range result.Manifest.Files {
					if !file.Private {
						paths = append(paths, file.Path)
					}
				}
			}
		}
		e := models.DomainEvent{EventID: objectlocation.ID("evt-"), EventType: models.EventGitCommitRequested, SourceUnit: "workspace_recovery", OccurredAt: time.Now().UTC(), ResourceType: resourceType, ResourceID: resourceID, WorkspaceID: s.workspace.ID, PayloadVersion: 1, Payload: models.GitCommitRequestedPayload{Paths: paths, Reason: "workspace.recovered", ResourceIDs: []string{resourceID}}}
		s.events.Post(mcevent.NewEvent(e.EventType, "workspace_recovery", "#", mcevent.NewHeader(), e))
	}
}
