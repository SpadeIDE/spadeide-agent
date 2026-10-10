package server

import "github.com/spadeide/spade-agent/internal/protocol"

func (c *client) ptyCreate(env protocol.Envelope) {
	p, ok := decodeParams[protocol.PTYCreateParams](c, env)
	if !ok {
		return
	}
	cwd := p.Cwd
	if cwd == "" {
		cwd = c.getWorkspace()
	}
	if cwd == "" {
		cwd = c.sessionHome()
	}
	id, err := c.terminal().Create(p.Cols, p.Rows, cwd)
	if err != nil {
		c.send(protocol.NewError(env.ID, protocol.CodeInternal, err.Error()))
		return
	}
	c.send(protocol.NewResponse(env.ID, protocol.PTYCreateResult{PTYID: id}))
}

func (c *client) ptyInput(env protocol.Envelope) {
	p, ok := decodeParams[protocol.PTYInputParams](c, env)
	if !ok {
		return
	}
	c.ack(env.ID, c.terminal().Input(p.PTYID, p.Data), protocol.CodeIOError)
}

func (c *client) ptyResize(env protocol.Envelope) {
	p, ok := decodeParams[protocol.PTYResizeParams](c, env)
	if !ok {
		return
	}
	c.ack(env.ID, c.terminal().Resize(p.PTYID, p.Cols, p.Rows), protocol.CodeIOError)
}

func (c *client) ptyClose(env protocol.Envelope) {
	p, ok := decodeParams[protocol.PTYIDParams](c, env)
	if !ok {
		return
	}
	_ = c.terminal().Close(p.PTYID)
	c.ack(env.ID, nil, "")
}
