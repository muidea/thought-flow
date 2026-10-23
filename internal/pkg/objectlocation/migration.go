package objectlocation

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"thoughtflow/internal/pkg/models"
)

type migrationFile struct {
	Source       string `json:"source"`
	Target       string `json:"target"`
	Hash         string `json:"hash"`
	Size         int64  `json:"size"`
	Private      bool   `json:"private"`
	RetainSource bool   `json:"retain_source,omitempty"`
}
type migrationState struct {
	Plan           models.MigrationPlan      `json:"plan"`
	Operation      models.WorkspaceOperation `json:"operation"`
	Phase          string                    `json:"phase"`
	Bindings       *models.ResourceBindings  `json:"bindings,omitempty"`
	Files          []migrationFile           `json:"files"`
	UpdatedAt      time.Time                 `json:"updated_at"`
	CreatedTargets []string                  `json:"created_targets,omitempty"`
}

func migrationPath(id string) string {
	return ".content-registry/private/migrations/" + id + "/state.json"
}
func (r *Registry) migrationFiles(source, target models.ObjectLocation) ([]migrationFile, error) {
	files := []migrationFile{}
	var total int64
	appendFile := func(from, to string, private bool) error {
		path, e := SafePath(r.Root, from)
		if e != nil {
			return e
		}
		info, e := os.Stat(path)
		if e != nil {
			return e
		}
		if !info.Mode().IsRegular() || info.Size() > MaxResourceBytes || len(files) >= 5000 {
			return ErrUnsafe
		}
		total += info.Size()
		if total > 512*1024*1024 || strings.Count(from, "/") > 16 || strings.Count(to, "/") > 16 {
			return ErrUnsafe
		}
		hash, size, e := FileHash(path)
		if e != nil {
			return e
		}
		files = append(files, migrationFile{Source: from, Target: to, Hash: hash, Size: size, Private: private})
		return nil
	}
	if e := appendFile(source.Placement.ThoughtPath, target.Placement.ThoughtPath, false); e != nil {
		return nil, e
	}
	for _, pair := range [][3]string{{source.Placement.DocumentRoot, target.Placement.DocumentRoot, ""}, {source.Placement.EvidenceRoot, target.Placement.EvidenceRoot, "private"}} {
		if pair[0] == "" {
			continue
		}
		base, e := SafePath(r.Root, pair[0])
		if e != nil {
			return nil, e
		}
		e = filepath.WalkDir(base, func(path string, d fs.DirEntry, e error) error {
			if os.IsNotExist(e) {
				return nil
			}
			if e != nil {
				return e
			}
			if d.Type()&os.ModeSymlink != 0 {
				return ErrUnsafe
			}
			if d.IsDir() {
				return nil
			}
			rel, _ := filepath.Rel(base, path)
			if pair[1] == "" {
				return ErrRecovery
			}
			return appendFile(pair[0]+"/"+filepath.ToSlash(rel), pair[1]+"/"+filepath.ToSlash(rel), pair[2] != "")
		})
		if e != nil {
			return nil, e
		}
	}
	bindings, e := r.relativeBindings(source)
	if e != nil {
		return nil, e
	}
	seenPaths := map[string]bool{}
	for _, b := range bindings.Bindings {
		if seenPaths[b.Path] {
			continue
		}
		seenPaths[b.Path] = true
		if b.Private {
			return nil, ErrUnsafe
		}
		targetPath := resourceMigrationPath(target.OwnerTopicID, b)
		if e = appendFile(b.Path, targetPath, false); e != nil {
			return nil, e
		}
		files[len(files)-1].RetainSource = true
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Source < files[j].Source })
	return files, nil
}
func (r *Registry) PreviewMigration(ctx context.Context, id, targetTopic string) (models.MigrationPlan, error) {
	unlock := r.ReadLease(id)
	defer unlock()
	source, e := r.Resolve(ctx, id)
	if e != nil {
		return models.MigrationPlan{}, e
	}
	if targetTopic != "" {
		t, e := r.Topic(targetTopic)
		if e != nil {
			return models.MigrationPlan{}, e
		}
		if t.Mode != models.TopicModeCollection {
			return models.MigrationPlan{}, ErrInvalid
		}
	}
	if source.OwnerTopicID == targetTopic {
		return models.MigrationPlan{}, ErrConflict
	}
	target := proposed(id, targetTopic)
	target.Generation = source.Generation + 1
	target.Lifecycle = "active"
	target.GlobalSearch = source.GlobalSearch
	target.ResourceBindingsRef = source.ResourceBindingsRef
	bindings, e := r.migratedBindings(source, target.OwnerTopicID)
	if e != nil {
		return models.MigrationPlan{}, e
	}
	if len(bindings.Bindings) > 0 {
		target.ResourceBindingsRef = bindingRef(bindings)
	}
	files, e := r.migrationFiles(source, target)
	if e != nil {
		return models.MigrationPlan{}, e
	}
	p := models.MigrationPlan{ID: ID("preview-"), ObjectID: id, TargetTopicID: targetTopic, Source: source, Target: target, BeforeGlobal: r.GlobalEligible(source), AfterGlobal: r.GlobalEligible(target), CreatedAt: time.Now().UTC(), Conflicts: []string{}, Missing: []string{}, ResourceHandling: "preserve immutable bytes and resource bindings"}
	for _, f := range files {
		p.Files = append(p.Files, models.FileDigest{Path: f.Source, Hash: f.Hash, Size: f.Size, Private: f.Private})
		p.TotalSize += f.Size
		path, e := SafePath(r.Root, f.Target)
		if e != nil {
			return p, e
		}
		if _, e = os.Stat(path); e == nil {
			if !r.reusableMigrationResource(f) {
				p.Conflicts = append(p.Conflicts, f.Target)
			}
		} else if !os.IsNotExist(e) {
			return p, e
		}
	}
	available, e := AvailableBytes(r.Root)
	if e != nil {
		return p, e
	}
	p.AvailableBytes = available
	if uint64(p.TotalSize)*3 > available {
		p.Conflicts = append(p.Conflicts, "insufficient_space")
	}
	p.PlanHash = HashJSON(p)
	return p, WriteJSON(r.Root, ".content-registry/private/previews/"+p.ID+".json", p, true)
}
func (r *Registry) ExecuteMigration(ctx context.Context, req models.MigrationRequest) (models.WorkspaceOperation, error) {
	if !ValidID(req.PreviewID) || !ValidID(req.OperationID) {
		return models.WorkspaceOperation{}, ErrInvalid
	}
	var p models.MigrationPlan
	if e := ReadJSON(r.Root, ".content-registry/private/previews/"+req.PreviewID+".json", &p); e != nil {
		return models.WorkspaceOperation{}, e
	}
	hash := p.PlanHash
	p.PlanHash = ""
	if hash != HashJSON(p) || hash != req.PlanHash || p.Source.Generation != req.ExpectedObjectGeneration || len(p.Conflicts) > 0 || len(p.Missing) > 0 {
		return models.WorkspaceOperation{}, ErrConflict
	}
	p.PlanHash = hash
	finish := r.lockMigration(p.ObjectID)
	defer finish()
	r.Namespace.Lock()
	unlock := r.LockObject(p.ObjectID)
	var state migrationState
	op, e := func() (models.WorkspaceOperation, error) {
		defer func() { unlock(); r.Namespace.Unlock() }()
		op, e := r.Begin(req.OperationID, "migration", p.ObjectID, p.TargetTopicID, req)
		if e != nil {
			return op, e
		}
		if op.State == "completed" {
			return op, nil
		}
		if op.State == "conflicted" {
			return op, ErrConflict
		}
		e = ReadJSON(r.Root, migrationPath(op.ID), &state)
		if os.IsNotExist(e) {
			l, e := r.Resolve(ctx, p.ObjectID)
			if e != nil {
				return op, e
			}
			if HashJSON(l) != HashJSON(p.Source) {
				return op, ErrConflict
			}
			files, e := r.migrationFiles(p.Source, p.Target)
			if e != nil {
				return op, e
			}
			bindings, e := r.migratedBindings(p.Source, p.TargetTopicID)
			if e != nil {
				return op, e
			}
			state = migrationState{Plan: p, Operation: op, Phase: "prepared", Files: files, Bindings: &bindings}
			if e = r.saveMigration(&state); e != nil {
				return op, e
			}
		} else if e != nil {
			return op, e
		}
		return op, nil
	}()
	if e != nil || op.State == "completed" {
		if errors.Is(e, ErrConflict) && op.Kind == "migration" && op.RequestHash == HashJSON(req) {
			op.State, op.Error = "conflicted", e.Error()
			if err := r.failMigration(op); err != nil {
				return op, errors.Join(e, err)
			}
		}
		return op, e
	}
	e = r.runMigration(&state)
	if e != nil {
		if err := r.recordMigrationFailure(&state, e); err != nil {
			return state.Operation, errors.Join(e, err)
		}
		return state.Operation, e
	}
	return state.Operation, nil
}
func (r *Registry) fault(phase string) error {
	if r.Fault != nil {
		return r.Fault(phase)
	}
	return nil
}
func (r *Registry) saveMigration(s *migrationState) error {
	now := time.Now().UTC()
	previous := s.UpdatedAt
	if previous.IsZero() {
		previous = s.Operation.CreatedAt
	}
	if !previous.IsZero() {
		r.SetMetric("migration_"+s.Phase+"_seconds", now.Sub(previous).Seconds())
	}
	s.UpdatedAt = now
	return WriteJSON(r.Root, migrationPath(s.Operation.ID), s, true)
}

// runMigration requires the per-object migration worker lock, and acquires
// namespace/object locks only for validation and commit. Private staging may be
// large; unrelated writes and edits of the source remain possible during copying.
func (r *Registry) runMigration(s *migrationState) error {
	base := ".content-registry/private/migrations/" + s.Operation.ID
	lease := r.ReadLease(s.Plan.ObjectID)
	l, err := r.Lookup(s.Plan.ObjectID)
	lease()
	if err != nil {
		return err
	}
	committed := l.LastOperationID == s.Operation.ID && l.Generation == s.Plan.Target.Generation
	if !committed {
		if HashJSON(l) != HashJSON(s.Plan.Source) {
			return ErrConflict
		}
		if err = r.fault("migration_copying"); err != nil {
			return err
		}
		for i, f := range s.Files {
			path, e := SafePath(r.Root, f.Source)
			if e != nil {
				return e
			}
			raw, e := ReadFileBounded(path, MaxResourceBytes)
			if e != nil {
				return e
			}
			if Hash(raw) != f.Hash || int64(len(raw)) != f.Size {
				return ErrConflict
			}
			if e = WriteFile(r.Root, base+"/stage/"+stageName(i), raw, 0600); e != nil {
				return e
			}
		}
		s.Phase = "staged"
		if e := r.saveMigration(s); e != nil {
			return e
		}
		if e := r.fault("migration_staged"); e != nil {
			return e
		}
	}
	r.Namespace.Lock()
	defer r.Namespace.Unlock()
	unlock := r.LockObject(s.Plan.ObjectID)
	defer unlock()
	return r.resumeMigration(s)
}

func (r *Registry) resumeMigration(s *migrationState) error {
	base := ".content-registry/private/migrations/" + s.Operation.ID
	l, e := r.Lookup(s.Plan.ObjectID)
	if e != nil {
		return e
	}
	committed := l.LastOperationID == s.Operation.ID && l.Generation == s.Plan.Target.Generation
	if !committed {
		if HashJSON(l) != HashJSON(s.Plan.Source) {
			return ErrConflict
		}
		if s.Plan.TargetTopicID != "" {
			topic, err := r.Topic(s.Plan.TargetTopicID)
			if err != nil || topic.Mode != models.TopicModeCollection {
				return ErrConflict
			}
		}
		if r.GlobalEligible(l) != s.Plan.BeforeGlobal || r.GlobalEligible(s.Plan.Target) != s.Plan.AfterGlobal {
			return ErrConflict
		}
		files, err := r.migrationFiles(l, s.Plan.Target)
		if err != nil {
			return err
		}
		bindings, err := r.migratedBindings(l, s.Plan.TargetTopicID)
		if err != nil {
			return err
		}
		if HashJSON(files) != HashJSON(s.Files) || HashJSON(bindings) != HashJSON(s.Bindings) {
			return ErrConflict
		}
		// Revalidate both the live bytes and the frozen preview. An external edit
		// invalidates the plan even after an interrupted staging operation.
		for _, f := range s.Files {
			path, e := SafePath(r.Root, f.Source)
			if e != nil {
				return e
			}
			hash, size, e := FileHash(path)
			if e != nil {
				return e
			}
			if hash != f.Hash || size != f.Size {
				return ErrConflict
			}
			found := false
			for _, expected := range s.Plan.Files {
				if expected.Path == f.Source && expected.Hash == f.Hash && expected.Size == f.Size {
					found = true
					break
				}
			}
			if !found {
				return ErrConflict
			}
		}
		for i, f := range s.Files {
			target, e := SafePath(r.Root, f.Target)
			if e != nil {
				return e
			}
			if hash, _, e := FileHash(target); e == nil {
				if hash != f.Hash || (!f.RetainSource && !s.createdTarget(f.Target)) {
					return ErrConflict
				}
			} else if !os.IsNotExist(e) {
				return e
			} else {
				stage, e := SafePath(r.Root, base+"/stage/"+stageName(i))
				if e != nil {
					return e
				}
				raw, e := ReadFileBounded(stage, MaxResourceBytes)
				if e != nil {
					return e
				}
				if Hash(raw) != f.Hash {
					return ErrRecovery
				}
				mode := os.FileMode(0644)
				dirMode := os.FileMode(0755)
				if f.Private {
					mode = 0600
					dirMode = 0700
				}
				if e = os.MkdirAll(filepath.Dir(target), dirMode); e != nil {
					return e
				}
				if e = os.Chmod(stage, mode); e != nil {
					return e
				}
				// Record ownership before publication so recovery never removes an
				// unrelated identical destination that existed before this operation.
				if !s.createdTarget(f.Target) {
					s.CreatedTargets = append(s.CreatedTargets, f.Target)
					if e = r.saveMigration(s); e != nil {
						return e
					}
				}
				if e = os.Rename(stage, target); e != nil {
					return e
				}
				if e = SyncDir(filepath.Dir(target)); e != nil {
					return e
				}
				if e = SyncDir(filepath.Dir(stage)); e != nil {
					return e
				}
			}
		}
		s.Phase = "promoted"
		if e = r.saveMigration(s); e != nil {
			return e
		}
		if e = r.fault("migration_promoted"); e != nil {
			return e
		}
		for _, f := range s.Files {
			path, e := SafePath(r.Root, f.Source)
			if e != nil {
				return e
			}
			hash, _, e := FileHash(path)
			if e != nil || hash != f.Hash {
				return ErrConflict
			}
		}
		target := s.Plan.Target
		if s.Bindings != nil && len(s.Bindings.Bindings) > 0 {
			ref := bindingRef(*s.Bindings)
			if target.ResourceBindingsRef == nil || *ref != *target.ResourceBindingsRef {
				return ErrConflict
			}
			for _, b := range s.Bindings.Bindings {
				var resource models.TopicResource
				if e = ReadJSON(r.Root, resourcePath(b.ResourceID), &resource); os.IsNotExist(e) {
					path, e := SafePath(r.Root, b.Path)
					if e != nil {
						return e
					}
					raw, e := ReadFileBounded(path, MaxResourceBytes)
					if e != nil {
						return e
					}
					mime, e := sniffResource(filepath.Base(b.Path), raw)
					if e != nil {
						return e
					}
					resource = models.TopicResource{ID: b.ResourceID, TopicID: target.OwnerTopicID, Name: filepath.Base(b.Path), Path: b.Path, MIME: mime, Hash: b.Hash, Size: int64(len(raw)), Shareable: true, ObjectIDs: []string{target.ObjectID}}
					if e = WriteJSON(r.Root, resourcePath(b.ResourceID), resource, false); e != nil {
						return e
					}
				} else if e != nil {
					return e
				}
			}
			if e = WriteJSON(r.Root, ref.Path, s.Bindings, false); e != nil {
				return e
			}
		}
		target.LastOperationID = s.Operation.ID
		s.Operation.TargetPath = RecordPath(target.ObjectID)
		s.Operation.CandidateHash = HashJSON(target)
		s.Operation.BeforeGeneration = l.Generation
		s.Operation.AfterGeneration = target.Generation
		s.Operation.EventType = "thought.ownership_changed"
		if e = r.Prepare(s.Operation); e != nil {
			return e
		}
		if e = WriteJSON(r.Root, RecordPath(target.ObjectID), target, false); e != nil {
			return e
		}
		r.BumpEpoch()
		s.Phase = "committed"
		if e = r.saveMigration(s); e != nil {
			return e
		}
		if e = r.fault("migration_committed"); e != nil {
			return e
		}
	}
	// Never overwrite a committed target during recovery. Preserve old bytes in
	// private recovery storage before retiring the source, independently per file.
	for _, f := range s.Files {
		if f.RetainSource {
			continue
		}
		target, e := SafePath(r.Root, f.Target)
		if e != nil {
			return e
		}
		if _, e = os.Stat(target); e != nil {
			return errors.Join(ErrRecovery, e)
		}
		source, e := SafePath(r.Root, f.Source)
		if e != nil {
			return e
		}
		raw, e := ReadFileBounded(source, MaxResourceBytes)
		if os.IsNotExist(e) {
			continue
		}
		if e != nil {
			return e
		}
		if Hash(raw) != f.Hash {
			return ErrRecovery
		}
		retired, e := SafePath(r.Root, base+"/retired/"+f.Source)
		if e != nil {
			return e
		}
		if e = os.MkdirAll(filepath.Dir(retired), 0700); e != nil {
			return e
		}
		if e = os.Chmod(source, 0600); e != nil {
			return e
		}
		if e = os.Rename(source, retired); e != nil {
			return e
		}
		if e = SyncDir(filepath.Dir(retired)); e != nil {
			return e
		}
		if e = SyncDir(filepath.Dir(source)); e != nil {
			return e
		}
	}
	s.Phase = "retired"
	if e = r.saveMigration(s); e != nil {
		return e
	}
	if e = r.fault("migration_retired"); e != nil {
		return e
	}
	if e = r.Complete(s.Operation); e != nil {
		return e
	}
	r.SetMetric("migration_duration_seconds", time.Since(s.Operation.CreatedAt).Seconds())
	s.Operation.State = "completed"
	s.Operation.Error = ""
	s.Phase = "completed"
	return r.saveMigration(s)
}
func stageName(i int) string { return strings.Repeat("0", 6-len(fmtInt(i))) + fmtInt(i) }
func fmtInt(i int) string {
	if i == 0 {
		return "0"
	}
	out := ""
	for i > 0 {
		out = string(rune('0'+i%10)) + out
		i /= 10
	}
	return out
}
func (r *Registry) RecoverMigrations() error {
	dir, e := SafePath(r.Root, ".content-registry/private/migrations")
	if e != nil {
		return e
	}
	entries, e := os.ReadDir(dir)
	if os.IsNotExist(e) {
		return nil
	}
	if e != nil {
		return e
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		var s migrationState
		if e = ReadJSON(r.Root, migrationPath(entry.Name()), &s); e != nil {
			return e
		}
		if s.Phase == "conflicted" {
			continue
		}
		if s.Phase == "completed" {
			if time.Since(s.UpdatedAt) >= 7*24*time.Hour {
				for _, name := range []string{"stage", "retired"} {
					path, err := SafePath(r.Root, ".content-registry/private/migrations/"+s.Operation.ID+"/"+name)
					if err != nil {
						return err
					}
					if err = os.RemoveAll(path); err != nil {
						return err
					}
				}
			}
			continue
		}
		unlock := r.lockMigration(s.Plan.ObjectID)
		e = r.runMigration(&s)
		if e != nil {
			committed := r.migrationCommitted(s)
			failureErr := r.recordMigrationFailure(&s, e)
			unlock()
			if failureErr != nil || committed {
				return errors.Join(e, failureErr)
			}
			// Uncommitted work has not changed authority. A stale preview or
			// temporary I/O failure remains inspectable without sealing startup.
			continue
		}
		unlock()
	}
	return nil
}

func (r *Registry) MigrationGitPaths(id string) []string {
	var s migrationState
	if !ValidID(id) || ReadJSON(r.Root, migrationPath(id), &s) != nil {
		return nil
	}
	paths := []string{RecordPath(s.Plan.ObjectID)}
	if s.Plan.Target.ResourceBindingsRef != nil {
		paths = append(paths, s.Plan.Target.ResourceBindingsRef.Path)
	}
	if s.Bindings != nil {
		for _, b := range s.Bindings.Bindings {
			if !b.Private {
				paths = append(paths, resourcePath(b.ResourceID))
			}
		}
	}
	for _, f := range s.Files {
		if !f.Private {
			paths = append(paths, f.Source, f.Target)
		}
	}
	return paths
}

func (r *Registry) RetryMigration(id string) (models.WorkspaceOperation, error) {
	if !ValidID(id) {
		return models.WorkspaceOperation{}, ErrInvalid
	}
	var s migrationState
	if e := ReadJSON(r.Root, migrationPath(id), &s); e != nil {
		return s.Operation, e
	}
	unlock := r.lockMigration(s.Plan.ObjectID)
	defer unlock()
	// Another worker may have completed while we waited for coordination.
	if e := ReadJSON(r.Root, migrationPath(id), &s); e != nil {
		return s.Operation, e
	}
	if s.Phase == "completed" {
		return s.Operation, nil
	}
	if s.Phase == "conflicted" {
		return s.Operation, ErrConflict
	}
	e := r.runMigration(&s)
	if e != nil {
		if err := r.recordMigrationFailure(&s, e); err != nil {
			return s.Operation, errors.Join(e, err)
		}
	}
	return s.Operation, e
}

func (r *Registry) failMigration(op models.WorkspaceOperation) error {
	r.AddMetric("migration_failures_total", 1)
	if e := r.Prepare(op); e != nil {
		return e
	}
	op.EventType = "topic.migration_failed"
	return WriteJSON(r.Root, ".content-registry/private/outbox/"+op.ID+"-failed.json", op, true)
}

func (r *Registry) reusableMigrationResource(f migrationFile) bool {
	if !f.RetainSource {
		return false
	}
	path, err := SafePath(r.Root, f.Target)
	if err != nil {
		return false
	}
	hash, size, err := FileHash(path)
	return err == nil && hash == f.Hash && size == f.Size
}
func (r *Registry) migrationCommitted(s migrationState) bool {
	l, err := r.Lookup(s.Plan.ObjectID)
	return err == nil && l.LastOperationID == s.Operation.ID && l.Generation == s.Plan.Target.Generation
}
func (r *Registry) recordMigrationFailure(s *migrationState, failure error) error {
	s.Operation.Error, s.Operation.State = failure.Error(), "failed"
	if !r.migrationCommitted(*s) && (errors.Is(failure, ErrConflict) || errors.Is(failure, os.ErrNotExist) || errors.Is(failure, ErrUnsafe)) {
		s.Phase, s.Operation.State = "conflicted", "conflicted"
		// Retire only exact, uncommitted object copies. Shared resources and any
		// externally changed target bytes are never removed by conflict cleanup.
		r.Namespace.Lock()
		unlock := r.LockObject(s.Plan.ObjectID)
		l, err := r.Lookup(s.Plan.ObjectID)
		if err == nil && l.Placement.ThoughtPath == s.Plan.Source.Placement.ThoughtPath {
			for _, f := range s.Files {
				if f.RetainSource || f.Target == f.Source || !s.createdTarget(f.Target) {
					continue
				}
				path, e := SafePath(r.Root, f.Target)
				if e != nil {
					err = e
					break
				}
				hash, size, e := FileHash(path)
				if e == nil && hash == f.Hash && size == f.Size {
					if e = os.Remove(path); e != nil {
						err = e
						break
					}
					if e = SyncDir(filepath.Dir(path)); e != nil {
						err = e
						break
					}
				}
			}
		}
		unlock()
		r.Namespace.Unlock()
		if err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	if err := r.failMigration(s.Operation); err != nil {
		return err
	}
	return r.saveMigration(s)
}

func (s migrationState) createdTarget(path string) bool {
	for _, target := range s.CreatedTargets {
		if target == path {
			return true
		}
	}
	return false
}
