package documentset

import (
	"errors"
	"reflect"
	"testing"
)

func TestPlannerEvidenceOnlyRebindsUniqueExactUserQuotation(t *testing.T) {
	capture := CaptureSnapshot{Messages: []Message{{Role: "user", Text: "Original scope. Repeated."}, {Role: "ai", Text: "Assistant-only claim."}, {Role: "user", Text: "保留原文：订单必须可恢复。 Repeated."}, {Role: "ai", Text: "订单必须可恢复。"}}}
	for _, tc := range []struct {
		name         string
		source       Source
		index, count int
		reason       string
	}{
		{"absolute-user-index", Source{2, "订单必须可恢复。"}, 2, 0, ""},
		{"user-turn-ordinal", Source{1, "订单必须可恢复。"}, 2, 1, ""},
		{"one-based-index", Source{3, "订单必须可恢复。"}, 2, 1, ""},
		{"out-of-range", Source{99, "订单必须可恢复。"}, 2, 1, ""},
		{"wrong-user", Source{0, "订单必须可恢复。"}, 2, 1, ""},
		{"empty", Source{0, ""}, 0, 0, "quote_empty"},
		{"assistant-only", Source{1, "Assistant-only claim."}, 1, 0, "not_user_message"},
		{"paraphrase", Source{2, "订单应当支持恢复"}, 2, 0, "quote_not_found"},
		{"ambiguous", Source{1, "Repeated."}, 1, 0, "not_user_message"},
		{"valid-explicit-repeat", Source{0, "Repeated."}, 0, 0, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fact := Fact{ID: "REQ-001", Kind: "requirement", Name: "Recovery", Content: "Exact planned content", Status: "confirmed", Source: tc.source}
			shared := SharedDesign{Facts: []Fact{fact}}
			before := append([]Message(nil), capture.Messages...)
			if count := RebindPlannerEvidence(&shared, capture); count != tc.count || shared.Facts[0].Source.Message != tc.index || shared.Facts[0].Source.Quote != tc.source.Quote || shared.Facts[0].Content != fact.Content || shared.Facts[0].Status != fact.Status || !reflect.DeepEqual(capture.Messages, before) {
				t.Fatal("evidence guessed, rewritten, or capture mutated", shared, count)
			}
			_, err := FreezeShared(shared, capture)
			if tc.reason == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				var evidence FactEvidenceError
				if !errors.As(err, &evidence) || !errors.Is(err, ErrInvalid) || evidence.Reason != tc.reason {
					t.Fatal("lost evidence gate or diagnosis", err)
				}
			}
		})
	}
}

func TestPlannerEvidenceAddressesCurrentSourcesButNeverHistoricalEvidence(t *testing.T) {
	c := CaptureSnapshot{Messages: []Message{{Role: "user", Text: "Old"}, {Role: "ai", Text: "Reply"}, {Role: "user", Text: "New scope evidence"}}}
	src := Source{1, "New scope evidence"}
	shared := SharedDesign{Facts: []Fact{{Source: src, Status: "proposed"}}, Conditions: map[string]Condition{"has_persistence": {Source: src}}, Scope: &DesignScope{Source: src, Excluded: []ScopeExclusion{{Source: src}}}, ScopeChanges: []ScopeChange{{Source: src}}, Obligations: []ObligationResolution{{Source: src}}, Questions: []Question{{Source: &Source{Message: 1, Quote: src.Quote}}}, Changes: []FactChange{{Source: src}}, History: []HistoricalFact{{Fact: Fact{Source: src}}}}
	if n := RebindPlannerEvidence(&shared, c); n != 8 {
		t.Fatal("missed current source", n)
	}
	for _, s := range []Source{shared.Facts[0].Source, shared.Conditions["has_persistence"].Source, shared.Scope.Source, shared.Scope.Excluded[0].Source, shared.ScopeChanges[0].Source, shared.Obligations[0].Source, *shared.Questions[0].Source, shared.Changes[0].Source} {
		if s.Message != 2 || s.Quote != src.Quote {
			t.Fatal(s)
		}
	}
	if shared.Facts[0].Status != "proposed" || shared.History[0].Fact.Source != src {
		t.Fatal("invented confirmation or modified history")
	}
}

func TestExplicitSourceDoesNotRebindDuringFreeze(t *testing.T) {
	c := CaptureSnapshot{Messages: []Message{{Role: "user", Text: "Old scope"}, {Role: "ai", Text: "Reply"}, {Role: "user", Text: "New requirement"}}}
	shared := SharedDesign{Facts: []Fact{{ID: "REQ-001", Kind: "requirement", Name: "New", Content: "New requirement", Status: "confirmed", Source: Source{1, "New requirement"}}}}
	_, err := FreezeShared(shared, c)
	var evidence FactEvidenceError
	if !errors.As(err, &evidence) || evidence.Reason != "not_user_message" || shared.Facts[0].Source.Message != 1 {
		t.Fatal("explicit source was silently rebound", err)
	}
}

func TestProposalEvidenceKeepsOriginalAssistantAddress(t *testing.T) {
	c := CaptureSnapshot{Messages: []Message{{Role: "user", Text: "Discuss persistence"}, {Role: "ai", Text: "Use checkpoint recovery"}, {Role: "user", Text: "Archive: Use checkpoint recovery"}}}
	s := SharedDesign{Facts: []Fact{{Status: "proposed", Source: Source{Message: 1, Quote: "Use checkpoint recovery"}}}}
	if n := RebindPlannerEvidence(&s, c); n != 0 || s.Facts[0].Source.Message != 1 {
		t.Fatal("proposal source rebound as user confirmation")
	}
}
