package objectlocation

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var safeID = regexp.MustCompile(`^[\pL\pN][\pL\pN_.-]{0,159}$`)

func ValidID(id string) bool { return safeID.MatchString(id) && id != "." && id != ".." }
func ID(prefix string) string {
	var b [16]byte
	if _, e := rand.Read(b[:]); e != nil {
		panic(e)
	}
	return prefix + hex.EncodeToString(b[:])
}
func Hash(raw []byte) string {
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}
func HashJSON(v any) string {
	raw, e := json.Marshal(v)
	if e != nil {
		panic(e)
	}
	var canonical any
	if e = json.Unmarshal(raw, &canonical); e != nil {
		panic(e)
	}
	raw, e = json.Marshal(canonical)
	if e != nil {
		panic(e)
	}
	return Hash(raw)
}

// SafePath rejects links even for missing descendants. Ancestor aliases above
// the workspace (notably macOS /var) are outside this boundary.
func SafePath(root, relative string) (string, error) {
	if relative == "" || filepath.IsAbs(relative) || strings.ContainsAny(relative, "\\\x00\r\n") {
		return "", ErrUnsafe
	}
	clean := filepath.Clean(filepath.FromSlash(relative))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", ErrUnsafe
	}
	abs, e := filepath.Abs(root)
	if e != nil {
		return "", e
	}
	path := filepath.Join(abs, clean)
	for cursor := path; ; cursor = filepath.Dir(cursor) {
		info, e := os.Lstat(cursor)
		if e == nil && info.Mode()&os.ModeSymlink != 0 {
			return "", ErrUnsafe
		}
		if e != nil && !os.IsNotExist(e) {
			return "", e
		}
		if cursor == abs {
			break
		}
	}
	return path, nil
}
func WriteFile(root, relative string, data []byte, mode os.FileMode) error {
	path, e := SafePath(root, relative)
	if e != nil {
		return e
	}
	dirMode := os.FileMode(0755)
	if mode.Perm() == 0600 {
		dirMode = 0700
	}
	if e = os.MkdirAll(filepath.Dir(path), dirMode); e != nil {
		return e
	}
	f, e := os.CreateTemp(filepath.Dir(path), ".write-*.tmp")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	if e = f.Chmod(mode); e == nil {
		_, e = f.Write(data)
	}
	if e == nil {
		e = f.Sync()
	}
	closeErr := f.Close()
	if e != nil {
		return e
	}
	if closeErr != nil {
		return closeErr
	}
	if e = os.Rename(f.Name(), path); e != nil {
		return e
	}
	return SyncDir(filepath.Dir(path))
}
func WriteJSON(root, relative string, value any, private bool) error {
	data, e := json.MarshalIndent(value, "", "  ")
	if e != nil {
		return e
	}
	mode := os.FileMode(0644)
	if private {
		mode = 0600
	}
	return WriteFile(root, relative, append(data, '\n'), mode)
}
func ReadJSON(root, relative string, value any) error {
	path, e := SafePath(root, relative)
	if e != nil {
		return e
	}
	data, e := ReadFileBounded(path, 32*1024*1024)
	if e != nil {
		return e
	}
	return json.Unmarshal(data, value)
}
func SyncDir(path string) error {
	f, e := os.Open(path)
	if e != nil {
		return e
	}
	defer f.Close()
	return f.Sync()
}
func FileHash(path string) (string, int64, error) {
	info, e := os.Lstat(path)
	if e != nil {
		return "", 0, e
	}
	if !info.Mode().IsRegular() || info.Size() > MaxResourceBytes {
		return "", 0, ErrUnsafe
	}
	f, e := os.Open(path)
	if e != nil {
		return "", 0, e
	}
	defer f.Close()
	h := sha256.New()
	n, e := io.Copy(h, f)
	return fmt.Sprintf("sha256:%x", h.Sum(nil)), n, e
}

// ReadFileBounded refuses special files and enforces the bound on actual bytes.
func ReadFileBounded(path string, limit int64) ([]byte, error) {
	info, e := os.Lstat(path)
	if e != nil {
		return nil, e
	}
	if !info.Mode().IsRegular() {
		return nil, ErrUnsafe
	}
	f, e := os.Open(path)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	info, e = f.Stat()
	if e != nil {
		return nil, e
	}
	if !info.Mode().IsRegular() || info.Size() > limit {
		return nil, ErrUnsafe
	}
	raw, e := io.ReadAll(io.LimitReader(f, limit+1))
	if int64(len(raw)) > limit {
		return nil, ErrUnsafe
	}
	return raw, e
}
