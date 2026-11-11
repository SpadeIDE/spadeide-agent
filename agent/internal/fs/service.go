package fsaccess

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/fsnotify/fsnotify"
	"github.com/spadeide/spade-agent/internal/protocol"
)

// MaxFileBytes caps single-file read/write so a huge file can't exhaust the
// agent's memory (whole file is buffered + base64-encoded for the wire).
const MaxFileBytes = 32 << 20 // 32 MiB

// MaxWatches caps concurrent directory watches (each is a kqueue/inotify fd).
const MaxWatches = 256

type watchEntry struct {
	id   string
	path string
	w    *fsnotify.Watcher
	refs int
}

// Sentinel errors. The transport layer maps these to protocol error codes with
// errors.Is, so the mapping never depends on message text.
var (
	ErrOutsideRoots   = errors.New("path outside allowlist")
	ErrSymlinkEscape  = errors.New("path escapes allowlist via symlink")
	ErrBrowseOnly     = errors.New("path is browse-only; open a folder as a project to edit")
	ErrInvalidPattern = errors.New("invalid pattern")
	ErrCanceled       = errors.New("search canceled")
)

// Service sandboxes filesystem access to allowlisted roots under the session home.
//
// Explorer/editor cannot leave that home (AddRoot outside the jail is rejected).
// A paired client still has pty.create (a shell as the bound Unix user, not a
// chroot). Home starts as a *browse* root (list/stat/read only) so the client can
// pick a project folder. workspace.open adds a *write* root — the active project —
// which is the only place mutations are allowed.
type Service struct {
	mu    sync.RWMutex
	roots []string // absolute paths (as requested)
	// realRoots holds the symlink-resolved form of each root, index-aligned
	// with roots, so we can reject symlink escapes out of the sandbox.
	realRoots []string
	// writable[i] is true when roots[i] may be mutated (write/mkdir/delete/rename/copy/move).
	writable []bool
	home     string
	jail     string // writes/open-project must stay under this home
	own      bool
	ownUID   int
	ownGID   int
	watch    map[string]*watchEntry // by watch id
	byPath   map[string]*watchEntry // resolved abs path → shared watcher
	onEvt    func(protocol.Envelope)
	// grepCancel aborts an in-flight walk when the client drops or starts
	// another search — otherwise reconnect storms pile WalkDirs on a huge tree.
	grepCancel context.CancelFunc
	// archiveCancel aborts an in-flight extract/compress (host zip/tar).
	archiveCancel context.CancelFunc
}

func New(home string, onEvent func(protocol.Envelope)) *Service {
	s := &Service{
		home:   home,
		jail:   filepath.Clean(home),
		watch:  make(map[string]*watchEntry),
		byPath: make(map[string]*watchEntry),
		onEvt:  onEvent,
	}
	s.roots = []string{home}
	s.realRoots = []string{realPath(home)}
	s.writable = []bool{false} // home is browse-only until opened as a project
	return s
}

func (s *Service) expand(path string) string {
	if strings.HasPrefix(path, "~/") {
		return filepath.Join(s.home, path[2:])
	}
	if path == "~" {
		return s.home
	}
	return path
}

func (s *Service) AddRoot(path string) error {
	abs, err := filepath.Abs(s.expand(path))
	if err != nil {
		return err
	}
	// Reject filesystem roots — opening `/` as a write root is never useful and
	// would make every path "inside" the project. Paired clients already have a
	// shell; this is accident-prevention, not containment.
	if abs == string(filepath.Separator) || abs == filepath.VolumeName(abs)+string(filepath.Separator) {
		return fmt.Errorf("cannot open filesystem root as a project")
	}
	s.mu.RLock()
	inJail := s.pathInJail(abs)
	s.mu.RUnlock()
	if !inJail {
		return fmt.Errorf("%w: project must be inside home", ErrOutsideRoots)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("not a directory")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, r := range s.roots {
		if r == abs {
			// Opening an existing browse root (e.g. home) as a project promotes it.
			s.writable[i] = true
			return nil
		}
	}
	s.roots = append(s.roots, abs)
	s.realRoots = append(s.realRoots, realPath(abs))
	s.writable = append(s.writable, true)
	return nil
}

func (s *Service) Roots() []protocol.WorkspaceRoot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]protocol.WorkspaceRoot, 0, len(s.roots)+3)
	// Suggested browse roots
	suggestions := []struct{ path, label string }{
		{s.home, "Home"},
		{filepath.Join(s.home, "Projects"), "Projects"},
		{filepath.Join(s.home, "Documents"), "Documents"},
		{filepath.Join(s.home, "Developer"), "Developer"},
		{filepath.Join(s.home, "src"), "src"},
		{filepath.Join(s.home, "Work"), "Work"},
	}
	seen := map[string]bool{}
	for _, sug := range suggestions {
		if _, err := os.Stat(sug.path); err == nil && !seen[sug.path] {
			out = append(out, protocol.WorkspaceRoot{Path: sug.path, Label: sug.label})
			seen[sug.path] = true
		}
	}
	for _, r := range s.roots {
		if !seen[r] {
			out = append(out, protocol.WorkspaceRoot{Path: r, Label: filepath.Base(r)})
			seen[r] = true
		}
	}
	return out
}
