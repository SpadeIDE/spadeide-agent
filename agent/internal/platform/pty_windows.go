//go:build windows

package platform

import (
	"fmt"
	"os"
	"os/exec"
)

func fallbackShell() string {
	return "powershell.exe"
}

func startPTY(cmd *exec.Cmd) (*os.File, error) {
	return nil, fmt.Errorf("windows ConPTY support planned; not implemented in MVP")
}

func setSize(f *os.File, cols, rows int) error {
	return fmt.Errorf("windows ConPTY support planned; not implemented in MVP")
}
