package documentset

import "testing"

func TestThreeWayMergeRetainsIndependentChanges(t *testing.T) {
	base := "# Scope\nBase scope\n\n# Design\nBase design\n\n# Verification\nBase verification\n"
	generated := "# Scope\nBase scope\n\n# Design\nNew design\n\n# Verification\nBase verification\n"
	manual := "# Scope\nManual scope\n\n# Design\nBase design\n\n# Verification\nBase verification\n"
	expected := "# Scope\nManual scope\n\n# Design\nNew design\n\n# Verification\nBase verification\n"
	if merged, ok := Merge(base, generated, manual); !ok || merged != expected {
		t.Fatal(ok, merged)
	}
	if _, ok := Merge(base, generated, generated+"a conflicting edit\n"); ok {
		t.Fatal("overlap accepted")
	}
	if merged, ok := Merge(base, base, manual); !ok || merged != manual {
		t.Fatal("manual-only edit lost")
	}
}
