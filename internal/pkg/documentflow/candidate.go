package documentflow

import (
	"context"
	"os"
	"thoughtflow/internal/pkg/documentprofile"
	"thoughtflow/internal/pkg/documentstore"
	"thoughtflow/internal/pkg/documentvalidation"
	"thoughtflow/internal/pkg/models"
)

// CheckCandidate validates exact rendered bytes without changing a document
// HEAD. Slow preview generation may be superseded before publication.
func CheckCandidate(body, title string, profile documentprofile.DocumentProfile, parameters map[string]string, sources models.SourceManifest, contextHash string) (models.DocumentSnapshot, error) {
	contract := models.DocumentContract{SchemaVersion: 1, ProfileRef: profile.Ref, Title: title, Parameters: clone(parameters), Language: "zh-CN", Purpose: parameters["purpose"], Audience: parameters["audience"], ContextHash: contextHash, ReviewPolicy: models.DocumentReviewPolicy{Version: documentvalidation.ReviewPolicyVersion}}
	if contract.Purpose == "" {
		contract.Purpose = parameters["problem"]
	}
	d := documentstore.RevisionData{Body: body, Profile: profile.Snapshot, Contract: contract, Sources: sources}
	if e := safeInputs(d); e != nil {
		return models.DocumentSnapshot{}, e
	}
	r := documentvalidation.Validate(d)
	return models.DocumentSnapshot{Body: body, Contract: contract, Sources: sources, Report: &r}, nil
}

// Candidate freezes a generated preview without publishing it as a Thought or final.
// It executes deterministic validation only, so preview never incurs an extra model call.
func (s *Service) Candidate(ctx context.Context, id, body, title string, profile documentprofile.DocumentProfile, parameters map[string]string, sources models.SourceManifest, contextHash string) (models.DocumentSnapshot, error) {
	if !s.Options.Enabled || !s.enabledFamily(profile.Ref.Family) || s.ctx.Err() != nil {
		return models.DocumentSnapshot{}, ErrUnavailable
	}
	if e := ctx.Err(); e != nil {
		return models.DocumentSnapshot{}, e
	}
	contract := models.DocumentContract{SchemaVersion: 1, ProfileRef: profile.Ref, Title: title, Parameters: clone(parameters), Language: "zh-CN", Purpose: parameters["purpose"], Audience: parameters["audience"], ContextHash: contextHash, ReviewPolicy: models.DocumentReviewPolicy{Version: documentvalidation.ReviewPolicyVersion, SemanticRequired: true, AllowManual: s.Options.AllowManual}}
	if contract.Purpose == "" {
		contract.Purpose = parameters["problem"]
	}
	d := documentstore.RevisionData{Body: body, Profile: profile.Snapshot, Contract: contract, Sources: sources, Revision: models.DocumentRevision{Origin: "candidate"}}
	if e := safeInputs(d); e != nil {
		return models.DocumentSnapshot{}, e
	}
	unlock := s.Store.Lock(id)
	h, e := s.Store.Head(id)
	if e != nil && !os.IsNotExist(e) {
		unlock()
		return models.DocumentSnapshot{}, e
	}
	h, e = s.Store.Publish(id, h.Generation, d)
	if e == nil {
		h.PendingProjection = false
		e = s.Store.SaveHead(id, h)
	}
	unlock()
	if e != nil {
		return models.DocumentSnapshot{}, e
	}
	d, e = s.Store.Revision(id, h.WorkingRevisionID)
	if e != nil {
		return models.DocumentSnapshot{}, e
	}
	r := documentvalidation.Validate(d)
	if e = s.Store.PutRecord(id, "reports", r.ReportID, r); e != nil {
		return models.DocumentSnapshot{}, e
	}
	return s.GetUnderLease(id)
}

// CheckDraft records deterministic results without requiring/paying for semantic
// review. An incomplete draft still saves; formal delivery remains a separate gate.
func (s *Service) CheckDraft(id, rev string) (models.ValidationReport, error) {
	if _, e := s.Store.ActiveHead(id); e != nil {
		return models.ValidationReport{}, e
	}
	d, e := s.Store.Revision(id, rev)
	if e != nil {
		return models.ValidationReport{}, e
	}
	r := documentvalidation.Validate(d)
	return r, s.Store.PutRecord(id, "reports", r.ReportID, r)
}
