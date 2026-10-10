package git

import (
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spadeide/spade-agent/internal/protocol"
)

// parseStatus reads `git status --porcelain=v2 --branch -z`.
//
// Records are NUL-terminated. A rename (`2`) is followed by a second,
// separate NUL-terminated field holding the original path, so the reader has
// to consume the next record rather than just split on NUL.
func parseStatus(raw string, out *protocol.GitRepoStatus) {
	records := strings.Split(raw, "\x00")
	for i := 0; i < len(records); i++ {
		rec := records[i]
		if rec == "" {
			continue
		}
		switch {
		case strings.HasPrefix(rec, "# "):
			parseHeader(rec, out)
		case strings.HasPrefix(rec, "1 "):
			if f, ok := parseEntry(rec, 8); ok {
				appendFile(out, f)
			}
		case strings.HasPrefix(rec, "2 "):
			if f, ok := parseEntry(rec, 9); ok {
				if i+1 < len(records) {
					f.Orig = records[i+1]
					i++ // the original path is its own record
				}
				appendFile(out, f)
			}
		case strings.HasPrefix(rec, "u "):
			if f, ok := parseEntry(rec, 10); ok {
				appendFile(out, f)
			}
		case strings.HasPrefix(rec, "? "):
			appendFile(out, protocol.GitFile{Rel: rec[2:], Index: ".", Work: "?"})
		}
	}
}

func parseHeader(rec string, out *protocol.GitRepoStatus) {
	key, value, ok := strings.Cut(strings.TrimPrefix(rec, "# "), " ")
	if !ok {
		return
	}
	switch key {
	case "branch.head":
		if value == "(detached)" {
			out.Detached = true
		} else {
			out.Branch = value
		}
	case "branch.upstream":
		out.Upstream = value
	case "branch.ab":
		ahead, behind, ok := strings.Cut(value, " ")
		if !ok {
			return
		}
		out.Ahead, _ = strconv.Atoi(strings.TrimPrefix(ahead, "+"))
		out.Behind, _ = strconv.Atoi(strings.TrimPrefix(behind, "-"))
	}
}

// parseEntry reads a changed-entry record whose path follows `fields`
// space-separated columns. XY is always the first column after the type.
func parseEntry(rec string, fields int) (protocol.GitFile, bool) {
	parts := strings.SplitN(rec, " ", fields+1)
	if len(parts) < fields+1 || len(parts[1]) < 2 {
		return protocol.GitFile{}, false
	}
	return protocol.GitFile{
		Rel:   parts[fields],
		Index: string(parts[1][0]),
		Work:  string(parts[1][1]),
	}, true
}

func appendFile(out *protocol.GitRepoStatus, f protocol.GitFile) {
	if len(out.Files) >= MaxFiles {
		out.Truncated = true
		return
	}
	f.Path = filepath.Join(out.Root, f.Rel)
	if f.Orig != "" {
		f.Orig = filepath.Join(out.Root, f.Orig)
	}
	out.Files = append(out.Files, f)
}
