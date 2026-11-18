package sandbox

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"weak"
)

// roots locates a live sandbox for legacy services which pass absolute resolved
// paths. The weak reference does not prolong the lifetime of its os.Root handle.
var roots = struct {
	sync.RWMutex
	entries map[string][]weak.Pointer[PathSandbox]
}{entries: make(map[string][]weak.Pointer[PathSandbox])}

func register(s *PathSandbox) {
	roots.Lock()
	defer roots.Unlock()
	refs := roots.entries[s.root][:0]
	for _, ref := range roots.entries[s.root] {
		if ref.Value() != nil {
			refs = append(refs, ref)
		}
	}
	roots.entries[s.root] = append(refs, weak.Make(s))
}

// OperationRoot validates a resolved path again and returns a descriptor-based
// root. Callers must use the returned Root operation, never reopen the pathname.
// Paths outside registered media roots are configuration/utility paths.
func OperationRoot(path string) (*os.Root, string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, "", err
	}
	roots.RLock()
	var chosen string
	var sb *PathSandbox
	for base, refs := range roots.entries {
		if abs == base || strings.HasPrefix(abs, base+string(os.PathSeparator)) {
			if len(base) > len(chosen) {
				chosen = base
				sb = nil
				for _, ref := range refs {
					if live := ref.Value(); live != nil {
						sb = live
						break
					}
				}
			}
		}
	}
	roots.RUnlock()
	if chosen == "" {
		return nil, path, nil
	}
	if sb == nil {
		return nil, "", fmt.Errorf("%w: sandbox is closed", ErrStorageConfig)
	}
	rel, err := filepath.Rel(chosen, abs)
	if err != nil {
		return nil, "", err
	}
	if _, err = sb.Resolve(filepath.ToSlash(rel), true, LiteralPath); err != nil {
		return nil, "", err
	}
	sb.handleMu.Lock()
	defer sb.handleMu.Unlock()
	if sb.handle == nil {
		return nil, "", fmt.Errorf("%w: create the storage directory before initializing its sandbox", ErrStorageConfig)
	}
	return sb.handle, rel, err
}
