package git

import (
	"errors"
	"strconv"
	"strings"

	"github.com/spadeide/spade-agent/internal/protocol"
)

// hunkPatch cuts one hunk out of a unified diff, keeping the file header so the
// result is a patch git will accept on its own.
func hunkPatch(raw string, index int) (string, error) {
	lines := strings.Split(raw, "\n")
	start := -1
	for i, line := range lines {
		if strings.HasPrefix(line, "@@") {
			start = i
			break
		}
	}
	if start < 0 {
		return "", errors.New("no changes left to apply")
	}
	header := strings.Join(lines[:start], "\n")

	var hunks [][]string
	var current []string
	for _, line := range lines[start:] {
		if strings.HasPrefix(line, "@@") && len(current) > 0 {
			hunks = append(hunks, current)
			current = nil
		}
		current = append(current, line)
	}
	if len(current) > 0 {
		hunks = append(hunks, current)
	}
	if index < 0 || index >= len(hunks) {
		return "", errors.New("that change is no longer there — refresh and try again")
	}

	body := strings.Join(hunks[index], "\n")
	patch := header + "\n" + body
	if !strings.HasSuffix(patch, "\n") {
		patch += "\n"
	}
	return patch, nil
}

// parseDiff turns unified diff text into hunks, tracking the line numbers on
// both sides so the client can render a gutter.
func parseDiff(raw string, out *protocol.GitDiffResult) {
	var hunk *protocol.GitDiffHunk
	oldLine, newLine := 0, 0
	total := 0

	flush := func() {
		if hunk != nil {
			out.Hunks = append(out.Hunks, *hunk)
			hunk = nil
		}
	}

	for _, line := range strings.Split(raw, "\n") {
		switch {
		case strings.HasPrefix(line, "Binary files"):
			out.Binary = true
			flush()
			return
		case strings.HasPrefix(line, "@@"):
			flush()
			oldLine, newLine = parseHunkHeader(line)
			hunk = &protocol.GitDiffHunk{Header: line}
		case hunk == nil:
			continue // file headers, index lines, ---/+++
		case total >= MaxDiffLines:
			out.Truncated = true
			flush()
			return
		case strings.HasPrefix(line, "+"):
			hunk.Lines = append(hunk.Lines, protocol.GitDiffLine{
				Kind: "add", Text: line[1:], New: newLine,
			})
			newLine++
			total++
		case strings.HasPrefix(line, "-"):
			hunk.Lines = append(hunk.Lines, protocol.GitDiffLine{
				Kind: "remove", Text: line[1:], Old: oldLine,
			})
			oldLine++
			total++
		case strings.HasPrefix(line, " "):
			hunk.Lines = append(hunk.Lines, protocol.GitDiffLine{
				Kind: "context", Text: line[1:], Old: oldLine, New: newLine,
			})
			oldLine++
			newLine++
			total++
		case line == `\ No newline at end of file`:
			continue
		}
	}
	flush()
}

// parseHunkHeader reads the starting line numbers out of `@@ -a,b +c,d @@`.
func parseHunkHeader(header string) (oldLine, newLine int) {
	body, _, _ := strings.Cut(strings.TrimPrefix(header, "@@ "), " @@")
	for _, field := range strings.Fields(body) {
		start, _, _ := strings.Cut(strings.TrimLeft(field, "-+"), ",")
		n, err := strconv.Atoi(start)
		if err != nil {
			continue
		}
		if strings.HasPrefix(field, "-") {
			oldLine = n
		} else if strings.HasPrefix(field, "+") {
			newLine = n
		}
	}
	return oldLine, newLine
}
