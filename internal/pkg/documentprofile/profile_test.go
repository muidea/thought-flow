package documentprofile

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"thoughtflow/assets/documentformats"
	"thoughtflow/internal/pkg/models"
)

func TestParseAndRenderBuiltinDesignDoc(t *testing.T) {
	profile, err := Parse([]byte(documentformats.DesignDocV1), Limits{MaxFormatBytes: 1 << 20, MaxSections: 32})
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if profile.Ref.ProfileID != models.DocumentProfileBuiltinDesignDoc || profile.Ref.Family != models.DocumentFamilyDesign {
		t.Fatalf("profile ref = %+v", profile.Ref)
	}
	sections := map[string]models.DocumentSection{}
	for _, section := range profile.Sections {
		sections[section.Key] = models.DocumentSection{Content: strings.Repeat("有效设计内容。", 12)}
	}
	result := Render(profile, models.DocumentDraft{Title: "订单幂等设计", Summary: "设计摘要", Sections: sections}, nil)
	if result.Validation.Status != models.ArchiveValidationValid {
		t.Fatalf("validation = %+v", result.Validation)
	}
	if !strings.Contains(result.Content, "## 背景与问题") || strings.Contains(result.Content, "{{") {
		t.Fatalf("rendered content = %q", result.Content)
	}
}

func TestRenderRejectsMissingRequiredSection(t *testing.T) {
	profile, err := Parse([]byte(documentformats.ResearchReportV1), Limits{})
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	result := Render(profile, models.DocumentDraft{Title: "报告", Sections: map[string]models.DocumentSection{}}, nil)
	if result.Validation.Status != models.ArchiveValidationInvalid {
		t.Fatalf("validation status = %q", result.Validation.Status)
	}
	if len(result.Validation.Issues) == 0 {
		t.Fatal("expected validation issues")
	}
}

func TestRenderRejectsUnknownStructuredCitation(t *testing.T) {
	profile, err := Parse([]byte(documentformats.NoteV1), Limits{})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	result := Render(profile, models.DocumentDraft{
		Title: "Citation check",
		Sections: map[string]models.DocumentSection{
			"body": {Content: "Supported by [S2]."},
		},
		References: []models.DocumentReference{{ID: "S1", SourceLink: "https://example.com/source"}},
	}, nil)
	if result.Validation.Status != models.ArchiveValidationInvalid {
		t.Fatalf("validation = %+v", result.Validation)
	}
	found := false
	for _, validationIssue := range result.Validation.Issues {
		if validationIssue.Code == "thoughtflow.profile.reference_unknown" {
			found = true
		}
	}
	if !found {
		t.Fatalf("issues = %+v", result.Validation.Issues)
	}
}

func TestRenderRejectsNestedDocumentAndArchiveProcessText(t *testing.T) {
	profile, err := Parse([]byte(documentformats.NoteV1), Limits{})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	result := Render(profile, models.DocumentDraft{
		Title: "Final title",
		Sections: map[string]models.DocumentSection{
			"body": {Content: "# Nested title\n\n候选正文，供文档生成器使用"},
		},
	}, nil)
	if result.Validation.Status != models.ArchiveValidationInvalid {
		t.Fatalf("validation = %+v", result.Validation)
	}
	want := map[string]bool{
		"thoughtflow.profile.single_title_required": false,
		"thoughtflow.profile.archive_process_text":  false,
	}
	for _, issue := range result.Validation.Issues {
		if _, ok := want[issue.Code]; ok {
			want[issue.Code] = true
		}
	}
	for code, found := range want {
		if !found {
			t.Fatalf("missing issue %s: %+v", code, result.Validation.Issues)
		}
	}
}

func TestRenderRejectsSubstantiallyDuplicatedSections(t *testing.T) {
	profile, err := Parse([]byte(documentformats.DesignDocV1), Limits{})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	sections := map[string]models.DocumentSection{}
	for idx, section := range profile.Sections {
		sections[section.Key] = models.DocumentSection{Content: fmt.Sprintf("section-%d", idx)}
	}
	duplicate := strings.Repeat("完整设计内容不得在多个章节中重复。", 40)
	sections["background"] = models.DocumentSection{Content: duplicate}
	sections["proposal"] = models.DocumentSection{Content: duplicate}
	result := Render(profile, models.DocumentDraft{Title: "Duplicate check", Summary: "summary", Sections: sections}, nil)
	if result.Validation.Status != models.ArchiveValidationInvalid {
		t.Fatalf("validation = %+v", result.Validation)
	}
	found := false
	for _, issue := range result.Validation.Issues {
		found = found || issue.Code == "thoughtflow.profile.section_duplicate"
	}
	if !found {
		t.Fatalf("issues = %+v", result.Validation.Issues)
	}
}

func TestRegistryPublishesCustomFormat(t *testing.T) {
	root := t.TempDir()
	registry, err := NewRegistry(root, models.DocumentProfileBuiltinNote, Limits{MaxFormatBytes: 1 << 20, MaxSections: 32})
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}
	raw := strings.Replace(documentformats.NoteV1, "builtin.note", "custom.team-note", 1)
	profile, err := registry.Publish([]byte(raw))
	if err != nil {
		t.Fatalf("Publish() error = %v", err)
	}
	if profile.Ref.ProfileID != "custom.team-note" {
		t.Fatalf("profile id = %q", profile.Ref.ProfileID)
	}
	path := filepath.Join(root, "published", "custom.team-note", "v1.md")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("published file: %v", err)
	}
	resolved, err := registry.Resolve(profile.Ref)
	if err != nil || resolved.Ref.ContentHash != profile.Ref.ContentHash {
		t.Fatalf("Resolve() = %+v, %v", resolved.Ref, err)
	}
}

func TestRegistryCreatesCustomDirectoryLayout(t *testing.T) {
	root := filepath.Join(t.TempDir(), "profiles")
	registry, err := NewRegistry(root, models.DocumentProfileBuiltinNote, Limits{MaxFormatBytes: 1 << 20, MaxSections: 32})
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}
	registry.Close()
	for _, dir := range []string{filepath.Join(root, "drafts"), filepath.Join(root, "published")} {
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatalf("expected directory %s: %v", dir, err)
		}
		if !info.IsDir() {
			t.Fatalf("expected %s to be a directory", dir)
		}
	}
}

func TestRegistryAutoReloadsPublishedProfilesAndStopsOnClose(t *testing.T) {
	root := t.TempDir()
	registry, err := NewRegistry(root, models.DocumentProfileBuiltinNote, Limits{MaxFormatBytes: 1 << 20, MaxSections: 32})
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}
	registry.StartAutoReload(10 * time.Millisecond)

	raw := strings.Replace(documentformats.NoteV1, "builtin.note", "custom.auto-loaded", 1)
	writePublishedProfile(t, root, "custom.auto-loaded", 1, raw)
	waitForRegistry(t, time.Second, func() bool {
		_, err := registry.ResolveLatest("custom.auto-loaded")
		return err == nil
	})

	writePublishedProfile(t, root, "custom.invalid", 1, "not a document format")
	waitForRegistry(t, time.Second, func() bool { return len(registry.Issues()) > 0 })
	if _, err := registry.ResolveLatest(models.DocumentProfileBuiltinNote); err != nil {
		t.Fatalf("invalid custom profile affected builtin profiles: %v", err)
	}

	registry.Close()
	stoppedRaw := strings.Replace(documentformats.NoteV1, "builtin.note", "custom.after-close", 1)
	writePublishedProfile(t, root, "custom.after-close", 1, stoppedRaw)
	time.Sleep(50 * time.Millisecond)
	if _, err := registry.ResolveLatest("custom.after-close"); !errors.Is(err, ErrProfileNotFound) {
		t.Fatalf("ResolveLatest(after Close) error = %v, want ErrProfileNotFound", err)
	}
}

func TestRegistryReloadKeepsPreviousSnapshotWhenPublishedVersionChanges(t *testing.T) {
	root := t.TempDir()
	registry, err := NewRegistry(root, models.DocumentProfileBuiltinNote, Limits{MaxFormatBytes: 1 << 20, MaxSections: 32})
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}
	raw := strings.Replace(documentformats.NoteV1, "builtin.note", "custom.team-note", 1)
	published, err := registry.Publish([]byte(raw))
	if err != nil {
		t.Fatalf("Publish() error = %v", err)
	}
	path := filepath.Join(root, "published", "custom.team-note", "v1.md")
	mutated := strings.Replace(raw, "Team Note", "Changed Team Note", 1)
	if mutated == raw {
		mutated = raw + "\n<!-- changed -->\n"
	}
	if err := os.WriteFile(path, []byte(mutated), 0o644); err != nil {
		t.Fatalf("mutate published profile: %v", err)
	}

	status := registry.Reload()
	if len(status.Issues) == 0 || status.Issues[0].Code != "thoughtflow.profile.conflict" {
		t.Fatalf("reload issues = %+v", status.Issues)
	}
	resolved, err := registry.Resolve(published.Ref)
	if err != nil {
		t.Fatalf("Resolve(previous ref) error = %v", err)
	}
	if resolved.Ref.ContentHash != published.Ref.ContentHash {
		t.Fatalf("resolved hash = %q, want previous %q", resolved.Ref.ContentHash, published.Ref.ContentHash)
	}
	if _, err := registry.ResolveLatest(published.Ref.ProfileID); !errors.Is(err, ErrProfileNotFound) {
		t.Fatalf("ResolveLatest(conflicted profile) error = %v, want ErrProfileNotFound", err)
	}
}

func TestRegistryRejectsSymbolicLinkProfile(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.md")
	raw := strings.Replace(documentformats.NoteV1, "builtin.note", "custom.outside", 1)
	if err := os.WriteFile(outside, []byte(raw), 0o644); err != nil {
		t.Fatalf("write outside format: %v", err)
	}
	dir := filepath.Join(root, "published", "custom.outside")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "v1.md")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	registry, err := NewRegistry(root, models.DocumentProfileBuiltinNote, Limits{MaxFormatBytes: 1 << 20, MaxSections: 32})
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	if _, err := registry.ResolveLatest("custom.outside"); !errors.Is(err, ErrProfileNotFound) {
		t.Fatalf("ResolveLatest(symlink) error = %v", err)
	}
	if len(registry.Issues()) == 0 {
		t.Fatal("expected symlink validation issue")
	}
}

func TestRegistryKeepsHistoricalResolveWhenPublishedFileBecomesInvalid(t *testing.T) {
	root := t.TempDir()
	registry, err := NewRegistry(root, models.DocumentProfileBuiltinNote, Limits{MaxFormatBytes: 1 << 20, MaxSections: 32})
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	raw := strings.Replace(documentformats.NoteV1, "builtin.note", "custom.invalidated", 1)
	published, err := registry.Publish([]byte(raw))
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	path := filepath.Join(root, "published", "custom.invalidated", "v1.md")
	if err := os.WriteFile(path, []byte("not a document format"), 0o644); err != nil {
		t.Fatalf("invalidate format: %v", err)
	}
	registry.Reload()
	if _, err := registry.Resolve(published.Ref); err != nil {
		t.Fatalf("historical Resolve: %v", err)
	}
	if _, err := registry.ResolveLatest(published.Ref.ProfileID); !errors.Is(err, ErrProfileNotFound) {
		t.Fatalf("ResolveLatest(invalidated) error = %v", err)
	}
}

func TestRegistryPublishRejectsSymlinkedPublishedDirectory(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "published")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	registry, err := NewRegistry(root, models.DocumentProfileBuiltinNote, Limits{MaxFormatBytes: 1 << 20, MaxSections: 32})
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	raw := strings.Replace(documentformats.NoteV1, "builtin.note", "custom.symlink-publish", 1)
	if _, err := registry.Publish([]byte(raw)); err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("Publish error = %v", err)
	}
}

func writePublishedProfile(t *testing.T, root, profileID string, version int, raw string) {
	t.Helper()
	dir := filepath.Join(root, "published", profileID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create published profile directory: %v", err)
	}
	path := filepath.Join(dir, fmt.Sprintf("v%d.md", version))
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatalf("write published profile: %v", err)
	}
}

func waitForRegistry(t *testing.T, timeout time.Duration, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if ready() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out waiting for document profile registry")
}
