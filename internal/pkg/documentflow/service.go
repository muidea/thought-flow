// Package documentflow is the single coordinator for typed document writes.
// It accepts projections as values, keeping storage independent of Markdown I/O.
package documentflow

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"thoughtflow/internal/pkg/documentprofile"
	"thoughtflow/internal/pkg/documentstore"
	"thoughtflow/internal/pkg/documentvalidation"
	"thoughtflow/internal/pkg/jobstore"
	"thoughtflow/internal/pkg/models"
)

var ErrRules = errors.New("document delivery requirements are not satisfied")
var ErrUnavailable = errors.New("document service unavailable")

type Reviewer interface {
	ReviewDocument(context.Context, documentstore.RevisionData) ([]models.DocumentIssue, string, error)
}
type Repairer interface {
	RepairDocument(context.Context, documentstore.RevisionData, []models.DocumentIssue, []string) (string, error)
}
type Options struct {
	Projected         func(string)
	Commit            func([]string)
	RuntimePath       string
	SourceHash        func(models.DocumentSource) (string, error)
	Jobs              *jobstore.Store
	Enabled           bool
	Families          []string
	Reviewer          Reviewer
	Repairer          Repairer
	ReviewTimeout     time.Duration
	MaxRepairAttempts int
	Concurrency       int
	SemanticRequired  bool
	AllowManual       bool
	Emit              func(string, map[string]any)
}
type Service struct {
	Store    *documentstore.Store
	Profiles *documentprofile.Registry
	Options  Options
	ctx      context.Context
	cancel   context.CancelFunc
	wg       sync.WaitGroup
	life     sync.Mutex
	closed   bool
	slots    chan struct{}
}

var services sync.Map
var activeValidations sync.Map

func Configure(root string, profiles *documentprofile.Registry, options Options) *Service {
	if options.ReviewTimeout <= 0 {
		options.ReviewTimeout = 45 * time.Second
	}
	if options.Concurrency <= 0 {
		options.Concurrency = 2
	}
	if options.MaxRepairAttempts < 0 {
		options.MaxRepairAttempts = 0
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &Service{Store: documentstore.New(root), Profiles: profiles, Options: options, ctx: ctx, cancel: cancel, slots: make(chan struct{}, options.Concurrency)}
	key, _ := filepath.Abs(root)
	services.Store(key, s)
	if options.Jobs != nil {
		if jobs, e := options.Jobs.List(); e == nil {
			for _, job := range jobs {
				if job.Type == "document_validate" && (job.Status == models.JobStatusRunning || job.Status == models.JobStatusQueued) {
					_, _ = options.Jobs.MarkCanceled(job, "Process restarted; report history is retained. Explicit retry is required.")
				}
			}
		}
	}
	return s
}
func For(root string) *Service {
	key, _ := filepath.Abs(root)
	if v, ok := services.Load(key); ok {
		return v.(*Service)
	}
	registry, _ := documentprofile.NewRegistry(filepath.Join(root, "document-formats"), "", documentprofile.Limits{})
	return Configure(root, registry, Options{Enabled: true, AllowManual: true, SemanticRequired: true, MaxRepairAttempts: 2})
}
func (s *Service) Close() { s.life.Lock(); s.closed = true; s.cancel(); s.life.Unlock(); s.wg.Wait() }
func (s *Service) begin(ctx context.Context) (context.Context, func(), func(), error) {
	s.life.Lock()
	if s.closed {
		s.life.Unlock()
		return nil, nil, nil, ErrUnavailable
	}
	s.wg.Add(1)
	s.life.Unlock()
	ctx, cancel := context.WithTimeout(ctx, s.Options.ReviewTimeout)
	stop := context.AfterFunc(s.ctx, cancel)
	select {
	case s.slots <- struct{}{}:
		var once sync.Once
		release := func() { once.Do(func() { <-s.slots }) }
		return ctx, release, func() { release(); stop(); cancel(); s.wg.Done() }, nil
	case <-ctx.Done():
		stop()
		cancel()
		s.wg.Done()
		return nil, nil, nil, ctx.Err()
	}
}
func typed(t models.Thought) bool {
	if t.Document != nil {
		return true
	}
	if t.DocumentProfile == nil {
		return false
	}
	switch t.DocumentProfile.Family {
	case models.DocumentFamilyDesign, models.DocumentFamilyResearch, models.DocumentFamilyArticle:
		return true
	}
	return false
}
func ExtractBody(c models.ThoughtContent) string { return documentstore.Normalize(c.AINotes) }
func title(t models.Thought) string {
	if t.UserTitle != "" {
		return t.UserTitle
	}
	return t.ExtractedTitle
}
func profileEqual(t models.Thought, d documentstore.RevisionData) bool {
	return t.DocumentProfile != nil && *t.DocumentProfile == d.Contract.ProfileRef
}

// SyncProjection covers every WriteThought call. Read-time observation detects
// external edits. Pending projection recovery always reads immutable HEAD first.
func (s *Service) SyncProjection(t *models.Thought, c *models.ThoughtContent, observe bool) (bool, error) {
	unlock := s.Store.Lock(t.ID)
	defer unlock()
	return s.SyncProjectionLocked(t, c, observe)
}

// SyncProjectionLocked is called only within the Markdown projection transaction.
func (s *Service) SyncProjectionLocked(t *models.Thought, c *models.ThoughtContent, observe bool) (bool, error) {
	if e := s.Store.CheckActive(t.ID); e != nil {
		return false, e
	}
	if !typed(*t) {
		if _, e := s.Store.ActiveHead(t.ID); os.IsNotExist(e) {
			return false, nil
		} else if e != nil {
			return false, e
		}
		return false, ErrRules
	}
	// Match the sections-v2 codec before freezing auxiliary content/links.
	// This also prevents its EOF newline from looking like an external edit.
	for _, section := range []*string{&c.Original, &c.ExtractedContent, &c.AINotes, &c.Links} {
		if strings.TrimSpace(*section) == "" {
			*section = ""
		} else {
			*section = documentstore.Normalize(*section)
		}
	}
	id := t.ID
	h, e := s.Store.ActiveHead(id)
	if e != nil && !os.IsNotExist(e) {
		return false, e
	}
	exists := e == nil
	if t.DocumentExpectedGeneration != nil && h.Generation != *t.DocumentExpectedGeneration {
		return false, documentstore.ErrConflict
	}
	if !exists && observe {
		return false, nil
	}
	var d documentstore.RevisionData
	if exists {
		d, e = s.Store.Revision(id, h.WorkingRevisionID)
		if e != nil {
			return false, e
		}
		if observe && h.PendingProjection {
			c.AINotes = d.Body
			t.UserTitle = d.Contract.Title
			ref := d.Contract.ProfileRef
			t.DocumentProfile = &ref
			t.Document = &models.DocumentLink{DocumentID: id, RevisionID: d.Revision.RevisionID, Generation: h.Generation}
			return true, nil
		}
		equal := ExtractBody(*c) == d.Body && title(*t) == d.Contract.Title && profileEqual(*t, d)
		if equal && t.DocumentParameters != nil {
			parameters := clone(t.DocumentParameters)
			parameters["_links_hash"] = documentstore.Hash([]byte(c.Links))
			equal = documentstore.HashJSON(parameters) == documentstore.HashJSON(d.Contract.Parameters)
		}
		if equal && t.DocumentSources != nil {
			equal = documentstore.HashJSON(*t.DocumentSources) == documentstore.HashJSON(d.Sources)
		}
		if equal && d.Contract.Parameters["_links_hash"] != documentstore.Hash([]byte(c.Links)) {
			equal = false
		}
		if equal {
			t.Document = &models.DocumentLink{DocumentID: id, RevisionID: h.WorkingRevisionID, Generation: h.Generation}
			return false, nil
		}
		if !observe && (h.PendingProjection || t.Document == nil || t.Document.RevisionID != h.WorkingRevisionID) {
			return false, documentstore.ErrConflict
		}
	}
	if !s.Options.Enabled || (t.DocumentProfile != nil && !s.enabledFamily(t.DocumentProfile.Family)) || s.ctx.Err() != nil {
		return false, ErrUnavailable
	}
	if t.DocumentProfile == nil {
		return false, ErrRules
	}
	// Freeze snapshot and inputs before publishing; never resolve latest in validation.
	if !exists || d.Contract.ProfileRef != *t.DocumentProfile {
		profile := documentprofile.DocumentProfile{}
		var err error
		if t.DocumentProfileSnapshot != "" {
			profile, err = documentprofile.Parse([]byte(t.DocumentProfileSnapshot), documentprofile.Limits{})
		} else if s.Profiles != nil {
			profile, err = s.Profiles.Resolve(*t.DocumentProfile)
		} else {
			err = ErrUnavailable
		}
		d.Profile = ""
		if err == nil && profile.Ref == *t.DocumentProfile {
			d.Profile = profile.Snapshot
		}
		d.Contract = models.DocumentContract{SchemaVersion: 1, ProfileRef: *t.DocumentProfile, Title: title(*t), Language: "zh-CN", Parameters: map[string]string{}, ReviewPolicy: models.DocumentReviewPolicy{Version: documentvalidation.ReviewPolicyVersion, SemanticRequired: s.Options.SemanticRequired, AllowManual: s.Options.AllowManual}}
	}
	if d.Contract.Parameters == nil {
		d.Contract.Parameters = map[string]string{}
	}
	if t.DocumentParameters != nil {
		d.Contract.Parameters = clone(t.DocumentParameters)
	}
	d.Contract.Parameters["_links_hash"] = documentstore.Hash([]byte(c.Links))
	d.Contract.Title = title(*t)
	d.Contract.Purpose = d.Contract.Parameters["purpose"]
	if d.Contract.Purpose == "" {
		d.Contract.Purpose = d.Contract.Parameters["problem"]
	}
	d.Contract.Audience = d.Contract.Parameters["audience"]
	if lang := d.Contract.Parameters["language"]; lang != "" {
		d.Contract.Language = lang
	}
	if t.DocumentSources != nil {
		d.Sources = *t.DocumentSources
	} else if !exists {
		d.Sources = models.SourceManifest{SchemaVersion: 1, Sources: []models.DocumentSource{}}
	}
	if e := safeInputs(d); e != nil {
		return false, e
	}
	projectionThought := *t
	projectionThought.Document = nil
	projectionContent := *c
	projectionContent.AINotes = "" // The canonical body is stored independently.
	d.Projection = &models.ThoughtSnapshot{Thought: projectionThought, Content: projectionContent}
	d.Body = ExtractBody(*c)
	d.Revision.Origin = "thought_write"
	if observe {
		d.Revision.Origin = "external_edit"
	}
	d.Revision.RepairCount = 0
	if s.ctx.Err() != nil {
		return false, ErrUnavailable
	}
	h, e = s.Store.Publish(id, h.Generation, d)
	if e != nil {
		return false, e
	}
	t.Document = &models.DocumentLink{DocumentID: id, RevisionID: h.WorkingRevisionID, Generation: h.Generation}
	if _, e = s.CheckDraft(id, h.WorkingRevisionID); e != nil {
		return false, e
	}
	s.emit("document.revision_created", id, h, "")
	return observe, nil
}
func clone(in map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range in {
		out[k] = v
	}
	return out
}
func (s *Service) Get(id string) (models.DocumentSnapshot, error) {
	h, e := s.Store.ActiveHead(id)
	if e != nil {
		return models.DocumentSnapshot{}, e
	}
	d, e := s.Store.Revision(id, h.WorkingRevisionID)
	if e != nil {
		return models.DocumentSnapshot{}, e
	}
	out := models.DocumentSnapshot{ResourceBindings: d.Resources.Bindings, DocumentID: id, Head: h, Revision: d.Revision, Contract: d.Contract, Sources: d.Sources, Body: d.Body, Freshness: "unavailable", SourceFreshness: "snapshot"}
	keys, e := s.Store.Records(id, "reports")
	if e != nil {
		return out, e
	}
	for _, key := range keys {
		r, e := s.Store.Report(id, key)
		if e != nil {
			return out, e
		}
		if r.RevisionID == h.WorkingRevisionID && (out.Report == nil || r.CreatedAt.After(out.Report.CreatedAt)) {
			copy := r
			out.Report = &copy
		}
	}
	out.SourceFreshness = s.sourceFreshness(id, d.Sources)
	if out.Report != nil && !h.PendingProjection {
		out.Freshness = "stale"
		if out.Report.ValidationKey == documentvalidation.Key(d) {
			out.Freshness = "current"
		}
	}
	if out.SourceFreshness == "unavailable" {
		out.Freshness = "unavailable"
	}
	if h.FinalizationID != "" {
		f, e := s.Store.Finalization(id, h.FinalizationID)
		if e != nil {
			return out, e
		}
		out.Finalization = &f
	}
	return out, nil
}
func (s *Service) CreateRevision(id string, expected uint64, d documentstore.RevisionData) (models.DocumentSnapshot, error) {
	if !s.Options.Enabled || s.ctx.Err() != nil {
		return models.DocumentSnapshot{}, ErrUnavailable
	}
	if !s.enabledFamily(d.Contract.ProfileRef.Family) {
		return models.DocumentSnapshot{}, ErrUnavailable
	}
	if e := safeInputs(d); e != nil {
		return models.DocumentSnapshot{}, e
	}
	unlock := s.Store.Lock(id)
	h, e := s.Store.Publish(id, expected, d)
	unlock()
	if e != nil {
		return models.DocumentSnapshot{}, e
	}
	s.emit("document.revision_created", id, h, "")
	return s.Get(id)
}
func (s *Service) enabledFamily(f string) bool {
	if len(s.Options.Families) == 0 {
		return true
	}
	for _, v := range s.Options.Families {
		if v == f {
			return true
		}
	}
	return false
}
func (s *Service) Validate(ctx context.Context, id, rev string, expected uint64, reviewMode ...string) (models.ValidationReport, error) {
	key := s.Store.Root + "/" + id
	if _, loaded := activeValidations.LoadOrStore(key, rev); loaded {
		return models.ValidationReport{}, documentstore.ErrConflict
	}
	defer activeValidations.Delete(key)

	if !s.Options.Enabled {
		return models.ValidationReport{}, ErrUnavailable
	}
	unlock := s.Store.Lock(id)
	h, e := s.Store.ActiveHead(id)
	if e == nil && (h.Generation != expected || h.WorkingRevisionID != rev) {
		e = documentstore.ErrConflict
	}
	var d documentstore.RevisionData
	if e == nil {
		d, e = s.Store.Revision(id, rev)
	}
	unlock()
	if e != nil {
		return models.ValidationReport{}, e
	}
	if !s.enabledFamily(d.Contract.ProfileRef.Family) {
		return models.ValidationReport{}, ErrUnavailable
	}
	operation := ""
	if len(reviewMode) > 1 {
		operation = reviewMode[1]
	}
	operationKey := ""
	if operation != "" {
		if !documentstore.ValidID(operation) {
			return models.ValidationReport{}, documentstore.ErrInvalid
		}
		operationKey = "v-" + strings.TrimPrefix(documentstore.Hash([]byte(operation)), "sha256:")
		if previous, e := s.Store.Report(id, operationKey); e == nil {
			mode := ""
			if len(reviewMode) > 0 {
				mode = reviewMode[0]
			}
			if previous.RevisionID != rev || previous.ReviewMode != mode {
				return models.ValidationReport{}, documentstore.ErrConflict
			}
			return previous, nil
		} else if !os.IsNotExist(e) {
			return models.ValidationReport{}, e
		}
	}
	runCtx, _, done, e := s.begin(ctx)
	if e != nil {
		return models.ValidationReport{}, e
	}
	defer done()
	var job models.Job
	if s.Options.Jobs != nil {
		job, e = s.Options.Jobs.Create("document_validate", "document", id, rev)
		if e != nil {
			return models.ValidationReport{}, e
		}
		job, e = s.Options.Jobs.MarkRunning(job)
		if e != nil {
			return models.ValidationReport{}, e
		}
	}
	completed := false
	defer func() {
		if s.Options.Jobs != nil && !completed {
			_, _ = s.Options.Jobs.MarkCanceled(job, "Validation interrupted; explicitly retry the frozen revision")
		}
	}()
	d.Evidence, e = s.evidence(id, d.Sources)
	if e != nil {
		d.Evidence = nil
	}
	manual := len(reviewMode) > 0 && reviewMode[0] == "manual"

	if manual && !s.Options.AllowManual {
		return models.ValidationReport{}, ErrRules
	}
	report := documentvalidation.Validate(d)
	if operationKey != "" {
		report.ReportID = operationKey
		report.OperationID = operation
		if len(reviewMode) > 0 {
			report.ReviewMode = reviewMode[0]
		}
	}
	if e != nil {
		report.Issues = append(report.Issues, models.DocumentIssue{ID: "evidence_unavailable", Code: "evidence_unavailable", Severity: "error", Group: "evidence", Basis: "Frozen private evidence is unavailable; restore the backup or create a new source snapshot."})
		report = documentvalidation.Recompute(report)
	}
	if report.Status != "failed" && s.Options.Reviewer != nil && !manual {
		issues, model, err := s.Options.Reviewer.ReviewDocument(runCtx, d)
		status := "passed"
		if err == nil && len(issues) > 0 {
			status = "needs_review"
		}
		if err != nil {
			status = "error"
			report.Status = "error"
			issues = []models.DocumentIssue{{Code: "semantic_error", Severity: "warning", Group: "consistency", Basis: "Semantic reviewer unavailable or returned invalid output; complete an explicit manual review.", HumanConfirmation: true}}
		}
		if err == nil {
			kept := []models.DocumentIssue{}
			for _, i := range report.Issues {
				if i.Code != "manual_review" {
					kept = append(kept, i)
				}
			}
			report.Issues = kept
		}
		for i := range issues {
			issues[i].Group = "consistency"
			issues[i].HumanConfirmation = true
			if issues[i].Severity != "error" {
				issues[i].Severity = "warning"
			}
		}
		report.Issues = append(report.Issues, issues...)
		report.ReviewRuns[len(report.ReviewRuns)-1] = models.DocumentReviewRun{Kind: "semantic", Status: status, Model: model, PromptVersion: "document-review-v1"}
		report = documentvalidation.Recompute(report)
		if err != nil {
			report.Status = "error"
		}
	}
	if runCtx.Err() != nil && !(errors.Is(runCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil && s.ctx.Err() == nil) {
		return models.ValidationReport{}, runCtx.Err()
	}
	// Publication must serialize with deletion even though model work runs outside the lock.
	unlock = s.Store.Lock(id)
	if _, e = s.Store.ActiveHead(id); e == nil {
		e = s.Store.PutRecord(id, "reports", report.ReportID, report)
	}
	unlock()
	if e != nil {
		return report, e
	}
	// A late report remains historical; it never updates HEAD or the new draft.
	h, e = s.Store.ActiveHead(id)
	if e == nil && h.WorkingRevisionID == rev {
		s.emit("document.validation_completed", id, h, report.ReportID)
	}
	if s.Options.Jobs != nil {
		_, _ = s.Options.Jobs.MarkSucceeded(job, report.ReportID)
	}
	completed = true
	return report, nil
}

type FinalizeRequest struct {
	RevisionID    string                        `json:"revision_id"`
	ReportID      string                        `json:"report_id"`
	OperationID   string                        `json:"operation_id"`
	DeliveryLevel string                        `json:"delivery_level"`
	Confirmations []models.DocumentConfirmation `json:"confirmations"`
}

func (s *Service) Finalize(id string, expected uint64, req FinalizeRequest) (models.FinalizationRecord, error) {
	if !documentstore.ValidID(req.OperationID) {
		return models.FinalizationRecord{}, documentstore.ErrInvalid
	}
	key := "f-" + strings.TrimPrefix(documentstore.Hash([]byte(req.OperationID)), "sha256:")
	requestHash := documentstore.HashJSON(req)
	// Source checks may read other Markdown documents (and acquire their locks).
	// Run them before the publication lock to avoid cyclic source lock ordering.
	sourceUnavailable := false
	if req.DeliveryLevel == "ready_for_use" {
		if d, e := s.Store.Revision(id, req.RevisionID); e == nil {
			sourceUnavailable = s.sourceFreshness(id, d.Sources) == "unavailable"
		}
	}
	unlock := s.Store.Lock(id)
	defer unlock()
	h, e := s.Store.ActiveHead(id)
	if e != nil {
		return models.FinalizationRecord{}, e
	}
	existing, e := s.Store.Finalization(id, key)
	if e == nil {
		if existing.RequestHash != requestHash {
			return existing, documentstore.ErrConflict
		}
		if s.finalizationPublished(id, key) {
			return existing, nil
		}
		if h.Generation != expected || h.WorkingRevisionID != req.RevisionID {
			return existing, documentstore.ErrConflict
		}
		h.FinalizedRevisionID = req.RevisionID
		h.FinalizationID = key
		h.Generation++
		return existing, s.Store.SaveHead(id, h)
	} else if !os.IsNotExist(e) {
		return existing, e
	}
	if !s.Options.Enabled {
		return existing, ErrUnavailable
	}
	if h.Generation != expected || h.WorkingRevisionID != req.RevisionID {
		return existing, documentstore.ErrConflict
	}
	d, e := s.Store.Revision(id, req.RevisionID)
	if e != nil {
		return existing, e
	}
	if !s.enabledFamily(d.Contract.ProfileRef.Family) {
		return existing, ErrUnavailable
	}
	r, e := s.Store.Report(id, req.ReportID)
	if e != nil {
		return existing, e
	}
	if r.RevisionID != req.RevisionID || r.ValidationKey != documentvalidation.Key(d) {
		return existing, documentstore.ErrConflict
	}
	if _, e := s.evidence(id, d.Sources); e != nil {
		return existing, ErrRules
	}
	structural := documentvalidation.Validate(d)
	for _, issue := range structural.Issues {
		if issue.Severity == "error" {
			return existing, ErrRules
		}
	}
	if req.DeliveryLevel != "proposal" && req.DeliveryLevel != "ready_for_use" {
		return existing, documentstore.ErrInvalid
	}
	knownIssues := map[string]bool{}
	for _, i := range r.Issues {
		knownIssues[i.ID] = true
	}
	confirmations := map[string]string{}
	for _, c := range req.Confirmations {
		if !knownIssues[c.IssueID] || confirmations[c.IssueID] != "" || strings.TrimSpace(c.Reason) == "" {
			return existing, ErrRules
		}
		confirmations[c.IssueID] = c.Reason
	}
	manual := false
	for _, issue := range r.Issues {
		// Only explicitly marked semantic questions can be confirmed. AST errors cannot.
		if issue.Severity == "error" && !issue.HumanConfirmation {
			return existing, ErrRules
		}
		if issue.HumanConfirmation {
			if confirmations[issue.ID] == "" || !d.Contract.ReviewPolicy.AllowManual || !s.Options.AllowManual {
				return existing, ErrRules
			}
			manual = true
		}
	}
	if r.Status == "failed" || r.Status == "not_run" || r.Status == "running" {
		return existing, ErrRules
	}
	if r.Status == "error" && !manual {
		return existing, ErrRules
	}
	if req.DeliveryLevel == "ready_for_use" {
		if sourceUnavailable {
			return existing, ErrRules
		}
		for _, i := range r.Issues {
			if i.Code == "blocking_open_question" {
				return existing, ErrRules
			}
		}
		if strings.TrimSpace(d.Contract.Parameters["unresolved_blockers"]) != "" {
			return existing, ErrRules
		}
	}
	method := "structure_and_semantic"
	if manual {
		method = "structure_and_manual"
	}
	f := models.FinalizationRecord{FinalizationID: key, PreviousFinalizationID: h.FinalizationID, OperationID: req.OperationID, RequestHash: requestHash, RevisionID: req.RevisionID, ReportID: req.ReportID, ValidationKey: r.ValidationKey, ConfirmedIssues: req.Confirmations, FinalizedAt: time.Now().UTC(), DeliveryLevel: req.DeliveryLevel, ReviewMethod: method}
	if e = s.Store.PutRecord(id, "finalizations", key, f); e != nil {
		return f, e
	}
	h.FinalizedRevisionID = req.RevisionID
	h.FinalizationID = key
	h.Generation++
	if e = s.Store.SaveHead(id, h); e != nil {
		return f, e
	}
	s.emit("document.finalized", id, h, r.ReportID)
	return f, nil
}
func (s *Service) Export(id, rev, mode string) (string, error) {
	unlock := s.Store.Lock(id)
	defer unlock()
	if _, e := s.Store.ActiveHead(id); e != nil {
		return "", e
	}
	if mode != "draft" && mode != "final" {
		return "", documentstore.ErrInvalid
	}
	d, e := s.Store.Revision(id, rev)
	if e != nil {
		return "", e
	}
	if mode == "final" {
		if d.ResourceError != "" {
			return "", ErrRules
		}
		h, e := s.Store.ActiveHead(id)
		if e != nil {
			return "", e
		}
		found := false
		visited := map[string]bool{}
		for key := h.FinalizationID; key != ""; {
			if visited[key] {
				return "", documentstore.ErrInvalid
			}
			visited[key] = true
			f, e := s.Store.Finalization(id, key)
			if e != nil {
				return "", e
			}
			key = f.PreviousFinalizationID
			if f.RevisionID != rev {
				continue
			}
			r, e := s.Store.Report(id, f.ReportID)
			if e != nil {
				return "", e
			}
			if f.ValidationKey == r.ValidationKey && r.RevisionID == rev && r.DocumentID == id {
				found = true
			}
		}

		if !found {
			return "", ErrRules
		}
	}
	return d.Body, nil
}
func (s *Service) Repair(ctx context.Context, id, rev, reportID string, expected uint64, issueIDs, sections []string) (models.DocumentSnapshot, error) {
	if !s.Options.Enabled || s.ctx.Err() != nil {
		return models.DocumentSnapshot{}, ErrUnavailable
	}
	h, e := s.Store.ActiveHead(id)
	if e != nil {
		return models.DocumentSnapshot{}, e
	}
	if h.Generation != expected || h.WorkingRevisionID != rev {
		return models.DocumentSnapshot{}, documentstore.ErrConflict
	}
	d, e := s.Store.Revision(id, rev)
	if e != nil {
		return models.DocumentSnapshot{}, e
	}
	if !s.enabledFamily(d.Contract.ProfileRef.Family) {
		return models.DocumentSnapshot{}, ErrUnavailable
	}
	r, e := s.Store.Report(id, reportID)
	if e != nil {
		return models.DocumentSnapshot{}, e
	}
	if r.RevisionID != rev || r.ValidationKey != documentvalidation.Key(d) {
		return models.DocumentSnapshot{}, documentstore.ErrConflict
	}
	if s.Options.Repairer == nil {
		return models.DocumentSnapshot{}, ErrUnavailable
	}
	if d.Revision.RepairCount >= s.Options.MaxRepairAttempts {
		return models.DocumentSnapshot{}, ErrRules
	}
	if len(issueIDs) == 0 || len(sections) == 0 {
		return models.DocumentSnapshot{}, documentstore.ErrInvalid
	}
	selected := []models.DocumentIssue{}
	for _, id := range issueIDs {
		found := false
		for _, i := range r.Issues {
			if i.ID == id {
				selected = append(selected, i)
				found = true
			}
		}
		if !found {
			return models.DocumentSnapshot{}, documentstore.ErrInvalid
		}
	}
	profile, e := documentprofile.Parse([]byte(d.Profile), documentprofile.Limits{})
	if e != nil {
		return models.DocumentSnapshot{}, e
	}
	for i, section := range sections {
		if heading := profile.Validation.SectionHeadings[section]; heading != "" {
			sections[i] = heading
		}
	}
	runCtx, release, done, e := s.begin(ctx)
	if e != nil {
		return models.DocumentSnapshot{}, e
	}
	defer done()
	candidate, e := s.Options.Repairer.RepairDocument(runCtx, d, selected, sections)
	if e != nil {
		return models.DocumentSnapshot{}, e
	}
	if runCtx.Err() != nil {
		return models.DocumentSnapshot{}, runCtx.Err()
	}
	if e = CheckRepairScope(d.Body, candidate, sections, profile.Validation.HeadingLevel); e != nil {
		return models.DocumentSnapshot{}, e
	}
	d.Body = candidate
	d.Revision.Origin = "repair"
	d.Revision.RepairCount++
	out, e := s.CreateRevision(id, expected, d)
	if e != nil {
		return out, e
	}
	// Validation acquires its own model slot. Keep this worker tracked through
	// completion, but release the repair call's slot before that nested step.
	release()
	_, e = s.Validate(ctx, id, out.Head.WorkingRevisionID, out.Head.Generation)
	if e != nil {
		return out, e
	}
	return s.Get(id)
}
func (s *Service) emit(kind, id string, h models.DocumentHead, report string) {
	if s.Options.Commit != nil {
		if paths, e := s.Store.ContentPaths(id); e == nil {
			s.Options.Commit(paths)
		}
	}
	if s.Options.Emit != nil {
		s.Options.Emit(kind, map[string]any{"document_id": id, "revision_id": h.WorkingRevisionID, "generation": h.Generation, "report_id": report})
	}
}
func safeInputs(d documentstore.RevisionData) error {
	if len(d.Body) > 1024*1024 || len(d.Sources.Sources) > 100 {
		return documentstore.ErrInvalid
	}
	for _, source := range d.Sources.Sources {
		if documentvalidation.SensitiveLocator(source.Locator) || documentvalidation.SensitiveLocator(documentvalidation.MarkdownLocator(source.Locator)) || strings.ContainsAny(source.Locator, "\r\n") {
			return fmt.Errorf("%w: credential-bearing source", documentstore.ErrInvalid)
		}
	}
	return nil
}

func (s *Service) finalizationPublished(id, target string) bool {
	h, e := s.Store.ActiveHead(id)
	if e != nil {
		return false
	}
	seen := map[string]bool{}
	for key := h.FinalizationID; key != "" && !seen[key]; {
		if key == target {
			return true
		}
		seen[key] = true
		f, e := s.Store.Finalization(id, key)
		if e != nil {
			return false
		}
		key = f.PreviousFinalizationID
	}
	return false
}

func (s *Service) ProjectionCommittedLocked(id, rev string) error {
	h, e := s.Store.ActiveHead(id)
	if e != nil {
		return e
	}
	wasPending := h.PendingProjection
	if e := s.Store.ProjectionDoneLocked(id, rev); e != nil {
		return e
	}
	if wasPending && s.Options.Projected != nil {
		d, e := s.Store.Revision(id, rev)
		if e != nil {
			return e
		}
		if d.Revision.Origin == "editor" || d.Revision.Origin == "external_edit" || d.Revision.Origin == "repair" {
			s.Options.Projected(id)
		}
	}
	if s.Options.Commit != nil {
		paths, e := s.Store.ContentPaths(id)
		if e != nil {
			return e
		}
		s.Options.Commit(paths)
	}
	return nil
}

// DeleteLocked revokes access before Markdown cleanup; caller holds the document lock.
func (s *Service) DeleteLocked(id string) error {
	h, e := s.Store.DeleteLocked(id)
	if h.DeletedAt != nil {
		s.emit("document.deleted", id, h, "")
	}
	return e
}

func (s *Service) ValidationRunning(id, revision string) bool {
	value, ok := activeValidations.Load(s.Store.Root + "/" + id)
	return ok && value == revision
}
