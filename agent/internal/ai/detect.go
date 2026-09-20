// Package ai detects installed AI coding CLIs on the host (KubeSpade set).
package ai

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/spadeide/spade-agent/internal/platform"
)

// Agent is one detected CLI.
type Agent struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Path string `json:"path"`
}

type def struct {
	id       string
	name     string
	binaries []string
	extras   func(home string) []string
}

var defs = []def{
	{
		id: "claude", name: "Claude Code", binaries: []string{"claude"},
		extras: func(home string) []string {
			return []string{
				filepath.Join(home, ".local", "bin", "claude"),
				filepath.Join(home, "bin", "claude"),
				"/opt/homebrew/bin/claude",
				"/usr/local/bin/claude",
				filepath.Join(home, ".claude", "local", "claude"),
			}
		},
	},
	{
		id: "gemini", name: "Gemini CLI", binaries: []string{"gemini"},
		extras: func(home string) []string {
			return []string{
				filepath.Join(home, ".local", "bin", "gemini"),
				filepath.Join(home, "bin", "gemini"),
				"/opt/homebrew/bin/gemini",
				"/usr/local/bin/gemini",
				filepath.Join(home, ".nvm", "current", "bin", "gemini"),
			}
		},
	},
	{
		id: "codex", name: "Codex CLI", binaries: []string{"codex"},
		extras: func(home string) []string {
			return []string{
				filepath.Join(home, ".local", "bin", "codex"),
				filepath.Join(home, "bin", "codex"),
				"/opt/homebrew/bin/codex",
				"/usr/local/bin/codex",
				filepath.Join(home, ".nvm", "current", "bin", "codex"),
			}
		},
	},
	{
		id: "copilot", name: "GitHub Copilot", binaries: []string{"copilot"},
		extras: func(home string) []string {
			return []string{
				filepath.Join(home, ".local", "bin", "copilot"),
				filepath.Join(home, "bin", "copilot"),
				"/opt/homebrew/bin/copilot",
				"/usr/local/bin/copilot",
				filepath.Join(home, ".nvm", "current", "bin", "copilot"),
			}
		},
	},
	{
		id: "cursor", name: "Cursor Agent", binaries: []string{"cursor-agent", "agent"},
		extras: func(home string) []string {
			return []string{
				filepath.Join(home, ".local", "bin", "cursor-agent"),
				filepath.Join(home, ".cursor", "bin", "cursor-agent"),
				filepath.Join(home, ".local", "bin", "agent"),
				filepath.Join(home, ".cursor", "bin", "agent"),
				filepath.Join(home, "bin", "cursor-agent"),
				filepath.Join(home, "bin", "agent"),
				"/opt/homebrew/bin/cursor-agent",
				"/opt/homebrew/bin/agent",
				"/usr/local/bin/cursor-agent",
				"/usr/local/bin/agent",
			}
		},
	},
}

// Detect scans PATH and known install dirs for AI coding CLIs.
// It never executes discovered binaries (defence in depth: a writable PATH
// entry must not become code execution during a passive ai.list).
func Detect() []Agent {
	home := platform.UserHome()
	pathEnv := os.Getenv("PATH")
	dirs := splitPath(pathEnv)
	var out []Agent
	for _, d := range defs {
		found := findAgent(d, home, dirs, pathEnv)
		if found != "" {
			out = append(out, Agent{ID: d.id, Name: d.name, Path: found})
		}
	}
	return out
}

func findAgent(d def, home string, dirs []string, pathEnv string) string {
	seen := map[string]bool{}
	candidates := make([]string, 0, 32)
	// Prefer hardcoded install locations over ambient PATH (F7).
	candidates = append(candidates, d.extras(home)...)
	for _, dir := range dirs {
		for _, bin := range d.binaries {
			candidates = append(candidates, filepath.Join(dir, bin))
		}
	}
	for _, c := range candidates {
		if seen[c] {
			continue
		}
		seen[c] = true
		if !isExecutable(c) {
			continue
		}
		if d.id == "cursor" && !looksLikeCursorAgent(c) {
			continue
		}
		return c
	}
	for _, bin := range d.binaries {
		if hit := which(bin, pathEnv); hit != "" {
			if d.id == "cursor" && !looksLikeCursorAgent(hit) {
				continue
			}
			return hit
		}
	}
	return ""
}

func isExecutable(path string) bool {
	st, err := os.Stat(path)
	if err != nil || st.IsDir() {
		return false
	}
	mode := st.Mode()
	return mode&0o111 != 0 || runtime.GOOS == "windows"
}

func which(name, pathEnv string) string {
	// Look up via PATH without executing the candidate binary.
	for _, dir := range splitPath(pathEnv) {
		p := filepath.Join(dir, name)
		if isExecutable(p) {
			return p
		}
	}
	return ""
}

func looksLikeCursorAgent(path string) bool {
	base := strings.ToLower(filepath.Base(path))
	if base == "cursor-agent" {
		return true
	}
	// Generic "agent" binary: only accept under a known Cursor install tree.
	if base == "agent" {
		lower := strings.ToLower(path)
		return strings.Contains(lower, string(filepath.Separator)+".cursor"+string(filepath.Separator)) ||
			strings.Contains(lower, string(filepath.Separator)+"cursor"+string(filepath.Separator))
	}
	return false
}

func splitPath(p string) []string {
	if p == "" {
		return nil
	}
	return strings.Split(p, string(os.PathListSeparator))
}
