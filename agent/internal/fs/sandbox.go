package fsaccess

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// AllowWrite runs the sandbox check for callers outside this package — the git
// service runs commands inside the opened project and must not be handed a path
// the client was never allowed to touch.
func (s *Service) AllowWrite(path string) (string, error) {
	return s.resolveWritable(path)
}

func (s *Service) resolve(path string) (string, error) {
	return s.resolveMode(path, false)
}

func (s *Service) resolveWritable(path string) (string, error) {
	return s.resolveMode(path, true)
}

func (s *Service) resolveMode(path string, needWrite bool) (string, error) {
	if path == "" {
		return "", fmt.Errorf("empty path")
	}
	if strings.HasPrefix(path, "~/") {
		path = filepath.Join(s.home, path[2:])
	} else if path == "~" {
		path = s.home
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	abs = filepath.Clean(abs)
	real := realPath(abs)

	s.mu.RLock()
	defer s.mu.RUnlock()

	matched := false
	browseHit := false
	symlinkEscape := false
	for i, root := range s.roots {
		if abs != root && !strings.HasPrefix(abs, root+string(os.PathSeparator)) {
			continue
		}
		realRoot := s.realRoots[i]
		if real != realRoot && !strings.HasPrefix(real, realRoot+string(os.PathSeparator)) {
			symlinkEscape = true
			continue
		}
		if needWrite && !s.writable[i] {
			browseHit = true
			continue
		}
		matched = true
	}
	if matched {
		return abs, nil
	}
	if symlinkEscape {
		return "", ErrSymlinkEscape
	}
	if needWrite && browseHit {
		return "", ErrBrowseOnly
	}
	return "", ErrOutsideRoots
}

// realPath resolves symlinks on the longest existing ancestor of p and rejoins
// the non-existing tail, so it works for paths being created (writes/mkdir).
func realPath(p string) string {
	p = filepath.Clean(p)
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		return resolved
	}
	parent := filepath.Dir(p)
	if parent == p { // reached root
		return p
	}
	return filepath.Join(realPath(parent), filepath.Base(p))
}
