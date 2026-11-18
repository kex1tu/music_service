// Package safefs applies descriptor-relative operations to registered media roots.
// Utility/configuration paths outside media roots retain normal os semantics.
package safefs

import (
	"crypto/rand"
	"encoding/hex"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	"music-manager/internal/core/sandbox"
)

func Open(name string) (*os.File, error) {
	root, rel, err := sandbox.OperationRoot(name)
	if err != nil {
		return nil, err
	}
	if root != nil {
		return root.Open(rel)
	}
	return os.Open(name)
}
func Create(name string) (*os.File, error) {
	root, rel, err := sandbox.OperationRoot(name)
	if err != nil {
		return nil, err
	}
	if root != nil {
		return root.Create(rel)
	}
	return os.Create(name)
}
func OpenFile(name string, flag int, perm os.FileMode) (*os.File, error) {
	root, rel, err := sandbox.OperationRoot(name)
	if err != nil {
		return nil, err
	}
	if root != nil {
		return root.OpenFile(rel, flag, perm)
	}
	return os.OpenFile(name, flag, perm)
}
func ReadFile(name string) ([]byte, error) {
	root, rel, err := sandbox.OperationRoot(name)
	if err != nil {
		return nil, err
	}
	if root != nil {
		return root.ReadFile(rel)
	}
	return os.ReadFile(name)
}
func Stat(name string) (os.FileInfo, error) {
	root, rel, err := sandbox.OperationRoot(name)
	if err != nil {
		return nil, err
	}
	if root != nil {
		return root.Stat(rel)
	}
	return os.Stat(name)
}
func Lstat(name string) (os.FileInfo, error) {
	root, rel, err := sandbox.OperationRoot(name)
	if err != nil {
		return nil, err
	}
	if root != nil {
		return root.Lstat(rel)
	}
	return os.Lstat(name)
}
func WriteFile(name string, data []byte, perm os.FileMode) error {
	root, rel, err := sandbox.OperationRoot(name)
	if err != nil {
		return err
	}
	if root != nil {
		return root.WriteFile(rel, data, perm)
	}
	return os.WriteFile(name, data, perm)
}
func Mkdir(name string, perm os.FileMode) error {
	root, rel, err := sandbox.OperationRoot(name)
	if err != nil {
		return err
	}
	if root != nil {
		return root.Mkdir(rel, perm)
	}
	return os.Mkdir(name, perm)
}
func MkdirAll(name string, perm os.FileMode) error {
	root, rel, err := sandbox.OperationRoot(name)
	if err != nil {
		return err
	}
	if root != nil {
		return root.MkdirAll(rel, perm)
	}
	return os.MkdirAll(name, perm)
}
func Remove(name string) error {
	root, rel, err := sandbox.OperationRoot(name)
	if err != nil {
		return err
	}
	if root != nil {
		if rel == "." {
			return sandbox.ErrPathTraversal
		}
		return root.Remove(rel)
	}
	return os.Remove(name)
}
func RemoveAll(name string) error {
	root, rel, err := sandbox.OperationRoot(name)
	if err != nil {
		return err
	}
	if root != nil {
		if rel == "." {
			return sandbox.ErrPathTraversal
		}
		return root.RemoveAll(rel)
	}
	return os.RemoveAll(name)
}

func ReadDir(name string) ([]os.DirEntry, error) {
	f, err := Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	entries, err := f.ReadDir(-1)
	if err == nil {
		slices.SortFunc(entries, func(a, b os.DirEntry) int { return strings.Compare(a.Name(), b.Name()) })
	}
	return entries, err
}
func Rename(src, dst string) error {
	a, ar, e := sandbox.OperationRoot(src)
	if e != nil {
		return e
	}
	b, br, e := sandbox.OperationRoot(dst)
	if e != nil {
		return e
	}
	if a == nil && b == nil {
		return os.Rename(src, dst)
	}
	if a != nil && a == b {
		return a.Rename(ar, br)
	}
	return &os.LinkError{Op: "rename", Old: src, New: dst, Err: syscall.EXDEV}
}

// Link publishes a filesystem entry without replacing an existing path.
func Link(src, dst string) error {
	a, ar, err := sandbox.OperationRoot(src)
	if err != nil {
		return err
	}
	b, br, err := sandbox.OperationRoot(dst)
	if err != nil {
		return err
	}
	if a == nil && b == nil {
		return os.Link(src, dst)
	}
	if a != nil && a == b {
		return a.Link(ar, br)
	}
	return &os.LinkError{Op: "link", Old: src, New: dst, Err: syscall.EXDEV}
}

func Chmod(name string, mode os.FileMode) error {
	f, e := Open(name)
	if e != nil {
		return e
	}
	defer f.Close()
	return f.Chmod(mode)
}
func WalkDir(name string, fn fs.WalkDirFunc) error {
	root, rel, e := sandbox.OperationRoot(name)
	if e != nil {
		return e
	}
	if root == nil {
		return filepath.WalkDir(name, fn)
	}
	return fs.WalkDir(root.FS(), filepath.ToSlash(rel), func(p string, d fs.DirEntry, e error) error {
		if p == filepath.ToSlash(rel) && d != nil {
			d = namedEntry{DirEntry: d, name: filepath.Base(name)}
		}
		return fn(filepath.Join(root.Name(), filepath.FromSlash(p)), d, e)
	})
}
func Walk(name string, fn filepath.WalkFunc) error {
	return WalkDir(name, func(p string, d fs.DirEntry, e error) error {
		if e != nil {
			return fn(p, nil, e)
		}
		info, e := d.Info()
		return fn(p, info, e)
	})
}

type namedEntry struct {
	fs.DirEntry
	name string
}

func (d namedEntry) Name() string { return d.name }

// CreateTemp creates the temporary file with the same descriptor containment as
// the final write. A random basename prevents collisions without a path reopen.
func CreateTemp(dir, pattern string) (*os.File, error) {
	if dir == "" {
		dir = os.TempDir()
	}
	if strings.ContainsAny(pattern, "/\\") {
		return nil, &os.PathError{Op: "createtemp", Path: pattern, Err: os.ErrInvalid}
	}
	for i := 0; i < 100; i++ {
		var token [16]byte
		if _, err := rand.Read(token[:]); err != nil {
			return nil, err
		}
		suffix := hex.EncodeToString(token[:])
		name := pattern + suffix
		if idx := strings.LastIndex(pattern, "*"); idx >= 0 {
			name = pattern[:idx] + suffix + pattern[idx+1:]
		}
		f, err := OpenFile(filepath.Join(dir, name), os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600)
		if os.IsExist(err) {
			continue
		}
		return f, err
	}
	return nil, os.ErrExist
}

// ServeFile opens once through containment and serves the already-open file.
func ServeFile(w http.ResponseWriter, r *http.Request, name string) {
	f, err := Open(name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.IsDir() {
		http.NotFound(w, r)
		return
	}
	http.ServeContent(w, r, info.Name(), info.ModTime(), f)
}
func Glob(pattern string) ([]string, error) {
	root, rel, e := sandbox.OperationRoot(pattern)
	if e != nil {
		return nil, e
	}
	if root == nil {
		return filepath.Glob(pattern)
	}
	names, e := fs.Glob(root.FS(), filepath.ToSlash(rel))
	if e != nil {
		return nil, e
	}
	for i := range names {
		names[i] = filepath.Join(root.Name(), filepath.FromSlash(names[i]))
	}
	return names, nil
}
