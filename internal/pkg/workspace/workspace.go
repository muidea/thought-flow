package workspace

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"thoughtflow/internal/pkg/appconfig"
	"thoughtflow/internal/pkg/models"
)

func Open(ctx context.Context, cfg appconfig.Config) (*models.Workspace, error) {
	_ = ctx
	rootPath, err := filepath.Abs(cfg.Workspace.ContentDir)
	if err != nil {
		return nil, err
	}
	dataPath, err := appconfig.RuntimeStateDir(cfg)
	if err != nil {
		return nil, err
	}

	ws := &models.Workspace{
		ID:              "local",
		RootPath:        rootPath,
		ThoughtsPath:    filepath.Join(rootPath, "thoughts"),
		TopicsPath:      filepath.Join(rootPath, "topics"),
		AttachmentsPath: filepath.Join(rootPath, "attachments"),
		RuntimePath:     dataPath,
		JobsPath:        filepath.Join(dataPath, "jobs"),
		GitEnabled:      cfg.GitSync.Enabled,
		CreatedAt:       time.Now().UTC(),
	}

	dirs := []string{
		ws.RootPath,
		ws.ThoughtsPath,
		ws.TopicsPath,
		ws.AttachmentsPath,
		ws.RuntimePath,
		ws.JobsPath,
		filepath.Join(ws.RuntimePath, "logs"),
	}
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}
	// Sweep any *.tmp leftovers from a previous crash of WriteThought.
	// The in-memory thoughtlock resets on restart, so the lock map is
	// consistent — the only thing that could be stale is a temp file
	// from a writer that died between os.WriteFile and os.Rename.
	if err := sweepOrphanTempFiles(ws.ThoughtsPath); err != nil {
		return nil, err
	}
	return ws, nil
}

// sweepOrphanTempFiles removes any *.tmp leftovers under dir. It is the
// startup counterpart to the tmp+rename atomic write pattern used by
// markdown.WriteThought: a process that crashes between os.WriteFile
// and os.Rename leaves a *.tmp file that no reader will pick up. We
// keep this helper here (rather than in markdown) to avoid an import
// cycle with workspace.EnsureInside.
func sweepOrphanTempFiles(dir string) error {
	if strings.TrimSpace(dir) == "" {
		return nil
	}
	if _, err := os.Stat(dir); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var firstErr error
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil || d.IsDir() {
			return walkErr
		}
		if !strings.HasSuffix(d.Name(), ".tmp") {
			return nil
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) && firstErr == nil {
			firstErr = err
		}
		return nil
	})
	if err != nil && firstErr == nil {
		return err
	}
	return firstErr
}

func EnsureInside(rootPath, targetPath string) error {
	absRoot, err := filepath.Abs(rootPath)
	if err != nil {
		return err
	}
	absTarget, err := filepath.Abs(targetPath)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(absRoot, absTarget)
	if err != nil {
		return err
	}
	if rel == "." {
		return nil
	}
	if rel == ".." || len(rel) >= 3 && rel[:3] == "../" {
		return os.ErrPermission
	}
	return nil
}

func RuntimeStatus(ws *models.Workspace) models.WorkspaceRuntimeStatus {
	status := models.WorkspaceRuntimeStatus{Status: "degraded"}
	if ws == nil {
		status.Error = "workspace is not ready"
		return status
	}
	status.ID = ws.ID
	status.RootPath = ws.RootPath
	status.ThoughtsPath = ws.ThoughtsPath
	status.TopicsPath = ws.TopicsPath
	status.AttachmentsPath = ws.AttachmentsPath
	status.RuntimePath = ws.RuntimePath
	status.JobsPath = ws.JobsPath
	status.GitEnabled = ws.GitEnabled
	if err := os.MkdirAll(ws.RuntimePath, 0o755); err != nil {
		status.Error = err.Error()
		return status
	}
	tmp, err := os.CreateTemp(ws.RuntimePath, ".status-*.tmp")
	if err != nil {
		status.Error = err.Error()
		return status
	}
	tmpPath := tmp.Name()
	_ = tmp.Close()
	_ = os.Remove(tmpPath)
	status.Writable = true
	status.Status = "ready"
	return status
}
