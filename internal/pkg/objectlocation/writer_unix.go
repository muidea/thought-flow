//go:build !windows

package objectlocation

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// AcquireWriter is held for the entire process lifecycle, before recovery and
// HTTP startup. Modules share it rather than each acquiring a separate lock.
func AcquireWriter(root string) (func(), error) {
	path, e := SafePath(root, ".content-registry/private/writer.lock")
	if e != nil {
		return nil, e
	}
	if e = os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		return nil, e
	}
	f, e := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return nil, e
	}
	if e = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
		f.Close()
		return nil, fmt.Errorf("workspace already has a writer: %w", e)
	}
	return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }, nil
}

func AvailableBytes(path string) (uint64, error) {
	var stat syscall.Statfs_t
	e := syscall.Statfs(path, &stat)
	return uint64(stat.Bavail) * uint64(stat.Bsize), e
}
