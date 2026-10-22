package documentflow

import (
	"strings"
	"testing"
)

func TestRepairScopeUsesFrozenHeadingLevel(t *testing.T) {
	for _, level := range []int{2, 3, 6} {
		heading := strings.Repeat("#", level)
		before := "# Document\n\n" + heading + " Allowed **section**\n\nOriginal content\n\n" + heading + " Adjacent\n\nKeep exactly\n"
		for _, after := range []string{before, strings.Replace(before, "Original content", "Repaired", 1)} {
			if err := CheckRepairScope(before, after, []string{"Allowed section"}, level); err != nil {
				t.Fatalf("H%d repair refused: %v", level, err)
			}
		}
		if err := CheckRepairScope(before, strings.Replace(before, "Keep exactly", "Changed", 1), []string{"Allowed section"}, level); err == nil {
			t.Fatalf("H%d adjacent edit accepted", level)
		}
		html := before + "\n<div>\n" + heading + " Fake section\n</div>\n"
		if err := CheckRepairScope(html, html, []string{"Fake section"}, level); err != nil {
			t.Fatalf("H%d differs from validator HTML-as-text parsing", level)
		}
	}
}
