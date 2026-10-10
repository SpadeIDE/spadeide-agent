package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/spadeide/spade-agent/internal/auth"
	"github.com/spadeide/spade-agent/internal/config"
	"github.com/spadeide/spade-agent/internal/control"
	"github.com/spadeide/spade-agent/internal/discovery"
	"github.com/spadeide/spade-agent/internal/identity"
	"github.com/spadeide/spade-agent/internal/remote"
	"github.com/spadeide/spade-agent/internal/rendezvous"
	"github.com/spadeide/spade-agent/internal/server"
)

func cmdServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	port := fs.Int("port", config.DefaultPort, "TLS listen port for iPad clients")
	controlPort := fs.Int("control-port", config.DefaultControlPort, "localhost control API port")
	foreground := fs.Bool("foreground", false, "stay attached (required under systemd)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if v := config.EnvPort(); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			*port = n
		}
	}
	if v := config.EnvControlPort(); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			*controlPort = n
		}
	}

	// Interactive `serve` without --foreground → background daemon.
	if !*foreground && !config.EnvDaemonSet() && !config.EnvUnderSystemd() {
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
	if err := refuseRoot(); err != nil {
		return err
	}
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
	if config.EnvDaemon() {
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

	unlock, err := acquireInstanceLock(store.PIDPath())
	if err != nil {
		if errors.Is(err, errAlreadyRunning) {
			log.Printf("already running (pid lock %s)", store.PIDPath())
			return nil
		}
		return err
	}
	defer unlock()

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

	offers := &remote.OfferHandler{
		Agent:       srv,
		MaxDials:    4,
		DialTimeout: 60 * time.Second,
		PunchWindow: 15 * time.Second,
	}
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
		OnOffer: offers.Handle,
	}
	ctrl.Remote = rv
	if on, srvURL, err := store.RemoteAccess(); err == nil && on {
		rv.Apply(true, srvURL)
	}

	if config.EnvMDNSDisabled() {
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
		if err != nil {
			log.Printf("listener stopped: %v", err)
		}
		return err
	}
}
