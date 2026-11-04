package documentset

import (
	"fmt"
	"strings"
)

// Source.Message always addresses the complete frozen Messages slice, including
// assistant turns. It is neither a user-turn ordinal nor a one-based index.
func EvidenceFailureReason(c CaptureSnapshot, source Source) string {
	if strings.TrimSpace(source.Quote) == "" {
		return "quote_empty"
	}
	if source.Message < 0 || source.Message >= len(c.Messages) {
		return "message_out_of_range"
	}
	if c.Messages[source.Message].Role != "user" {
		return "not_user_message"
	}
	if !strings.Contains(c.Messages[source.Message].Text, source.Quote) {
		return "quote_not_found"
	}
	return ""
}

type FactEvidenceError struct {
	FactID  string
	Message int
	Reason  string
}

func (e FactEvidenceError) Error() string {
	return fmt.Sprintf("%s: fact %s lacks user evidence (source.message=%d; reason=%s)", ErrInvalid, e.FactID, e.Message, e.Reason)
}

func (e FactEvidenceError) Unwrap() error { return ErrInvalid }

func ValidateFactEvidence(s SharedDesign, c CaptureSnapshot) error {
	for _, f := range s.Facts {
		if !ValidKey(f.ID) {
			return ErrInvalid
		}
		if f.Status == "confirmed" {
			if reason := EvidenceFailureReason(c, f.Source); reason != "" {
				return FactEvidenceError{FactID: f.ID, Message: f.Source.Message, Reason: reason}
			}
		}
	}
	return nil
}

// RebindPlannerEvidence corrects only an invalid address with a unique exact
// quotation in a user message. It never rewrites quotations, changes fact status,
// fills missing evidence, infers meaning, or rebinds historical snapshot sources.
// Use only at the model adapter boundary; explicit API sources remain strict.
func RebindPlannerEvidence(s *SharedDesign, c CaptureSnapshot) int {
	count := 0
	rebind := func(source *Source) {
		if source == nil || strings.TrimSpace(source.Quote) == "" || UserEvidence(c, *source) {
			return
		}
		match := -1
		for i, m := range c.Messages {
			if m.Role == "user" && strings.Contains(m.Text, source.Quote) {
				if match >= 0 {
					return // Do not guess which confirmation turn was intended.
				}
				match = i
			}
		}
		if match >= 0 {
			source.Message = match
			count++
		}
	}
	for i := range s.Facts {
		f := s.Facts[i]
		// Extraction may legitimately retain exact assistant proposal sources;
		// user-confirmation rebinding must not replace their provenance.
		if f.Status != "confirmed" && f.Source.Message >= 0 && f.Source.Message < len(c.Messages) && strings.TrimSpace(f.Source.Quote) != "" && strings.Contains(c.Messages[f.Source.Message].Text, f.Source.Quote) {
			continue
		}
		rebind(&s.Facts[i].Source)
	}
	for key, condition := range s.Conditions {
		rebind(&condition.Source)
		s.Conditions[key] = condition
	}
	for i := range s.ScopeChanges {
		rebind(&s.ScopeChanges[i].Source)
	}
	if s.Scope != nil {
		rebind(&s.Scope.Source)
		for i := range s.Scope.Excluded {
			rebind(&s.Scope.Excluded[i].Source)
		}
	}
	for i := range s.Obligations {
		rebind(&s.Obligations[i].Source)
	}
	for i := range s.Questions {
		rebind(s.Questions[i].Source)
	}
	for i := range s.Changes {
		rebind(&s.Changes[i].Source)
	}
	return count
}
