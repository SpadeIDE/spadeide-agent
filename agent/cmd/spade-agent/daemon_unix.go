//go:build unix

package main

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spadeide/spade-agent/internal/config"
	"github.com/spadeide/spade-agent/internal/control"
)

var errAlreadyRunning = fmt.Errorf("already running")

func refuseRoot() error {
	if os.Geteuid() != 0 {
		return nil
	}
	if config.IsSystemMode() {
		return nil
	}
	return fmt.Errorf("refusing to run as root — a paired iPad would get a root shell.\nIf you installed with sudo ./install.sh --system, systemd already runs the agent:\n  sudo systemctl status spade-agent\n  sudo spadeide pair --no-start\nDo not start a second copy with: sudo spadeide")
}

func controlPortBusy() bool {
	c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", config.DefaultControlPort), 300*time.Millisecond)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

func errForeignAgent() error {
	return fmt.Errorf("another SpadeIDE agent is already listening on 127.0.0.1:%d\nTalk to it with:\n  sudo systemctl status spade-agent\n  sudo spadeide pair --no-start\nDo not start a second copy as root.", config.DefaultControlPort)
}

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil
}

func readPID() (int, bool) {
	store, err := config.NewStore("")
	if err != nil {
		return 0, false
	}
	data, err := os.ReadFile(store.PIDPath())
	if err != nil {
		return 0, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, false
	}
	return pid, processAlive(pid)
}

func agentRunning() bool {
	cli, err := control.DefaultClient()
	if err == nil && cli.Ready() {
		return true
	}
	_, ok := readPID()
	return ok
}

// acquireInstanceLock exclusive-locks the pid file so systemd + `spadeide pair`
// cannot run two serves that bind-fail and RestartSec=3 crash-loop.
func acquireInstanceLock(pidPath string) (func(), error) {
	f, err := os.OpenFile(pidPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, errAlreadyRunning
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}

// startDaemon re-execs this binary in the background (new session), waits until
// the control API answers, then returns. systemd / --foreground stay attached.
func startDaemon(extra []string) error {
	if err := refuseRoot(); err != nil {
		return err
	}
	if agentRunning() {
		fmt.Println("SpadeIDE Agent is already running.")
		fmt.Println("Show QR:  spadeide")
		return nil
	}
	if controlPortBusy() {
		return errForeignAgent()
	}

	exe, err := os.Executable()
	if err != nil {
		return err
	}
	args := append([]string{"serve", "--foreground"}, extra...)
	cmd := exec.Command(exe, args...)
	cmd.Env = append(os.Environ(), "SPADE_AGENT_DAEMON=1")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	store, err := config.NewStore("")
	if err != nil {
		return err
	}
	logFile, err := os.OpenFile(store.LogPath(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.Stdin = nil

	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		return err
	}
	_ = logFile.Close()
	// Detach: don't wait on the child.
	_ = cmd.Process.Release()

	cli, err := control.DefaultClient()
	if err != nil {
		return err
	}
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if cli.Ready() {
			pid, _ := readPID()
			fmt.Printf("SpadeIDE Agent started (pid %d).\n", pid)
			fmt.Println("Listening for iPads. Show the pair QR with:")
			fmt.Println("  spadeide")
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("agent started but control API did not become ready — check %s", store.LogPath())
}

func stopDaemon() error {
	cli, err := control.DefaultClient()
	if err == nil && cli.Ready() {
		// No stop endpoint yet — signal the PID.
	}
	pid, ok := readPID()
	if !ok {
		fmt.Println("SpadeIDE Agent is not running.")
		return nil
	}
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		return err
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !processAlive(pid) {
			fmt.Printf("SpadeIDE Agent stopped (was pid %d).\n", pid)
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	_ = syscall.Kill(pid, syscall.SIGKILL)
	fmt.Printf("SpadeIDE Agent killed (pid %d).\n", pid)
	return nil
}
