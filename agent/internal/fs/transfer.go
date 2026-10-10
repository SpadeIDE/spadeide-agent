package fsaccess

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

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
	s.OwnTree(dest)
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
	s.chownUp(dest)
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
