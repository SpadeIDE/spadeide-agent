package main

import (
	"flag"
	"fmt"

	"github.com/spadeide/spade-agent/internal/control"
)

// cmdPair ensures the agent is up, rotates the pair secret (fresh QR for a new
// tablet), prints the QR + URL, and lists paired / online devices.
func cmdPair(args []string) error {
	fs := flag.NewFlagSet("pair", flag.ContinueOnError)
	noRotate := fs.Bool("no-rotate", false, "keep the current pair secret")
	noStart := fs.Bool("no-start", false, "do not start the agent if it is stopped")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if !agentRunning() {
		if *noStart {
			return fmt.Errorf("agent is not running — run: spadeide")
		}
		if err := startDaemon(nil); err != nil {
			return err
		}
	}

	cli, err := control.DefaultClient()
	if err != nil {
		return err
	}
	var st *control.Status
	if *noRotate {
		st, err = cli.Status()
	} else {
		st, err = cli.RotatePair()
	}
	if err != nil {
		return err
	}

	printPairBoard(st)
	return nil
}
