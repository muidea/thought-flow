package workspace

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"thoughtflow/internal/pkg/models"
)

var ignoreMu sync.Mutex

// Runtime sidecars may contain unarchived/private material. Protect manual
// git add -A as well as the automatic committer's path exclusions.
func ensurePrivateIgnores(ws *models.Workspace) error {
	ignoreMu.Lock()
	defer ignoreMu.Unlock()
	path := filepath.Join(ws.RootPath, ".gitignore")
	if err := EnsureNoSymlinks(ws.RootPath, path); err != nil {
		return err
	}
	raw, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	rules := []string{"/.scratchpad/", "/.thoughtflow/", "/.content-registry/private/", "/topics/*/.private/", ".stage-*/", ".write-*.tmp", "/topics/*/candidates.yaml", "/documents/capture-*/", "/documents/compose-*/"}
	if rel, err := filepath.Rel(ws.RootPath, ws.RuntimePath); err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel) {
		escape := strings.NewReplacer("\\", "\\\\", " ", "\\ ", "#", "\\#", "!", "\\!", "*", "\\*", "?", "\\?", "[", "\\[", "]", "\\]")
		rules = append(rules, "/"+escape.Replace(filepath.ToSlash(rel))+"/")
	}
	body := string(raw)
	changed := false
	for _, rule := range rules {
		found := false
		for _, line := range strings.Split(body, "\n") {
			if line == rule {
				found = true
				break
			}
		}
		if found {
			continue
		}
		if body != "" && !strings.HasSuffix(body, "\n") {
			body += "\n"
		}
		body += rule + "\n"
		changed = true
	}
	if !changed {
		return nil
	}
	tmp, err := os.CreateTemp(ws.RootPath, ".gitignore-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err = tmp.WriteString(body); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
