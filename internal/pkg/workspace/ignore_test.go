package workspace

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"thoughtflow/internal/pkg/models"
)

func TestPrivateIgnoresPreserveUserRulesAndProtectManualGitAdds(t *testing.T) {
	root := t.TempDir()
	ignorePath := filepath.Join(root, ".gitignore")
	original := "# User rules\n*.log"
	if err := os.WriteFile(ignorePath, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	ws := &models.Workspace{RootPath: root, RuntimePath: filepath.Join(root, "local state[1]")}
	if err := ensurePrivateIgnores(ws); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(ignorePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(first[:len(original)]) != original {
		t.Fatal("user rules changed")
	}
	if err := ensurePrivateIgnores(ws); err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(ignorePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatal("reopen duplicates ignore rules")
	}
	if out, err := exec.Command("git", "-C", root, "init").CombinedOutput(); err != nil {
		t.Fatalf("git init: %s %v", out, err)
	}
	for _, path := range []string{".scratchpad/session.json", ".thoughtflow/state.json", "topics/subject/candidates.yaml", "documents/capture-session/HEAD.json", "documents/compose-draft/HEAD.json", "local state[1]/jobs/private.json", "existing.log"} {
		if out, err := exec.Command("git", "-C", root, "check-ignore", "--", path).CombinedOutput(); err != nil {
			t.Errorf("private path %q is not ignored: %s %v", path, out, err)
		}
	}
	for _, path := range []string{"thoughts/note.md", "topics/subject/index.md", "documents/note/HEAD.json"} {
		if out, err := exec.Command("git", "-C", root, "check-ignore", "--", path).CombinedOutput(); err == nil {
			t.Errorf("public content %q ignored: %s", path, out)
		}
	}
}
