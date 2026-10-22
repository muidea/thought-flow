package documentvalidation

import (
	"encoding/json"
	"strings"
	"testing"

	"thoughtflow/assets/documentformats"
)

func TestAllRenderedLinkNodesAreValidated(t *testing.T) {
	for _, node := range []string{
		"![diagram](URL)", "[link](URL)", "<URL>",
		"# [Document](URL)", "### [Appendix](URL)", "### ![diagram](URL)", "- <URL>",
	} {
		for _, locator := range []string{"https://example.org/safe", "https://example.org/?token=SECRET", "https://user:SECRET@example.org/"} {
			d := fixture(t, documentformats.DesignDocV2)
			d.Body = strings.Replace(d.Body, "## 方案设计\n", "## 方案设计\n\n"+strings.ReplaceAll(node, "URL", locator)+"\n\n", 1)
			r := Validate(d)
			unsafe := strings.Contains(locator, "SECRET")
			if strings.Contains(codes(r), "unsafe_link") != unsafe {
				t.Fatalf("link safety missed %s: %+v", node, r)
			}
			raw, _ := json.Marshal(r)
			if strings.Contains(string(raw), "SECRET") {
				t.Fatal("secret in diagnostics")
			}
		}
	}
}
