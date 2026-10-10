//go:build darwin || linux

package platform

import (
	"os/exec"
	"syscall"
	"time"
)

// KillGroup signals the process group of a Setsid PTY session.
// SIGHUP is synchronous; SIGKILL is deferred so Close/CloseAll stay off the
// hot path (do not Wait — the caller's waitLoop owns that).
func KillGroup(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	pid := cmd.Process.Pid
	_ = syscall.Kill(-pid, syscall.SIGHUP)
	go func() {
		time.Sleep(500 * time.Millisecond)
		_ = syscall.Kill(-pid, syscall.SIGKILL)
	}()
	return nil
}
