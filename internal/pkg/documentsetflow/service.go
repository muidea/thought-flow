// Package documentsetflow coordinates bounded, durable set attempts. External
// calls run without namespace/object write locks; publication is a short CAS.
package documentsetflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"thoughtflow/internal/pkg/ai"
	"thoughtflow/internal/pkg/documentprofile"
	ds "thoughtflow/internal/pkg/documentset"
	"thoughtflow/internal/pkg/documentsetstore"
	"thoughtflow/internal/pkg/documentstore"
	"thoughtflow/internal/pkg/documentvalidation"
	"thoughtflow/internal/pkg/markdown"
	"thoughtflow/internal/pkg/models"
	"thoughtflow/internal/pkg/objectlocation"
	"thoughtflow/internal/pkg/topicstore"
)

type Planner interface {
	PlanDocumentSet(context.Context, ds.CaptureSnapshot, ds.Profile, *ds.SharedDesign) (ds.SharedDesign, error)
}
type Reviewer interface {
	ReviewDocumentSet(context.Context, []ds.ReviewItem, []ds.Artifact, ds.SharedDesign, ds.CaptureSnapshot) (ds.ReviewResponse, string, error)
}
type Options struct {
	Planner            Planner
	Generator          ai.DocumentGenerationProvider
	Reviewer           Reviewer
	Snapshot           func(string) (ds.CaptureSnapshot, error)
	SnapshotUnderLease func(string) (ds.CaptureSnapshot, error)
	ModelHash          string
	Emit               func(string, map[string]any) error
	Commit             func([]string) error
	Enabled            bool
}
type Service struct {
	Store   *documentsetstore.Store
	opts    Options
	mu      sync.Mutex
	running map[string]context.CancelFunc
	wg      sync.WaitGroup
	closing bool
	ctx     context.Context
	cancel  context.CancelFunc
}

func New(root string, opts Options) *Service {
	ctx, cancel := context.WithCancel(context.Background())
	return &Service{Store: documentsetstore.New(root), opts: opts, running: map[string]context.CancelFunc{}, ctx: ctx, cancel: cancel}
}
func (s *Service) Close() {
	s.mu.Lock()
	s.closing = true
	s.cancel()
	for _, cancel := range s.running {
		cancel()
	}
	s.mu.Unlock()
	s.wg.Wait()
}
func (s *Service) Profiles() []ds.Profile {
	out := ds.Builtins()
	root, e := objectlocation.SafePath(s.Store.Root, ".content-registry/document-set-profiles")
	if e != nil {
		return out
	}
	latest := map[string]ds.Profile{}
	filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			return nil
		}
		rel, err := filepath.Rel(s.Store.Root, path)
		if err != nil {
			return nil
		}
		var p ds.Profile
		if objectlocation.ReadJSON(s.Store.Root, filepath.ToSlash(rel), &p) == nil && ds.ValidateProfile(p) == nil && p.Enabled && p.Version > latest[p.ID].Version {
			latest[p.ID] = p
		}
		return nil
	})
	for _, p := range latest {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
func (s *Service) ResolveProfile(id string, version int, hash string) (ds.Profile, error) {
	for _, p := range s.Profiles() {
		if p.ID == id && p.Version == version && p.Hash == hash && p.Enabled {
			return p, nil
		}
	}
	var p ds.Profile
	if !ds.ValidKey(id) || version < 1 {
		return p, ds.ErrInvalid
	}
	e := objectlocation.ReadJSON(s.Store.Root, fmt.Sprintf(".content-registry/document-set-profiles/%s/v%d.json", id, version), &p)
	if e == nil {
		e = ds.ValidateProfile(p)
	}
	if e == nil && (!p.Enabled || p.Hash != hash) {
		e = ds.ErrConflict
	}
	return p, e
}
func (s *Service) PublishProfile(p ds.Profile) error {
	if strings.HasPrefix(p.ID, "builtin.") {
		return ds.ErrInvalid
	}
	if e := ds.ValidateProfile(p); e != nil {
		return e
	}
	// Immutable version publication uses an exclusive link, like the single
	// profile registry. A same-version replacement never changes in-flight work.
	return s.Store.PublishProfile(p)
}

type PlanRequest struct {
	OperationID        string           `json:"operation_id"`
	SetID              string           `json:"set_id,omitempty"`
	Name               string           `json:"name"`
	ExpectedGeneration *uint64          `json:"expected_generation"`
	CaptureGeneration  *uint64          `json:"capture_generation"`
	ProfileID          string           `json:"profile_id"`
	ProfileVersion     int              `json:"profile_version"`
	ProfileHash        string           `json:"profile_hash"`
	Shared             *ds.SharedDesign `json:"shared_design,omitempty"`
}

func (s *Service) Plan(ctx context.Context, session string, req PlanRequest) (ds.Plan, error) {
	if !s.opts.Enabled {
		return ds.Plan{}, ds.ErrUnavailable
	}
	if req.ExpectedGeneration == nil || req.CaptureGeneration == nil || req.ProfileHash == "" {
		return ds.Plan{}, ds.ErrPrecondition
	}
	if !documentstore.ValidID(req.OperationID) {
		return ds.Plan{}, ds.ErrInvalid
	}
	set := req.SetID
	if set == "" {
		set = "set-" + strings.TrimPrefix(documentstore.Hash([]byte(session+req.OperationID)), "sha256:")[:24]
	}
	if !documentstore.ValidID(set) {
		return ds.Plan{}, ds.ErrInvalid
	}
	// Serialize only plan reservation/persistence; never an LLM call. Same op
	// concurrent planning is reserved durably before the call and not replayed.
	requestHash := documentstore.HashJSON(struct {
		Session string
		Request PlanRequest
	}{session, req})
	if existing, e := s.Store.Plan(set, req.OperationID); e == nil {
		var marker map[string]string
		if e = objectlocation.ReadJSON(s.Store.Root, documentsetstore.Private(set)+"/planning/"+req.OperationID+".json", &marker); e != nil {
			return ds.Plan{}, e
		}
		if marker["request_hash"] != requestHash {
			return ds.Plan{}, ds.ErrConflict
		}
		return existing, nil
	}
	p, e := s.ResolveProfile(req.ProfileID, req.ProfileVersion, req.ProfileHash)
	if e != nil {
		return ds.Plan{}, e
	}
	if s.opts.Snapshot == nil {
		return ds.Plan{}, ds.ErrUnavailable
	}
	snapshot, e := s.opts.Snapshot(session)
	if e != nil {
		return ds.Plan{}, e
	}
	if snapshot.Generation != *req.CaptureGeneration {
		return ds.Plan{}, ds.ErrConflict
	}
	unlock := objectlocation.For(s.Store.Root).LockObject("set-plan-" + req.OperationID)
	var marker map[string]string
	e = objectlocation.ReadJSON(s.Store.Root, documentsetstore.Private(set)+"/planning/"+req.OperationID+".json", &marker)
	if e == nil {
		unlock()
		if marker["request_hash"] != requestHash {
			return ds.Plan{}, ds.ErrConflict
		}
		return ds.Plan{}, fmt.Errorf("%w: planning already started; check persisted operation before retrying", ds.ErrConflict)
	}
	if !os.IsNotExist(e) {
		unlock()
		return ds.Plan{}, e
	}
	e = objectlocation.WriteJSON(s.Store.Root, documentsetstore.Private(set)+"/planning/"+req.OperationID+".json", map[string]string{"request_hash": requestHash, "state": "running"}, true)
	unlock()
	if e != nil {
		return ds.Plan{}, e
	}
	var previous *ds.SharedDesign
	var previousPlan *ds.Plan
	head, e := s.Store.HeadOrEmpty(set)
	if e != nil {
		return ds.Plan{}, e
	}
	if head.Generation != *req.ExpectedGeneration {
		return ds.Plan{}, ds.ErrConflict
	}
	if head.Working != "" {
		r, e := s.Store.Read(set, head.Working)
		if e != nil {
			return ds.Plan{}, e
		}
		previous = &r.Plan.Shared
		previousPlan = &r.Plan
	}
	var planningCall *ds.Call
	shared := req.Shared
	if shared == nil {
		if s.opts.Planner == nil {
			return ds.Plan{}, ds.ErrUnavailable
		}
		limited, cancel := context.WithTimeout(ai.WithGenerationScope(ctx, req.OperationID), time.Duration(p.Limits.WallSeconds)*time.Second)
		defer cancel()
		inputBytes := lenJSON(map[string]any{"capture": snapshot, "profile": p, "previous": previous})
		if inputBytes > p.Limits.MaxInputBytes || inputBytes > p.Limits.MaxInputTokens || inputBytes > p.Limits.MaxContextBytes {
			return ds.Plan{}, ds.ErrBudget
		}
		planningCall = &ds.Call{ID: documentstore.ID("call-"), Kind: "planning", State: "running", InputBytes: inputBytes, InputTokens: inputBytes, TokensEstimated: true}
		value, e := s.opts.Planner.PlanDocumentSet(limited, snapshot, p, previous)
		if e != nil {
			objectlocation.WriteJSON(s.Store.Root, documentsetstore.Private(set)+"/planning/"+req.OperationID+".json", map[string]string{"request_hash": requestHash, "state": "uncertain", "error": e.Error()}, true)
			return ds.Plan{}, e
		}
		planningCall.OutputBytes = lenJSON(value)
		planningCall.OutputTokens = planningCall.OutputBytes
		planningCall.State = "completed"
		if planningCall.OutputBytes > p.Limits.MaxOutputBytes || planningCall.OutputTokens > p.Limits.MaxOutputTokens {
			return ds.Plan{}, ds.ErrBudget
		}
		if previousPlan != nil {
			guardConfirmedFacts(&value, *previousPlan, snapshot)
		}
		shared = &value
	}
	// Existing collections are explicit targets; new sets get a server-generated
	// name to avoid slug aliases and never adopt another collection by accident.
	ts := topicstore.New(s.Store.Root)
	var catalogGen uint64
	if req.SetID == "" {
		name := req.Name
		if strings.TrimSpace(name) == "" {
			name = p.Name
		}
		_, e = ts.Create(ctx, models.TopicCreateRequest{Name: set, Description: name, Mode: models.TopicModeCollection, OperationID: "create-" + req.OperationID, SearchPolicy: &models.TopicSearchPolicy{TopicGlobalVisible: true, OwnedThoughtsGlobalDefault: false}})
		if e != nil {
			return ds.Plan{}, e
		}
	}
	catalog, e := ts.Catalog(ctx, set)
	if e != nil {
		return ds.Plan{}, e
	}
	catalogGen = catalog.Generation
	plan, e := ds.BuildPlan(set, p, snapshot, *shared, *req.ExpectedGeneration, catalogGen)
	if e != nil {
		return plan, e
	}
	plan.PlanningCall = planningCall
	plan.ID = req.OperationID
	plan.CreatedAt = time.Now().UTC()
	for _, m := range plan.Members {
		plan.Baselines[m.Key] = documentsetstore.WorkingHash(s.Store.Root, m.DocumentID)
		if l, err := objectlocation.For(s.Store.Root).Lookup(m.DocumentID); err == nil {
			plan.LocationBaselines[m.Key] = documentstore.HashJSON(l)
		} else if !os.IsNotExist(err) {
			return plan, err
		}
	}
	plan.Hash = ds.PlanHash(plan)
	if e = objectlocation.WriteJSON(s.Store.Root, documentsetstore.Private(set)+"/captures/"+plan.ID+".json", snapshot, true); e != nil {
		return plan, e
	}
	if e = s.Store.SavePlan(plan); e != nil {
		return plan, e
	}
	if e = objectlocation.WriteJSON(s.Store.Root, documentsetstore.Private(set)+"/planning/"+req.OperationID+".json", map[string]string{"request_hash": requestHash, "state": "completed"}, true); e != nil {
		return plan, e
	}
	return plan, nil
}

type RunRequest struct {
	MarkdownPreviewID  string  `json:"markdown_preview_id,omitempty"`
	OperationID        string  `json:"operation_id"`
	SetID              string  `json:"set_id"`
	PlanID             string  `json:"plan_id"`
	PlanHash           string  `json:"plan_hash"`
	ExpectedGeneration *uint64 `json:"expected_generation"`
	CaptureGeneration  *uint64 `json:"capture_generation"`
}

func (s *Service) Start(req RunRequest) (ds.Run, error) {
	if !s.opts.Enabled {
		return ds.Run{}, ds.ErrUnavailable
	}
	if req.ExpectedGeneration == nil || req.CaptureGeneration == nil {
		return ds.Run{}, ds.ErrPrecondition
	}
	if !documentstore.ValidID(req.OperationID) {
		return ds.Run{}, ds.ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing {
		return ds.Run{}, ds.ErrUnavailable
	}
	hash := documentstore.HashJSON(req)
	if old, e := s.Store.Run(req.OperationID); e == nil {
		if old.RequestHash != hash {
			return old, ds.ErrConflict
		}
		return old, nil
	} else if !os.IsNotExist(e) {
		return old, e
	}
	plan, e := s.Store.Plan(req.SetID, req.PlanID)
	if e != nil {
		return ds.Run{}, e
	}
	if plan.Hash != req.PlanHash || plan.BaseGeneration != *req.ExpectedGeneration || plan.Capture.Generation != *req.CaptureGeneration {
		return ds.Run{}, ds.ErrConflict
	}
	run := ds.Run{ID: req.OperationID, OperationID: req.OperationID, RequestHash: hash, Plan: plan, State: "queued", ModelHash: s.opts.ModelHash, CreatedAt: time.Now().UTC(), Members: []ds.MemberProgress{}, Calls: []ds.Call{}, MarkdownPreviewID: req.MarkdownPreviewID}
	if req.MarkdownPreviewID != "" {
		var preview MarkdownPreview
		if e = objectlocation.ReadJSON(s.Store.Root, markdownPath(req.SetID, req.MarkdownPreviewID), &preview); e != nil {
			return run, e
		}
		if preview.PlanHash != plan.Hash {
			return run, ds.ErrConflict
		}
	}
	if plan.PlanningCall != nil {
		run.Calls = append(run.Calls, *plan.PlanningCall)
	}
	for _, q := range plan.Shared.Questions {
		if q.Blocking {
			run.State = "needs_clarification"
		}
	}
	for _, m := range plan.Members {
		run.Members = append(run.Members, ds.MemberProgress{Key: m.Key, State: "queued"})
	}
	if e = s.Store.SaveRun(run); e != nil {
		return run, e
	}
	if run.State == "queued" {
		s.launch(run.ID)
	}
	return run, nil
}
func (s *Service) launch(id string) {
	ctx, cancel := context.WithCancel(s.ctx)
	s.running[id] = cancel
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer func() { s.mu.Lock(); delete(s.running, id); s.mu.Unlock(); cancel() }()
		s.execute(ctx, id)
	}()
}
func lenJSON(v any) int { b, _ := json.Marshal(v); return len(b) }
func (s *Service) save(r *ds.Run) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	stored, e := s.Store.Run(r.ID)
	if e != nil {
		return e
	}
	if stored.CancelRequested {
		r.CancelRequested = true
	}
	if r.CancelRequested {
		r.State = "cancelled"
	}
	r.UpdatedAt = time.Now().UTC()
	return s.Store.SaveRun(*r)
}
func (s *Service) callStart(r *ds.Run, kind, key string, input any) (int, error) {
	if r.CancelRequested {
		return 0, context.Canceled
	}
	n := lenJSON(input)
	limits := r.Plan.Profile.Limits
	total := n
	tokens := n // Conservative UTF-8 byte-token upper bound; provider usage is unavailable through this interface.
	for _, c := range r.Calls {
		total += c.InputBytes
		tokens += c.InputTokens
	}
	if len(r.Calls) >= limits.MaxCalls || total > limits.MaxInputBytes || tokens > limits.MaxInputTokens || n > limits.MaxContextBytes {
		return 0, ds.ErrBudget
	}
	call := ds.Call{ID: documentstore.ID("call-"), Kind: kind, Member: key, State: "running", InputBytes: n, InputTokens: n, TokensEstimated: true}
	r.Calls = append(r.Calls, call)
	e := s.save(r)
	if e == nil && r.CancelRequested {
		e = context.Canceled
	}
	return len(r.Calls) - 1, e
}
func (s *Service) callDone(r *ds.Run, index int, value any, err error) error {
	call := &r.Calls[index]
	call.OutputBytes = lenJSON(value)
	call.OutputTokens = call.OutputBytes
	call.State = "completed"
	if err != nil {
		call.State = "uncertain"
		call.Error = err.Error()
		var p ai.ProviderError
		if errors.As(err, &p) && (ai.CanAutomaticallyRetry(err) || p.Code == "thoughtflow.ai.conversion_unsupported" || p.Code == "thoughtflow.ai.invalid_request" || strings.HasPrefix(p.Code, "thoughtflow.ai.invalid_json")) {
			call.State = "failed"
		}
	}
	total := 0
	tokens := 0
	for _, c := range r.Calls {
		tokens += c.OutputTokens
		total += c.OutputBytes
	}
	if total > r.Plan.Profile.Limits.MaxOutputBytes || tokens > r.Plan.Profile.Limits.MaxOutputTokens {
		err = ds.ErrBudget
	}
	if e := s.save(r); e != nil {
		return e
	}
	return err
}
func (s *Service) fail(r *ds.Run, err error) {
	r.Error = err.Error()
	r.State = "failed_terminal"
	if errors.Is(err, ds.ErrConflict) {
		r.State = "stale"
	}
	if errors.Is(err, context.Canceled) {
		r.State = "cancelled"
	}
	if ai.CanAutomaticallyRetry(err) {
		r.State = "failed_retryable"
	}
	s.save(r)
	s.emit("document_set.failed", *r, "")
}
func (s *Service) execute(parent context.Context, id string) {
	run, e := s.Store.Run(id)
	if e != nil {
		return
	}
	ctx, cancel := context.WithTimeout(ai.WithGenerationScope(parent, id), time.Duration(run.Plan.Profile.Limits.WallSeconds)*time.Second)
	defer cancel()
	if e = s.check(run.Plan); e != nil {
		s.fail(&run, e)
		return
	}
	run.State = "generating"
	if e = s.save(&run); e != nil {
		return
	}
	s.emit("document_set.progress", run, "")
	var candidate ds.Revision
	if run.Candidate != nil {
		candidate = *run.Candidate
	} else {
		h, _ := s.Store.HeadOrEmpty(run.Plan.SetID)
		candidate = ds.Revision{SchemaVersion: 1, ID: documentstore.ID("set-r-"), Parent: h.Working, OperationID: run.OperationID, Plan: run.Plan, CreatedAt: time.Now().UTC(), Members: []ds.Artifact{}}
	}
	artifacts := map[string]ds.Artifact{}
	for _, a := range candidate.Members {
		artifacts[a.Key] = a
	}
	for len(artifacts) < len(run.Plan.Members) {
		progress := false
		for i, m := range run.Plan.Members {
			if artifacts[m.Key].Key != "" {
				continue
			}
			deps := map[string]string{}
			waiting := false
			for _, k := range m.DependsOn {
				if artifacts[k].Key == "" {
					waiting = true
				}
				deps[k] = artifacts[k].RevisionID
			}
			if waiting {
				continue
			}
			progress = true
			if e = ctx.Err(); e != nil {
				s.fail(&run, e)
				return
			}
			run.Members[i].State = "running"
			if e = s.save(&run); e != nil {
				return
			}
			a, err := s.generate(ctx, &run, m, deps)
			if err != nil {
				run.Members[i].State = "failed"
				run.Members[i].Error = err.Error()
				s.fail(&run, err)
				return
			}
			artifacts[m.Key] = a
			candidate.Members = append(candidate.Members, a)
			run.Candidate = &candidate
			run.Members[i].State = "generated"
			if e = s.save(&run); e != nil {
				return
			}
			s.emit("document_set.progress", run, m.Key)
		}
		if !progress {
			s.fail(&run, ds.ErrInvalid)
			return
		}
	}
	sort.Slice(candidate.Members, func(i, j int) bool { return candidate.Members[i].Key < candidate.Members[j].Key })
	run.State = "checking"
	candidate.Report = ds.Validate(candidate)
	run.Candidate = &candidate
	if e = s.save(&run); e != nil {
		return
	}
	if candidate.Report.StructuralStatus != "passed" {
		run.State = "awaiting_review"
		run.Error = "Joint structural checks failed"
		ds.Rehash(&candidate)
		s.save(&run)
		return
	}
	if s.opts.Reviewer != nil {
		e = s.review(ctx, &run, &candidate)
		if e == nil && candidate.Report.Status != "ready" {
			e = s.repair(ctx, &run, &candidate)
		}
		if e != nil {
			candidate.Report.SemanticStatus = "error"
			candidate.Report.Status = "awaiting_review"
			run.Error = e.Error()
		}
	}
	ds.Rehash(&candidate)
	run.Candidate = &candidate
	if e = ctx.Err(); e != nil {
		s.fail(&run, e)
		return
	}
	if candidate.Report.Status != "ready" {
		run.State = "awaiting_review"
		s.save(&run)
		s.emit("document_set.awaiting_review", run, "")
		return
	}
	if e = s.commit(&run); e != nil {
		s.fail(&run, e)
	}
}
func (s *Service) generate(ctx context.Context, run *ds.Run, m ds.PlanMember, deps map[string]string) (ds.Artifact, error) {
	if s.opts.Generator == nil {
		return ds.Artifact{}, ds.ErrUnavailable
	}
	// Reuse only semantically unaffected exact role revisions. Manual changes
	// are retained on the working document and cannot be auto-overwritten.
	var manualBase, manualBody string
	head, headErr := s.Store.HeadOrEmpty(run.Plan.SetID)
	if headErr != nil {
		return ds.Artifact{}, headErr
	}
	if head.Working != "" {
		old, err := s.Store.Read(run.Plan.SetID, head.Working)
		if err != nil {
			return ds.Artifact{}, err
		}
		affected := map[string]bool{}
		for _, id := range ds.Impact(old.Plan.Shared, run.Plan.Shared) {
			affected[id] = true
		}
		for _, previous := range old.Members {
			if previous.Key != m.Key {
				continue
			}
			current, content, err := markdown.ReadThought(s.Store.Root, m.DocumentID)
			if err != nil {
				return ds.Artifact{}, err
			}
			if current.UserTitle != previous.Contract.Title {
				return ds.Artifact{}, fmt.Errorf("%w: manual title changed in %s; retain title and revise the plan explicitly", ds.ErrConflict, m.Key)
			}
			if documentstore.Normalize(content.AINotes) != previous.Body {
				manualBase, manualBody = previous.Body, documentstore.Normalize(content.AINotes)
			}
			changed := false
			for _, id := range m.Covers {
				if affected[id] {
					changed = true
				}
			}
			for _, f := range run.Plan.Shared.Facts {
				if (f.Kind == "term" || f.Kind == "constraint") && affected[f.ID] {
					changed = true
				}
			}
			oldFacts, oldRelations := ds.ApplicableFacts(old.Plan, m)
			newFacts, newRelations := ds.ApplicableFacts(run.Plan, m)
			if documentstore.HashJSON(oldFacts) != documentstore.HashJSON(newFacts) || documentstore.HashJSON(oldRelations) != documentstore.HashJSON(newRelations) || documentstore.HashJSON(previous.Dependencies) != documentstore.HashJSON(deps) {
				changed = true
			}
			if !changed && manualBody == "" && len(run.RepairIssues[m.Key]) == 0 && previous.ProfileSnapshot == m.ProfileSnapshot && reusableContract(previous.Contract, ds.Contract(run.Plan, m)) && documentstore.HashJSON(previous.Sources) == documentstore.HashJSON(run.Plan.Capture.Sources) {
				previous.ReusedFrom = old.ID
				previous.Dependencies = deps
				return previous, nil
			}
		}
	}
	profile, e := documentprofile.Parse([]byte(m.ProfileSnapshot), documentprofile.Limits{})
	if e != nil {
		return ds.Artifact{}, e
	}

	facts, relations := ds.ApplicableFacts(run.Plan, m)
	upstream := map[string]any{}
	for key, revision := range deps {
		for _, member := range run.Plan.Members {
			if member.Key != key {
				continue
			}
			frozen, err := documentstore.New(s.Store.Root).FrozenRevision(member.DocumentID, revision)
			if err != nil {
				return ds.Artifact{}, err
			}
			upstream[key] = map[string]any{"document_id": member.DocumentID, "revision_id": revision, "body_hash": frozen.Revision.BodyHash, "body": frozen.Body}
		}
	}
	contextBytes, _ := json.Marshal(map[string]any{"shared_design_hash": run.Plan.Shared.Hash, "member": m, "facts": facts, "relations": relations, "dependencies": deps, "upstream": upstream, "repair_issues": run.RepairIssues[m.Key]})
	request := ai.DocumentGenerationRequest{Profile: profile, Parameters: run.Plan.Shared.Inputs, Context: ai.DocumentSourceContext{Title: m.Title, Summary: run.Plan.Shared.Inputs["goal"], Body: string(contextBytes)}}
	imported, e := s.markdownMember(*run, m.Key)
	if e != nil {
		return ds.Artifact{}, e
	}
	var draft models.DocumentDraft
	if imported != nil {
		draft = models.DocumentDraft{Title: m.Title, Sections: map[string]models.DocumentSection{}}
		for _, sec := range profile.Sections {
			draft.Sections[sec.Key] = models.DocumentSection{Content: "Imported source: " + imported.Path + ". This candidate requires all profile and set checks before publication."}
		}
	} else {
		draft, e = boundedCall(ctx, s, run, "member", m.Key, request, func(ctx context.Context) (models.DocumentDraft, error) {
			return s.opts.Generator.GenerateDocument(ctx, request)
		})
		if e != nil {
			return ds.Artifact{}, e
		}
		if e = ctx.Err(); e != nil {
			return ds.Artifact{}, e
		}
	}
	// Choose the actual declared role section, never add unknown top-level
	// headings. The automatically rendered blocks remain immutable contracts.
	section := ""
	for _, v := range profile.Sections {
		if v.Key == "design" {
			section = v.Key
		}
	}
	if section == "" {
		section = profile.Sections[0].Key
	}
	part := draft.Sections[section]
	if imported != nil {
		part.Content = importedSection(imported.Derived)
	}
	part.Content += ds.CommonBlock(run.Plan, m)
	draft.Sections[section] = part
	draft.Title = m.Title
	rendered := documentprofile.Render(profile, draft, request.Parameters)
	if rendered.Validation.Status == models.ArchiveValidationInvalid {
		return ds.Artifact{}, fmt.Errorf("%w: member rendering: %v", ds.ErrRules, rendered.Validation.Issues)
	}
	body := documentstore.Normalize(rendered.Content)
	if manualBody != "" {
		merged, ok := ds.Merge(manualBase, body, manualBody)
		run.Reconciliations = append(run.Reconciliations, ds.Reconciliation{Member: m.Key, Base: manualBase, Generated: body, Manual: manualBody, Merged: merged, Conflict: !ok})
		if e = s.save(run); e != nil {
			return ds.Artifact{}, e
		}
		if !ok {
			return ds.Artifact{}, fmt.Errorf("%w: overlapping manual and generated changes in %s; all three versions retained", ds.ErrConflict, m.Key)
		}
		body = merged
	}
	contract := ds.Contract(run.Plan, m)
	d := documentstore.RevisionData{Body: body, Profile: m.ProfileSnapshot, Contract: contract, Sources: run.Plan.Capture.Sources}
	d.Sources.SchemaVersion = 1
	if imported != nil {
		d.Projection = &models.ThoughtSnapshot{Thought: models.Thought{ID: m.DocumentID, MarkdownImport: &models.MarkdownImport{Filename: imported.Path, SHA256: imported.OriginalHash, Size: len(imported.Original)}}, Content: models.ThoughtContent{Original: imported.Original, AINotes: body}}
	}
	registry := objectlocation.For(s.Store.Root)
	registry.Namespace.Lock()
	if e = registry.RequireSetWriter(); e != nil {
		registry.Namespace.Unlock()
		return ds.Artifact{}, e
	}
	l, e := registry.Reserve(m.DocumentID, run.Plan.SetID, run.OperationID)
	if e == nil {
		if l.ManagedSetID != "" && l.ManagedSetID != run.Plan.SetID {
			e = ds.ErrConflict
		} else {
			changedLocation := l.ManagedSetID != run.Plan.SetID || l.ManagedMemberKey != m.Key
			l.ManagedSetID = run.Plan.SetID
			l.ManagedMemberKey = m.Key
			if l.Lifecycle == "reserved" {
				l.Lifecycle = "set_pending"
				changedLocation = true
			}
			if changedLocation {
				e = registry.CommitLocation(l, "document_set.pending")
			}
		}
	}
	registry.Namespace.Unlock()
	if e != nil {
		return ds.Artifact{}, e
	}
	release := registry.ReadLease(m.DocumentID)
	defer release()
	h, e := documentstore.New(s.Store.Root).Head(m.DocumentID)
	if e != nil && !os.IsNotExist(e) {
		return ds.Artifact{}, e
	}
	if documentsetstore.WorkingHash(s.Store.Root, m.DocumentID) != run.Plan.Baselines[m.Key] {
		return ds.Artifact{}, ds.ErrConflict
	}
	stored, e := documentstore.New(s.Store.Root).Prepare(m.DocumentID, h.Generation, d)
	if e != nil {
		return ds.Artifact{}, e
	}
	a := ds.Artifact{Key: m.Key, DocumentID: m.DocumentID, RevisionID: stored.Revision.RevisionID, Body: stored.Body, BodyHash: stored.Revision.BodyHash, Contract: contract, ProfileSnapshot: m.ProfileSnapshot, Sources: d.Sources, Dependencies: deps}
	if imported != nil {
		a.Original = imported.Original
		a.OriginalHash = imported.OriginalHash
	}
	a.ResourceHash = stored.Revision.ResourceBindingsHash
	a.Resources = stored.Resources
	a.SingleReport = documentvalidation.Validate(stored)
	return a, nil
}
func (s *Service) review(ctx context.Context, run *ds.Run, candidate *ds.Revision) error {
	var capture ds.CaptureSnapshot
	if e := objectlocation.ReadJSON(s.Store.Root, documentsetstore.Private(run.Plan.SetID)+"/captures/"+run.Plan.ID+".json", &capture); e != nil {
		return e
	}
	if capture.ContextHash != run.Plan.Capture.ContextHash || capture.Generation != run.Plan.Capture.Generation {
		return ds.ErrConflict
	}
	checklist := candidate.Report.Checklist
	response := ds.ReviewResponse{Evidence: []ds.ReviewEvidence{}, Issues: []ds.Issue{}}
	reviewer := ""
	for start := 0; start < len(checklist); {
		end := start + 8
		if end > len(checklist) {
			end = len(checklist)
		}
		var batch []ds.ReviewItem
		var members []ds.Artifact
		for {
			batch = checklist[start:end]
			needed := map[string]bool{}
			for _, item := range batch {
				for _, m := range item.Members {
					needed[m] = true
				}
			}
			members = []ds.Artifact{}
			for _, a := range candidate.Members {
				if needed[a.Key] {
					members = append(members, a)
				}
			}
			if lenJSON(map[string]any{"checklist": batch, "members": members, "shared_design": candidate.Plan.Shared, "frozen_capture": capture}) <= run.Plan.Profile.Limits.MaxContextBytes {
				break
			}
			if end-start == 1 {
				return fmt.Errorf("%w: review item %s exceeds context; coverage remains incomplete and requires complete manual review", ds.ErrBudget, batch[0].ID)
			}
			end = start + (end-start)/2
		}
		input := struct {
			Items   []ds.ReviewItem
			Members []ds.Artifact
			Shared  ds.SharedDesign
			Capture ds.CaptureSnapshot
		}{batch, members, candidate.Plan.Shared, capture}
		type reviewed struct {
			Response ds.ReviewResponse
			Model    string
		}
		value, e := boundedCall(ctx, s, run, "joint_review", "", input, func(ctx context.Context) (reviewed, error) {
			out, model, err := s.opts.Reviewer.ReviewDocumentSet(ctx, batch, members, candidate.Plan.Shared, capture)
			return reviewed{out, model}, err
		})
		if e != nil {
			return e
		}
		reviewer = value.Model
		response.Evidence = append(response.Evidence, value.Response.Evidence...)
		response.Issues = append(response.Issues, value.Response.Issues...)
		start = end
	}
	return ds.ApplyReview(candidate, response, "model_reviewed", reviewer)
}
func (s *Service) check(p ds.Plan) error { return s.checkWithLease(p, false) }
func (s *Service) checkWithLease(p ds.Plan, underLease bool) error {
	h, e := s.Store.HeadOrEmpty(p.SetID)
	if e != nil {
		return e
	}
	if h.Generation != p.BaseGeneration {
		return ds.ErrConflict
	}
	if s.opts.Snapshot == nil {
		return ds.ErrUnavailable
	}
	snapshotReader := s.opts.Snapshot
	if underLease && s.opts.SnapshotUnderLease != nil {
		snapshotReader = s.opts.SnapshotUnderLease
	}
	capture, e := snapshotReader(p.Capture.SessionID)
	if e != nil {
		return e
	}
	if capture.Generation != p.Capture.Generation || capture.ContextHash != p.Capture.ContextHash || capture.ScopeHash != p.Capture.ScopeHash || documentstore.HashJSON(capture.Sources) != documentstore.HashJSON(p.Capture.Sources) {
		return ds.ErrConflict
	}
	var c models.TopicCatalog
	if e = objectlocation.ReadJSON(s.Store.Root, "topics/"+p.SetID+"/catalog.json", &c); e != nil {
		return e
	}
	if c.Generation != p.CatalogGeneration {
		return ds.ErrConflict
	}
	for _, m := range p.Members {
		if baseline := p.LocationBaselines[m.Key]; baseline != "" {
			l, e := objectlocation.For(s.Store.Root).Lookup(m.DocumentID)
			if e != nil {
				return e
			}
			if documentstore.HashJSON(l) != baseline {
				return ds.ErrConflict
			}
		}
		if documentsetstore.WorkingHash(s.Store.Root, m.DocumentID) != p.Baselines[m.Key] {
			return ds.ErrConflict
		}
	}
	return nil
}
func (s *Service) commit(run *ds.Run) error {
	release := objectlocation.For(s.Store.Root).LockObject("set-publish-" + run.ID)
	defer release()
	stored, err := s.Store.Run(run.ID)
	if err != nil {
		return err
	}
	if stored.CancelRequested {
		return context.Canceled
	}
	run.State = "committing"
	if e := s.save(run); e != nil {
		return e
	}
	if run.CancelRequested {
		return context.Canceled
	}
	e := s.Store.Commit(*run.Candidate, run.Plan.BaseGeneration, run.RequestHash, func() error { return s.checkWithLease(run.Plan, true) })
	if e != nil {
		return e
	}
	issues := s.Store.Recover(s.finish)
	if len(issues) > 0 {
		run.Error = "Committed; derived projections pending recovery: " + issues[0].Error()
	}
	run.State = "committed"
	return s.save(run)
}
func (s *Service) finish(in documentsetstore.Intent) error {
	if s.opts.Emit != nil {
		current, e := s.Store.Head(in.SetID)
		if e != nil {
			return e
		}
		revision, e := s.Store.Read(in.SetID, current.Working)
		if e != nil {
			return e
		}
		for _, a := range revision.Members {
			if e = s.opts.Emit("thought.patched", map[string]any{"set_id": in.SetID, "thought_id": a.DocumentID, "event_id": in.OperationID + ":" + a.DocumentID, "generation": current.Generation}); e != nil {
				return e
			}
		}
		kind := "document_set.committed"
		if in.Kind == "finalization" {
			kind = "document_set.finalized"
		}
		if e := s.opts.Emit(kind, map[string]any{"event_id": in.OperationID + ":committed", "set_id": in.SetID, "run_id": in.OperationID, "set_revision_id": in.RevisionID}); e != nil {
			return e
		}
	}
	if s.opts.Commit != nil {
		paths, e := s.Store.ContentPaths(in.SetID)
		if e != nil {
			return e
		}
		return s.opts.Commit(paths)
	}
	return nil
}
func (s *Service) emit(kind string, run ds.Run, key string) {
	if s.opts.Emit != nil {
		s.opts.Emit(kind, map[string]any{"set_id": run.Plan.SetID, "run_id": run.ID, "generation": run.Plan.BaseGeneration, "member_key": key, "state": run.State})
	}
}
func (s *Service) Cancel(id string) (ds.Run, error) {
	release := objectlocation.For(s.Store.Root).LockObject("set-publish-" + id)
	defer release()
	s.mu.Lock()
	defer s.mu.Unlock()
	r, e := s.Store.Run(id)
	if e != nil {
		return r, e
	}
	if r.State == "committed" {
		return r, ds.ErrConflict
	}
	r.CancelRequested = true
	r.State = "cancelled"
	if cancel := s.running[id]; cancel != nil {
		cancel()
	}
	e = s.Store.SaveRun(r)
	return r, e
}
func (s *Service) Recover() []error {
	issues := s.Store.RecoverImports()
	issues = append(issues, s.Store.Recover(s.finish)...)
	runs, e := s.Store.Runs()
	if e != nil {
		return append(issues, e)
	}
	for _, r := range runs {
		if r.Plan.SetID == "" {
			continue
		}
		if r.Candidate != nil && s.Store.Reachable(r.Plan.SetID, r.Candidate.ID) {
			r.State = "committed"
			s.Store.SaveRun(r)
			continue
		}
		switch r.State {
		case "queued", "planning", "generating", "checking", "committing", "ready":
			r.State = "failed_terminal"
			r.Error = "Interrupted before publication; saved artifacts retained. Unknown calls are not replayed."
			for i := range r.Calls {
				if r.Calls[i].State == "running" {
					r.Calls[i].State = "uncertain"
				}
			}
			s.Store.SaveRun(r)
		}
	}
	return issues
}

func reusableContract(a, b models.DocumentContract) bool {
	a.ContextHash = ""
	b.ContextHash = ""
	return documentstore.HashJSON(a) == documentstore.HashJSON(b)
}
