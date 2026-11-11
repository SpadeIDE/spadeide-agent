package server

import (
	"encoding/json"
	"errors"
	"os"

	fsaccess "github.com/spadeide/spade-agent/internal/fs"
	"github.com/spadeide/spade-agent/internal/protocol"
)

type handlerFunc func(*client, protocol.Envelope)

var handlers = map[string]handlerFunc{
	protocol.MethodHello:            (*client).hello,
	protocol.MethodPing:             (*client).ping,
	protocol.MethodHostLogin:        (*client).hostLoginRPC,
	protocol.MethodWorkspaceRoots:   (*client).workspaceRoots,
	protocol.MethodWorkspaceOpen:    (*client).workspaceOpen,
	protocol.MethodFSList:           (*client).fsList,
	protocol.MethodFSStat:           (*client).fsStat,
	protocol.MethodFSRead:           (*client).fsRead,
	protocol.MethodFSMkdir:          (*client).fsMkdir,
	protocol.MethodFSDelete:         (*client).fsDelete,
	protocol.MethodFSWatch:          (*client).fsWatch,
	protocol.MethodFSWrite:          (*client).fsWrite,
	protocol.MethodFSRename:         (*client).fsRename,
	protocol.MethodFSCopy:           (*client).fsCopyOrMove,
	protocol.MethodFSMove:           (*client).fsCopyOrMove,
	protocol.MethodFSSearch:         (*client).fsSearch,
	protocol.MethodFSGrep:           (*client).fsGrep,
	protocol.MethodFSReplaceInFiles: (*client).fsReplaceInFiles,
	protocol.MethodFSUnwatch:        (*client).fsUnwatch,
	protocol.MethodFSExtract:        (*client).fsExtract,
	protocol.MethodFSArchive:        (*client).fsArchive,
	protocol.MethodGitStatus:        (*client).gitStatus,
	protocol.MethodGitStage:         (*client).gitStageUnstageDiscard,
	protocol.MethodGitUnstage:       (*client).gitStageUnstageDiscard,
	protocol.MethodGitDiscard:       (*client).gitStageUnstageDiscard,
	protocol.MethodGitCommit:        (*client).gitCommit,
	protocol.MethodGitPush:          (*client).gitPushOrPull,
	protocol.MethodGitPull:          (*client).gitPushOrPull,
	protocol.MethodGitDiff:          (*client).gitDiff,
	protocol.MethodGitApplyHunk:     (*client).gitApplyHunk,
	protocol.MethodGitBranches:      (*client).gitBranches,
	protocol.MethodGitCheckout:      (*client).gitCheckout,
	protocol.MethodPTYCreate:        (*client).ptyCreate,
	protocol.MethodPTYInput:         (*client).ptyInput,
	protocol.MethodPTYResize:        (*client).ptyResize,
	protocol.MethodPTYClose:         (*client).ptyClose,
	protocol.MethodAIList:           (*client).aiList,
}

// testHookPanicMethod, when non-empty, makes handle panic for that method (tests).
var testHookPanicMethod string

func (c *client) handle(env protocol.Envelope) {
	if testHookPanicMethod != "" && env.Method == testHookPanicMethod {
		panic("test-induced panic")
	}
	if env.Method == "" {
		c.send(protocol.NewError(env.ID, protocol.CodeInvalidParams, "missing method"))
		return
	}
	if !c.requireBound(env) {
		return
	}
	h, ok := handlers[env.Method]
	if !ok {
		c.send(protocol.NewError(env.ID, protocol.CodeNotFound, "unknown method: "+env.Method))
		return
	}
	h(c, env)
}

// decodeParams unmarshals env.Params into T, answering invalid_params and
// reporting false when the payload doesn't fit.
func decodeParams[T any](c *client, env protocol.Envelope) (T, bool) {
	var p T
	if err := json.Unmarshal(env.Params, &p); err != nil {
		c.send(protocol.NewError(env.ID, protocol.CodeInvalidParams, err.Error()))
		return p, false
	}
	return p, true
}

// errorCode maps a filesystem error to its protocol code. Uses errors.Is on the
// fsaccess sentinels, so the mapping never depends on message text.
func errorCode(err error) string {
	switch {
	case errors.Is(err, fsaccess.ErrOutsideRoots),
		errors.Is(err, fsaccess.ErrSymlinkEscape),
		errors.Is(err, fsaccess.ErrBrowseOnly):
		return protocol.CodeForbidden
	case errors.Is(err, fsaccess.ErrInvalidPattern):
		return protocol.CodeInvalidParams
	case errors.Is(err, os.ErrNotExist):
		return protocol.CodeNotFound
	default:
		return protocol.CodeIOError
	}
}

// reply answers with result, or with the mapped error when err is non-nil.
func (c *client) reply(id string, result any, err error) {
	if err != nil {
		c.send(protocol.NewError(id, errorCode(err), err.Error()))
		return
	}
	c.send(protocol.NewResponse(id, result))
}

// ack answers with an empty result, or with err under the given code.
func (c *client) ack(id string, err error, code string) {
	if err != nil {
		c.send(protocol.NewError(id, code, err.Error()))
		return
	}
	c.send(protocol.NewResponse(id, map[string]any{}))
}

// handlePath runs fn for the `{ "path": … }` methods and answers with its result.
func (c *client) handlePath(env protocol.Envelope, fn func(string) (any, error)) {
	p, ok := decodeParams[protocol.PathParams](c, env)
	if !ok {
		return
	}
	res, err := fn(p.Path)
	c.reply(env.ID, res, err)
}
