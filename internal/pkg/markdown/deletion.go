package markdown

import (
	"os"
	"path/filepath"

	"thoughtflow/internal/pkg/documentflow"
	"thoughtflow/internal/pkg/workspace"
)

// DeleteThought serializes revocation and projection cleanup with all document
// writes. A durable tombstone also makes interrupted deletion safe to retry.
func DeleteThought(rootPath, thoughtID string) error {
	flow := documentflow.For(rootPath)
	unlock := flow.Store.Lock(thoughtID)
	defer unlock()
	path := filepath.Join(rootPath, ThoughtRelativePath(thoughtID))
	if e := workspace.EnsureNoSymlinks(rootPath, path); e != nil {
		return e
	}
	_, headErr := flow.Store.Head(thoughtID)
	if headErr != nil && !os.IsNotExist(headErr) {
		return headErr
	}
	_, fileErr := os.Stat(path)
	if fileErr != nil && !os.IsNotExist(fileErr) {
		return fileErr
	}
	if os.IsNotExist(headErr) && os.IsNotExist(fileErr) {
		return os.ErrNotExist
	}
	if e := flow.DeleteLocked(thoughtID); e != nil {
		return e
	}
	if e := os.Remove(path); e != nil && !os.IsNotExist(e) {
		return e
	}
	dir, e := os.Open(filepath.Dir(path))
	if os.IsNotExist(e) {
		return nil
	}
	if e != nil {
		return e
	}
	defer dir.Close()
	return dir.Sync()
}
