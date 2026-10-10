package server

import (
	"github.com/spadeide/spade-agent/internal/ai"
	"github.com/spadeide/spade-agent/internal/protocol"
)

func (c *client) aiList(env protocol.Envelope) {
	found := ai.Detect()
	agents := make([]protocol.AIAgent, 0, len(found))
	for _, a := range found {
		agents = append(agents, protocol.AIAgent{ID: a.ID, Name: a.Name, Path: a.Path})
	}
	c.send(protocol.NewResponse(env.ID, protocol.AIListResult{Agents: agents}))
}
