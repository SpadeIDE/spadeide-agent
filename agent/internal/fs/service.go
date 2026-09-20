package fsaccess

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/fsnotify/fsnotify"
	"github.com/google/uuid"
	"github.com/spadeide/spade-agent/internal/protocol"
)

// MaxFileBytes caps single-file read/write so a huge file can't exhaust the
// agent's memory (whole file is buffered + base64-encoded for the wire).
const MaxFileBytes = 32 << 20 // 32 MiB

// Sentinel errors. The transport layer maps these to protocol error codes with
// errors.Is, so the mapping never depends on message text.
var (
	ErrOutsideRoots   = errors.New("path outside allowlist")
	ErrSymlinkEscape  = errors.New("path escapes allowlist via symlink")
	ErrBrowseOnly     = errors.New("path is browse-only; open a folder as a project to edit")
	ErrInvalidPattern = errors.New("invalid pattern")
	ErrCanceled       = errors.New("search canceled")
)

// Service sandboxes filesystem access to allowlisted roots.
//
// This is accident-prevention for a paired device, not a security boundary:
// a paired client already has pty.create (a shell as the host user). Home
// starts as a *browse* root (list/stat/read only) so the client can pick a
// project folder. workspace.open adds a *write* root — the active project —
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
	watch    map[string]*fsnotify.Watcher
	onEvt    func(protocol.Envelope)
	// grepCancel aborts an in-flight walk when the client drops or starts
	// another search — otherwise reconnect storms pile WalkDirs on a huge tree.
	grepCancel context.CancelFunc
}

func New(home string, onEvent func(protocol.Envelope)) *Service {
	s := &Service{
		home:  home,
		watch: make(map[string]*fsnotify.Watcher),
		onEvt: onEvent,
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

	best := -1
	bestLen := -1
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
		if len(root) > bestLen {
			best = i
			bestLen = len(root)
		}
	}
	if best >= 0 {
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

func (s *Service) List(path string) (*protocol.ListResult, error) {
	abs, err := s.resolve(path)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(abs)
	if err != nil {
		return nil, err
	}
	out := make([]protocol.DirEntry, 0, len(entries))
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, protocol.DirEntry{
			Name:    e.Name(),
			Path:    filepath.Join(abs, e.Name()),
			IsDir:   e.IsDir(),
			Size:    info.Size(),
			ModTime: info.ModTime().UnixMilli(),
		})
	}
	return &protocol.ListResult{Entries: out}, nil
}

func (s *Service) Stat(path string) (*protocol.DirEntry, error) {
	abs, err := s.resolve(path)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, err
	}
	return &protocol.DirEntry{
		Name:    info.Name(),
		Path:    abs,
		IsDir:   info.IsDir(),
		Size:    info.Size(),
		ModTime: info.ModTime().UnixMilli(),
	}, nil
}

func (s *Service) Read(path string) (*protocol.ReadResult, error) {
	abs, err := s.resolve(path)
	if err != nil {
		return nil, err
	}
	if info, err := os.Stat(abs); err == nil {
		if info.IsDir() {
			return nil, fmt.Errorf("is a directory")
		}
		if info.Size() > MaxFileBytes {
			return nil, fmt.Errorf("file too large: %d bytes (max %d)", info.Size(), MaxFileBytes)
		}
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return nil, err
	}
	enc := "binary"
	if utf8.Valid(data) {
		enc = "utf-8"
	}
	return &protocol.ReadResult{
		Path:     abs,
		Content:  base64.StdEncoding.EncodeToString(data),
		Encoding: enc,
	}, nil
}

func (s *Service) Write(path, contentB64 string) (*protocol.WriteResult, error) {
	abs, err := s.resolveWritable(path)
	if err != nil {
		return nil, err
	}
	data, err := base64.StdEncoding.DecodeString(contentB64)
	if err != nil {
		return nil, fmt.Errorf("invalid base64")
	}
	if len(data) > MaxFileBytes {
		return nil, fmt.Errorf("file too large: %d bytes (max %d)", len(data), MaxFileBytes)
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(abs, data, 0o644); err != nil {
		return nil, err
	}
	return &protocol.WriteResult{Path: abs, Bytes: len(data)}, nil
}

// ignoredDirs are skipped during Search — heavy build/dep/VCS directories that
// would swamp a fuzzy file picker with noise.
var ignoredDirs = map[string]bool{
	".git": true, "node_modules": true, ".build": true, "build": true,
	"dist": true, "DerivedData": true, ".next": true, ".nuxt": true,
	"vendor": true, "Pods": true, ".venv": true, "venv": true,
	"__pycache__": true, ".gradle": true, ".idea": true, "target": true,
	".terraform": true, ".terragrunt-cache": true, ".cache": true,
	".mypy_cache": true, ".pytest_cache": true,
}

// Search walks the (allowlisted) root and returns a flat, capped list of files
// for the fuzzy file picker. Directories in ignoredDirs are pruned.
func (s *Service) Search(root string, limit int) (*protocol.SearchResult, error) {
	abs, err := s.resolve(root)
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 50000 {
		limit = 5000
	}
	files := make([]protocol.SearchFile, 0, 512)
	truncated := false

	err = filepath.WalkDir(abs, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil // skip unreadable entries, keep walking
		}
		if d.IsDir() {
			if path != abs && ignoredDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if len(files) >= limit {
			truncated = true
			return filepath.SkipAll
		}
		rel, relErr := filepath.Rel(abs, path)
		if relErr != nil {
			rel = path
		}
		files = append(files, protocol.SearchFile{
			Path: path,
			Name: d.Name(),
			Rel:  rel,
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &protocol.SearchResult{Files: files, Truncated: truncated}, nil
}

const maxGrepFileBytes = 1 << 20 // 1 MiB per file for content search

// Grep walks root (or a single file path) and finds text matches. Same ignore
// rules as Search. Binary / oversized files are skipped.
func (s *Service) Grep(p protocol.GrepParams) (*protocol.GrepResult, error) {
	if strings.TrimSpace(p.Query) == "" {
		return &protocol.GrepResult{Matches: nil}, nil
	}
	abs, err := s.resolve(p.Root)
	if err != nil {
		return nil, err
	}
	matcher, err := compileMatcher(p.Query, p.CaseSensitive, p.WholeWord, p.IsRegex)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidPattern, err)
	}
	limit := p.MaxResults
	if limit <= 0 {
		limit = 400
	}
	if limit > 2000 {
		limit = 2000
	}

	ctx, cancel := context.WithCancel(context.Background())
	s.mu.Lock()
	if s.grepCancel != nil {
		s.grepCancel()
	}
	s.grepCancel = cancel
	s.mu.Unlock()
	defer cancel()

	matches := make([]protocol.GrepMatch, 0, 256)
	truncated := false

	info, err := os.Stat(abs)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		ms, _ := grepFile(abs, abs, filepath.Base(abs), matcher, limit)
		return &protocol.GrepResult{Matches: ms, Truncated: len(ms) >= limit}, nil
	}

	err = filepath.WalkDir(abs, func(path string, d os.DirEntry, walkErr error) error {
		if ctx.Err() != nil {
			return filepath.SkipAll
		}
		if walkErr != nil || truncated {
			return nil
		}
		if d.IsDir() {
			if path != abs && ignoredDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if skipGrepName(d.Name()) {
			return nil
		}
		remain := limit - len(matches)
		if remain <= 0 {
			truncated = true
			return filepath.SkipAll
		}
		rel, relErr := filepath.Rel(abs, path)
		if relErr != nil {
			rel = path
		}
		ms, hitCap := grepFile(path, abs, rel, matcher, remain)
		matches = append(matches, ms...)
		if hitCap || len(matches) >= limit {
			truncated = true
			return filepath.SkipAll
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if ctx.Err() != nil {
		return nil, ErrCanceled
	}
	return &protocol.GrepResult{Matches: matches, Truncated: truncated}, nil
}

func skipGrepName(name string) bool {
	lower := strings.ToLower(name)
	switch lower {
	case "package-lock.json", "pnpm-lock.yaml", "yarn.lock", "go.sum", "cargo.lock",
		"composer.lock", "poetry.lock", "terraform.tfstate", "terraform.tfstate.backup":
		return true
	}
	if strings.HasSuffix(lower, ".min.js") || strings.HasSuffix(lower, ".min.css") || strings.HasSuffix(lower, ".js.map") {
		return true
	}
	switch strings.ToLower(filepath.Ext(name)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".ico", ".bmp",
		".woff", ".woff2", ".ttf", ".otf", ".eot",
		".zip", ".gz", ".tar", ".bz2", ".7z", ".rar",
		".pdf", ".mp3", ".mp4", ".mov", ".wav",
		".wasm", ".exe", ".dll", ".so", ".dylib", ".o", ".a",
		".class", ".jar", ".pyc",
		".tfstate", ".lock", ".map", ".bin", ".dmg", ".iso":
		return true
	}
	return false
}

type lineMatcher interface {
	findAll(line string) [][2]int // start, end byte offsets
}

type literalMatcher struct {
	needle string
	lower  bool
}

func (m literalMatcher) findAll(line string) [][2]int {
	src := line
	needle := m.needle
	if m.lower {
		src = strings.ToLower(line)
		needle = strings.ToLower(m.needle)
	}
	var out [][2]int
	from := 0
	for {
		i := strings.Index(src[from:], needle)
		if i < 0 {
			break
		}
		start := from + i
		end := start + len(needle)
		out = append(out, [2]int{start, end})
		from = end
		if needle == "" {
			break
		}
	}
	return out
}

type regexMatcher struct {
	re *regexp.Regexp
}

func (m regexMatcher) findAll(line string) [][2]int {
	idxs := m.re.FindAllStringIndex(line, -1)
	out := make([][2]int, 0, len(idxs))
	for _, pair := range idxs {
		out = append(out, [2]int{pair[0], pair[1]})
	}
	return out
}

func compileMatcher(query string, caseSensitive, wholeWord, isRegex bool) (lineMatcher, error) {
	pattern := query
	if !isRegex {
		pattern = regexp.QuoteMeta(query)
	}
	if wholeWord {
		pattern = `\b(?:` + pattern + `)\b`
	}
	if !caseSensitive {
		pattern = `(?i)` + pattern
	}
	// Fast path for literals — default Search is case-insensitive and used
	// to pay a full regexp per line of every file.
	if !isRegex && !wholeWord {
		return literalMatcher{needle: query, lower: !caseSensitive}, nil
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, err
	}
	return regexMatcher{re: re}, nil
}

func grepFile(path, root, rel string, matcher lineMatcher, limit int) ([]protocol.GrepMatch, bool) {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() || info.Size() > maxGrepFileBytes || info.Size() == 0 {
		return nil, false
	}
	data, err := os.ReadFile(path)
	if err != nil || !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		return nil, false
	}
	text := string(data)
	var out []protocol.GrepMatch
	lineNo := 1
	offset := 0
	for {
		nl := strings.IndexByte(text[offset:], '\n')
		var line string
		var next int
		if nl < 0 {
			line = text[offset:]
			next = len(text)
		} else {
			line = text[offset : offset+nl]
			if strings.HasSuffix(line, "\r") {
				line = strings.TrimSuffix(line, "\r")
			}
			next = offset + nl + 1
		}
		for _, span := range matcher.findAll(line) {
			preview := strings.TrimRight(line, "\r")
			if len(preview) > 240 {
				preview = preview[:240] + "…"
			}
			out = append(out, protocol.GrepMatch{
				Path:      path,
				Rel:       rel,
				Line:      lineNo,
				Column:    utf8.RuneCountInString(line[:span[0]]) + 1,
				EndColumn: utf8.RuneCountInString(line[:span[1]]) + 1,
				Preview:   preview,
			})
			if len(out) >= limit {
				return out, true
			}
		}
		if nl < 0 {
			break
		}
		offset = next
		lineNo++
	}
	return out, false
}

// ReplaceInFiles rewrites files under root (writable) replacing all matches.
func (s *Service) ReplaceInFiles(p protocol.ReplaceInFilesParams) (*protocol.ReplaceInFilesResult, error) {
	if strings.TrimSpace(p.Query) == "" {
		return &protocol.ReplaceInFilesResult{}, nil
	}
	matcher, err := compileMatcher(p.Query, p.CaseSensitive, p.WholeWord, p.IsRegex)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidPattern, err)
	}

	var targets []string
	if len(p.Paths) > 0 {
		targets = append(targets, p.Paths...)
	} else {
		abs, err := s.resolve(p.Root)
		if err != nil {
			return nil, err
		}
		info, err := os.Stat(abs)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() {
			targets = []string{abs}
		} else {
			_ = filepath.WalkDir(abs, func(path string, d os.DirEntry, walkErr error) error {
				if walkErr != nil {
					return nil
				}
				if d.IsDir() {
					if path != abs && ignoredDirs[d.Name()] {
						return filepath.SkipDir
					}
					return nil
				}
				if skipGrepName(d.Name()) {
					return nil
				}
				targets = append(targets, path)
				return nil
			})
		}
	}

	filesChanged := 0
	replacements := 0
	for _, path := range targets {
		abs, err := s.resolveWritable(path)
		if err != nil {
			continue
		}
		info, err := os.Stat(abs)
		if err != nil || info.IsDir() || info.Size() > maxGrepFileBytes {
			continue
		}
		data, err := os.ReadFile(abs)
		if err != nil || !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
			continue
		}
		original := string(data)
		replaced, n := replaceAllMatches(original, matcher, p.Replacement)
		if n == 0 {
			continue
		}
		if err := os.WriteFile(abs, []byte(replaced), info.Mode().Perm()); err != nil {
			continue
		}
		filesChanged++
		replacements += n
	}
	return &protocol.ReplaceInFilesResult{FilesChanged: filesChanged, Replacements: replacements}, nil
}

func replaceAllMatches(text string, matcher lineMatcher, replacement string) (string, int) {
	if rm, ok := matcher.(regexMatcher); ok {
		idxs := rm.re.FindAllStringSubmatchIndex(text, -1)
		if len(idxs) == 0 {
			return text, 0
		}
		var b strings.Builder
		last := 0
		for _, pair := range idxs {
			b.WriteString(text[last:pair[0]])
			b.Write(rm.re.ExpandString(nil, replacement, text, pair))
			last = pair[1]
		}
		b.WriteString(text[last:])
		return b.String(), len(idxs)
	}

	if lm, ok := matcher.(literalMatcher); ok && !lm.lower {
		count := strings.Count(text, lm.needle)
		if count == 0 || lm.needle == "" {
			return text, 0
		}
		return strings.ReplaceAll(text, lm.needle, replacement), count
	}

	lines := strings.SplitAfter(text, "\n")
	total := 0
	for i, line := range lines {
		keepNL := strings.HasSuffix(line, "\n")
		body := line
		if keepNL {
			body = line[:len(line)-1]
		}
		hasCR := strings.HasSuffix(body, "\r")
		if hasCR {
			body = body[:len(body)-1]
		}
		spans := matcher.findAll(body)
		if len(spans) == 0 {
			continue
		}
		var b strings.Builder
		last := 0
		for _, span := range spans {
			b.WriteString(body[last:span[0]])
			b.WriteString(replacement)
			last = span[1]
			total++
		}
		b.WriteString(body[last:])
		body = b.String()
		if hasCR {
			body += "\r"
		}
		if keepNL {
			body += "\n"
		}
		lines[i] = body
	}
	return strings.Join(lines, ""), total
}

func (s *Service) Mkdir(path string) error {
	abs, err := s.resolveWritable(path)
	if err != nil {
		return err
	}
	return os.MkdirAll(abs, 0o755)
}

func (s *Service) Delete(path string) error {
	abs, err := s.resolveWritable(path)
	if err != nil {
		return err
	}
	return os.RemoveAll(abs)
}

func (s *Service) Copy(from, toDir string) (string, error) {
	absFrom, err := s.resolve(from)
	if err != nil {
		return "", err
	}
	absToDir, err := s.resolveWritable(toDir)
	if err != nil {
		return "", err
	}
	info, err := os.Lstat(absFrom)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("cannot copy a symlink")
	}

	destDir := absToDir
	// Paste exactly where the client said. A lone child folder (1 → 2) is
	// not a retarget — only an explicit toDir of 2 goes into 2.

	name := uniqueCopyName(destDir, info.Name())
	dest := filepath.Join(destDir, name)
	if _, err := s.resolveWritable(dest); err != nil {
		return "", err
	}
	if dest == absFrom {
		return "", fmt.Errorf("cannot copy a path onto itself")
	}

	if info.IsDir() {
		if err := copyTree(absFrom, dest); err != nil {
			_ = os.RemoveAll(dest)
			return "", err
		}
	} else {
		if err := copyFile(absFrom, dest, info.Mode().Perm()); err != nil {
			return "", err
		}
	}
	return dest, nil
}

func (s *Service) Move(from, toDir string) (string, error) {
	absFrom, err := s.resolveWritable(from)
	if err != nil {
		return "", err
	}
	absToDir, err := s.resolveWritable(toDir)
	if err != nil {
		return "", err
	}
	info, err := os.Lstat(absFrom)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("cannot move a symlink")
	}

	destDir := absToDir
	if info.IsDir() {
		sep := string(os.PathSeparator)
		if destDir == absFrom || strings.HasPrefix(destDir, absFrom+sep) {
			return "", fmt.Errorf("cannot move a folder into itself")
		}
	}

	dest := filepath.Join(destDir, info.Name())
	if dest == absFrom {
		return absFrom, nil
	}
	name := uniqueCopyName(destDir, info.Name())
	dest = filepath.Join(destDir, name)
	if _, err := s.resolveWritable(dest); err != nil {
		return "", err
	}
	if err := os.Rename(absFrom, dest); err != nil {
		return "", err
	}
	return dest, nil
}

// uniqueCopyName keeps the original name when free; otherwise `stem copy.ext`,
// then `stem copy 2.ext`. Dotfiles without another extension stay intact
// (`.gitignore` → `.gitignore copy`).
func uniqueCopyName(destDir, name string) string {
	if _, err := os.Stat(filepath.Join(destDir, name)); err != nil {
		return name
	}
	stem, ext := splitCopyName(name)
	candidate := stem + " copy" + ext
	for n := 2; n < 1000; n++ {
		if _, err := os.Stat(filepath.Join(destDir, candidate)); err != nil {
			return candidate
		}
		candidate = fmt.Sprintf("%s copy %d%s", stem, n, ext)
	}
	return fmt.Sprintf("%s copy %d%s", stem, time.Now().UnixNano(), ext)
}

func splitCopyName(name string) (stem, ext string) {
	ext = filepath.Ext(name)
	if ext == "" || ext == name {
		return name, ""
	}
	return strings.TrimSuffix(name, ext), ext
}

func copyFile(src, dst string, perm os.FileMode) error {
	if perm == 0 {
		perm = 0o644
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, perm)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		_ = os.Remove(dst)
		return copyErr
	}
	if closeErr != nil {
		_ = os.Remove(dst)
		return closeErr
	}
	return nil
}

func copyTree(src, dst string) error {
	sep := string(os.PathSeparator)
	return filepath.WalkDir(src, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		// Pasting a folder into itself creates dst inside src — do not walk it.
		if path == dst || strings.HasPrefix(path, dst+sep) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := dst
		if rel != "." {
			target = filepath.Join(dst, rel)
		}
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		return copyFile(path, target, info.Mode().Perm())
	})
}

func (s *Service) Rename(from, to string) (string, error) {
	absFrom, err := s.resolveWritable(from)
	if err != nil {
		return "", err
	}
	absTo, err := s.resolveWritable(to)
	if err != nil {
		return "", err
	}
	if err := os.Rename(absFrom, absTo); err != nil {
		return "", err
	}
	return absTo, nil
}

func (s *Service) Watch(path string) (string, error) {
	abs, err := s.resolve(path)
	if err != nil {
		return "", err
	}
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return "", err
	}
	if err := w.Add(abs); err != nil {
		_ = w.Close()
		return "", err
	}
	id := uuid.NewString()
	s.mu.Lock()
	s.watch[id] = w
	s.mu.Unlock()

	go func() {
		for {
			select {
			case ev, ok := <-w.Events:
				if !ok {
					return
				}
				op := "write"
				switch {
				case ev.Op&fsnotify.Create != 0:
					op = "create"
				case ev.Op&fsnotify.Remove != 0:
					op = "remove"
				case ev.Op&fsnotify.Rename != 0:
					op = "rename"
				case ev.Op&fsnotify.Write != 0:
					op = "write"
				}
				if s.onEvt != nil {
					s.onEvt(protocol.NewEvent(protocol.EventFSChanged, protocol.FSChangedParams{
						WatchID: id,
						Path:    ev.Name,
						Op:      op,
					}))
				}
			case _, ok := <-w.Errors:
				if !ok {
					return
				}
			}
		}
	}()
	return id, nil
}

func (s *Service) Unwatch(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	w, ok := s.watch[id]
	if !ok {
		return fmt.Errorf("watch not found")
	}
	delete(s.watch, id)
	return w.Close()
}

func (s *Service) Close() {
	s.mu.Lock()
	if s.grepCancel != nil {
		s.grepCancel()
		s.grepCancel = nil
	}
	for id, w := range s.watch {
		_ = w.Close()
		delete(s.watch, id)
	}
	s.mu.Unlock()
}
