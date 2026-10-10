package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/spadeide/spade-agent/internal/control"
)

func cmdRemote(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: spadeide remote on|off|status [--server wss://…]")
	}
	sub := args[0]
	fs := flag.NewFlagSet("remote", flag.ContinueOnError)
	server := fs.String("server", "", "rendezvous server URL (wss://…)")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}

	switch sub {
	case "status":
		return withAgentOrStore(func(a *agentContext) error {
			if !a.Live {
				on, srv, err := a.Store.RemoteAccess()
				if err != nil {
					return err
				}
				fmt.Printf("Remote access %s (agent stopped)\n", onOff(on))
				if srv != "" {
					fmt.Printf("Server        %s\n", srv)
				}
				return nil
			}
			st, err := a.CLI.Status()
			if err != nil {
				return err
			}
			fmt.Printf("Remote access %s\n", remoteLabel(st))
			return nil
		})
	case "on", "off":
		enabled := sub == "on"
		return withAgentOrStore(func(a *agentContext) error {
			if !a.Live {
				if err := a.Store.SetRemoteAccess(enabled, *server); err != nil {
					return err
				}
				fmt.Printf("Remote access %s (saved; start agent to apply)\n", onOff(enabled))
				return nil
			}
			st, err := a.CLI.SetRemote(enabled, *server)
			if err != nil {
				return err
			}
			fmt.Printf("Remote access %s\n", remoteLabel(st))
			return nil
		})
	default:
		return fmt.Errorf("usage: spadeide remote on|off|status")
	}
}

func cmdPairReset() error {
	return withAgentOrStore(func(a *agentContext) error {
		if a.Live {
			if _, err := a.CLI.ResetPair(); err != nil {
				return err
			}
			fmt.Println("All paired devices removed (agent was running).")
			return nil
		}
		if err := a.Store.ResetPairing(); err != nil {
			return err
		}
		fmt.Println("All paired devices removed.")
		return nil
	})
}

func cmdUnpair(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: spadeide unpair <id|name-prefix>")
	}
	query := args[0]
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
	matches := matchDevices(st.Devices, query)
	if len(matches) == 0 {
		return fmt.Errorf("no device matches %q", query)
	}
	if len(matches) > 1 {
		fmt.Fprintf(os.Stderr, "ambiguous %q — matches:\n", query)
		for _, d := range matches {
			fmt.Fprintf(os.Stderr, "  %s  %s\n", d.Name, d.ID)
		}
		return fmt.Errorf("refine the id or name")
	}
	st, err = cli.UnpairDevice(matches[0].ID)
	if err != nil {
		return err
	}
	fmt.Printf("Removed %s (%s)\n", matches[0].Name, matches[0].ID)
	printDeviceList(st)
	return nil
}

func matchDevices(devs []control.DeviceInfo, query string) []control.DeviceInfo {
	q := strings.ToLower(query)
	var out []control.DeviceInfo
	for _, d := range devs {
		if strings.EqualFold(d.ID, query) || strings.HasPrefix(strings.ToLower(d.ID), q) {
			out = append(out, d)
			continue
		}
		if strings.Contains(strings.ToLower(d.Name), q) {
			out = append(out, d)
		}
	}
	seen := map[string]bool{}
	uniq := out[:0]
	for _, d := range out {
		if seen[d.ID] {
			continue
		}
		seen[d.ID] = true
		uniq = append(uniq, d)
	}
	return uniq
}
