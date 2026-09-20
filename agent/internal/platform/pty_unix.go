//go:build darwin || linux

package platform

import (
	"os"
	"os/exec"

	"github.com/creack/pty"
)

func fallbackShell() string {
	for _, s := range []string{"/bin/bash", "/bin/zsh", "/bin/sh"} {
		if _, err := os.Stat(s); err == nil {
			return s
		}
	}
	return "/bin/sh"
}

func startPTY(cmd *exec.Cmd) (*os.File, error) {
	return pty.Start(cmd)
}

func setSize(f *os.File, cols, rows int) error {
	return pty.Setsize(f, &pty.Winsize{
		Rows: uint16(rows),
		Cols: uint16(cols),
	})
}
