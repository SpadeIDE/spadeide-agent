//go:build !unix

package main

import "fmt"

func agentRunning() bool { return false }

func startDaemon(extra []string) error {
	return fmt.Errorf("background start is not supported on this OS — use: spadeide serve --foreground")
}

func stopDaemon() error {
	return fmt.Errorf("stop is not supported on this OS")
}
