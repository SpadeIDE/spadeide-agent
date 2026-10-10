package fsaccess

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spadeide/spade-agent/internal/protocol"
)

// newTestService builds a Service with a writable workspace root (home is
// browse-only by default; tests that mutate files must AddRoot / open it).
func newTestService(t *testing.T, root string) *Service {
	t.Helper()
	s := New(root, nil)
	if err := s.AddRoot(root); err != nil {
		t.Fatalf("AddRoot: %v", err)
	}
	return s
}

func TestBrowseHomeRejectsWriteUntilOpened(t *testing.T) {
	home := t.TempDir()
	proj := filepath.Join(home, "proj")
	if err := os.Mkdir(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	svc := New(home, nil) // browse-only home

	if _, err := svc.List(home); err != nil {
		t.Fatalf("list home should work: %v", err)
	}
	target := filepath.Join(home, "nope.txt")
	if _, err := svc.Write(target, base64.StdEncoding.EncodeToString([]byte("x"))); err == nil {
		t.Fatalf("expected write under browse-only home to fail")
	}
	if err := svc.AddRoot(proj); err != nil {
		t.Fatal(err)
	}
	ok := filepath.Join(proj, "ok.txt")
	if _, err := svc.Write(ok, base64.StdEncoding.EncodeToString([]byte("hi"))); err != nil {
		t.Fatalf("write in opened project failed: %v", err)
	}
}

func TestAddRootRejectsFilesystemRoot(t *testing.T) {
	svc := New(t.TempDir(), nil)
	if err := svc.AddRoot("/"); err == nil {
		t.Fatal("expected AddRoot(/) to fail")
	}
}

func TestAddRootRejectsOutsideJail(t *testing.T) {
	home := t.TempDir()
	outside := t.TempDir()
	svc := New(home, nil)
	if err := svc.AddRoot(outside); err == nil {
		t.Fatal("expected AddRoot outside home jail to fail")
	}
	inside := filepath.Join(home, "proj")
	if err := os.Mkdir(inside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := svc.AddRoot(inside); err != nil {
		t.Fatalf("AddRoot inside home: %v", err)
	}
}

func TestResolveRejectsOutsideAllowlist(t *testing.T) {
	root := t.TempDir()
	s := newTestService(t, root)

	outside := filepath.Join(filepath.Dir(root), "outside.txt")
	if _, err := s.resolve(outside); err == nil {
		t.Fatalf("expected rejection for path outside allowlist, got nil")
	}
	if _, err := s.resolve(filepath.Join(root, "..", "escape.txt")); err == nil {
		t.Fatalf("expected rejection for .. escape, got nil")
	}
}

func TestResolveRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	secret := t.TempDir() // a sibling dir the sandbox must never reach

	if err := os.WriteFile(filepath.Join(secret, "secret.txt"), []byte("top secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A symlink *inside* the sandbox pointing at the secret dir.
	link := filepath.Join(root, "link")
	if err := os.Symlink(secret, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	svc := newTestService(t, root)
	escapePath := filepath.Join(link, "secret.txt")
	if _, err := svc.resolve(escapePath); err == nil {
		t.Fatalf("expected symlink escape to be rejected, got nil")
	}
	// Reading through the symlink must also fail.
	if _, err := svc.Read(escapePath); err == nil {
		t.Fatalf("expected Read through symlink escape to fail")
	}
}

func TestReadWriteWithinRoot(t *testing.T) {
	root := t.TempDir()
	svc := newTestService(t, root)

	target := filepath.Join(root, "sub", "file.txt")
	if _, err := svc.Write(target, base64.StdEncoding.EncodeToString([]byte("hello"))); err != nil {
		t.Fatalf("write within root failed: %v", err)
	}
	res, err := svc.Read(target)
	if err != nil {
		t.Fatalf("read within root failed: %v", err)
	}
	got, _ := base64.StdEncoding.DecodeString(res.Content)
	if string(got) != "hello" {
		t.Fatalf("round-trip mismatch: %q", got)
	}
}

func TestReadRejectsOversizeFile(t *testing.T) {
	root := t.TempDir()
	svc := newTestService(t, root)

	big := filepath.Join(root, "big.bin")
	f, err := os.Create(big)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(MaxFileBytes + 1); err != nil {
		t.Fatal(err)
	}
	f.Close()

	if _, err := svc.Read(big); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("expected 'too large' error, got %v", err)
	}
}

func TestWriteRejectsOversizePayload(t *testing.T) {
	root := t.TempDir()
	svc := newTestService(t, root)

	payload := base64.StdEncoding.EncodeToString(make([]byte, MaxFileBytes+1))
	if _, err := svc.Write(filepath.Join(root, "x.bin"), payload); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("expected 'too large' error, got %v", err)
	}
}

func TestSearchListsFilesAndPrunesIgnoredDirs(t *testing.T) {
	root := t.TempDir()
	mustWrite := func(rel string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite("main.go")
	mustWrite("src/app.swift")
	mustWrite("node_modules/dep/index.js") // should be pruned
	mustWrite(".git/config")               // should be pruned

	svc := newTestService(t, root)
	res, err := svc.Search(root, 0)
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}
	rels := map[string]bool{}
	for _, f := range res.Files {
		rels[f.Rel] = true
	}
	if !rels["main.go"] || !rels[filepath.Join("src", "app.swift")] {
		t.Fatalf("expected project files, got %v", rels)
	}
	for rel := range rels {
		if strings.Contains(rel, "node_modules") || strings.HasPrefix(rel, ".git") {
			t.Fatalf("ignored dir leaked into results: %q", rel)
		}
	}
}

func TestSearchRespectsLimit(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 10; i++ {
		if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("f%d.txt", i)), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	res, err := newTestService(t, root).Search(root, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Files) != 3 || !res.Truncated {
		t.Fatalf("expected 3 files + truncated, got %d truncated=%v", len(res.Files), res.Truncated)
	}
}

func TestGrepFindsMatchesAndReplace(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "proj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proj, "a.txt"), []byte("hello world\nHello again\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	svc := newTestService(t, root)
	if err := svc.AddRoot(proj); err != nil {
		t.Fatal(err)
	}

	res, err := svc.Grep(protocol.GrepParams{Root: proj, Query: "hello", CaseSensitive: false})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Matches) != 2 {
		t.Fatalf("expected 2 matches, got %d", len(res.Matches))
	}

	rep, err := svc.ReplaceInFiles(protocol.ReplaceInFilesParams{
		Root: proj, Query: "hello", Replacement: "hi", CaseSensitive: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	if rep.FilesChanged != 1 || rep.Replacements != 2 {
		t.Fatalf("unexpected replace result: %+v", rep)
	}
	data, _ := os.ReadFile(filepath.Join(proj, "a.txt"))
	if !strings.Contains(string(data), "hi world") || !strings.Contains(string(data), "hi again") {
		t.Fatalf("file not updated: %q", data)
	}
}

func TestGrepSkipsBinariesAndLockfiles(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "app.go"), []byte("version 6.59.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "photo.png"), []byte("version 6.59.0"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "package-lock.json"), []byte("version 6.59.0"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := newTestService(t, root).Grep(protocol.GrepParams{Root: root, Query: "6.59.0"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Matches) != 1 || res.Matches[0].Rel != "app.go" {
		t.Fatalf("expected only app.go, got %+v", res.Matches)
	}
}

func TestSplitCopyName(t *testing.T) {
	cases := []struct{ name, stem, ext string }{
		{"main.tf", "main", ".tf"},
		{".gitignore", ".gitignore", ""},
		{"folder", "folder", ""},
		{"archive.tar.gz", "archive.tar", ".gz"},
	}
	for _, c := range cases {
		stem, ext := splitCopyName(c.name)
		if stem != c.stem || ext != c.ext {
			t.Fatalf("%q: got %q %q, want %q %q", c.name, stem, ext, c.stem, c.ext)
		}
	}
}

func TestCopyFileSameDirAddsCopySuffix(t *testing.T) {
	root := t.TempDir()
	svc := newTestService(t, root)
	src := filepath.Join(root, "main.tf")
	if err := os.WriteFile(src, []byte("resource {}"), 0o644); err != nil {
		t.Fatal(err)
	}

	first, err := svc.Copy(src, root)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(first) != "main copy.tf" {
		t.Fatalf("first copy name: %s", first)
	}
	got, err := os.ReadFile(first)
	if err != nil || string(got) != "resource {}" {
		t.Fatalf("first copy contents: %q %v", got, err)
	}

	second, err := svc.Copy(src, root)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(second) != "main copy 2.tf" {
		t.Fatalf("second copy name: %s", second)
	}
}

func TestCopyIntoParentDoesNotFollowLoneChild(t *testing.T) {
	root := t.TempDir()
	svc := newTestService(t, root)
	one := filepath.Join(root, "1")
	two := filepath.Join(one, "2")
	if err := os.MkdirAll(two, 0o755); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(root, "note.txt")
	if err := os.WriteFile(src, []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	path, err := svc.Copy(src, one)
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(one, "note.txt") {
		t.Fatalf("should land in folder 1, got %s", path)
	}
	if _, err := os.Stat(filepath.Join(two, "note.txt")); !os.IsNotExist(err) {
		t.Fatal("must not follow the lone child into folder 2")
	}
}

func TestCopyIntoOtherDirKeepsName(t *testing.T) {
	root := t.TempDir()
	svc := newTestService(t, root)
	src := filepath.Join(root, "a.txt")
	dest := filepath.Join(root, "out")
	if err := os.WriteFile(src, []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	path, err := svc.Copy(src, dest)
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(dest, "a.txt") {
		t.Fatalf("expected original name, got %s", path)
	}
}

func TestCopyDirectoryRecursiveAndSelfPaste(t *testing.T) {
	root := t.TempDir()
	svc := newTestService(t, root)
	dir := filepath.Join(root, "aws")
	if err := os.MkdirAll(filepath.Join(dir, "dev"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "dev", "main.tf"), []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Paste onto the folder itself → inside as `aws copy`, not retargeted.
	path, err := svc.Copy(dir, dir)
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(dir, "aws") {
		t.Fatalf("self-paste path: %s", path)
	}
	data, err := os.ReadFile(filepath.Join(path, "dev", "main.tf"))
	if err != nil || string(data) != "ok" {
		t.Fatalf("copied tree missing file: %v %q", err, data)
	}
}

func TestMoveFileToOtherDir(t *testing.T) {
	root := t.TempDir()
	svc := newTestService(t, root)
	src := filepath.Join(root, "a.txt")
	destDir := filepath.Join(root, "out")
	if err := os.WriteFile(src, []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(destDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path, err := svc.Move(src, destDir)
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(destDir, "a.txt") {
		t.Fatalf("moved path: %s", path)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Fatalf("source should be gone, stat: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "hi" {
		t.Fatalf("moved contents: %q %v", got, err)
	}
}

func TestMoveSameDirIsNoop(t *testing.T) {
	root := t.TempDir()
	svc := newTestService(t, root)
	src := filepath.Join(root, "a.txt")
	if err := os.WriteFile(src, []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	path, err := svc.Move(src, root)
	if err != nil {
		t.Fatal(err)
	}
	if path != src {
		t.Fatalf("expected same path, got %s", path)
	}
	if _, err := os.Stat(src); err != nil {
		t.Fatalf("source should still exist: %v", err)
	}
}

func TestMoveRejectsFolderIntoSelf(t *testing.T) {
	root := t.TempDir()
	svc := newTestService(t, root)
	dir := filepath.Join(root, "aws")
	if err := os.MkdirAll(filepath.Join(dir, "dev"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Move(dir, dir); err == nil {
		t.Fatal("expected move-into-self to fail")
	}
	if _, err := svc.Move(dir, filepath.Join(dir, "dev")); err == nil {
		t.Fatal("expected move-into-child to fail")
	}
}
