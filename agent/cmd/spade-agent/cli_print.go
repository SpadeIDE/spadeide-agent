package main

import (
	"fmt"
	"os"
	"time"

	"github.com/skip2/go-qrcode"
	"github.com/spadeide/spade-agent/internal/control"
)

func printPairBoard(st *control.Status) {
	fmt.Println()
	fmt.Println("SpadeIDE — pair a tablet")
	fmt.Printf("  Host     %s (%s:%d)\n", st.Host, st.IP, st.Port)
	fmt.Printf("  Version  %s\n", st.Version)
	exp := time.Unix(st.PairSecretExpires, 0)
	fmt.Printf("  Secret   expires %s\n", exp.Format(time.Kitchen))
	fmt.Println()

	if st.PairURL != "" {
		if qr, err := qrcode.New(st.PairURL, qrcode.Medium); err == nil {
			fmt.Println(qr.ToSmallString(false))
		} else {
			fmt.Fprintf(os.Stderr, "qr: %v\n", err)
		}
		fmt.Println("URL (paste into SpadeIDE if you cannot scan):")
		fmt.Println(st.PairURL)
		fmt.Println()
	}

	fmt.Printf("Paired tablets (%d) — more than one is fine:\n", len(st.Devices))
	if len(st.Devices) == 0 {
		fmt.Println("  (none yet)")
	} else {
		for _, d := range st.Devices {
			mark := "○ "
			if d.Online {
				mark = "● "
			}
			fmt.Printf("  %s%s\n", mark, d.Name)
		}
		fmt.Println("  ● online now   ○ paired, not connected")
	}

	fmt.Println()
	fmt.Printf("Online right now: %d\n", len(st.Clients))
	for _, c := range st.Clients {
		fmt.Printf("  ● %s\n", c.Name)
	}
	if len(st.Clients) == 0 {
		fmt.Println("  (waiting for a connection)")
	}
	fmt.Println()
}

func printDeviceList(st *control.Status) {
	if len(st.Devices) == 0 {
		fmt.Println("  (none paired)")
		return
	}
	for _, d := range st.Devices {
		mark := "○"
		if d.Online {
			mark = "●"
		}
		short := d.ID
		if len(short) > 8 {
			short = short[:8]
		}
		fmt.Printf("  %s %s  %s\n", mark, d.Name, short)
	}
	fmt.Println("  ● online   ○ paired offline")
}

func onOff(v bool) string {
	if v {
		return "on"
	}
	return "off"
}

func remoteLabel(st *control.Status) string {
	label := onOff(st.RemoteAccess)
	if st.RemoteAccess {
		if st.RemoteRegistered {
			label += ", registered"
		} else {
			label += ", not registered"
		}
	}
	if st.RemoteServer != "" {
		label += " (" + st.RemoteServer + ")"
	}
	return label
}
