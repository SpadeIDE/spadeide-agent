package config

import (
	"os"
	"strings"
)

// Environment variables the agent and CLI honour. Documented in the README.
const (
	EnvAgentHome        = "SPADE_AGENT_HOME"         // override config directory
	EnvAgentMode        = "SPADE_AGENT_MODE"         // "system" → /var/lib/spade-agent
	EnvAgentPort        = "SPADE_AGENT_PORT"         // WSS/QUIC listen port
	EnvAgentControlPort = "SPADE_AGENT_CONTROL_PORT" // loopback control API port
	EnvAgentDaemon      = "SPADE_AGENT_DAEMON"       // "1" when re-exec'd as daemon
	EnvDisableMDNS      = "SPADE_DISABLE_MDNS"       // "1" skips LAN discovery
	EnvControlToken     = "SPADE_CONTROL_TOKEN"      // override control.token file
	EnvRendezvousURL    = "SPADE_RENDEZVOUS_URL"     // override product rendezvous
	EnvInvocationID     = "INVOCATION_ID"            // set by systemd; with euid 0 → system mode
)

// EnvHome returns SPADE_AGENT_HOME, or empty.
func EnvHome() string { return strings.TrimSpace(os.Getenv(EnvAgentHome)) }

// EnvMode returns SPADE_AGENT_MODE, or empty.
func EnvMode() string { return strings.TrimSpace(os.Getenv(EnvAgentMode)) }

// EnvPort returns SPADE_AGENT_PORT, or empty.
func EnvPort() string { return strings.TrimSpace(os.Getenv(EnvAgentPort)) }

// EnvControlPort returns SPADE_AGENT_CONTROL_PORT, or empty.
func EnvControlPort() string { return strings.TrimSpace(os.Getenv(EnvAgentControlPort)) }

// EnvDaemon reports whether SPADE_AGENT_DAEMON is "1".
func EnvDaemon() bool { return os.Getenv(EnvAgentDaemon) == "1" }

// EnvDaemonSet reports whether SPADE_AGENT_DAEMON is set at all (any value).
func EnvDaemonSet() bool { return os.Getenv(EnvAgentDaemon) != "" }

// EnvMDNSDisabled reports whether SPADE_DISABLE_MDNS is "1".
func EnvMDNSDisabled() bool { return os.Getenv(EnvDisableMDNS) == "1" }

// EnvControlTokenValue returns SPADE_CONTROL_TOKEN, or empty.
func EnvControlTokenValue() string { return strings.TrimSpace(os.Getenv(EnvControlToken)) }

// EnvRendezvous returns SPADE_RENDEZVOUS_URL, or empty.
func EnvRendezvous() string { return strings.TrimSpace(os.Getenv(EnvRendezvousURL)) }

// EnvUnderSystemd reports whether INVOCATION_ID is set (systemd unit).
func EnvUnderSystemd() bool { return os.Getenv(EnvInvocationID) != "" }
