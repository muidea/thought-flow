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

func TestSweepOrphanTempFilesRemovesDotTmp(t *testing.T) {
	thoughts := t.TempDir()
	target := filepath.Join(thoughts, "2026", "06")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	real := filepath.Join(target, "abc12345.md")
	if err := os.WriteFile(real, []byte("---\nid: abc12345\n---\n\n# note\n"), 0o644); err != nil {
		t.Fatalf("WriteFile real: %v", err)
	}
	leftovers := []string{
		filepath.Join(target, "abc12345.md.1700000000000000000.tmp"),
		filepath.Join(target, "abc12345.md.1700000000000000001.tmp"),
	}
	for _, p := range leftovers {
		if err := os.WriteFile(p, []byte("stale"), 0o644); err != nil {
			t.Fatalf("WriteFile leftover: %v", err)
		}
	}

	if err := sweepOrphanTempFiles(thoughts); err != nil {
		t.Fatalf("sweepOrphanTempFiles: %v", err)
	}
	for _, p := range leftovers {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("expected %s to be removed, stat err = %v", p, err)
		}
	}
	if _, err := os.Stat(real); err != nil {
		t.Fatalf("real thought file should survive, stat err = %v", err)
	}
}

func TestSweepOrphanTempFilesNoopWhenDirMissing(t *testing.T) {
	if err := sweepOrphanTempFiles(filepath.Join(t.TempDir(), "does", "not", "exist")); err != nil {
		t.Fatalf("sweepOrphanTempFiles on missing dir should be a no-op, got %v", err)
	}
}

func TestOpenSweepsOrphanTempFilesAtStartup(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "workspace")
	dataDir := filepath.Join(base, "data")
	thoughts := filepath.Join(root, "thoughts", "2026", "06")
	if err := os.MkdirAll(thoughts, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	leftover := filepath.Join(thoughts, "abc12345.md.1700000000000000000.tmp")
	if err := os.WriteFile(leftover, []byte("stale"), 0o644); err != nil {
		t.Fatalf("WriteFile leftover: %v", err)
	}

	if _, err := Open(context.Background(), appconfig.Config{
		Workspace: appconfig.WorkspaceConfig{ContentDir: root},
		Runtime:   appconfig.RuntimeConfig{StateDir: dataDir},
	}); err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if _, err := os.Stat(leftover); !os.IsNotExist(err) {
		t.Fatalf("expected leftover %s to be swept, stat err = %v", leftover, err)
	}
}
