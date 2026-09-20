package protocol

import "encoding/json"

// Envelope is the wire format for all messages.
type Envelope struct {
	ID     string          `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	OK     *bool           `json:"ok,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *ErrorBody      `json:"error,omitempty"`
	Event  string          `json:"event,omitempty"`
}

type ErrorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

const (
	CodeUnauthorized  = "unauthorized"
	CodeForbidden     = "forbidden"
	CodeNotFound      = "not_found"
	CodeInvalidParams = "invalid_params"
	CodeIOError       = "io_error"
	CodeInternal      = "internal"
	CodeNotPaired     = "not_paired"
	CodeBadSecret     = "bad_secret"
)

const (
	MethodHello            = "hello"
	MethodPing             = "ping"
	MethodWorkspaceRoots   = "workspace.roots"
	MethodWorkspaceOpen    = "workspace.open"
	MethodFSList           = "fs.list"
	MethodFSRead           = "fs.read"
	MethodFSWrite          = "fs.write"
	MethodFSMkdir          = "fs.mkdir"
	MethodFSDelete         = "fs.delete"
	MethodFSRename         = "fs.rename"
	MethodFSCopy           = "fs.copy"
	MethodFSMove           = "fs.move"
	MethodFSStat           = "fs.stat"
	MethodFSWatch          = "fs.watch"
	MethodFSUnwatch        = "fs.unwatch"
	MethodFSSearch         = "fs.search"
	MethodFSGrep           = "fs.grep"
	MethodFSReplaceInFiles = "fs.replaceInFiles"
	MethodGitStatus        = "git.status"
	MethodGitStage         = "git.stage"
	MethodGitUnstage       = "git.unstage"
	MethodGitDiscard       = "git.discard"
	MethodGitCommit        = "git.commit"
	MethodGitPush          = "git.push"
	MethodGitPull          = "git.pull"
	MethodGitDiff          = "git.diff"
	MethodGitBranches      = "git.branches"
	MethodGitCheckout      = "git.checkout"
	MethodGitApplyHunk     = "git.applyHunk"
	MethodPTYCreate        = "pty.create"
	MethodPTYInput         = "pty.input"
	MethodPTYResize        = "pty.resize"
	MethodPTYClose         = "pty.close"
	MethodAIList           = "ai.list"
)

const (
	EventPTYData   = "pty.data"
	EventPTYExit   = "pty.exit"
	EventFSChanged = "fs.changed"
)

func NewResponse(id string, result any) Envelope {
	ok := true
	raw, _ := json.Marshal(result)
	return Envelope{ID: id, OK: &ok, Result: raw}
}

func NewError(id, code, message string) Envelope {
	ok := false
	return Envelope{
		ID:    id,
		OK:    &ok,
		Error: &ErrorBody{Code: code, Message: message},
	}
}

func NewEvent(event string, params any) Envelope {
	raw, _ := json.Marshal(params)
	return Envelope{Event: event, Params: raw}
}
