package git

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spadeide/spade-agent/internal/protocol"
)

// rec joins porcelain records the way `-z` emits them: NUL after each one.
func rec(records ...string) string {
	return strings.Join(records, "\x00") + "\x00"
}

func TestParseStatusReadsBranchAndCounts(t *testing.T) {
	out := &protocol.GitRepoStatus{Root: "/repo"}
	parseStatus(rec(
		"# branch.oid abc123",
		"# branch.head main",
		"# branch.upstream origin/main",
		"# branch.ab +3 -2",
	), out)

	if out.Branch != "main" || out.Upstream != "origin/main" {
		t.Fatalf("branch/upstream = %q/%q", out.Branch, out.Upstream)
	}
	if out.Ahead != 3 || out.Behind != 2 {
		t.Fatalf("ahead/behind = %d/%d, want 3/2", out.Ahead, out.Behind)
	}
	if out.Detached {
		t.Fatal("should not be detached")
	}
}

func TestParseStatusDetachedHead(t *testing.T) {
	out := &protocol.GitRepoStatus{Root: "/repo"}
	parseStatus(rec("# branch.head (detached)"), out)
	if !out.Detached || out.Branch != "" {
		t.Fatalf("detached=%v branch=%q", out.Detached, out.Branch)
	}
}

func TestParseStatusOrdinaryAndUntracked(t *testing.T) {
	out := &protocol.GitRepoStatus{Root: "/repo"}
	parseStatus(rec(
		"1 M. N... 100644 100644 100644 aaa bbb staged.txt",
		"1 .M N... 100644 100644 100644 aaa bbb dirty.txt",
		"1 MM N... 100644 100644 100644 aaa bbb both.txt",
		"? new.txt",
	), out)

	if len(out.Files) != 4 {
		t.Fatalf("got %d files: %+v", len(out.Files), out.Files)
	}
	want := []protocol.GitFile{
		{Rel: "staged.txt", Index: "M", Work: "."},
		{Rel: "dirty.txt", Index: ".", Work: "M"},
		{Rel: "both.txt", Index: "M", Work: "M"},
		{Rel: "new.txt", Index: ".", Work: "?"},
	}
	for i, w := range want {
		got := out.Files[i]
		if got.Rel != w.Rel || got.Index != w.Index || got.Work != w.Work {
			t.Errorf("file %d = %+v, want %+v", i, got, w)
		}
		if got.Path != filepath.Join("/repo", w.Rel) {
			t.Errorf("file %d path = %q", i, got.Path)
		}
	}
}

// Default `git status` reports an untracked directory as `? path/dir/` and does
// not list files inside. Rel must keep the trailing slash so the iPad can mark
// the directory itself (and inherit tint for children).
func TestParseStatusUntrackedDirectoryKeepsTrailingSlash(t *testing.T) {
	out := &protocol.GitRepoStatus{Root: "/repo"}
	parseStatus(rec("? evolution-b2c/prod/reader-frontend/"), out)
	if len(out.Files) != 1 {
		t.Fatalf("got %d files: %+v", len(out.Files), out.Files)
	}
	f := out.Files[0]
	if f.Rel != "evolution-b2c/prod/reader-frontend/" || f.Work != "?" {
		t.Errorf("rel/work = %+v", f)
	}
	if f.Path != "/repo/evolution-b2c/prod/reader-frontend" {
		t.Errorf("path = %q (want cleaned, no trailing slash)", f.Path)
	}
}

// A rename record is followed by a *separate* NUL-terminated field holding the
// original path. Reading it as an ordinary record would lose a file and shift
// everything after it.
func TestParseStatusRenameConsumesOriginalPath(t *testing.T) {
	out := &protocol.GitRepoStatus{Root: "/repo"}
	parseStatus(rec(
		"2 R. N... 100644 100644 100644 aaa bbb R100 after.txt",
		"before.txt",
		"? untouched.txt",
	), out)

	if len(out.Files) != 2 {
		t.Fatalf("got %d files: %+v", len(out.Files), out.Files)
	}
	if out.Files[0].Rel != "after.txt" || out.Files[0].Orig != filepath.Join("/repo", "before.txt") {
		t.Errorf("rename = %+v", out.Files[0])
	}
	if out.Files[1].Rel != "untouched.txt" {
		t.Errorf("record after a rename = %+v", out.Files[1])
	}
}

func TestParseStatusUnmerged(t *testing.T) {
	out := &protocol.GitRepoStatus{Root: "/repo"}
	parseStatus(rec("u UU N... 100644 100644 100644 100644 aaa bbb ccc conflict.txt"), out)
	if len(out.Files) != 1 {
		t.Fatalf("got %d files", len(out.Files))
	}
	if f := out.Files[0]; f.Rel != "conflict.txt" || f.Index != "U" || f.Work != "U" {
		t.Errorf("unmerged = %+v", f)
	}
}

func TestParseStatusKeepsSpacesInPaths(t *testing.T) {
	out := &protocol.GitRepoStatus{Root: "/repo"}
	parseStatus(rec("1 .M N... 100644 100644 100644 aaa bbb dir with spaces/a b.txt"), out)
	if len(out.Files) != 1 || out.Files[0].Rel != "dir with spaces/a b.txt" {
		t.Fatalf("files = %+v", out.Files)
	}
}

func TestParseStatusCapsFileCount(t *testing.T) {
	records := make([]string, 0, MaxFiles+10)
	for i := 0; i < MaxFiles+10; i++ {
		records = append(records, "? file.txt")
	}
	out := &protocol.GitRepoStatus{Root: "/repo"}
	parseStatus(rec(records...), out)
	if len(out.Files) != MaxFiles || !out.Truncated {
		t.Fatalf("files=%d truncated=%v", len(out.Files), out.Truncated)
	}
}

// Status reports a missing repository as data, not as an error — the client
// draws "not a repository" rather than an error banner.
func TestStatusOutsideRepositoryIsNotAnError(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	out, err := New().Status(t.TempDir())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !out.Installed {
		t.Error("git is on PATH but Installed is false")
	}
	if len(out.Repos) != 0 {
		t.Errorf("a temp dir holds no repositories, got %+v", out.Repos)
	}
}

func TestStatusReadsARealRepository(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "--initial-branch", "main"},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "Test"},
	} {
		if _, stderr, err := run(30_000_000_000, dir, args...); err != nil {
			t.Fatalf("git %v: %v %s", args, err, stderr)
		}
	}
	if err := writeFile(filepath.Join(dir, "new.txt"), "hello"); err != nil {
		t.Fatal(err)
	}

	out, err := New().Status(dir)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if len(out.Repos) != 1 {
		t.Fatalf("expected one repository, got %+v", out.Repos)
	}
	repo := out.Repos[0]
	if repo.Branch != "main" {
		t.Errorf("branch = %q, want main", repo.Branch)
	}
	if len(repo.Files) != 1 || repo.Files[0].Work != "?" {
		t.Fatalf("files = %+v, want one untracked", repo.Files)
	}

	svc := New()
	if err := svc.Stage(dir, []string{filepath.Join(dir, "new.txt")}); err != nil {
		t.Fatalf("stage: %v", err)
	}
	staged, err := svc.Status(dir)
	if err != nil {
		t.Fatalf("status after stage: %v", err)
	}
	if len(staged.Repos) != 1 || len(staged.Repos[0].Files) != 1 || staged.Repos[0].Files[0].Index != "A" {
		t.Fatalf("after stage = %+v, want one added", staged.Repos)
	}

	commit, err := svc.Commit(dir, "first", false, false)
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	if commit.Subject != "first" || commit.Commit == "" {
		t.Errorf("commit = %+v", commit)
	}
	clean, err := svc.Status(dir)
	if err != nil {
		t.Fatalf("status after commit: %v", err)
	}
	if len(clean.Repos) != 1 || len(clean.Repos[0].Files) != 0 {
		t.Errorf("expected a clean tree, got %+v", clean.Repos)
	}
}

func TestCommitRejectsEmptyMessage(t *testing.T) {
	if _, err := New().Commit(t.TempDir(), "   ", false, false); err == nil {
		t.Fatal("expected an error for an empty message")
	}
}

func writeFile(path, contents string) error {
	return os.WriteFile(path, []byte(contents), 0o644)
}

func TestParseDiffHunksAndLineNumbers(t *testing.T) {
	raw := `diff --git a/main.go b/main.go
index 111..222 100644
--- a/main.go
+++ b/main.go
@@ -10,4 +10,5 @@ func main() {
 	keep()
-	gone()
+	added()
+	more()
 	tail()
`
	out := &protocol.GitDiffResult{}
	parseDiff(raw, out)

	if len(out.Hunks) != 1 {
		t.Fatalf("hunks = %d", len(out.Hunks))
	}
	lines := out.Hunks[0].Lines
	if len(lines) != 5 {
		t.Fatalf("lines = %d: %+v", len(lines), lines)
	}
	want := []struct {
		kind     string
		old, new int
	}{
		{"context", 10, 10},
		{"remove", 11, 0},
		{"add", 0, 11},
		{"add", 0, 12},
		{"context", 12, 13},
	}
	for i, w := range want {
		got := lines[i]
		if got.Kind != w.kind || got.Old != w.old || got.New != w.new {
			t.Errorf("line %d = %s old=%d new=%d, want %s old=%d new=%d",
				i, got.Kind, got.Old, got.New, w.kind, w.old, w.new)
		}
	}
}

func TestParseDiffFlagsBinary(t *testing.T) {
	out := &protocol.GitDiffResult{}
	parseDiff("diff --git a/x.png b/x.png\nBinary files a/x.png and b/x.png differ\n", out)
	if !out.Binary || len(out.Hunks) != 0 {
		t.Fatalf("binary=%v hunks=%d", out.Binary, len(out.Hunks))
	}
}

func TestParseDiffSkipsFileHeaders(t *testing.T) {
	out := &protocol.GitDiffResult{}
	parseDiff("diff --git a/a b/a\n--- a/a\n+++ b/a\n", out)
	if len(out.Hunks) != 0 {
		t.Fatalf("headers alone should yield no hunks, got %+v", out.Hunks)
	}
}

func TestDiffAndBranchesOnARealRepository(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	svc := New()
	for _, args := range [][]string{
		{"init", "--initial-branch", "main"},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "Test"},
	} {
		if _, stderr, err := run(30_000_000_000, dir, args...); err != nil {
			t.Fatalf("git %v: %v %s", args, err, stderr)
		}
	}
	file := filepath.Join(dir, "a.txt")
	if err := writeFile(file, "one\ntwo\n"); err != nil {
		t.Fatal(err)
	}

	// Untracked: diffed against /dev/null, so every line reads as added.
	untracked, err := svc.Diff(dir, file, false, false)
	if err != nil {
		t.Fatalf("diff untracked: %v", err)
	}
	if !untracked.Untracked || len(untracked.Hunks) != 1 {
		t.Fatalf("untracked diff = %+v", untracked)
	}
	for _, line := range untracked.Hunks[0].Lines {
		if line.Kind != "add" {
			t.Fatalf("untracked line should be an addition: %+v", line)
		}
	}

	if err := svc.Stage(dir, []string{file}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Commit(dir, "first", false, false); err != nil {
		t.Fatal(err)
	}
	if err := writeFile(file, "one\nchanged\n"); err != nil {
		t.Fatal(err)
	}

	diff, err := svc.Diff(dir, file, false, false)
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	if len(diff.Hunks) != 1 {
		t.Fatalf("hunks = %+v", diff.Hunks)
	}
	var adds, removes int
	for _, line := range diff.Hunks[0].Lines {
		switch line.Kind {
		case "add":
			adds++
		case "remove":
			removes++
		}
	}
	if adds != 1 || removes != 1 {
		t.Errorf("adds=%d removes=%d, want 1/1", adds, removes)
	}

	// Staging that edit empties the working-tree diff, but the HEAD diff — what
	// the editor's gutter marks read — still reports it. That is the whole point
	// of the flag: staging a change must not make it stop being a change.
	if err := svc.Stage(dir, []string{file}); err != nil {
		t.Fatal(err)
	}
	if worktree, err := svc.Diff(dir, file, false, false); err != nil {
		t.Fatalf("diff after staging: %v", err)
	} else if len(worktree.Hunks) != 0 {
		t.Errorf("working tree should be clean after staging, got %+v", worktree.Hunks)
	}
	head, err := svc.Diff(dir, file, false, true)
	if err != nil {
		t.Fatalf("diff against head: %v", err)
	}
	if len(head.Hunks) != 1 {
		t.Fatalf("head diff hunks = %+v", head.Hunks)
	}

	branches, err := svc.Branches(dir)
	if err != nil {
		t.Fatalf("branches: %v", err)
	}
	if len(branches.Branches) != 1 || branches.Branches[0].Name != "main" || !branches.Branches[0].Current {
		t.Fatalf("branches = %+v", branches.Branches)
	}

	if err := svc.Checkout(dir, "feature", true); err != nil {
		t.Fatalf("checkout -b: %v", err)
	}
	after, err := svc.Status(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Repos) != 1 || after.Repos[0].Branch != "feature" {
		t.Errorf("branch after checkout = %+v", after.Repos)
	}
}

func TestCheckoutRejectsEmptyBranch(t *testing.T) {
	if err := New().Checkout(t.TempDir(), "  ", false); err == nil {
		t.Fatal("expected an error for an empty branch name")
	}
}

func TestCheckoutRejectsDashPrefix(t *testing.T) {
	if err := New().Checkout(t.TempDir(), "--orphan", false); err == nil {
		t.Fatal("expected an error for a dash-prefixed branch")
	}
}

func TestHunkPatchCutsOneHunkWithHeader(t *testing.T) {
	raw := `diff --git a/a.txt b/a.txt
index 111..222 100644
--- a/a.txt
+++ b/a.txt
@@ -1,3 +1,3 @@
 one
-two
+TWO
@@ -20,3 +20,3 @@
 twenty
-x
+X
`
	first, err := hunkPatch(raw, 0)
	if err != nil {
		t.Fatalf("hunk 0: %v", err)
	}
	if !strings.HasPrefix(first, "diff --git a/a.txt b/a.txt\n") {
		t.Errorf("patch lost its header:\n%s", first)
	}
	if !strings.Contains(first, "@@ -1,3 +1,3 @@") || strings.Contains(first, "@@ -20,3") {
		t.Errorf("patch should hold only the first hunk:\n%s", first)
	}
	if !strings.HasSuffix(first, "\n") {
		t.Error("patch must end with a newline or git apply rejects it")
	}

	second, err := hunkPatch(raw, 1)
	if err != nil {
		t.Fatalf("hunk 1: %v", err)
	}
	if !strings.Contains(second, "@@ -20,3 +20,3 @@") || strings.Contains(second, "@@ -1,3") {
		t.Errorf("second hunk wrong:\n%s", second)
	}
}

func TestHunkPatchRejectsMissingHunk(t *testing.T) {
	if _, err := hunkPatch("diff --git a/a b/a\n", 0); err == nil {
		t.Error("a diff with no hunks should fail")
	}
	raw := "--- a/a\n+++ b/a\n@@ -1 +1 @@\n-a\n+b\n"
	if _, err := hunkPatch(raw, 5); err == nil {
		t.Error("an out-of-range index should fail")
	}
}

func TestApplyHunkStagesDiscardsAndUnstages(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	svc := New()
	for _, args := range [][]string{
		{"init", "--initial-branch", "main"},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "Test"},
	} {
		if _, stderr, err := run(30_000_000_000, dir, args...); err != nil {
			t.Fatalf("git %v: %v %s", args, err, stderr)
		}
	}
	file := filepath.Join(dir, "a.txt")
	// Two edits far enough apart to land in separate hunks.
	base := ""
	for i := 1; i <= 40; i++ {
		base += fmt.Sprintf("line %d\n", i)
	}
	if err := writeFile(file, base); err != nil {
		t.Fatal(err)
	}
	if err := svc.Stage(dir, []string{file}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Commit(dir, "base", false, false); err != nil {
		t.Fatal(err)
	}

	edited := strings.Replace(base, "line 2\n", "CHANGED 2\n", 1)
	edited = strings.Replace(edited, "line 35\n", "CHANGED 35\n", 1)
	if err := writeFile(file, edited); err != nil {
		t.Fatal(err)
	}

	diff, err := svc.Diff(dir, file, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(diff.Hunks) != 2 {
		t.Fatalf("expected two hunks, got %d", len(diff.Hunks))
	}

	// Stage only the first hunk: the index gets one change, the tree keeps both.
	if err := svc.ApplyHunk(dir, file, 0, "stage"); err != nil {
		t.Fatalf("stage hunk: %v", err)
	}
	stagedDiff, err := svc.Diff(dir, file, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(stagedDiff.Hunks) != 1 {
		t.Fatalf("staged hunks = %d, want 1", len(stagedDiff.Hunks))
	}
	if !strings.Contains(stagedDiff.Hunks[0].Header, "-1,") && !strings.Contains(stagedDiff.Hunks[0].Header, "-2,") {
		t.Errorf("staged the wrong hunk: %q", stagedDiff.Hunks[0].Header)
	}

	// Put it back.
	if err := svc.ApplyHunk(dir, file, 0, "unstage"); err != nil {
		t.Fatalf("unstage hunk: %v", err)
	}
	after, err := svc.Diff(dir, file, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Hunks) != 0 {
		t.Fatalf("index should be clean again, got %+v", after.Hunks)
	}

	// Discard the second hunk only: the first edit survives in the file.
	if err := svc.ApplyHunk(dir, file, 1, "discard"); err != nil {
		t.Fatalf("discard hunk: %v", err)
	}
	contents, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(contents), "CHANGED 2") {
		t.Error("discarding the second hunk should leave the first edit alone")
	}
	if strings.Contains(string(contents), "CHANGED 35") {
		t.Error("the second edit should be gone")
	}
}

func TestApplyHunkRejectsUnknownMode(t *testing.T) {
	if err := New().ApplyHunk(t.TempDir(), "a.txt", 0, "nonsense"); err == nil {
		t.Fatal("expected an error for an unknown mode")
	}
}

// initRepo makes a work tree with one committed file, for the scan tests.
func initRepo(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "--initial-branch", "main"},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "Test"},
	} {
		if _, stderr, err := run(30_000_000_000, dir, args...); err != nil {
			t.Fatalf("git %v in %s: %v %s", args, dir, err, stderr)
		}
	}
}

// The shape that started this: a project folder holding several checkouts side
// by side, none of them at the top.
func TestStatusFindsEveryRepositoryUnderTheProject(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	project := t.TempDir()
	initRepo(t, filepath.Join(project, "infra"))
	initRepo(t, filepath.Join(project, "services", "api"))
	if err := os.MkdirAll(filepath.Join(project, "notes"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeFile(filepath.Join(project, "infra", "new.txt"), "x"); err != nil {
		t.Fatal(err)
	}

	out, err := New().Status(project)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if len(out.Repos) != 2 {
		t.Fatalf("expected two repositories, got %+v", out.Repos)
	}
	// Sorted, so the panel's order never jumps between refreshes.
	if out.Repos[0].Name != "infra" || out.Repos[1].Name != filepath.Join("services", "api") {
		t.Errorf("names = %q, %q", out.Repos[0].Name, out.Repos[1].Name)
	}
	if len(out.Repos[0].Files) != 1 || out.Repos[0].Files[0].Rel != "new.txt" {
		t.Errorf("infra files = %+v", out.Repos[0].Files)
	}
	if len(out.Repos[1].Files) != 0 {
		t.Errorf("services/api should be clean, got %+v", out.Repos[1].Files)
	}
	for _, repo := range out.Repos {
		if !strings.HasPrefix(repo.Root, project) {
			t.Errorf("root %q escaped the project", repo.Root)
		}
	}
}

// A checkout inside a checkout belongs to that checkout, not to the project.
func TestDiscoverReposStopsAtAWorkTree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	project := t.TempDir()
	outer := filepath.Join(project, "outer")
	initRepo(t, outer)
	initRepo(t, filepath.Join(outer, "nested"))

	found, truncated := discoverRepos(project)
	if truncated {
		t.Error("two repositories should not truncate")
	}
	if len(found) != 1 || found[0] != outer {
		t.Fatalf("found = %v, want just %q", found, outer)
	}
}

func TestDiscoverReposFindsTheProjectItself(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	project := t.TempDir()
	initRepo(t, project)

	found, _ := discoverRepos(project)
	if len(found) != 1 || found[0] != project {
		t.Fatalf("found = %v, want just the project root", found)
	}
}

func TestDiscoverReposSkipsHeavyDirectories(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	project := t.TempDir()
	initRepo(t, filepath.Join(project, "node_modules", "dep"))
	initRepo(t, filepath.Join(project, "real"))

	found, _ := discoverRepos(project)
	if len(found) != 1 || filepath.Base(found[0]) != "real" {
		t.Fatalf("found = %v, want only the real checkout", found)
	}
}

func TestDiscoverReposSkipsDotDirectories(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	project := t.TempDir()
	initRepo(t, filepath.Join(project, ".local", "hidden"))
	initRepo(t, filepath.Join(project, "visible"))

	found, _ := discoverRepos(project)
	if len(found) != 1 || filepath.Base(found[0]) != "visible" {
		t.Fatalf("found = %v, want only the visible checkout", found)
	}
}

func TestDiscoverReposRespectsDepth(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	project := t.TempDir()
	deep := filepath.Join(project, "a", "b", "c", "d", "too-deep")
	initRepo(t, deep)

	found, _ := discoverRepos(project)
	if len(found) != 0 {
		t.Fatalf("a repository %d levels down should be out of reach, got %v",
			MaxRepoDepth+2, found)
	}
}

// A repository with no commits has no HEAD. The gutter marks ask for a HEAD
// diff on every changed file, so that must degrade to the working tree rather
// than surfacing git's "ambiguous argument" as an error.
func TestDiffAgainstHeadWithoutCommits(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	if _, stderr, err := run(30_000_000_000, dir, "init", "--initial-branch", "main"); err != nil {
		t.Fatalf("git init: %v %s", err, stderr)
	}
	file := filepath.Join(dir, "a.txt")
	if err := writeFile(file, "one\n"); err != nil {
		t.Fatal(err)
	}
	res, err := New().Diff(dir, file, false, true)
	if err != nil {
		t.Fatalf("diff against a missing HEAD should fall back, got %v", err)
	}
	if !res.Untracked || len(res.Hunks) != 1 {
		t.Fatalf("res = %+v", res)
	}
}

// A clean tracked file must never come back as an all-added untracked hunk —
// that is exactly the false green gutter bars. Absolute pathspecs used to
// trip ls-files and fall into --no-index.
func TestDiffCleanTrackedIsEmpty(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	svc := New()
	for _, args := range [][]string{
		{"init", "--initial-branch", "main"},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "Test"},
	} {
		if _, stderr, err := run(30_000_000_000, dir, args...); err != nil {
			t.Fatalf("git %v: %v %s", args, err, stderr)
		}
	}
	file := filepath.Join(dir, "subdir", "clean.yaml")
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeFile(file, "a: 1\nb: 2\n"); err != nil {
		t.Fatal(err)
	}
	if err := svc.Stage(dir, []string{file}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Commit(dir, "add", false, false); err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{file, filepath.Join(dir, "subdir", "clean.yaml")} {
		res, err := svc.Diff(dir, path, false, true)
		if err != nil {
			t.Fatalf("diff %s: %v", path, err)
		}
		if res.Untracked || len(res.Hunks) != 0 {
			t.Fatalf("clean tracked file must have empty diff, got untracked=%v hunks=%d path=%s",
				res.Untracked, len(res.Hunks), path)
		}
	}
	if !svc.tracked(dir, file) {
		t.Fatal("absolute path must still count as tracked")
	}
}

func TestMissingHeadDetection(t *testing.T) {
	if !missingHead("fatal: bad revision 'HEAD'") {
		t.Fatal("expected missing HEAD")
	}
	if missingHead("fatal: ambiguous pathspec") {
		t.Fatal("pathspec errors must not look like missing HEAD")
	}
}
