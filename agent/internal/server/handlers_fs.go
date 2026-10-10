package server

import (
	"log"
	"time"

	"github.com/spadeide/spade-agent/internal/protocol"
)

func (c *client) fsList(env protocol.Envelope) {
	c.handlePath(env, func(path string) (any, error) { return c.filesystem().List(path) })
}

func (c *client) fsStat(env protocol.Envelope) {
	c.handlePath(env, func(path string) (any, error) { return c.filesystem().Stat(path) })
}

func (c *client) fsRead(env protocol.Envelope) {
	c.handlePath(env, func(path string) (any, error) { return c.filesystem().Read(path) })
}

func (c *client) fsMkdir(env protocol.Envelope) {
	c.handlePath(env, func(path string) (any, error) {
		return map[string]string{"path": path}, c.filesystem().Mkdir(path)
	})
}

func (c *client) fsDelete(env protocol.Envelope) {
	c.handlePath(env, func(path string) (any, error) {
		return map[string]string{"path": path}, c.filesystem().Delete(path)
	})
}

func (c *client) fsWatch(env protocol.Envelope) {
	c.handlePath(env, func(path string) (any, error) {
		id, err := c.filesystem().Watch(path)
		return protocol.WatchResult{WatchID: id}, err
	})
}

func (c *client) fsWrite(env protocol.Envelope) {
	p, ok := decodeParams[protocol.WriteParams](c, env)
	if !ok {
		return
	}
	res, err := c.filesystem().Write(p.Path, p.Content)
	c.reply(env.ID, res, err)
}

func (c *client) fsRename(env protocol.Envelope) {
	p, ok := decodeParams[protocol.RenameParams](c, env)
	if !ok {
		return
	}
	path, err := c.filesystem().Rename(p.From, p.To)
	c.reply(env.ID, map[string]string{"path": path}, err)
}

func (c *client) fsCopyOrMove(env protocol.Envelope) {
	p, ok := decodeParams[protocol.CopyParams](c, env)
	if !ok {
		return
	}
	var path string
	var err error
	if env.Method == protocol.MethodFSMove {
		path, err = c.filesystem().Move(p.From, p.ToDir)
	} else {
		path, err = c.filesystem().Copy(p.From, p.ToDir)
	}
	c.reply(env.ID, map[string]string{"path": path}, err)
}

func (c *client) fsSearch(env protocol.Envelope) {
	p, ok := decodeParams[protocol.SearchParams](c, env)
	if !ok {
		return
	}
	root := p.Root
	if root == "" {
		root = c.getWorkspace()
	}
	res, err := c.filesystem().Search(root, p.Limit)
	c.reply(env.ID, res, err)
}

func (c *client) fsGrep(env protocol.Envelope) {
	p, ok := decodeParams[protocol.GrepParams](c, env)
	if !ok {
		return
	}
	if p.Root == "" {
		p.Root = c.getWorkspace()
	}
	start := time.Now()
	res, err := c.filesystem().Grep(p)
	if err != nil {
		log.Printf("fs.grep failed q=%q root=%s dur=%s: %v", p.Query, p.Root, time.Since(start).Round(time.Millisecond), err)
		c.reply(env.ID, nil, err)
		return
	}
	log.Printf("fs.grep q=%q root=%s matches=%d truncated=%v dur=%s",
		p.Query, p.Root, len(res.Matches), res.Truncated, time.Since(start).Round(time.Millisecond))
	c.send(protocol.NewResponse(env.ID, res))
}

func (c *client) fsReplaceInFiles(env protocol.Envelope) {
	p, ok := decodeParams[protocol.ReplaceInFilesParams](c, env)
	if !ok {
		return
	}
	if p.Root == "" {
		p.Root = c.getWorkspace()
	}
	res, err := c.filesystem().ReplaceInFiles(p)
	c.reply(env.ID, res, err)
}

func (c *client) fsUnwatch(env protocol.Envelope) {
	p, ok := decodeParams[protocol.UnwatchParams](c, env)
	if !ok {
		return
	}
	c.ack(env.ID, c.filesystem().Unwatch(p.WatchID), protocol.CodeNotFound)
}
