package server

import (
	"github.com/spadeide/spade-agent/internal/protocol"
)

func (c *client) gitStatus(env protocol.Envelope) {
	root, ok := c.gitRoot(env, "")
	if !ok {
		return
	}
	res, err := c.git.Status(root)
	c.reply(env.ID, res, err)
}

func (c *client) gitStageUnstageDiscard(env protocol.Envelope) {
	p, ok := decodeParams[protocol.GitPathsParams](c, env)
	if !ok {
		return
	}
	root, ok := c.gitRoot(env, p.Root)
	if !ok {
		return
	}
	paths, ok := c.allowWriteAll(env, p.Paths)
	if !ok {
		return
	}
	var err error
	switch env.Method {
	case protocol.MethodGitStage:
		err = c.git.Stage(root, paths)
	case protocol.MethodGitUnstage:
		err = c.git.Unstage(root, paths)
	default:
		err = c.git.Discard(root, paths)
	}
	if err == nil {
		c.ownGit(root)
	}
	c.ack(env.ID, err, protocol.CodeIOError)
}

func (c *client) gitCommit(env protocol.Envelope) {
	p, ok := decodeParams[protocol.GitCommitParams](c, env)
	if !ok {
		return
	}
	root, ok := c.gitRoot(env, p.Root)
	if !ok {
		return
	}
	res, err := c.git.Commit(root, p.Message, p.StageAll, p.Amend)
	if err == nil {
		c.ownGit(root)
	}
	c.reply(env.ID, res, err)
}

func (c *client) gitPushOrPull(env protocol.Envelope) {
	p, ok := decodeParams[protocol.GitPathsParams](c, env)
	if !ok {
		return
	}
	root, ok := c.gitRoot(env, p.Root)
	if !ok {
		return
	}
	var res *protocol.GitPushResult
	var err error
	if env.Method == protocol.MethodGitPush {
		res, err = c.git.Push(root)
	} else {
		res, err = c.git.Pull(root)
	}
	if err == nil {
		c.ownGit(root)
	}
	c.reply(env.ID, res, err)
}

func (c *client) gitDiff(env protocol.Envelope) {
	p, ok := decodeParams[protocol.GitDiffParams](c, env)
	if !ok {
		return
	}
	root, ok := c.gitRoot(env, p.Root)
	if !ok {
		return
	}
	path, ok := c.allowWriteAll(env, []string{p.Path})
	if !ok {
		return
	}
	res, err := c.git.Diff(root, path[0], p.Staged, p.AgainstHead)
	c.reply(env.ID, res, err)
}

func (c *client) gitApplyHunk(env protocol.Envelope) {
	p, ok := decodeParams[protocol.GitHunkParams](c, env)
	if !ok {
		return
	}
	root, ok := c.gitRoot(env, p.Root)
	if !ok {
		return
	}
	path, ok := c.allowWriteAll(env, []string{p.Path})
	if !ok {
		return
	}
	err := c.git.ApplyHunk(root, path[0], p.HunkIndex, p.Mode)
	if err == nil {
		c.ownGit(root)
	}
	c.ack(env.ID, err, protocol.CodeIOError)
}

func (c *client) gitBranches(env protocol.Envelope) {
	root, ok := c.gitRoot(env, "")
	if !ok {
		return
	}
	res, err := c.git.Branches(root)
	c.reply(env.ID, res, err)
}

func (c *client) gitCheckout(env protocol.Envelope) {
	p, ok := decodeParams[protocol.GitCheckoutParams](c, env)
	if !ok {
		return
	}
	root, ok := c.gitRoot(env, p.Root)
	if !ok {
		return
	}
	err := c.git.Checkout(root, p.Branch, p.Create)
	if err == nil {
		c.ownGit(root)
	}
	c.ack(env.ID, err, protocol.CodeIOError)
}

// gitRoot picks the directory git runs in. Only the opened project qualifies:
// git writes, so an arbitrary client-supplied path would step around the same
// sandbox that guards fs.write.
func (c *client) gitRoot(env protocol.Envelope, requested string) (string, bool) {
	root := requested
	if root == "" {
		root = c.getWorkspace()
	}
	if root == "" {
		c.send(protocol.NewError(env.ID, protocol.CodeForbidden, "no project is open"))
		return "", false
	}
	resolved, err := c.filesystem().AllowWrite(root)
	if err != nil {
		c.send(protocol.NewError(env.ID, errorCode(err), err.Error()))
		return "", false
	}
	return resolved, true
}

// allowWriteAll runs every path past the sandbox before git is let near them.
func (c *client) allowWriteAll(env protocol.Envelope, paths []string) ([]string, bool) {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		resolved, err := c.filesystem().AllowWrite(p)
		if err != nil {
			c.send(protocol.NewError(env.ID, errorCode(err), err.Error()))
			return nil, false
		}
		out = append(out, resolved)
	}
	return out, true
}
