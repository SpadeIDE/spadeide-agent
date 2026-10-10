//go:build unix

package platform

import (
	"os/exec"
	"syscall"
)

// SetCredential runs cmd as uid/gid with the user's existing supplementary
// groups (same memberships a normal login would get — e.g. docker). Does not
// grant anything new. No-op if uid is 0 (never spawn a root PTY).
func SetCredential(cmd *exec.Cmd, uid, gid uint32, groups []uint32) {
	if cmd == nil || uid == 0 {
		return
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cred := &syscall.Credential{Uid: uid, Gid: gid}
	if len(groups) > 0 {
		cred.Groups = groups
	}
	cmd.SysProcAttr.Credential = cred
}
