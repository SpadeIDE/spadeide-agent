package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/skip2/go-qrcode"
	"github.com/spadeide/spade-agent/internal/auth"
	"github.com/spadeide/spade-agent/internal/config"
	"github.com/spadeide/spade-agent/internal/control"
	"github.com/spadeide/spade-agent/internal/discovery"
	"github.com/spadeide/spade-agent/internal/identity"
	"github.com/spadeide/spade-agent/internal/rendezvous"
	"github.com/spadeide/spade-agent/internal/server"
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

	// Bare `spadeide` → ensure agent is up, show QR + URL + connections.
	if cmd == "" {
		cmd = "pair"
	}

	var err error
	switch cmd {
	case "start":
		err = startDaemon(args)
	case "stop":
		err = stopDaemon()
	case "serve":
		err = cmdServe(args)
	case "pair", "qr":
		err = cmdPair(args)
	case "status":
		err = cmdStatus(args)
	case "devices":
		err = cmdDevices(args)
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

  spadeide                     start if needed, show QR + pair URL
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

func cmdServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	port := fs.Int("port", config.DefaultPort, "TLS listen port for iPad clients")
	controlPort := fs.Int("control-port", config.DefaultControlPort, "localhost control API port")
	foreground := fs.Bool("foreground", false, "stay attached (required under systemd)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if v := os.Getenv("SPADE_AGENT_PORT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			*port = n
		}
	}
	if v := os.Getenv("SPADE_AGENT_CONTROL_PORT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			*controlPort = n
		}
	}

	// Interactive `serve` without --foreground → background daemon.
	if !*foreground && os.Getenv("SPADE_AGENT_DAEMON") == "" && os.Getenv("INVOCATION_ID") == "" {
		rest := []string{}
		if *port != config.DefaultPort {
			rest = append(rest, "--port", strconv.Itoa(*port))
		}
		if *controlPort != config.DefaultControlPort {
			rest = append(rest, "--control-port", strconv.Itoa(*controlPort))
		}
		return startDaemon(rest)
	}

	return runServe(*port, *controlPort)
}

func runServe(port, controlPort int) error {
	store, err := config.NewStore("")
	if err != nil {
		return err
	}

	logFile, err := os.OpenFile(store.LogPath(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer logFile.Close()
	// Daemon: logs only to file. Foreground (systemd / debug): file + stderr.
	if os.Getenv("SPADE_AGENT_DAEMON") == "1" {
		log.SetOutput(logFile)
	} else {
		log.SetOutput(io.MultiWriter(os.Stderr, logFile))
	}

	authMgr, err := auth.NewManager(store)
	if err != nil {
		return err
	}
	id, err := identity.LoadOrCreate(store.Dir)
	if err != nil {
		return err
	}
	log.Printf("agentId %s", id.ID)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	if err := store.WritePID(os.Getpid()); err != nil {
		log.Printf("pid file warning: %v", err)
	}
	defer store.ClearPID()
	_ = store.WriteControlPort(controlPort)

	srv := server.New(port, store, authMgr, id)
	ctrl := control.New(controlPort, store, authMgr, srv)
	if _, err := ctrl.EnsureToken(); err != nil {
		return fmt.Errorf("control token: %w", err)
	}

	offerSem := make(chan struct{}, 4)
	rv := &rendezvous.Dialer{
		Identity:           id,
		MaxOffersPerMinute: 12,
		RvSecret: func() []byte {
			raw, err := store.EnsureRvSecret()
			if err != nil {
				return nil
			}
			return raw
		},
		OnOffer: func(_ context.Context, sid, ticket, baseURL, mode string, punch net.PacketConn) {
			select {
			case offerSem <- struct{}{}:
				defer func() { <-offerSem }()
			default:
				log.Printf("rendezvous: dropping offer sid=%s (too many in flight)", sid)
				if punch != nil {
					_ = punch.Close()
				}
				return
			}
			offerCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			if punch != nil {
				pctx, pcancel := context.WithTimeout(context.Background(), 15*time.Second)
				go func(pc net.PacketConn) {
					defer pcancel()
					srv.ServePunchQUIC(pctx, pc)
				}(punch)
			}
			if mode == "punch" {
				log.Printf("rendezvous: punch-only offer sid=%s (no relay dial)", sid)
				return
			}
			log.Printf("rendezvous: offer sid=%s — dialing relay", sid)
			ws, err := rendezvous.DialRelay(offerCtx, baseURL, ticket)
			if err != nil {
				log.Printf("rendezvous: relay dial failed: %v", err)
				return
			}
			srv.ServeRelaySession(rendezvous.WrapRelay(ws))
		},
	}
	ctrl.Remote = rv
	if on, srvURL, err := store.RemoteAccess(); err == nil && on {
		rv.Apply(true, srvURL)
	}

	if os.Getenv("SPADE_DISABLE_MDNS") == "1" {
		log.Printf("mDNS: disabled (advertised by host app)")
	} else if _, err := discovery.Start(ctx, port, authMgr.Fingerprint(), srv.Host); err != nil {
		log.Printf("mDNS warning: %v (continuing without discovery)", err)
	}

	log.Printf("listening on %s:%d (control 127.0.0.1:%d)", srv.LocalIP, srv.Port, controlPort)
	log.Printf("pair URL: %s", srv.PairURL())

	// Pair secrets expire by TTL (10 min) and after a successful pair / explicit
	// rotate. Do not background-rotate — that invalidated the on-screen QR mid-scan.

	errCh := make(chan error, 2)
	go func() { errCh <- ctrl.ListenAndServe(ctx) }()
	go func() { errCh <- srv.ListenAndServe(ctx) }()

	select {
	case <-ctx.Done():
		return nil
	case err := <-errCh:
		cancel()
		return err
	}
}

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

func cmdStatus(args []string) error {
	_ = args
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

func cmdDevices(args []string) error {
	_ = args
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
	// Dedup if both id and name matched
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

func cmdRemote(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: spadeide remote on|off|status [--server wss://…]")
	}
	cli, err := control.DefaultClient()
	if err != nil {
		return err
	}

	switch args[0] {
	case "status":
		if !cli.Ready() {
			// Fall back to state file when agent is stopped.
			store, err := config.NewStore("")
			if err != nil {
				return err
			}
			on, srv, err := store.RemoteAccess()
			if err != nil {
				return err
			}
			fmt.Printf("Remote access %s (agent stopped)\n", onOff(on))
			if srv != "" {
				fmt.Printf("Server        %s\n", srv)
			}
			return nil
		}
		st, err := cli.Status()
		if err != nil {
			return err
		}
		fmt.Printf("Remote access %s\n", remoteLabel(st))
		return nil
	case "on", "off":
		enabled := args[0] == "on"
		server := ""
		rest := args[1:]
		for i := 0; i < len(rest); i++ {
			if rest[i] == "--server" && i+1 < len(rest) {
				server = rest[i+1]
				i++
			}
		}
		if !cli.Ready() {
			store, err := config.NewStore("")
			if err != nil {
				return err
			}
			if err := store.SetRemoteAccess(enabled, server); err != nil {
				return err
			}
			fmt.Printf("Remote access %s (saved; start agent to apply)\n", onOff(enabled))
			return nil
		}
		st, err := cli.SetRemote(enabled, server)
		if err != nil {
			return err
		}
		fmt.Printf("Remote access %s\n", remoteLabel(st))
		return nil
	default:
		return fmt.Errorf("usage: spadeide remote on|off|status")
	}
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

func cmdPairReset() error {
	cli, err := control.DefaultClient()
	if err != nil {
		return err
	}
	if cli.Ready() {
		if _, err := cli.ResetPair(); err != nil {
			return err
		}
		fmt.Println("All paired devices removed (agent was running).")
		return nil
	}
	store, err := config.NewStore("")
	if err != nil {
		return err
	}
	if err := store.ResetPairing(); err != nil {
		return err
	}
	fmt.Println("All paired devices removed.")
	return nil
}
