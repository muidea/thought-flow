package workspace

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"thoughtflow/internal/pkg/appconfig"
)

func TestOpenCreatesWorkspaceDirectories(t *testing.T) {
	root := filepath.Join(t.TempDir(), "workspace")
	ws, err := Open(context.Background(), appconfig.Config{
		Workspace: appconfig.WorkspaceConfig{Root: root},
		GitSync:   appconfig.GitSyncConfig{Enabled: true},
	})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}

	for _, dir := range []string{ws.RootPath, ws.ThoughtsPath, ws.TopicsPath, ws.RuntimePath, ws.JobsPath} {
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatalf("expected directory %s: %v", dir, err)
		}
		if !info.IsDir() {
			t.Fatalf("expected %s to be directory", dir)
		}
	}
	if !ws.GitEnabled {
		t.Fatalf("expected git enabled flag to follow config")
	}
}

func TestEnsureInsideRejectsOutsidePath(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(filepath.Dir(root), "outside.md")
	if err := EnsureInside(root, outside); err == nil {
		t.Fatalf("expected outside path to be rejected")
	}
}
