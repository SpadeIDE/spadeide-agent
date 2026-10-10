package main

import (
	"github.com/spadeide/spade-agent/internal/config"
	"github.com/spadeide/spade-agent/internal/control"
)

// agentContext holds a control client and, when the agent is stopped, a config store.
type agentContext struct {
	CLI   *control.Client
	Store *config.Store
	Live  bool
}

// withAgentOrStore opens the default control client. If the agent is not running,
// it also opens the on-disk config store so callers can read or persist state.
func withAgentOrStore(fn func(*agentContext) error) error {
	cli, err := control.DefaultClient()
	if err != nil {
		return err
	}
	ctx := &agentContext{CLI: cli, Live: cli.Ready()}
	if !ctx.Live {
		store, err := config.NewStore("")
		if err != nil {
			return err
		}
		ctx.Store = store
	}
	return fn(ctx)
}
