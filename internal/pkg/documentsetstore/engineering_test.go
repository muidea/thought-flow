package documentsetstore

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"

	ds "thoughtflow/internal/pkg/documentset"
	"thoughtflow/internal/pkg/documentstore"
	"thoughtflow/internal/testutil/designfixture"
)

func packageBytes(t *testing.T, raw []byte) (map[string][]byte, PackageManifest) {
	t.Helper()
	zr, e := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if e != nil {
		t.Fatal(e)
	}
	files := map[string][]byte{}
	var manifest PackageManifest
	for _, f := range zr.File {
		r, e := f.Open()
		if e != nil {
			t.Fatal(e)
		}
		b, e := io.ReadAll(r)
		r.Close()
		if e != nil {
			t.Fatal(e)
		}
		if f.Name == "package.json" {
			if e = json.Unmarshal(b, &manifest); e != nil {
				t.Fatal(e)
			}
		} else {
			files[f.Name] = b
		}
	}
	manifest.Files = nil
	return files, manifest
}
func TestEngineeringPublicPackageAndMixedReview(t *testing.T) {
	r := designfixture.ReviewedRevision(t)
	s := New(t.TempDir())
	if _, e := s.exportEngineering(r, false); e == nil {
		t.Fatal("missing publication authorization accepted")
	}
	raw, e := s.exportEngineering(r, true)
	if e != nil {
		t.Fatal(e)
	}
	if check, e := VerifyPublicPackage(raw, r.Report.PublicPayloadHash); e != nil || check.Status != "PASS" || check.Publisher != "NOT_AUTHENTICATED" {
		t.Fatal(check, e)
	}
	for _, which := range []string{"body", "contract", "review", "conversion", "extra", "missing", "payload-anchor", "private"} {
		t.Run(which, func(t *testing.T) {
			files, m := packageBytes(t, raw)
			switch which {
			case "body":
				files["architecture.md"] = append(files["architecture.md"], []byte("\nchanged")...)
			case "contract":
				var d ds.PublicDescriptor
				json.Unmarshal(files["design.json"], &d)
				d.Facts[0].Content = "Changed requirement"
				files["design.json"], _ = ds.PublicJSON(d)
			case "review":
				var report PublicReview
				json.Unmarshal(files["review.json"], &report)
				report.PayloadHash = "sha256:another-payload"
				files["review.json"], _ = ds.PublicJSON(report)
				m.PublicReviewHash = documentstore.Hash(files["review.json"])
			case "conversion":
				var d ds.PublicDescriptor
				json.Unmarshal(files["design.json"], &d)
				d.Conversion = "unknown"
				files["design.json"], _ = ds.PublicJSON(d)
			case "extra":
				files["unexpected.txt"] = []byte("unexpected")
			case "missing":
				delete(files, "architecture.html")
			case "payload-anchor":
				m.PublicPayloadHash = "sha256:wrong"
			case "private":
				files["private.txt"] = []byte("password=SUPERSECRET")
			}
			changed, e := zipFiles(files, m)
			if e != nil {
				t.Fatal(e)
			}
			if _, e := VerifyPublicPackage(changed, r.Report.PublicPayloadHash); e == nil {
				t.Fatal("recomputed distribution manifest concealed mutation")
			}
		})
	}
	files, _ := packageBytes(t, raw)
	for _, b := range files {
		if bytes.Contains(b, []byte("PRIVATE-CAPTURE-CANARY")) || bytes.Contains(b, []byte(`"session_id"`)) || bytes.Contains(b, []byte(`"quote"`)) {
			t.Fatal("private metadata leaked")
		}
	}
}
func TestHandoffSourceMappingAndLayeredBudgets(t *testing.T) {
	r := designfixture.ReviewedRevision(t)
	d, _, e := ds.PublicPayload(r)
	if e != nil {
		t.Fatal(e)
	}
	p, e := BuildHandoff(d, HandoffRequest{})
	if e != nil {
		t.Fatal(e)
	}
	if len(p.Input.Acceptance) != 0 || p.Budget.Consumer != "NOT_RUN" || p.Budget.Assembly != "NOT_RUN" || p.Budget.Merged != "PENDING_TEMPLATE" || p.Input.Version != nil || p.Input.Revision != nil || p.Confirmation != "UNCONFIRMED" {
		t.Fatalf("fabricated consumer readiness: %+v", p)
	}
	if len(p.Mappings) != len(d.Facts) || len(p.Blockers) > 0 {
		t.Fatal("lost facts", p.Blockers)
	}
	for _, m := range p.Mappings {
		if m.Channel != "design_resource" || m.ConsumerConfirmation != "PENDING_CONSUMER_DERIVATION" {
			t.Fatal("design became direct input")
		}
	}
	v, rev := 1, uint64(1)
	req := HandoffRequest{Goal: "Build the confirmed orders design", TemplateID: "real-template", WorkspaceID: "real-workspace", Version: &v, InputRevision: &rev, Direct: []DirectAcceptance{{Text: "Verify restart", DesignFact: "TEST-ORDER"}}}
	p, e = BuildHandoff(d, req)
	if e != nil {
		t.Fatal(e)
	}
	req.ConfirmedInputHash = p.InputHash
	p, e = BuildHandoff(d, req)
	if e != nil || p.Confirmation != "CONFIRMED_PRODUCER_INPUT" {
		t.Fatal(p, e)
	}
	for _, mapping := range p.Mappings {
		if mapping.Channel == "direct_acceptance" && (mapping.InputAdoption != p.Confirmation || mapping.ConsumerConfirmation != "PENDING_CONSUMER_CONFIRMATION") {
			t.Fatal("local material confirmation forged consumer confirmation", mapping)
		}
	}
	req.Direct[0].Text = "Changed acceptance"
	stale, e := BuildHandoff(d, req)
	if e != nil || stale.Status != "BLOCKED" {
		t.Fatal("changed exact input reused confirmation", e)
	}
	req.Direct[0].Text = "Verify restart"
	*req.InputRevision = 2
	stale, _ = BuildHandoff(d, req)
	if stale.Status != "BLOCKED" {
		t.Fatal("new revision reused old confirmation")
	}
	req.ConfirmedInputHash = ""
	req.Template = &ConsumerTemplate{ID: req.TemplateID, Items: []string{"template rule"}}
	req.Template.Hash = TemplateHash(*req.Template)
	p, _ = BuildHandoff(d, req)
	req.ConfirmedInputHash = p.InputHash
	req.Template.Items[0] = "new template rule"
	req.Template.Hash = TemplateHash(*req.Template)
	stale, _ = BuildHandoff(d, req)
	if stale.Status != "BLOCKED" {
		t.Fatal("template change reused confirmation")
	}
}
func TestHandoffByteAndMergedBudgetBoundaries(t *testing.T) {
	r := designfixture.ReviewedRevision(t)
	d, _, _ := ds.PublicPayload(r)
	// The fixed consumer example: 128037 input bytes may still exceed the
	// merged-acceptance limit after one 4096-byte template item. Resources add
	// their own reference bytes to the input formula; nothing is truncated.
	v, rev := 1, uint64(1)
	req := HandoffRequest{Goal: strings.Repeat("g", 35), TemplateID: "t", WorkspaceID: "w", Version: &v, InputRevision: &rev}
	for i := 0; i < 32; i++ {
		req.Direct = append(req.Direct, DirectAcceptance{Text: strings.Repeat("x", 4000)})
	}
	req.Template = &ConsumerTemplate{ID: "t", Items: []string{strings.Repeat("a", 4096)}}
	req.Template.Hash = TemplateHash(*req.Template)
	p, e := BuildHandoff(d, req)
	if e != nil {
		t.Fatal(e)
	}
	if p.Budget.InputBytes < 128037 || p.Budget.InputBytes > 131072 || p.Budget.MergedLowerBound != 132096 || p.Budget.Merged != "FAIL" || p.Status != "BLOCKED" {
		t.Fatalf("merged lower bound missed: %+v", p.Budget)
	}
	for _, n := range []int{4096, 4097} {
		req.Direct = []DirectAcceptance{{Text: strings.Repeat("界", n/3) + strings.Repeat("x", n%3)}}
		req.Template = nil
		p, e := BuildHandoff(d, req)
		if e != nil {
			t.Fatal(e)
		}
		if (len(p.Blockers) == 0) != (n == 4096) {
			t.Fatal("byte boundary", n, p.Blockers)
		}
	}
	req.Direct = nil
	req.Goal = strings.Repeat("a", 32768)
	p, _ = BuildHandoff(d, req)
	if len(p.Blockers) != 0 {
		t.Fatal(p.Blockers)
	}
	req.Goal += "a"
	p, _ = BuildHandoff(d, req)
	if p.Status != "BLOCKED" {
		t.Fatal("oversize goal accepted")
	}
	req.Goal = "goal"
	d.Facts[0].Content = strings.Repeat("界", 12000)
	p, e = BuildHandoff(d, req)
	if e != nil {
		t.Fatal(e)
	}
	if p.Status != "BLOCKED" || !strings.Contains(strings.Join(p.Blockers, ","), "indivisible_fact") {
		t.Fatal("oversize fact truncated or accepted", p.Blockers)
	}
}

func TestHandoffInvalidInputsNeverReportPass(t *testing.T) {
	d, _, e := ds.PublicPayload(designfixture.ReviewedRevision(t))
	if e != nil {
		t.Fatal(e)
	}
	for _, tc := range []struct {
		name string
		req  HandoffRequest
	}{
		{"unbound-long-template", HandoffRequest{TemplateID: strings.Repeat("t", 257)}},
		{"unbound-long-workspace", HandoffRequest{WorkspaceID: strings.Repeat("w", 257)}},
		{"unbound-version", HandoffRequest{Version: newValue(2)}},
		{"unbound-revision", HandoffRequest{InputRevision: newValue(uint64(0))}},
		{"empty-acceptance", HandoffRequest{Direct: []DirectAcceptance{{Text: " \t\n "}}}},
		{"nul-acceptance", HandoffRequest{Direct: []DirectAcceptance{{Text: "must\x00not"}}}},
		{"invalid-utf8", HandoffRequest{Direct: []DirectAcceptance{{Text: string([]byte{0xff})}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, e := BuildHandoff(d, tc.req)
			if e != nil || p.Status != "BLOCKED" || p.Budget.Input != "FAIL" {
				t.Fatalf("invalid supplied input reported usable: %+v %v", p, e)
			}
		})
	}
	for _, n := range []int{128, 129} {
		req := HandoffRequest{}
		for i := 0; i < n; i++ {
			req.Direct = append(req.Direct, DirectAcceptance{Text: fmt.Sprintf("Check %d", i)})
		}
		p, e := BuildHandoff(d, req)
		if e != nil || (p.Status != "BLOCKED") != (n == 128) || len(p.Input.Acceptance) != n {
			t.Fatal("acceptance count changed or miscounted", n, e, p.Blockers)
		}
	}
}

func newValue[T any](v T) *T { return &v }

func TestHandoffResourceBudgetsKeepEveryByte(t *testing.T) {
	d, _, e := ds.PublicPayload(designfixture.ReviewedRevision(t))
	if e != nil {
		t.Fatal(e)
	}
	// Isolate transport sizing from semantic validation (the Store validates the
	// complete design before invoking this adapter). Each JSON fact is indivisible.
	base := d.Facts[0]
	makeFact := func(i, size int) ds.PublicFact {
		f := base
		f.ID, f.Content = fmt.Sprintf("REQ-%d", i), ""
		raw, e := ds.PublicJSON(f)
		if e != nil {
			t.Fatal(e)
		}
		overhead := len("\n## " + f.ID + "\n\n" + string(raw) + "\n")
		if size < overhead {
			t.Fatal("invalid test size", size, overhead)
		}
		f.Content = strings.Repeat("x", size-overhead)
		return f
	}
	for _, size := range []int{32768, 32769} {
		d.Facts = []ds.PublicFact{makeFact(0, size)}
		p, e := BuildHandoff(d, HandoffRequest{})
		if e != nil || len(p.Resources) != 2 || p.Resources[1].Bytes != size ||
			(p.Status != "BLOCKED") != (size == 32768) {
			t.Fatal("per-resource boundary or truncation", size, e, p.Blockers)
		}
	}
	d.Facts = nil
	header, e := BuildHandoff(d, HandoffRequest{})
	if e != nil {
		t.Fatal(e)
	}
	for _, total := range []int{131072, 131073} {
		d.Facts = nil
		for i := 0; i < 3; i++ {
			d.Facts = append(d.Facts, makeFact(i, 32768))
		}
		d.Facts = append(d.Facts, makeFact(3, total-header.Budget.ResourceBytes-3*32768))
		p, e := BuildHandoff(d, HandoffRequest{})
		if e != nil || p.Budget.ResourceBytes != total || (p.Status != "BLOCKED") != (total == 131072) {
			t.Fatal("resource snapshot boundary", total, e, p.Budget, p.Blockers)
		}
	}
	for _, count := range []int{8, 9} {
		d.Facts = nil
		for i := 1; i < count; i++ {
			d.Facts = append(d.Facts, makeFact(i, 32768))
		}
		p, e := BuildHandoff(d, HandoffRequest{})
		if e != nil || len(p.Resources) != count || len(p.Input.ResourceRefs) != count || p.Status != "BLOCKED" {
			t.Fatal("oversized resource plan was shortened", count, e, p.Blockers)
		}
	}
	d.Facts = []ds.PublicFact{base}
	d.Set = "../outside"
	p, e := BuildHandoff(d, HandoffRequest{})
	if e != nil || p.Status != "BLOCKED" || !strings.Contains(strings.Join(p.Blockers, ","), "resource_invalid") {
		t.Fatal("unsafe resource path accepted", e, p.Blockers)
	}
}
