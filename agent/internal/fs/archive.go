package fsaccess

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Extract unpacks a zip/tar archive into a sibling folder named after the
// archive stem (foo.tar.gz → foo/). Host tools only — large archives stay on disk.
func (s *Service) Extract(ctx context.Context, path string) (string, error) {
	abs, err := s.resolve(path)
	if err != nil {
		return "", err
	}
	info, err := os.Lstat(abs)
	if err != nil {
		return "", err
	}
	if info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("not an archive file")
	}
	kind := detectArchiveKind(abs)
	if kind == archiveUnknown {
		return "", fmt.Errorf("unsupported archive type (use .zip, .tar, .tar.gz, .tgz, .tar.bz2)")
	}

	parent := filepath.Dir(abs)
	if _, err := s.resolveWritable(parent); err != nil {
		return "", err
	}
	stem := archiveStem(filepath.Base(abs))
	if stem == "" || stem == "." || stem == ".." {
		return "", fmt.Errorf("invalid archive name")
	}
	// Folder dest: keep dotted stems intact (`notes.txt` → `notes.txt copy`).
	destName := uniqueSiblingName(parent, stem, "")
	dest := filepath.Join(parent, destName)
	if _, err := s.resolveWritable(dest); err != nil {
		return "", err
	}

	ctx, cancel := s.beginArchiveOp(ctx)
	defer cancel()

	if err := validateArchiveMembers(ctx, kind, abs); err != nil {
		return "", err
	}
	if err := os.Mkdir(dest, 0o755); err != nil {
		return "", err
	}
	if err := runExtract(ctx, kind, abs, dest); err != nil {
		_ = os.RemoveAll(dest)
		return "", err
	}
	return dest, nil
}

// Archive compresses path into a sibling archive (zip or tar.gz).
func (s *Service) Archive(ctx context.Context, path, format string) (string, error) {
	format = strings.ToLower(strings.TrimSpace(format))
	var suffix string
	switch format {
	case "zip":
		suffix = ".zip"
	case "tar.gz", "tgz":
		format = "tar.gz"
		suffix = ".tar.gz"
	default:
		return "", fmt.Errorf("unsupported archive format %q (use zip or tar.gz)", format)
	}

	abs, err := s.resolve(path)
	if err != nil {
		return "", err
	}
	info, err := os.Lstat(abs)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("cannot archive a symlink")
	}

	parent := filepath.Dir(abs)
	if _, err := s.resolveWritable(parent); err != nil {
		return "", err
	}
	base := filepath.Base(abs)
	// Prefer uniqueCopyName for .zip; for .tar.gz use suffix-aware helper so
	// collisions become `name copy.tar.gz` (not `name.tar copy.gz`).
	var outName string
	if suffix == ".tar.gz" {
		outName = uniqueSiblingName(parent, base, suffix)
	} else {
		outName = uniqueCopyName(parent, base+suffix)
	}
	out := filepath.Join(parent, outName)
	if _, err := s.resolveWritable(out); err != nil {
		return "", err
	}
	if out == abs {
		return "", fmt.Errorf("cannot archive a path onto itself")
	}

	ctx, cancel := s.beginArchiveOp(ctx)
	defer cancel()

	if err := runArchive(ctx, format, parent, base, out); err != nil {
		_ = os.Remove(out)
		return "", err
	}
	return out, nil
}

func (s *Service) beginArchiveOp(parent context.Context) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	s.mu.Lock()
	if s.archiveCancel != nil {
		s.archiveCancel()
	}
	s.archiveCancel = cancel
	s.mu.Unlock()
	return ctx, cancel
}

type archiveKind int

const (
	archiveUnknown archiveKind = iota
	archiveZip
	archiveTar
	archiveTarGz
	archiveTarBz2
)

func detectArchiveKind(path string) archiveKind {
	lower := strings.ToLower(path)
	switch {
	case strings.HasSuffix(lower, ".tar.gz"), strings.HasSuffix(lower, ".tgz"):
		return archiveTarGz
	case strings.HasSuffix(lower, ".tar.bz2"), strings.HasSuffix(lower, ".tbz2"), strings.HasSuffix(lower, ".tbz"):
		return archiveTarBz2
	case strings.HasSuffix(lower, ".tar"):
		return archiveTar
	case strings.HasSuffix(lower, ".zip"):
		return archiveZip
	default:
		return archiveUnknown
	}
}

// archiveStem strips known archive suffixes for the sibling folder name.
func archiveStem(name string) string {
	lower := strings.ToLower(name)
	for _, suf := range []string{".tar.gz", ".tar.bz2", ".tgz", ".tbz2", ".tbz", ".tar", ".zip"} {
		if strings.HasSuffix(lower, suf) {
			return name[:len(name)-len(suf)]
		}
	}
	return name
}

// uniqueSiblingName is uniqueCopyName for a stem + compound suffix (.tar.gz).
func uniqueSiblingName(destDir, stem, suffix string) string {
	candidate := stem + suffix
	if _, err := os.Stat(filepath.Join(destDir, candidate)); err != nil {
		return candidate
	}
	candidate = stem + " copy" + suffix
	for n := 2; n < 1000; n++ {
		if _, err := os.Stat(filepath.Join(destDir, candidate)); err != nil {
			return candidate
		}
		candidate = fmt.Sprintf("%s copy %d%s", stem, n, suffix)
	}
	return fmt.Sprintf("%s copy %d%s", stem, time.Now().UnixNano(), suffix)
}

func validateArchiveMembers(ctx context.Context, kind archiveKind, archive string) error {
	var names []string
	var err error
	switch kind {
	case archiveZip:
		names, err = listZipMembers(ctx, archive)
	case archiveTar, archiveTarGz, archiveTarBz2:
		names, err = listTarMembers(ctx, kind, archive)
	default:
		return fmt.Errorf("unsupported archive type")
	}
	if err != nil {
		return err
	}
	for _, name := range names {
		if unsafeArchiveMember(name) {
			return fmt.Errorf("archive refused: unsafe path %q", name)
		}
	}
	return nil
}

func unsafeArchiveMember(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" {
		return false
	}
	// zipinfo / tar may list directories with trailing slash
	clean := strings.TrimSuffix(name, "/")
	if clean == "" {
		return false
	}
	if filepath.IsAbs(clean) || strings.HasPrefix(clean, `\`) {
		return true
	}
	// Windows drive / UNC in zip
	if len(clean) >= 2 && clean[1] == ':' {
		return true
	}
	parts := strings.FieldsFunc(clean, func(r rune) bool { return r == '/' || r == '\\' })
	for _, p := range parts {
		if p == ".." {
			return true
		}
	}
	return false
}

func listZipMembers(ctx context.Context, archive string) ([]string, error) {
	tool, err := lookTool("unzip")
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, tool, "-Z1", archive)
	out, err := cmd.Output()
	if err != nil {
		return nil, toolError("unzip", err, out)
	}
	return splitNonEmptyLines(string(out)), nil
}

func listTarMembers(ctx context.Context, kind archiveKind, archive string) ([]string, error) {
	tool, err := lookTool("tar")
	if err != nil {
		return nil, err
	}
	args := []string{"-tf", archive}
	switch kind {
	case archiveTarGz:
		args = []string{"-tzf", archive}
	case archiveTarBz2:
		args = []string{"-tjf", archive}
	}
	cmd := exec.CommandContext(ctx, tool, args...)
	out, err := cmd.Output()
	if err != nil {
		return nil, toolError("tar", err, out)
	}
	return splitNonEmptyLines(string(out)), nil
}

func runExtract(ctx context.Context, kind archiveKind, archive, dest string) error {
	switch kind {
	case archiveZip:
		tool, err := lookTool("unzip")
		if err != nil {
			return err
		}
		cmd := exec.CommandContext(ctx, tool, "-q", archive, "-d", dest)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return toolError("unzip", err, out)
		}
		return nil
	case archiveTar, archiveTarGz, archiveTarBz2:
		tool, err := lookTool("tar")
		if err != nil {
			return err
		}
		args := []string{"-xf", archive, "-C", dest}
		switch kind {
		case archiveTarGz:
			args = []string{"-xzf", archive, "-C", dest}
		case archiveTarBz2:
			args = []string{"-xjf", archive, "-C", dest}
		}
		cmd := exec.CommandContext(ctx, tool, args...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return toolError("tar", err, out)
		}
		return nil
	default:
		return fmt.Errorf("unsupported archive type")
	}
}

func runArchive(ctx context.Context, format, parent, base, out string) error {
	switch format {
	case "zip":
		tool, err := lookTool("zip")
		if err != nil {
			return err
		}
		// Relative paths inside the zip; run from parent.
		cmd := exec.CommandContext(ctx, tool, "-r", "-q", out, base)
		cmd.Dir = parent
		outb, err := cmd.CombinedOutput()
		if err != nil {
			return toolError("zip", err, outb)
		}
		return nil
	case "tar.gz":
		tool, err := lookTool("tar")
		if err != nil {
			return err
		}
		cmd := exec.CommandContext(ctx, tool, "-czf", out, base)
		cmd.Dir = parent
		outb, err := cmd.CombinedOutput()
		if err != nil {
			return toolError("tar", err, outb)
		}
		return nil
	default:
		return fmt.Errorf("unsupported archive format %q", format)
	}
}

func lookTool(name string) (string, error) {
	path, err := exec.LookPath(name)
	if err != nil {
		return "", fmt.Errorf("%s not found on PATH", name)
	}
	return path, nil
}

func toolError(tool string, err error, out []byte) error {
	msg := strings.TrimSpace(string(out))
	if msg == "" {
		if ee, ok := err.(*exec.ExitError); ok {
			msg = strings.TrimSpace(string(ee.Stderr))
		}
	}
	if msg == "" {
		return fmt.Errorf("%s: %w", tool, err)
	}
	// Keep messages short for the iPad toast.
	if len(msg) > 400 {
		msg = msg[:400] + "…"
	}
	return fmt.Errorf("%s: %s", tool, msg)
}

func splitNonEmptyLines(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimRight(line, "\r")
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}
