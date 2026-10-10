package fsaccess

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/spadeide/spade-agent/internal/protocol"
)

// ReplaceInFiles rewrites files under root (writable) replacing all matches.
func (s *Service) ReplaceInFiles(p protocol.ReplaceInFilesParams) (*protocol.ReplaceInFilesResult, error) {
	if strings.TrimSpace(p.Query) == "" {
		return &protocol.ReplaceInFilesResult{}, nil
	}
	matcher, err := compileMatcher(p.Query, p.CaseSensitive, p.WholeWord, p.IsRegex)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidPattern, err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	s.mu.Lock()
	if s.grepCancel != nil {
		s.grepCancel()
	}
	s.grepCancel = cancel
	s.mu.Unlock()
	defer cancel()

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
			err = walkProject(ctx, abs, true, func(path, _ string) error {
				targets = append(targets, path)
				return nil
			})
			if err != nil {
				return nil, err
			}
			if ctx.Err() != nil {
				return nil, ErrCanceled
			}
		}
	}

	filesChanged := 0
	replacements := 0
	skipped := 0

	for _, path := range targets {
		if ctx.Err() != nil {
			return nil, ErrCanceled
		}
		abs, err := s.resolveWritable(path)
		if err != nil {
			continue
		}
		info, err := os.Stat(abs)
		if err != nil || info.IsDir() || info.Size() > maxGrepFileBytes {
			if err != nil {
				skipped++
			}
			continue
		}
		data, err := os.ReadFile(abs)
		if err != nil {
			skipped++
			continue
		}
		if !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
			skipped++
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
		s.chownPath(abs)
		filesChanged++
		replacements += n
	}

	return &protocol.ReplaceInFilesResult{
		FilesChanged: filesChanged,
		Replacements: replacements,
		Skipped:      skipped,
	}, nil
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
