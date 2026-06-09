package workspace

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"thoughtflow/internal/pkg/appconfig"
	"thoughtflow/internal/pkg/models"
)

func Open(ctx context.Context, cfg appconfig.Config) (*models.Workspace, error) {
	_ = ctx
	rootPath, err := filepath.Abs(cfg.Workspace.Root)
	if err != nil {
		return nil, err
	}

	ws := &models.Workspace{
		ID:              "local",
		RootPath:        rootPath,
		ThoughtsPath:    filepath.Join(rootPath, "thoughts"),
		TopicsPath:      filepath.Join(rootPath, "topics"),
		AttachmentsPath: filepath.Join(rootPath, "attachments"),
		RuntimePath:     filepath.Join(rootPath, ".thoughtflow"),
		JobsPath:        filepath.Join(rootPath, ".thoughtflow", "jobs"),
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
	return ws, nil
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
