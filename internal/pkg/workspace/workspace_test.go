package workspace

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"thoughtflow/internal/pkg/appconfig"
)

func TestOpenCreatesWorkspaceDirectories(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "workspace")
	dataDir := filepath.Join(base, "data")
	ws, err := Open(context.Background(), appconfig.Config{
		Workspace: appconfig.WorkspaceConfig{ContentDir: root},
		Runtime:   appconfig.RuntimeConfig{StateDir: dataDir},
		GitSync:   appconfig.GitSyncConfig{Enabled: true},
	})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}

	for _, dir := range []string{ws.RootPath, ws.ThoughtsPath, ws.TopicsPath, ws.AttachmentsPath, ws.RuntimePath, ws.JobsPath} {
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
	if ws.RuntimePath != dataDir || ws.JobsPath != filepath.Join(dataDir, "jobs") {
		t.Fatalf("runtime paths = %#v", ws)
	}
}

func TestRuntimeStatusCreatesWritableRuntimeDirectory(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "workspace")
	dataDir := filepath.Join(base, "data")
	ws, err := Open(context.Background(), appconfig.Config{
		Workspace: appconfig.WorkspaceConfig{ContentDir: root},
		Runtime:   appconfig.RuntimeConfig{StateDir: dataDir},
	})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if err := os.RemoveAll(ws.RuntimePath); err != nil {
		t.Fatalf("RemoveAll(runtime) error = %v", err)
	}

	status := RuntimeStatus(ws)

	if status.Status != "ready" || !status.Writable || status.RuntimePath != ws.RuntimePath || status.Error != "" {
		t.Fatalf("RuntimeStatus() = %#v", status)
	}
	info, err := os.Stat(ws.RuntimePath)
	if err != nil {
		t.Fatalf("expected runtime directory: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("expected %s to be directory", ws.RuntimePath)
	}
}

func TestEnsureInsideRejectsOutsidePath(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(filepath.Dir(root), "outside.md")
	if err := EnsureInside(root, outside); err == nil {
		t.Fatalf("expected outside path to be rejected")
	}
}
