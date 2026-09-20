package discovery

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/grandcat/zeroconf"
)

// Publisher advertises the agent on the LAN via mDNS/Bonjour.
type Publisher struct {
	mu     sync.Mutex
	mode   string // "avahi" | "zeroconf"
	server *zeroconf.Server
	cmd    *exec.Cmd
}

// Start advertises `_spadeide._tcp` with TXT fp=/ver=/name=.
//
// On Linux with avahi-daemon, a userspace Go responder fights for UDP 5353 and
// often fails from the iPad's point of view. Prefer registering through avahi
// (`avahi-publish-service`); fall back to embedded zeroconf when avahi tools
// are missing (containers, minimal images).
func Start(ctx context.Context, port int, fingerprint, hostName string) (*Publisher, error) {
	hostName = sanitizeHost(hostName)
	txt := []string{
		fmt.Sprintf("fp=%s", fingerprint),
		"ver=1",
		fmt.Sprintf("name=%s", hostName),
	}

	if p, err := startAvahi(ctx, hostName, port, txt); err == nil {
		log.Printf("mDNS: advertising via avahi (_spadeide._tcp :%d)", port)
		return p, nil
	} else if !errors.Is(err, errAvahiUnavailable) {
		log.Printf("mDNS: avahi publish failed (%v); trying embedded responder", err)
	}

	p, err := startZeroconf(ctx, hostName, port, txt)
	if err != nil {
		return nil, err
	}
	log.Printf("mDNS: advertising via embedded zeroconf (_spadeide._tcp :%d)", port)
	return p, nil
}

func sanitizeHost(hostName string) string {
	hostName = strings.TrimSpace(hostName)
	if hostName == "" {
		h, err := os.Hostname()
		if err != nil || h == "" {
			return "spade-agent"
		}
		hostName = h
	}
	// Avahi rejects some characters in the service name; keep it simple.
	hostName = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		case r == '-' || r == '_' || r == '.' || r == ' ':
			return r
		default:
			return '-'
		}
	}, hostName)
	if hostName == "" {
		return "spade-agent"
	}
	return hostName
}

var errAvahiUnavailable = errors.New("avahi-publish not found")

func lookAvahiPublish() (string, []string, error) {
	// `avahi-publish-service name type port [TXT…]`
	// Older packages expose the same binary as `avahi-publish` with `-s`.
	if path, err := exec.LookPath("avahi-publish-service"); err == nil {
		return path, nil, nil
	}
	if path, err := exec.LookPath("avahi-publish"); err == nil {
		return path, []string{"-s"}, nil
	}
	return "", nil, errAvahiUnavailable
}

func startAvahi(ctx context.Context, hostName string, port int, txt []string) (*Publisher, error) {
	bin, prefix, err := lookAvahiPublish()
	if err != nil {
		return nil, err
	}
	args := append(append([]string{}, prefix...), hostName, "_spadeide._tcp", fmt.Sprintf("%d", port))
	args = append(args, txt...)

	cmd := exec.Command(bin, args...)
	if err := cmd.Start(); err != nil {
		return nil, err
	}

	waitErr := make(chan error, 1)
	go func() { waitErr <- cmd.Wait() }()

	// avahi-publish exits immediately if the daemon refuses registration.
	select {
	case err := <-waitErr:
		if err != nil {
			return nil, fmt.Errorf("avahi-publish exited: %w", err)
		}
		return nil, errors.New("avahi-publish exited unexpectedly")
	case <-time.After(400 * time.Millisecond):
	}

	p := &Publisher{mode: "avahi", cmd: cmd}
	go func() {
		<-ctx.Done()
		p.Stop()
	}()
	go func() {
		if err := <-waitErr; err != nil && ctx.Err() == nil {
			log.Printf("mDNS: avahi-publish ended: %v", err)
		}
	}()
	return p, nil
}

func startZeroconf(ctx context.Context, hostName string, port int, txt []string) (*Publisher, error) {
	server, err := zeroconf.Register(hostName, "_spadeide._tcp", "local.", port, txt, nil)
	if err != nil {
		return nil, err
	}
	p := &Publisher{mode: "zeroconf", server: server}
	go func() {
		<-ctx.Done()
		p.Stop()
	}()
	return p, nil
}

func (p *Publisher) Stop() {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.server != nil {
		p.server.Shutdown()
		p.server = nil
	}
	if p.cmd != nil && p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
		p.cmd = nil
	}
}
