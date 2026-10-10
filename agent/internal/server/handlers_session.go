package server

import (
	"path/filepath"
	"runtime"

	"github.com/spadeide/spade-agent/internal/config"
	"github.com/spadeide/spade-agent/internal/protocol"
)

func (c *client) hello(env protocol.Envelope) {
	mode := "user"
	if config.IsSystemMode() {
		mode = "system"
	}
	home := c.sessionHome()
	shell := c.sessionShell()
	if mode == "system" && c.boundUser() == "" {
		home = ""
		shell = ""
	}
	c.send(protocol.NewResponse(env.ID, protocol.HelloResult{
		Agent:    config.AppName,
		Version:  config.Version,
		OS:       runtime.GOOS,
		Arch:     runtime.GOARCH,
		Home:     home,
		Shell:    shell,
		Host:     mustHostname(),
		Mode:     mode,
		UnixUser: c.boundUser(),
	}))
}

func (c *client) ping(env protocol.Envelope) {
	c.send(protocol.NewResponse(env.ID, map[string]bool{"pong": true}))
}

func (c *client) hostLoginRPC(env protocol.Envelope) {
	if c.srv != nil {
		c.srv.hostLogin(c, env)
		return
	}
	c.send(protocol.NewError(env.ID, protocol.CodeInternal, "login unavailable"))
}

func (c *client) workspaceRoots(env protocol.Envelope) {
	c.send(protocol.NewResponse(env.ID, protocol.WorkspaceRootsResult{Roots: c.filesystem().Roots()}))
}

func (c *client) workspaceOpen(env protocol.Envelope) {
	p, ok := decodeParams[protocol.PathParams](c, env)
	if !ok {
		return
	}
	fs := c.filesystem()
	if err := fs.AddRoot(p.Path); err != nil {
		c.reply(env.ID, nil, err)
		return
	}
	// iPad never sends fs.unwatch; drop prior project watches here.
	fs.ClearWatches()
	// Report the path as the agent resolved it, not as the client spelled it.
	opened := p.Path
	if st, err := fs.Stat(p.Path); err == nil {
		opened = st.Path
	}
	c.setWorkspace(opened)
	c.send(protocol.NewResponse(env.ID, protocol.WorkspaceOpenResult{
		Path: opened,
		Name: filepath.Base(opened),
	}))
}
