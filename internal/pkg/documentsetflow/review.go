package documentsetflow

import (
	"context"
	"errors"
	"os"
	"strings"
	"time"

	ds "thoughtflow/internal/pkg/documentset"
	"thoughtflow/internal/pkg/documentsetstore"
	"thoughtflow/internal/pkg/documentstore"
	"thoughtflow/internal/pkg/objectlocation"
)

type ReviewRequest struct {
	OperationID        string              `json:"operation_id"`
	ExpectedGeneration *uint64             `json:"expected_generation"`
	CandidateHash      string              `json:"candidate_manifest_hash"`
	RunID              string              `json:"run_id"`
	Reviewer           string              `json:"reviewer"`
	Evidence           []ds.ReviewEvidence `json:"evidence"`
	Decisions          []ds.ReviewDecision `json:"decisions"`
}

func (s *Service) Review(req ReviewRequest) (ds.Run, error) {
	if req.ExpectedGeneration == nil || req.CandidateHash == "" {
		return ds.Run{}, ds.ErrPrecondition
	}
	if !documentstore.ValidID(req.OperationID) || strings.TrimSpace(req.Reviewer) == "" {
		return ds.Run{}, ds.ErrInvalid
	}
	s.mu.Lock()
	if s.running[req.RunID] != nil {
		s.mu.Unlock()
		return ds.Run{}, ds.ErrConflict
	}
	s.mu.Unlock()
	release := objectlocation.For(s.Store.Root).LockObject("set-review-" + req.RunID)
	defer release()
	run, e := s.Store.Run(req.RunID)
	if e != nil {
		return run, e
	}
	if run.Candidate == nil || run.CancelRequested {
		return run, ds.ErrConflict
	}
	candidate := *run.Candidate
	hash := documentstore.HashJSON(req)
	record := documentsetstore.Private(run.Plan.SetID) + "/reviews/" + req.OperationID + ".json"
	var prior struct {
		Hash  string `json:"hash"`
		RunID string `json:"run_id"`
	}
	if e = objectlocation.ReadJSON(s.Store.Root, record, &prior); e == nil {
		if prior.Hash != hash || prior.RunID != req.RunID {
			return run, ds.ErrConflict
		}
		if run.State == "committed" {
			return run, nil
		}
	} else if !os.IsNotExist(e) {
		return run, e
	}
	if run.State == "committed" || candidate.Report.CandidateHash != req.CandidateHash || candidate.Plan.BaseGeneration != *req.ExpectedGeneration {
		return run, ds.ErrConflict
	}
	if candidate.Report.StructuralStatus != "passed" {
		return run, ds.ErrRules
	}
	for _, issue := range candidate.Report.Issues {
		if issue.Hard || issue.Severity == "error" {
			return run, ds.ErrRules
		}
	}
	// Every checklist item needs located evidence. Risk confirmations are separate
	// from structural gates and bind to this exact candidate.
	issues := candidate.Report.Issues
	decisions := map[string]ds.ReviewDecision{}
	for _, d := range req.Decisions {
		if d.IssueID == "" || strings.TrimSpace(d.Reason) == "" || len(d.Locations) == 0 || decisions[d.IssueID].IssueID != "" {
			return run, ds.ErrInvalid
		}
		decisions[d.IssueID] = d
	}
	for _, issue := range issues {
		if !issue.Confirmable || decisions[issue.ID].IssueID == "" {
			return run, ds.ErrRules
		}
	}
	candidate.Report.Issues = nil
	if e = ds.ApplyReview(&candidate, ds.ReviewResponse{Evidence: req.Evidence, Issues: []ds.Issue{}}, "manual_reviewed", req.Reviewer); e != nil {
		return run, e
	}
	candidate.Report.Issues = issues
	candidate.Report.ReviewDecisions = req.Decisions
	ds.Rehash(&candidate)
	if candidate.Report.Status != "ready" {
		return run, ds.ErrRules
	}
	if e = objectlocation.WriteJSON(s.Store.Root, record, map[string]string{"hash": hash, "run_id": req.RunID}, true); e != nil {
		return run, e
	}
	run.Candidate = &candidate
	run.State = "ready"
	if e = s.save(&run); e != nil {
		return run, e
	}
	e = s.commit(&run)
	return run, e
}

type FinalizeRequest struct {
	OperationID        string  `json:"operation_id"`
	ExpectedGeneration *uint64 `json:"expected_generation"`
	RevisionID         string  `json:"set_revision_id"`
	RevisionHash       string  `json:"set_revision_hash"`
	ApprovedBy         string  `json:"approved_by"`
	DeliveryLevel      string  `json:"delivery_level"`
}

func (s *Service) Finalize(id string, req FinalizeRequest) (ds.Head, error) {
	if req.ExpectedGeneration == nil || req.RevisionHash == "" {
		return ds.Head{}, ds.ErrPrecondition
	}
	if !documentstore.ValidID(req.OperationID) || strings.TrimSpace(req.ApprovedBy) == "" || (req.DeliveryLevel != "proposal" && req.DeliveryLevel != "ready_for_use") {
		return ds.Head{}, ds.ErrInvalid
	}
	registry := objectlocation.For(s.Store.Root)
	registry.Namespace.Lock()
	reconcile := false
	defer func() {
		registry.Namespace.Unlock()
		if reconcile {
			s.Store.Recover(s.finish)
		}
	}()
	h, e := s.Store.Head(id)
	if e != nil {
		return h, e
	}
	record := documentsetstore.Private(id) + "/finalizations/" + req.OperationID + ".json"
	hash := documentstore.HashJSON(req)
	var previous struct {
		Hash string  `json:"request_hash"`
		Head ds.Head `json:"head"`
	}
	if e = objectlocation.ReadJSON(s.Store.Root, record, &previous); e == nil {
		if previous.Hash != hash {
			return h, ds.ErrConflict
		}
		for _, f := range h.Finalizations {
			if f.OperationID == req.OperationID {
				reconcile = true
				return previous.Head, nil
			}
		}
		if h.Generation != *req.ExpectedGeneration {
			return h, ds.ErrConflict
		}
	} else if !os.IsNotExist(e) {
		return h, e
	}
	if h.Generation != *req.ExpectedGeneration || h.Working != req.RevisionID {
		return h, ds.ErrConflict
	}
	r, e := s.Store.Read(id, req.RevisionID)
	if e != nil {
		return h, e
	}
	if r.Hash != req.RevisionHash {
		return h, ds.ErrConflict
	}
	check := ds.Validate(r)
	if ds.Ready(r) != nil || check.StructuralStatus != "passed" || r.Report.Status != "ready" || r.Report.CandidateHash != ds.CandidateHash(r) || (r.Report.SemanticStatus != "model_reviewed" && r.Report.SemanticStatus != "manual_reviewed") {
		return h, ds.ErrRules
	}
	if s.opts.Snapshot == nil && len(r.Plan.Capture.Sources.Sources) != 0 {
		return h, ds.ErrUnavailable
	}
	if s.opts.Snapshot != nil {
		snapshotReader := s.opts.Snapshot
		if s.opts.SnapshotUnderLease != nil {
			snapshotReader = s.opts.SnapshotUnderLease
		}
		now, e := snapshotReader(r.Plan.Capture.SessionID)
		if e == nil && (now.Generation != r.Plan.Capture.Generation || now.ContextHash != r.Plan.Capture.ContextHash || documentstore.HashJSON(now.Sources) != documentstore.HashJSON(r.Plan.Capture.Sources)) {
			return h, ds.ErrConflict
		}
		if e != nil {
			// A restored, source-free set can use its authenticated frozen gate without
			// the original conversation. Missing related evidence is never this case.
			if !errors.Is(e, ds.ErrCaptureMissing) || len(r.Plan.Capture.Sources.Sources) != 0 {
				return h, e
			}
		}
	}
	h.Generation++
	h.Finalized = r.ID
	h.OperationID = req.OperationID
	h.Finalization = &ds.Finalization{ID: documentstore.ID("set-f-"), RevisionID: r.ID, RevisionHash: r.Hash, ReportHash: r.Report.Hash, DeliveryLevel: req.DeliveryLevel, ApprovedBy: req.ApprovedBy, At: time.Now().UTC(), OperationID: req.OperationID}
	h.Finalizations = append(h.Finalizations, *h.Finalization)
	if e = s.Store.WriteFinalization(id, *h.Finalization); e != nil {
		return h, e
	}
	intent := documentsetstore.Intent{Kind: "finalization", FinalizationID: h.Finalization.ID, OperationID: req.OperationID, RequestHash: hash, SetID: id, RevisionID: r.ID, RevisionHash: r.Hash, Expected: *req.ExpectedGeneration, Events: []string{"document_set.finalized"}}
	if e = s.Store.SaveIntent(intent); e != nil {
		return h, e
	}
	if e = objectlocation.WriteJSON(s.Store.Root, record, struct {
		Hash string  `json:"request_hash"`
		Head ds.Head `json:"head"`
	}{hash, h}, true); e != nil {
		return h, e
	}
	if s.Store.Fault != nil {
		if e = s.Store.Fault("finalization_intent_written"); e != nil {
			return h, e
		}
	}
	if e = objectlocation.WriteJSON(s.Store.Root, documentsetstore.Base(id)+"/head.json", h, false); e != nil {
		return h, e
	}
	if s.Store.Fault != nil {
		if e = s.Store.Fault("finalization_head_published"); e != nil {
			return h, e
		}
	}
	reconcile = true
	intent.Committed = true
	if e = s.Store.SaveIntent(intent); e != nil {
		return h, e
	}

	return h, nil
}

type RetryRequest struct {
	OperationID          string  `json:"operation_id"`
	ExpectedGeneration   *uint64 `json:"expected_generation"`
	AcknowledgeUncertain bool    `json:"acknowledge_uncertain"`
}

func (s *Service) Retry(id string, req RetryRequest) (ds.Run, error) {
	return s.RetryWithBinding(id, req, nil)
}

// Bind the persisted Capture pointer before launching a retry. Failed CAS keeps
// the candidate private and cannot replace a newer session attempt.
func (s *Service) RetryWithBinding(id string, req RetryRequest, bind func(ds.Run) error) (ds.Run, error) {
	if req.ExpectedGeneration == nil {
		return ds.Run{}, ds.ErrPrecondition
	}
	if !documentstore.ValidID(req.OperationID) || req.OperationID == id {
		return ds.Run{}, ds.ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing || s.running[id] != nil {
		return ds.Run{}, ds.ErrConflict
	}
	old, e := s.Store.Run(id)
	if e != nil {
		return old, e
	}
	hash := documentstore.HashJSON(struct {
		ID  string
		Req RetryRequest
	}{id, req})
	if existing, e := s.Store.Run(req.OperationID); e == nil {
		if existing.RequestHash != hash {
			return existing, ds.ErrConflict
		}
		if e = validateRunMembers(existing); e != nil {
			return existing, e
		}
		if bind != nil {
			if e = bind(existing); e != nil {
				return existing, e
			}
		}
		if existing.State == "queued" && s.running[existing.ID] == nil {
			s.launch(existing.ID)
		}
		return existing, nil
	} else if !os.IsNotExist(e) {
		return old, e
	}
	if e = validateRunMembers(old); e != nil {
		return old, e
	}
	if old.ModelHash != s.opts.ModelHash {
		return old, ds.ErrConflict
	}
	if old.State == "committed" || old.State == "generating" || old.State == "checking" {
		return old, ds.ErrConflict
	}
	if old.Plan.BaseGeneration != *req.ExpectedGeneration {
		return old, ds.ErrConflict
	}
	if e = s.check(old.Plan); e != nil {
		return old, e
	}
	for _, c := range old.Calls {
		if (c.State == "uncertain" || c.State == "running") && !req.AcknowledgeUncertain {
			return old, ds.ErrRules
		}
	}
	next := old
	next.ID = req.OperationID
	next.OperationID = req.OperationID
	next.RequestHash = hash
	next.State = "queued"
	next.Error = ""
	next.CancelRequested = false
	next.CreatedAt = time.Now().UTC()
	// A retry is a new attempt with a new immutable candidate, reusing only saved
	// successful member artifacts. Calls and consumed budget remain in the ledger.
	if old.Candidate != nil {
		candidate := *old.Candidate
		candidate.ID = documentstore.ID("set-r-")
		candidate.OperationID = req.OperationID
		candidate.Report = ds.Report{}
		candidate.Hash = ""
		next.Candidate = &candidate
	}
	if e = s.Store.SaveRun(next); e != nil {
		return next, e
	}
	next, e = s.Store.Run(next.ID)
	if e != nil {
		return next, e
	}
	if bind != nil {
		if e = bind(next); e != nil {
			return next, e
		}
	}
	s.launch(next.ID)
	return next, nil
}

// Search is an authoritative fixed-version lexical query. No asynchronous
// working-document index can mix snippets from another selected set revision.
func (s *Service) Search(id, revision, query string) ([]ds.Artifact, error) {
	r, e := s.Store.Read(id, revision)
	if e != nil {
		return nil, e
	}
	if strings.TrimSpace(query) == "" || len(query) > 1000 {
		return nil, ds.ErrInvalid
	}
	out := []ds.Artifact{}
	for _, a := range r.Members {
		if strings.Contains(strings.ToLower(a.Body), strings.ToLower(query)) {
			out = append(out, a)
		}
	}
	return out, nil
}

var _ = context.Canceled

// RecoverRun completes projections and durable notifications for a published
// attempt. It cannot regenerate, publish a candidate, or incur model charges.
func (s *Service) RecoverRun(id string) (ds.Run, error) {
	release := objectlocation.For(s.Store.Root).LockObject("set-publish-" + id)
	defer release()
	s.mu.Lock()
	busy := s.closing || s.running[id] != nil
	s.mu.Unlock()
	if busy {
		return ds.Run{}, ds.ErrConflict
	}
	run, e := s.Store.Run(id)
	if e != nil {
		return run, e
	}
	if run.Candidate == nil || !s.Store.Reachable(run.Plan.SetID, run.Candidate.ID) {
		return run, ds.ErrConflict
	}
	issues := s.Store.RecoverSet(run.Plan.SetID, s.finish)
	run.State = "committed"
	run.Error = ""
	if len(issues) > 0 {
		run.Error = "Committed; derived projections pending recovery: " + issues[0].Error()
	}
	if e = s.Store.SaveRun(run); e != nil {
		return run, e
	}
	if len(issues) > 0 {
		return run, issues[0]
	}
	return run, nil
}
