package biz

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"thoughtflow/internal/pkg/models"
)

func TestEnqueueChangeSetMergesAndFiltersPaths(t *testing.T) {
	root := t.TempDir()
	service := NewService(&models.Workspace{
		ID:          "local",
		RootPath:    root,
		RuntimePath: filepath.Join(root, "runtime-data"),
	}, nil, nil, nil, true, time.Hour)
	if service.timer != nil {
		defer service.timer.Stop()
	}

	changeSet := service.EnqueueChangeSet(
		[]string{
			"thoughts/2026/06/a.md",
			"thoughts/2026/06/a.md",
			"topics/demo/index.md",
			".thoughtflow/jobs/job.yaml",
			".thoughtflow/thoughtflow.duckdb",
			"runtime-data/jobs/job.yaml",
			"runtime-data/logs/app.log",
			"runtime.duckdb",
			"runtime.duckdb.wal",
			"../outside.md",
		},
		"capture",
		[]string{"thought-a", "thought-a"},
	)

	if changeSet.ID == "" {
		t.Fatalf("expected changeset id")
	}
	if changeSet.Reason != "capture" {
		t.Fatalf("reason = %q", changeSet.Reason)
	}
	if len(changeSet.Paths) != 2 {
		t.Fatalf("paths = %#v", changeSet.Paths)
	}
	if changeSet.Paths[0] != "thoughts/2026/06/a.md" || changeSet.Paths[1] != "topics/demo/index.md" {
		t.Fatalf("paths = %#v", changeSet.Paths)
	}
	if len(changeSet.ResourceIDs) != 1 || changeSet.ResourceIDs[0] != "thought-a" {
		t.Fatalf("resource ids = %#v", changeSet.ResourceIDs)
	}
	if changeSet.CreatedAt.IsZero() || changeSet.DebounceUntil.IsZero() || !changeSet.DebounceUntil.After(changeSet.CreatedAt) {
		t.Fatalf("change set times = %#v", changeSet)
	}
}

func TestEnqueueChangeSetReturnsCurrentPendingSnapshot(t *testing.T) {
	service := NewService(&models.Workspace{ID: "local"}, nil, nil, nil, true, time.Hour)
	if service.timer != nil {
		defer service.timer.Stop()
	}

	first := service.EnqueueChangeSet([]string{"thoughts/a.md"}, "capture", []string{"a"})
	second := service.EnqueueChangeSet([]string{"topics/demo/index.md"}, "topic_update", []string{"topic-demo"})

	if second.ID != first.ID {
		t.Fatalf("expected same pending changeset id, first=%q second=%q", first.ID, second.ID)
	}
	if len(second.Paths) != 2 {
		t.Fatalf("paths = %#v", second.Paths)
	}
	if second.Reason != "capture,topic_update" {
		t.Fatalf("reason = %q", second.Reason)
	}
	if len(second.ResourceIDs) != 2 || second.ResourceIDs[0] != "a" || second.ResourceIDs[1] != "topic-demo" {
		t.Fatalf("resource ids = %#v", second.ResourceIDs)
	}
}

func TestNormalizedCommitPathRejectsRuntimeAndUnsafePaths(t *testing.T) {
	rejected := []string{
		"",
		".",
		"..",
		"../outside.md",
		"/tmp/outside.md",
		".thoughtflow",
		".thoughtflow/jobs/job.yaml",
		"thoughtflow.duckdb",
		"thoughtflow.duckdb.wal",
	}
	for _, path := range rejected {
		if got, ok := normalizedCommitPath(path); ok {
			t.Fatalf("normalizedCommitPath(%q) = %q, true", path, got)
		}
	}

	if got, ok := normalizedCommitPath("topics/demo/../demo/index.md"); !ok || got != "topics/demo/index.md" {
		t.Fatalf("normalizedCommitPath() = %q, %v", got, ok)
	}
}

func TestRecentCommitsReturnsPathHistory(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git is unavailable: %v", err)
	}
	root := t.TempDir()
	thoughtPath := filepath.ToSlash(filepath.Join("thoughts", "2026", "06", "20260609-143010-detail.md"))
	fullPath := filepath.Join(root, filepath.FromSlash(thoughtPath))
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(fullPath, []byte("# Detail thought\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	runGitForTest(t, root, "init")
	runGitForTest(t, root, "config", "user.name", "ThoughtFlow Test")
	runGitForTest(t, root, "config", "user.email", "thoughtflow-test@example.test")
	runGitForTest(t, root, "add", "--", thoughtPath)
	runGitForTest(t, root, "commit", "-m", "thoughtflow: add detail thought")

	service := NewService(&models.Workspace{ID: "local", RootPath: root}, nil, nil, nil, true, time.Hour)
	records := service.RecentCommits(context.Background(), thoughtPath, "20260609-143010-detail", 5)

	if len(records) != 1 {
		t.Fatalf("records = %#v", records)
	}
	if records[0].CommitHash == "" ||
		records[0].Message != "thoughtflow: add detail thought" ||
		len(records[0].Paths) != 1 ||
		records[0].Paths[0] != thoughtPath ||
		len(records[0].ResourceIDs) != 1 ||
		records[0].ResourceIDs[0] != "20260609-143010-detail" ||
		records[0].CommittedAt.IsZero() {
		t.Fatalf("record = %#v", records[0])
	}
}

func TestRuntimeStatusReportsRepositoryIdentityAndDirtyState(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git is unavailable: %v", err)
	}
	root := t.TempDir()
	runGitForTest(t, root, "init")
	runGitForTest(t, root, "config", "user.name", "ThoughtFlow Test")
	runGitForTest(t, root, "config", "user.email", "thoughtflow-test@example.test")
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("dirty\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	service := NewService(&models.Workspace{ID: "local", RootPath: root}, nil, nil, nil, true, time.Hour)
	status := service.RuntimeStatus(context.Background())

	if status.Status != "ready" ||
		!status.Enabled ||
		!status.Repository ||
		!status.IdentityConfigured ||
		!status.Dirty {
		t.Fatalf("status = %#v", status)
	}
}

func TestRuntimeStatusReportsMissingGitIdentity(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git is unavailable: %v", err)
	}
	root := t.TempDir()
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(root, "global-gitconfig"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	runGitForTest(t, root, "init")

	service := NewService(&models.Workspace{ID: "local", RootPath: root}, nil, nil, nil, true, time.Hour)
	status := service.RuntimeStatus(context.Background())

	if status.Status != "degraded" || status.IdentityConfigured || !strings.Contains(status.Error, "user.name and user.email") {
		t.Fatalf("status = %#v", status)
	}
}

func TestRuntimeStatusReportsDisabledGit(t *testing.T) {
	service := NewService(&models.Workspace{ID: "local", RootPath: t.TempDir()}, nil, nil, nil, false, time.Hour)
	status := service.RuntimeStatus(context.Background())

	if status.Status != "disabled" || status.Enabled {
		t.Fatalf("status = %#v", status)
	}
}

// TestEnsureRepositoryInitsLocalRepoWhenNestedUnderParentRepo locks
// in the fix for the audit failure where the workspace sat inside an
// ancestor git repo (the thought-flow checkout itself) whose
// .gitignore covered the workspace path. `git add` would then reject
// every thought with "ignored by .gitignore". ensureRepository must
// detect that the work-tree toplevel differs from the workspace path
// and run a dedicated `git init` inside the workspace.
func TestEnsureRepositoryInitsLocalRepoWhenNestedUnderParentRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git is unavailable: %v", err)
	}
	parent := t.TempDir()
	runGitForTest(t, parent, "init")
	runGitForTest(t, parent, "config", "user.name", "Parent")
	runGitForTest(t, parent, "config", "user.email", "parent@example.test")
	// Mirror the project's .gitignore pattern: a subdirectory of the
	// parent repo is excluded, so reusing the parent repo for the
	// workspace would make `git add thoughts/...` fail.
	if err := os.WriteFile(filepath.Join(parent, ".gitignore"), []byte("workspace/\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	workspace := filepath.Join(parent, "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	service := NewService(&models.Workspace{ID: "local", RootPath: workspace}, nil, nil, nil, true, time.Hour)
	if err := service.ensureRepository(context.Background()); err != nil {
		t.Fatalf("ensureRepository() error = %v", err)
	}

	// The workspace should now be its own work tree (`.git` directly
	// inside it), not just a subdir of the parent's repo.
	dotGit := filepath.Join(workspace, ".git")
	if _, err := os.Stat(dotGit); err != nil {
		t.Fatalf("workspace .git not present: %v", err)
	}
	toplevelRaw, err := exec.Command("git", "-C", workspace, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatalf("git rev-parse --show-toplevel: %v", err)
	}
	gotToplevel, err := filepath.EvalSymlinks(strings.TrimSpace(string(toplevelRaw)))
	if err != nil {
		gotToplevel = strings.TrimSpace(string(toplevelRaw))
	}
	want, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		want = workspace
	}
	if filepath.Clean(gotToplevel) != filepath.Clean(want) {
		t.Fatalf("workspace toplevel = %q, want %q", gotToplevel, want)
	}
}

func runGitForTest(t *testing.T, root string, args ...string) {
	t.Helper()
	cmdArgs := append([]string{"-C", root}, args...)
	cmd := exec.Command("git", cmdArgs...)
	raw, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %s: %v", strings.Join(cmdArgs, " "), strings.TrimSpace(string(raw)), err)
	}
}

func TestCommitRecordsDeletionAndPreservesUnrelatedIndex(t *testing.T) {
	for _, stagedDeletion := range []bool{false, true} {
		t.Run(fmt.Sprint(stagedDeletion), func(t *testing.T) {
			root := newCommitTestRepo(t)
			thought := "thoughts/delete.md"
			writeCommitTestFile(t, root, thought, "delete me\n")
			writeCommitTestFile(t, root, "user.md", "original\n")
			runGitForTest(t, root, "add", "--", thought, "user.md")
			runGitForTest(t, root, "commit", "-m", "initial")
			writeCommitTestFile(t, root, "user.md", "staged version\n")
			writeCommitTestFile(t, root, ".thoughtflow/jobs/private.yaml", "private\n")
			runGitForTest(t, root, "add", "--", "user.md", ".thoughtflow/jobs/private.yaml")
			writeCommitTestFile(t, root, "user.md", "unstaged version\n")
			before := gitOutputForTest(t, root, "ls-files", "--stage", "--", "user.md", ".thoughtflow/jobs/private.yaml")
			if err := os.Remove(filepath.Join(root, thought)); err != nil {
				t.Fatal(err)
			}
			if stagedDeletion {
				runGitForTest(t, root, "add", "-A", "--", thought)
			}
			svc := NewService(&models.Workspace{ID: "local", RootPath: root}, nil, nil, nil, true, time.Hour)
			record, err := svc.Commit(context.Background(), []string{thought, "topics/optional.yaml"}, []string{"delete"})
			if err != nil {
				t.Fatal(err)
			}
			if record.CommitHash == "" {
				t.Fatal("missing commit")
			}
			if got := gitOutputForTest(t, root, "ls-tree", "-r", "--name-only", "HEAD", "--", thought, ".thoughtflow"); got != "" {
				t.Fatalf("deleted/private file in HEAD: %q", got)
			}
			if got := gitOutputForTest(t, root, "show", "HEAD:user.md"); got != "original\n" {
				t.Fatalf("unrelated content committed: %q", got)
			}
			after := gitOutputForTest(t, root, "ls-files", "--stage", "--", "user.md", ".thoughtflow/jobs/private.yaml")
			if after != before {
				t.Fatalf("user index changed: before=%q after=%q", before, after)
			}
			if got := gitOutputForTest(t, root, "diff-tree", "--no-commit-id", "--name-status", "-r", "HEAD"); got != "D\t"+thought+"\n" {
				t.Fatalf("commit diff=%q", got)
			}
			raw, err := os.ReadFile(filepath.Join(root, "user.md"))
			if err != nil || string(raw) != "unstaged version\n" {
				t.Fatalf("working tree changed: %q %v", raw, err)
			}
		})
	}
}

func TestCommitScopesNewAndNoopCommits(t *testing.T) {
	for _, withThought := range []bool{false, true} {
		t.Run(fmt.Sprint(withThought), func(t *testing.T) {
			root := newCommitTestRepo(t)
			runGitForTest(t, root, "commit", "--allow-empty", "-m", "initial")
			writeCommitTestFile(t, root, ".thoughtflow/jobs/private.yaml", "private\n")
			runGitForTest(t, root, "add", "--", ".thoughtflow/jobs/private.yaml")
			paths := []string{"topics/never-created.yaml"}
			if withThought {
				writeCommitTestFile(t, root, "thoughts/new[1].md", "new\n")
				paths = append(paths, "thoughts/new[1].md")
			}
			svc := NewService(&models.Workspace{ID: "local", RootPath: root}, nil, nil, nil, true, time.Hour)
			if _, err := svc.Commit(context.Background(), paths, nil); err != nil {
				t.Fatal(err)
			}
			if got := gitOutputForTest(t, root, "ls-tree", "-r", "--name-only", "HEAD", "--", ".thoughtflow"); got != "" {
				t.Fatalf("runtime data committed: %q", got)
			}
			if got := gitOutputForTest(t, root, "diff", "--cached", "--name-only"); got != ".thoughtflow/jobs/private.yaml\n" {
				t.Fatalf("staged files=%q", got)
			}
			if withThought && gitOutputForTest(t, root, "show", "HEAD:thoughts/new[1].md") != "new\n" {
				t.Fatal("new thought missing")
			}
		})
	}
}

func newCommitTestRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip(err)
	}
	root := t.TempDir()
	runGitForTest(t, root, "init")
	runGitForTest(t, root, "config", "user.name", "ThoughtFlow Test")
	runGitForTest(t, root, "config", "user.email", "thoughtflow-test@example.test")
	return root
}
func writeCommitTestFile(t *testing.T, root, path, body string) {
	t.Helper()
	full := filepath.Join(root, path)
	if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
}
func gitOutputForTest(t *testing.T, root string, args ...string) string {
	t.Helper()
	raw, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %s: %v", args, raw, err)
	}
	return string(raw)
}

func TestTopicWorkspaceCommitAllowlistExcludesPrivateCanaries(t *testing.T) {
	root := newCommitTestRepo(t)
	runGitForTest(t, root, "commit", "--allow-empty", "-m", "initial")
	allowed := []string{"topics/t/catalog.json", "topics/t/attachments/res-a/file.txt", ".content-registry/objects/note.json"}
	denied := []string{"topics/t/.private/evidence/n/hash.txt", "topics/t/candidates.yaml", "topics/t/unlisted.txt", ".content-registry/private/outbox/op.json", ".content-registry/private/migrations/op/state.json", "topics/t/documents/n/revisions/r/unlisted.json"}
	for _, path := range append(append([]string{}, allowed...), denied...) {
		writeCommitTestFile(t, root, path, "CANARY "+path)
	}
	svc := NewService(&models.Workspace{ID: "local", RootPath: root}, nil, nil, nil, true, time.Hour)
	if _, e := svc.Commit(context.Background(), append(allowed, denied...), nil); e != nil {
		t.Fatal(e)
	}
	tree := gitOutputForTest(t, root, "ls-tree", "-r", "--name-only", "HEAD")
	for _, path := range allowed {
		if !strings.Contains(tree, path+"\n") {
			t.Fatal("missing", path)
		}
	}
	for _, path := range denied {
		if strings.Contains(tree, path+"\n") {
			t.Fatal("private/unlisted file committed", path)
		}
	}
}

func TestDocumentSetGitAllowlistKeepsMetadataAndExcludesAttempts(t *testing.T) {
	for _, path := range []string{"topics/set-a/document-set/head.json", "topics/set-a/document-set/revisions/set-r-a/manifest.json", "topics/set-a/document-set/revisions/set-r-a/validation.json", "topics/set-a/document-set/finalizations/set-f-a.json"} {
		if _, ok := normalizedCommitPath(path); !ok {
			t.Fatal("public set metadata rejected", path)
		}
	}
	for _, path := range []string{".content-registry/private/document-set-runs/run-a.json", "topics/set-a/.private/evidence/private.txt", "topics/set-a/document-set/revisions/set-r-a/attempt.json"} {
		if _, ok := normalizedCommitPath(path); ok {
			t.Fatal("private set state admitted", path)
		}
	}
}
