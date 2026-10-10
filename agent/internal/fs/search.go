package fsaccess

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/spadeide/spade-agent/internal/protocol"
)

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

// walkProject visits regular files under root, pruning ignoredDirs and,
// when skipBinary is set, names that are never worth reading.
func walkProject(ctx context.Context, root string, skipBinary bool, fn func(path, rel string) error) error {
	return filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
		if ctx != nil && ctx.Err() != nil {
			return filepath.SkipAll
		}
		if walkErr != nil {
			return nil
		}
		if d.IsDir() {
			if path != root && ignoredDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if skipBinary && skipGrepName(d.Name()) {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			rel = path
		}
		return fn(path, rel)
	})
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

	err = walkProject(context.Background(), abs, true, func(path, rel string) error {
		if len(files) >= limit {
			truncated = true
			return filepath.SkipAll
		}
		files = append(files, protocol.SearchFile{
			Path: path,
			Name: filepath.Base(path),
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

	err = walkProject(ctx, abs, true, func(path, rel string) error {
		if truncated {
			return filepath.SkipAll
		}
		remain := limit - len(matches)
		if remain <= 0 {
			truncated = true
			return filepath.SkipAll
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
