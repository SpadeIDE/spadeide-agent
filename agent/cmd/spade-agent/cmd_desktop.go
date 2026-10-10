package main

import (
	"fmt"
	"os"
	"os/exec"
)

func cmdDesktop() error {
	if !graphicalSession() {
		return fmt.Errorf("no graphical session — from a terminal run: spadeide pair")
	}
	gui, err := exec.LookPath("spadeide-gui")
	if err != nil {
		return fmt.Errorf("desktop app not installed — install the Ubuntu/Debian .deb, then open SpadeIDE Agent from Activities")
	}
	cmd := exec.Command(gui)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func graphicalSession() bool {
	return os.Getenv("DISPLAY") != "" || os.Getenv("WAYLAND_DISPLAY") != ""
}

func guiInstalled() bool {
	_, err := exec.LookPath("spadeide-gui")
	return err == nil
}
