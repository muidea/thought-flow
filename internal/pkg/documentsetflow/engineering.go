package documentsetflow

import (
	"sort"
	"thoughtflow/internal/pkg/documentset"
	"thoughtflow/internal/pkg/documentstore"
)

// Resolve only from the retained, verified ancestry. Caller-supplied history is
// never accepted as evidence. Copies are held solely to make offline checking possible.
func (s *Service) resolveHistory(set, parent string, shared *documentset.SharedDesign) error {
	refs := map[documentset.FactReference]bool{}
	for _, c := range shared.Changes {
		for _, ref := range c.From {
			refs[ref] = true
		}
	}
	shared.History = nil
	seen := map[string]bool{}
	for parent != "" {
		if seen[parent] {
			return documentset.ErrInvalid
		}
		seen[parent] = true
		rev, e := s.Store.Read(set, parent)
		if e != nil {
			return e
		}
		for ref := range refs {
			if ref.SetID != set || ref.RevisionID != rev.ID {
				continue
			}
			found := false
			for _, f := range rev.Plan.Shared.Facts {
				if f.ID == ref.FactID && f.Hash == ref.Hash {
					shared.History = append(shared.History, documentset.HistoricalFact{Ref: ref, Fact: f})
					found = true
					break
				}
			}
			if !found {
				return documentset.ErrInvalid
			}
			delete(refs, ref)
		}
		parent = rev.Parent
	}
	if len(refs) > 0 {
		return documentset.ErrInvalid
	}
	sort.Slice(shared.History, func(i, j int) bool {
		a, b := shared.History[i].Ref, shared.History[j].Ref
		return a.RevisionID+"/"+a.FactID < b.RevisionID+"/"+b.FactID
	})
	return documentset.ValidateHistory(*shared)
}

func guardEngineeringScope(next *documentset.SharedDesign, old documentset.Plan, capture documentset.CaptureSnapshot) {
	for _, prior := range old.Shared.Questions {
		if prior.Kind == "" || prior.Resolution != "" {
			continue
		} // Empty kind is reserved for server-generated rule diagnostics.
		found := false
		for i, q := range next.Questions {
			if q.ID != prior.ID {
				continue
			}
			found = true
			authorized := q.Source != nil && q.Source.Message >= old.Capture.MessageCount && documentset.UserEvidence(capture, *q.Source)
			if !authorized && (q.Resolution != "" || q.Blocking != prior.Blocking || q.Kind != prior.Kind || documentstore.HashJSON(q.Facts) != documentstore.HashJSON(prior.Facts)) {
				next.Questions[i] = prior
			}
		}
		if !found {
			next.Questions = append(next.Questions, prior)
		}
	}
	// New scope, obligations and relation semantics also require NEW user input.
	// A fact quotation inherited from the old plan is not approval of these changes.
	type semantics struct {
		Scope       *documentset.DesignScope
		Obligations []documentset.ObligationResolution
		Relations   []documentset.Relation
	}
	a, b := semantics{next.Scope, next.Obligations, next.Relations}, semantics{old.Shared.Scope, old.Shared.Obligations, old.Shared.Relations}
	// Quotes are private envelopes; compare stable semantic bytes only.
	clean := func(v semantics) string {
		if v.Scope != nil {
			x := *v.Scope
			x.Source = documentset.Source{}
			x.Excluded = append([]documentset.ScopeExclusion{}, x.Excluded...)
			for i := range x.Excluded {
				x.Excluded[i].Source = documentset.Source{}
			}
			v.Scope = &x
		}
		v.Obligations = append([]documentset.ObligationResolution{}, v.Obligations...)
		for i := range v.Obligations {
			v.Obligations[i].Source = documentset.Source{}
		}
		return documentstore.HashJSON(v)
	}
	if clean(a) == clean(b) {
		return
	}
	authorized := next.Scope != nil && next.Scope.Source.Message >= old.Capture.MessageCount && documentset.UserEvidence(capture, next.Scope.Source)
	if !authorized {
		next.Questions = append(next.Questions, documentset.Question{Kind: "question", ID: "scope-change-approval", Blocking: true, Message: "Scope, obligation or relation changes require new user approval"})
	}
}
