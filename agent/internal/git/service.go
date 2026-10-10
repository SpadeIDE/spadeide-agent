// Package git runs git on the host for the iPad's Source Control view.
//
// Everything shells out to the user's own git, so it sees their config,
// credential helpers and SSH keys exactly as their terminal does. Commands run
// non-interactively: git that would stop to ask for a password fails instead,
// and the client tells the user to finish the job in the terminal.
package git

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/spadeide/spade-agent/internal/protocol"
)

// MaxFiles caps a status payload. A repo mid-rebase can report tens of
// thousands of paths, which is neither useful on screen nor cheap to ship.
const MaxFiles = 2000

var (
	// ErrNotInstalled means there is no git on the host's PATH.
	ErrNotInstalled = errors.New("git is not installed on the host")
	// ErrNotARepo means the workspace is not inside a git work tree.
	ErrNotARepo = errors.New("not a git repository")
)

type Service struct{}

func New() *Service { return &Service{} }

// Status describes every repository the workspace holds.
//
// A project is often a folder of checkouts rather than one checkout, so the
// workspace is scanned rather than assumed to be a work tree itself. "No git"
// and "no repository anywhere" come back as data, not errors: both are ordinary
// states the client shows a message for.
func (s *Service) Status(root string) (*protocol.GitStatusResult, error) {
	out := &protocol.GitStatusResult{}
	if _, err := exec.LookPath("git"); err != nil {
		return out, nil
	}
	out.Installed = true
	if v, _, err := run(10*time.Second, "", "--version"); err == nil {
		out.Version = strings.TrimPrefix(strings.TrimSpace(v), "git version ")
	}

	roots, truncated := discoverRepos(root)
	out.Truncated = truncated
	if len(roots) == 0 {
		return out, nil
	}

	// Each repository is independent, and a big one can take a moment, so read
	// them side by side rather than one after another.
	statuses := make([]protocol.GitRepoStatus, len(roots))
	var wg sync.WaitGroup
	for i, repo := range roots {
		wg.Add(1)
		go func(i int, repo string) {
			defer wg.Done()
			statuses[i] = s.repoStatus(repo, root)
		}(i, repo)
	}
	wg.Wait()

	out.Repos = statuses
	return out, nil
}

// repoStatus reads one work tree. A repository that fails to report is still
// listed, with whatever was learned — one broken checkout should not blank the
// panel for the rest.
func (s *Service) repoStatus(repo, workspace string) protocol.GitRepoStatus {
	status := protocol.GitRepoStatus{Root: repo, Name: repoName(repo, workspace)}
	raw, _, err := run(30*time.Second, repo,
		"--no-optional-locks", "status", "--porcelain=v2", "--branch", "-z")
	if err != nil {
		return status
	}
	parseStatus(raw, &status)
	return status
}

// repoName labels a repository by where it sits in the project.
func repoName(repo, workspace string) string {
	if rel, err := filepath.Rel(workspace, repo); err == nil && rel != "." && !strings.HasPrefix(rel, "..") {
		return rel
	}
	return filepath.Base(repo)
}

// MaxRepoDepth bounds the search for repositories below the project, and
// MaxRepos bounds how many are reported. Both keep a deep tree of checkouts
// from turning a refresh into a filesystem crawl.
const (
	MaxRepoDepth = 3
	MaxRepos     = 40
	// discoverBudget caps a scan of a huge home directory (Home Assistant,
	// Docker bind mounts) so git.status cannot run for minutes.
	discoverBudget = 8 * time.Second
)

// skipDuringScan are directories that never hold a checkout worth listing.
var skipDuringScan = map[string]bool{
	".git": true, "node_modules": true, ".build": true, "build": true,
	"dist": true, "DerivedData": true, "vendor": true, "Pods": true,
	".venv": true, "venv": true, "__pycache__": true, ".gradle": true,
	"target": true, ".terraform": true, ".terragrunt-cache": true,
	".cache": true, ".next": true, ".nuxt": true,
	"Library": true, "Applications": true, "containers": true, "overlay2": true,
}

// skipScanDir is true for directories we should not walk into while looking
// for checkouts. Hidden folders (except a `.git` work tree itself) are skipped
// so a project opened at $HOME does not crawl `.local`, `.config`, `.ssh`.
func skipScanDir(name string) bool {
	if skipDuringScan[name] {
		return true
	}
	return strings.HasPrefix(name, ".") && name != ".git"
}

// discoverRepos finds work trees at or under root. A folder holding `.git` is a
// repository and is not descended into: its submodules belong to it, not to the
// project. Returns the roots sorted, so the panel's order never jumps.
func discoverRepos(root string) ([]string, bool) {
	var found []string
	truncated := false
	deadline := time.Now().Add(discoverBudget)

	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return nil
		}
		if time.Now().After(deadline) || len(found) >= MaxRepos {
			truncated = true
			return filepath.SkipAll
		}
		if path != root && skipScanDir(d.Name()) {
			return filepath.SkipDir
		}
		if depth(root, path) > MaxRepoDepth {
			return filepath.SkipDir
		}
		// `.git` is a directory in a normal checkout and a file in a worktree
		// or submodule; either marks the top of a work tree.
		if _, err := os.Lstat(filepath.Join(path, ".git")); err == nil {
			found = append(found, path)
			return filepath.SkipDir
		}
		return nil
	})

	sort.Strings(found)
	return found, truncated
}

func depth(root, path string) int {
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == "." {
		return 0
	}
	return strings.Count(rel, string(os.PathSeparator)) + 1
}

// Stage adds paths to the index; an empty list stages everything.
func (s *Service) Stage(root string, paths []string) error {
	args := []string{"add", "--"}
	if len(paths) == 0 {
		args = []string{"add", "--all", "--"}
	}
	_, stderr, err := run(60*time.Second, root, append(args, paths...)...)
	return wrap(err, stderr)
}

// Unstage restores the index from HEAD for paths; empty unstages everything.
func (s *Service) Unstage(root string, paths []string) error {
	args := append([]string{"restore", "--staged", "--"}, paths...)
	if len(paths) == 0 {
		args = []string{"reset", "--quiet", "HEAD", "--"}
	}
	_, stderr, err := run(60*time.Second, root, args...)
	return wrap(err, stderr)
}

// Discard throws away working-tree changes for paths. Untracked files are left
// alone: deleting something git never knew about is not ours to do silently.
func (s *Service) Discard(root string, paths []string) error {
	if len(paths) == 0 {
		return errors.New("no paths given")
	}
	_, stderr, err := run(60*time.Second, root, append([]string{"restore", "--worktree", "--"}, paths...)...)
	return wrap(err, stderr)
}

// Commit records the index. With stageAll, tracked changes are staged first,
// the way `git commit -a` does.
func (s *Service) Commit(root, message string, stageAll, amend bool) (*protocol.GitCommitResult, error) {
	if strings.TrimSpace(message) == "" && !amend {
		return nil, errors.New("commit message is empty")
	}
	args := []string{"commit", "--message", message}
	if stageAll {
		args = append(args, "--all")
	}
	if amend {
		args = append(args, "--amend")
	}
	if _, stderr, err := run(60*time.Second, root, args...); err != nil {
		return nil, wrap(err, stderr)
	}
	sha, _, _ := run(10*time.Second, root, "rev-parse", "--short", "HEAD")
	subject, _, _ := run(10*time.Second, root, "log", "-1", "--pretty=%s")
	return &protocol.GitCommitResult{
		Commit:  strings.TrimSpace(sha),
		Subject: strings.TrimSpace(subject),
	}, nil
}

// Push publishes the current branch, setting upstream when it has none.
func (s *Service) Push(root string) (*protocol.GitPushResult, error) {
	args := []string{"push"}
	if up, _, err := run(10*time.Second, root, "rev-parse", "--abbrev-ref", "@{upstream}"); err != nil || strings.TrimSpace(up) == "" {
		branch, _, err := run(10*time.Second, root, "rev-parse", "--abbrev-ref", "HEAD")
		if err != nil {
			return nil, wrap(err, "")
		}
		args = append(args, "--set-upstream", "origin", strings.TrimSpace(branch))
	}
	stdout, stderr, err := run(3*time.Minute, root, args...)
	if err != nil {
		return nil, wrap(err, stderr)
	}
	return &protocol.GitPushResult{Output: strings.TrimSpace(stdout + "\n" + stderr)}, nil
}

// Pull fast-forwards or merges, without ever opening an editor.
func (s *Service) Pull(root string) (*protocol.GitPushResult, error) {
	stdout, stderr, err := run(3*time.Minute, root, "pull", "--no-edit")
	if err != nil {
		return nil, wrap(err, stderr)
	}
	return &protocol.GitPushResult{Output: strings.TrimSpace(stdout + "\n" + stderr)}, nil
}

// MaxDiffLines caps a diff payload. Past a few thousand rows nobody is reading
// it on an iPad, and the JSON gets expensive.
const MaxDiffLines = 4000

// Diff returns the changes for one path, already split into hunks and lines.
// An untracked file has nothing to compare against, so it is diffed against
// /dev/null and comes back as one all-added hunk.
func (s *Service) Diff(root, path string, staged, againstHead bool) (*protocol.GitDiffResult, error) {
	rel := repoPath(root, path)
	out := &protocol.GitDiffResult{Path: path, Rel: rel, Staged: staged}

	args := []string{"diff", "--no-color", "--unified=3"}
	switch {
	case againstHead:
		args = append(args, "HEAD")
	case staged:
		args = append(args, "--cached")
	}
	// Always pathspec relative to root. Absolute paths make `ls-files` / some
	// diff invocations miss a tracked file, and the empty-diff fallback then
	// paints every line green via --no-index.
	args = append(args, "--", rel)

	raw, stderr, err := runDiff(60*time.Second, root, args...)
	if err != nil && againstHead && missingHead(stderr) {
		// A repository with no commits has no HEAD to compare against; the
		// working tree is the best answer there.
		return s.Diff(root, path, staged, false)
	}
	if err != nil {
		return nil, wrap(err, stderr)
	}
	if strings.TrimSpace(raw) == "" && !staged && !s.tracked(root, rel) {
		// git has never seen this file, so there is nothing to compare it
		// against: show the whole of it as new.
		raw, stderr, err = runDiff(60*time.Second, root,
			"diff", "--no-color", "--unified=3", "--no-index", "--", os.DevNull, rel)
		if err != nil {
			return nil, wrap(err, stderr)
		}
		out.Untracked = strings.TrimSpace(raw) != ""
	}
	parseDiff(raw, out)
	return out, nil
}

// repoPath is the pathspec git wants: relative to the work tree when possible.
func repoPath(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == "" || strings.HasPrefix(rel, "..") {
		return path
	}
	return filepath.ToSlash(rel)
}

// missingHead is true when git refused HEAD because the repo has no commits yet.
// Other diff failures must not fall through to --no-index (that paints a clean
// tracked file as all-added).
func missingHead(stderr string) bool {
	s := strings.ToLower(stderr)
	return strings.Contains(s, "unknown revision") ||
		strings.Contains(s, "bad revision") ||
		strings.Contains(s, "ambiguous argument 'head'") ||
		strings.Contains(s, "needed a single revision") ||
		strings.Contains(s, "does not have any commits yet")
}

// tracked reports whether the index knows this path. An empty diff alone can't
// tell the difference between "untracked" and "tracked and unchanged" — and
// getting that wrong renders a clean file as if every line of it were new.
func (s *Service) tracked(root, path string) bool {
	rel := repoPath(root, path)
	raw, _, err := run(20*time.Second, root, "ls-files", "--", rel)
	return err == nil && strings.TrimSpace(raw) != ""
}

// ApplyHunk stages, unstages, or discards a single hunk.
//
// The diff is re-read here and the hunk cut out of it, so the patch git applies
// is one git wrote moments earlier — the client only says which one it meant.
func (s *Service) ApplyHunk(root, path string, hunkIndex int, mode string) error {
	var readArgs, applyArgs []string
	switch mode {
	case "stage":
		readArgs = []string{"diff", "--no-color", "--unified=3", "--", path}
		applyArgs = []string{"apply", "--cached", "-"}
	case "unstage":
		readArgs = []string{"diff", "--cached", "--no-color", "--unified=3", "--", path}
		applyArgs = []string{"apply", "--cached", "--reverse", "-"}
	case "discard":
		readArgs = []string{"diff", "--no-color", "--unified=3", "--", path}
		applyArgs = []string{"apply", "--reverse", "-"}
	default:
		return fmt.Errorf("unknown hunk mode %q", mode)
	}

	raw, stderr, err := runDiff(60*time.Second, root, readArgs...)
	if err != nil {
		return wrap(err, stderr)
	}
	patch, err := hunkPatch(raw, hunkIndex)
	if err != nil {
		return err
	}
	_, stderr, err = runInput(60*time.Second, root, patch, applyArgs...)
	return wrap(err, stderr)
}

// Branches lists local branches, flagging the one that is checked out.
func (s *Service) Branches(root string) (*protocol.GitBranchesResult, error) {
	raw, stderr, err := run(20*time.Second, root, "for-each-ref",
		"--format=%(refname:short)\t%(upstream:short)\t%(HEAD)", "refs/heads")
	if err != nil {
		return nil, wrap(err, stderr)
	}
	out := &protocol.GitBranchesResult{}
	for _, line := range strings.Split(raw, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) < 3 {
			continue
		}
		out.Branches = append(out.Branches, protocol.GitBranch{
			Name:     parts[0],
			Upstream: parts[1],
			Current:  parts[2] == "*",
		})
	}
	return out, nil
}

// Checkout switches branches, optionally creating one from the current HEAD.
// Uncommitted work that would be overwritten makes git refuse, and that refusal
// is passed through verbatim — it is the useful part.
func (s *Service) Checkout(root, branch string, create bool) error {
	branch = strings.TrimSpace(branch)
	if branch == "" {
		return errors.New("branch name is empty")
	}
	if strings.HasPrefix(branch, "-") {
		return errors.New("branch name must not start with '-'")
	}
	args := []string{"checkout"}
	if create {
		// `checkout -b -- name` treats `--` as the branch name — put the
		// name immediately after -b. Leading dashes are rejected above.
		args = append(args, "-b", branch)
	} else {
		args = append(args, "--", branch)
	}
	_, stderr, err := run(60*time.Second, root, args...)
	return wrap(err, stderr)
}
