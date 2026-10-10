package platform

import (
	"bufio"
	"os"
	"os/exec"
	"os/user"
	"runtime"
	"strconv"
	"strings"
)

// DefaultShell returns the preferred login shell for this OS.
func DefaultShell() string {
	if s := strings.TrimSpace(os.Getenv("SHELL")); s != "" {
		return s
	}
	if s := lookupPasswdShell(); s != "" {
		return s
	}
	return fallbackShell()
}

// UserHome returns the real home directory for the current user.
func UserHome() string {
	if h := os.Getenv("HOME"); h != "" {
		return h
	}
	if u, err := user.Current(); err == nil && u.HomeDir != "" {
		return u.HomeDir
	}
	return ""
}

// UserName returns the login name.
func UserName() string {
	if u := os.Getenv("USER"); u != "" {
		return u
	}
	if u := os.Getenv("LOGNAME"); u != "" {
		return u
	}
	if u, err := user.Current(); err == nil {
		return u.Username
	}
	return ""
}

// ShellCommand builds an interactive login shell — same class of session as
// Terminal.app / iTerm (loads profile + rc, history, PATH from path_helper).
func ShellCommand(shell string) *exec.Cmd {
	if shell == "" {
		shell = DefaultShell()
	}
	if runtime.GOOS == "windows" {
		return exec.Command(shell)
	}
	// -l: login (zprofile/bash_profile, path_helper on macOS)
	// PTY makes the shell interactive (isatty) so zshrc/bashrc + history apply.
	return exec.Command(shell, "-l")
}

// EnrichEnv returns an environment suitable for a user terminal session.
// Starts from the process env and fills gaps typical of GUI-launched agents.
func EnrichEnv(base []string, cols, rows int) []string {
	env := map[string]string{}
	for _, e := range base {
		if i := strings.IndexByte(e, '='); i > 0 {
			env[e[:i]] = e[i+1:]
		}
	}

	if env["HOME"] == "" {
		if h := UserHome(); h != "" {
			env["HOME"] = h
		}
	}
	if env["USER"] == "" {
		if u := UserName(); u != "" {
			env["USER"] = u
		}
	}
	if env["LOGNAME"] == "" {
		env["LOGNAME"] = env["USER"]
	}
	if env["SHELL"] == "" {
		env["SHELL"] = DefaultShell()
	}
	env["TERM"] = "xterm-256color"
	if env["COLORTERM"] == "" {
		env["COLORTERM"] = "truecolor"
	}
	if env["LANG"] == "" {
		env["LANG"] = "en_US.UTF-8"
	}

	env["COLUMNS"] = strconv.Itoa(cols)
	env["LINES"] = strconv.Itoa(rows)
	env["SPADEIDE"] = "1"

	out := make([]string, 0, len(env))
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	return out
}

// OverrideEnv copies base, then applies set. Empty values delete the key.
func OverrideEnv(base []string, set map[string]string) []string {
	env := map[string]string{}
	for _, e := range base {
		if i := strings.IndexByte(e, '='); i > 0 {
			env[e[:i]] = e[i+1:]
		}
	}
	for k, v := range set {
		if v == "" {
			delete(env, k)
		} else {
			env[k] = v
		}
	}
	out := make([]string, 0, len(env))
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	return out
}

// StartPTY starts cmd attached to a PTY and returns the master file.
func StartPTY(cmd *exec.Cmd) (*os.File, error) {
	return startPTY(cmd)
}

// SetSize resizes the PTY.
func SetSize(f *os.File, cols, rows int) error {
	return setSize(f, cols, rows)
}

func lookupPasswdShell() string {
	u, err := user.Current()
	if err != nil || u.Username == "" {
		return ""
	}
	if runtime.GOOS == "darwin" {
		if s := dsclUserShell(u.Username); s != "" {
			return s
		}
	}
	f, err := os.Open("/etc/passwd")
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.Split(line, ":")
		if len(parts) >= 7 && parts[0] == u.Username {
			return parts[6]
		}
	}
	return ""
}

func dsclUserShell(username string) string {
	out, err := exec.Command("dscl", ".", "-read", "/Users/"+username, "UserShell").Output()
	if err != nil {
		return ""
	}
	// "UserShell: /bin/zsh"
	s := strings.TrimSpace(string(out))
	const p = "UserShell:"
	if i := strings.Index(s, p); i >= 0 {
		return strings.TrimSpace(s[i+len(p):])
	}
	return ""
}
