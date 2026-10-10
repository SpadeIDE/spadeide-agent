//go:build linux

package hostauth

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// Authenticate checks username/password via unix_chkpwd (same stack as pam_unix,
// including yescrypt on Ubuntu). Requires euid 0. Never logs the password.
//
// Works on Ubuntu desktop/server, Debian, Fedora, etc. whenever libpam-modules
// (or equivalent) provides unix_chkpwd — the same helper used by the login screen.
func Authenticate(username, password string) (*Account, error) {
	if os.Geteuid() != 0 {
		return nil, errors.New("unix login requires a privileged (system) agent")
	}
	acct, err := LookupAccount(username)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(password) == "" {
		return nil, ErrBadPassword
	}
	bin := unixChkPwdPath()
	if bin == "" {
		return nil, fmt.Errorf("%w: unix_chkpwd not found (install libpam-modules)", ErrBadPassword)
	}
	// Second arg must be "nullok" or "nonull" — "non-null" is rejected by PAM.
	cmd := exec.Command(bin, acct.Name, "nonull")
	cmd.Stdin = strings.NewReader(password + "\x00")
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	// Empty env: avoid leaking agent context; unix_chkpwd does not need $PATH
	// when invoked with an absolute path.
	cmd.Env = []string{}
	if err := cmd.Run(); err != nil {
		return nil, ErrBadPassword
	}
	if sh := passwdShell(acct.Name); sh != "" {
		acct.Shell = sh
	}
	return acct, nil
}

func unixChkPwdPath() string {
	for _, p := range []string{
		"/sbin/unix_chkpwd",
		"/usr/sbin/unix_chkpwd",
		"/usr/libexec/unix_chkpwd", // Fedora / RHEL layout
	} {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	if p, err := exec.LookPath("unix_chkpwd"); err == nil {
		return p
	}
	return ""
}
