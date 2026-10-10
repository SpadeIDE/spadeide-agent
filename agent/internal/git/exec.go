package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

type runOpts struct {
	stdin io.Reader
}

type RunOption func(*runOpts)

func withStdin(r io.Reader) RunOption {
	return func(o *runOpts) { o.stdin = r }
}

func withStdinString(s string) RunOption {
	return withStdin(strings.NewReader(s))
}

// run executes git non-interactively and returns stdout and stderr separately.
func run(timeout time.Duration, dir string, args ...string) (string, string, error) {
	return execGit(timeout, dir, args)
}

func runInput(timeout time.Duration, dir, stdin string, args ...string) (string, string, error) {
	return execGit(timeout, dir, args, withStdinString(stdin))
}

func execGit(timeout time.Duration, dir string, args []string, opts ...RunOption) (string, string, error) {
	var o runOpts
	for _, opt := range opts {
		opt(&o)
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = gitEnv()
	if o.stdin != nil {
		cmd.Stdin = o.stdin
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if ctx.Err() != nil {
		return stdout.String(), stderr.String(), fmt.Errorf("git %s timed out", args[0])
	}
	return stdout.String(), stderr.String(), err
}

// runDiff is `run` for the diff family, where exit status 1 means "there are
// differences" rather than failure.
func runDiff(timeout time.Duration, dir string, args ...string) (string, string, error) {
	stdout, stderr, err := run(timeout, dir, args...)
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return stdout, stderr, nil
	}
	return stdout, stderr, err
}

// gitEnv keeps every invocation non-interactive and predictable.
func gitEnv() []string {
	return append(os.Environ(),
		// Never block on a prompt the iPad cannot answer.
		"GIT_TERMINAL_PROMPT=0",
		// Don't fight a terminal git for the index lock while polling status.
		"GIT_OPTIONAL_LOCKS=0",
		// Stable, parseable messages regardless of the user's locale.
		"LC_ALL=C",
	)
}

// wrap turns git's exit status into the message git actually printed, which is
// far more use to the user than "exit status 128".
func wrap(err error, stderr string) error {
	if err == nil {
		return nil
	}
	msg := strings.TrimSpace(stderr)
	if msg == "" {
		return err
	}
	if line, _, ok := strings.Cut(msg, "\n"); ok && strings.HasPrefix(msg, "fatal:") {
		msg = line
	}
	return errors.New(msg)
}
