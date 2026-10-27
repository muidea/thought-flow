package documentset

import (
	"testing"

	"thoughtflow/internal/pkg/models"
)

func TestSingleRiskIdentityIgnoresDisplayOrdinal(t *testing.T) {
	first := models.DocumentIssue{ID: "i2", Code: "source_unverified", Severity: "warning", Group: "evidence", Section: "S1", Line: 12, Evidence: "[S1]", Basis: "Unverified source", HumanConfirmation: true}
	id := singleIssueID("architecture", first)
	reordered := first
	reordered.ID = "i30"
	if singleIssueID("architecture", reordered) != id {
		t.Fatal("display ordering invalidated the same risk")
	}
	for _, change := range []string{"member", "source", "position", "evidence", "rule"} {
		t.Run(change, func(t *testing.T) {
			issue, member := first, "architecture"
			switch change {
			case "member":
				member = "verification"
			case "source":
				issue.Section = "S2"
			case "position":
				issue.Line++
			case "evidence":
				issue.Evidence = "[S2]"
			case "rule":
				issue.Code = "reference_mismatch"
			}
			if singleIssueID(member, issue) == id {
				t.Fatal("different risk reused the approval identity")
			}
		})
	}
}
