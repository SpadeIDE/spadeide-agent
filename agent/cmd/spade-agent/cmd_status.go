package main

import (
	"fmt"

	"github.com/spadeide/spade-agent/internal/control"
)

func cmdStatus() error {
	cli, err := control.DefaultClient()
	if err != nil {
		return err
	}
	if !cli.Ready() {
		fmt.Println("SpadeIDE Agent is not running.")
		fmt.Println("Start / pair:  spadeide")
		return nil
	}
	st, err := cli.Status()
	if err != nil {
		return err
	}
	fmt.Printf("Running       yes (%s:%d)\n", st.IP, st.Port)
	fmt.Printf("Host          %s\n", st.Host)
	fmt.Printf("Fingerprint   %s\n", st.Fingerprint)
	if st.AgentID != "" {
		fmt.Printf("Agent ID      %s\n", st.AgentID)
	}
	fmt.Printf("Config        %s\n", st.ConfigDir)
	fmt.Printf("Remote access %s\n", remoteLabel(st))
	fmt.Printf("Paired        %d   Online %d\n", len(st.Devices), len(st.Clients))
	printDeviceList(st)
	return nil
}

func cmdDevices() error {
	cli, err := control.DefaultClient()
	if err != nil {
		return err
	}
	if !cli.Ready() {
		return fmt.Errorf("agent is not running — run: spadeide")
	}
	st, err := cli.Status()
	if err != nil {
		return err
	}
	printDeviceList(st)
	return nil
}
