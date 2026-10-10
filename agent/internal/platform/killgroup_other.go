//go:build !darwin && !linux

package platform

import "os/exec"

// KillGroup is a no-op on platforms without Unix process groups.
func KillGroup(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}
