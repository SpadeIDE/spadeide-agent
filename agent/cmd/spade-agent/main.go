package main

import (
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/spadeide/spade-agent/internal/config"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("spadeide: ")

	args := os.Args[1:]
	cmd := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd = args[0]
		args = args[1:]
	}

	// Bare `spadeide` on a desktop with the GUI package → the tray app
	// (KubeSpade-style). `spadeide pair` still prints the QR in a terminal.
	if cmd == "" {
		if graphicalSession() && guiInstalled() {
			cmd = "desktop"
		} else {
			cmd = "pair"
		}
	}

	var err error
	switch cmd {
	case "start":
		err = startDaemon(args)
	case "stop":
		err = stopDaemon()
	case "serve":
		err = cmdServe(args)
	case "desktop":
		err = cmdDesktop()
	case "pair", "qr":
		err = cmdPair(args)
	case "status":
		err = cmdStatus()
	case "devices":
		err = cmdDevices()
	case "unpair":
		err = cmdUnpair(args)
	case "remote":
		err = cmdRemote(args)
	case "pair-reset", "reset":
		err = cmdPairReset()
	case "version", "-v", "--version":
		fmt.Println(config.Version)
	case "help", "-h", "--help":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", cmd)
		printUsage()
		os.Exit(2)
	}
	if err != nil {
		log.Fatal(err)
	}
}

func printUsage() {
	fmt.Fprintf(os.Stderr, `spadeide — SpadeIDE host agent

  spadeide                     desktop app if installed, else QR in the terminal
  spadeide desktop             tray + window (Ubuntu / Debian .deb)
  spadeide pair                start if needed, print QR + pair URL
  spadeide status              paired / online tablets (no new QR)
  spadeide devices             list paired tablets
  spadeide unpair <id|name>    remove one paired tablet
  spadeide remote on|off       allow / deny internet reachability (default off)
  spadeide remote status       show remote-access setting
  spadeide stop                stop the background agent
  spadeide serve --foreground  stay attached (for systemd)
  spadeide reset               forget all paired devices
  spadeide version

Defaults: client port %d, control http://127.0.0.1:%d

`, config.DefaultPort, config.DefaultControlPort)
}
