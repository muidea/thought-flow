package documentvalidation

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"thoughtflow/assets/documentformats"
)

// The same fixtures run through the embedded Web renderer in app.test.js.
func TestEncodedRenderedDestinations(t *testing.T) {
	raw, err := os.ReadFile("testdata/rendered-links.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name, Locator, Rendered string
		Unsafe                  bool
	}
	if err = json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	for _, tc := range cases {
		t.Run(tc.Name, func(t *testing.T) {
			if got := MarkdownLocator(tc.Locator); got != tc.Rendered {
				t.Fatalf("validation sees %q, renderer sees %q", got, tc.Rendered)
			}
			for _, node := range []string{"![diagram](URL)", "[link](URL)", "# [Title](URL)", "[link][ref]\n\n[ref]: URL"} {
				d := fixture(t, documentformats.DesignDocV2)
				d.Body += "\n\n" + strings.ReplaceAll(node, "URL", tc.Locator) + "\n"
				r := Validate(d)
				if strings.Contains(codes(r), "unsafe_link") != tc.Unsafe {
					t.Fatalf("%s: unexpected link safety: %+v", node, r)
				}
				diagnostics, err := json.Marshal(r)
				if err != nil || strings.Contains(string(diagnostics), "SECRET") {
					t.Fatalf("secret in diagnostics: %s (%v)", diagnostics, err)
				}
			}
		})
	}
}

func TestEncodedDeliveredReferenceCannotUseSafeManifest(t *testing.T) {
	d := fixture(t, documentformats.DesignDocV2)
	d.Body = strings.Replace(d.Body, "- [S1] https://example.org/evidence: https://example.org/evidence",
		"- [S1] [source](https://example.org/?to&#107;en=SECRET)", 1)
	r := Validate(d)
	if !strings.Contains(codes(r), "reference_locator") || !strings.Contains(codes(r), "reference_mismatch") {
		t.Fatalf("rendered reference bypassed frozen manifest: %+v", r)
	}
	raw, _ := json.Marshal(r)
	if strings.Contains(string(raw), "SECRET") {
		t.Fatal("secret in reference diagnostics")
	}
}
