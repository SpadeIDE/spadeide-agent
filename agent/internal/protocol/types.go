package protocol

// Shared request/response param structs.

type HelloParams struct {
	Client  string `json:"client"`
	Version string `json:"version"`
}

type HelloResult struct {
	Agent   string `json:"agent"`
	Version string `json:"version"`
	OS      string `json:"os"`
	Arch    string `json:"arch"`
	Home    string `json:"home"`
	Shell   string `json:"shell"`
	Host    string `json:"host"`
}

type PathParams struct {
	Path string `json:"path"`
}

type WorkspaceRoot struct {
	Path  string `json:"path"`
	Label string `json:"label"`
}

type WorkspaceRootsResult struct {
	Roots []WorkspaceRoot `json:"roots"`
}

type WorkspaceOpenResult struct {
	Path string `json:"path"`
	Name string `json:"name"`
}

type DirEntry struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	IsDir   bool   `json:"isDir"`
	Size    int64  `json:"size"`
	ModTime int64  `json:"modTime"` // unix ms
}

type ListResult struct {
	Entries []DirEntry `json:"entries"`
}

type ReadResult struct {
	Path     string `json:"path"`
	Content  string `json:"content"` // base64
	Encoding string `json:"encoding"`
}

type WriteParams struct {
	Path    string `json:"path"`
	Content string `json:"content"` // base64
}

type WriteResult struct {
	Path  string `json:"path"`
	Bytes int    `json:"bytes"`
}

type RenameParams struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// CopyParams copies a file or folder into toDir. If the basename already
// exists there, the agent picks `name copy.ext`, then `name copy 2.ext`.
type CopyParams struct {
	From  string `json:"from"`
	ToDir string `json:"toDir"`
}

// MoveParams is the same shape as CopyParams — cut/paste uses this.
type MoveParams = CopyParams

type WatchResult struct {
	WatchID string `json:"watchId"`
}

type UnwatchParams struct {
	WatchID string `json:"watchId"`
}

type SearchParams struct {
	Root  string `json:"root"`
	Limit int    `json:"limit,omitempty"`
}

type SearchFile struct {
	Path string `json:"path"`
	Name string `json:"name"`
	Rel  string `json:"rel"` // path relative to the search root
}

type SearchResult struct {
	Files     []SearchFile `json:"files"`
	Truncated bool         `json:"truncated"` // true if the limit was hit
}

// GrepParams is text search across files under root (VS Code Search).
type GrepParams struct {
	Root          string `json:"root"`
	Query         string `json:"query"`
	CaseSensitive bool   `json:"caseSensitive,omitempty"`
	WholeWord     bool   `json:"wholeWord,omitempty"`
	IsRegex       bool   `json:"isRegex,omitempty"`
	MaxResults    int    `json:"maxResults,omitempty"`
}

type GrepMatch struct {
	Path      string `json:"path"`
	Rel       string `json:"rel"`
	Line      int    `json:"line"`      // 1-based
	Column    int    `json:"column"`    // 1-based start
	EndColumn int    `json:"endColumn"` // 1-based end (exclusive in rune sense ≈ byte for ASCII)
	Preview   string `json:"preview"`
}

type GrepResult struct {
	Matches   []GrepMatch `json:"matches"`
	Truncated bool        `json:"truncated"`
}

// ReplaceInFilesParams replaces all matches under root (or listed paths).
type ReplaceInFilesParams struct {
	Root          string   `json:"root"`
	Query         string   `json:"query"`
	Replacement   string   `json:"replacement"`
	CaseSensitive bool     `json:"caseSensitive,omitempty"`
	WholeWord     bool     `json:"wholeWord,omitempty"`
	IsRegex       bool     `json:"isRegex,omitempty"`
	Paths         []string `json:"paths,omitempty"` // if empty, all files under root
}

type ReplaceInFilesResult struct {
	FilesChanged int `json:"filesChanged"`
	Replacements int `json:"replacements"`
}

type FSChangedParams struct {
	WatchID string `json:"watchId"`
	Path    string `json:"path"`
	Op      string `json:"op"`
}

// GitFile is one path git reports as changed. `Index` and `Work` are the two
// porcelain-v2 status characters: staged side and working-tree side, where "."
// means unchanged and "?" (work) means untracked.
type GitFile struct {
	Path  string `json:"path"`
	Rel   string `json:"rel"`
	Index string `json:"index"`
	Work  string `json:"work"`
	Orig  string `json:"orig,omitempty"` // previous path, for a rename
}

// GitRepoStatus is one repository's state.
type GitRepoStatus struct {
	Root      string    `json:"root"`
	Name      string    `json:"name"` // path relative to the workspace, or its own folder name
	Branch    string    `json:"branch,omitempty"`
	Detached  bool      `json:"detached,omitempty"`
	Upstream  string    `json:"upstream,omitempty"`
	Ahead     int       `json:"ahead"`
	Behind    int       `json:"behind"`
	Files     []GitFile `json:"files"`
	Truncated bool      `json:"truncated,omitempty"`
}

// GitStatusResult answers "what is the state of this workspace". A project can
// hold several repositories side by side — a folder of checkouts rather than
// one checkout — so every repository found under it is reported. A missing git
// and a project with no repository at all are states the client draws, not
// failures, which is why they are data here rather than errors.
type GitStatusResult struct {
	Installed bool            `json:"installed"`
	Version   string          `json:"version,omitempty"`
	Repos     []GitRepoStatus `json:"repos"`
	Truncated bool            `json:"truncated,omitempty"` // more repositories than the scan cap
}

// GitDiffLine is one row of a unified diff, already split out so the client
// renders rather than parses. `Old` and `New` are 1-based line numbers, zero
// where the line does not exist on that side.
type GitDiffLine struct {
	Kind string `json:"kind"` // "context" | "add" | "remove"
	Text string `json:"text"`
	Old  int    `json:"old,omitempty"`
	New  int    `json:"new,omitempty"`
}

type GitDiffHunk struct {
	Header string        `json:"header"`
	Lines  []GitDiffLine `json:"lines"`
}

type GitDiffResult struct {
	Path      string        `json:"path"`
	Rel       string        `json:"rel"`
	Staged    bool          `json:"staged"`
	Binary    bool          `json:"binary,omitempty"`
	Untracked bool          `json:"untracked,omitempty"`
	Hunks     []GitDiffHunk `json:"hunks"`
	Truncated bool          `json:"truncated,omitempty"`
}

type GitDiffParams struct {
	Root   string `json:"root,omitempty"`
	Path   string `json:"path"`
	Staged bool   `json:"staged,omitempty"`
	// AgainstHead compares with the last commit instead of the index, so work
	// that is already staged still counts as changed. The editor's gutter marks
	// use it; the diff view does not, because a hunk staged from a HEAD diff
	// would not line up with the index.
	AgainstHead bool `json:"againstHead,omitempty"`
}

// GitHunkParams targets one hunk of one file. The agent re-reads the diff and
// picks the hunk itself, so the client never has to build a patch — and a stale
// client can't apply one.
type GitHunkParams struct {
	Root      string `json:"root,omitempty"`
	Path      string `json:"path"`
	HunkIndex int    `json:"hunkIndex"`
	Mode      string `json:"mode"` // "stage" | "unstage" | "discard"
}

type GitBranch struct {
	Name     string `json:"name"`
	Current  bool   `json:"current"`
	Upstream string `json:"upstream,omitempty"`
}

type GitBranchesResult struct {
	Branches []GitBranch `json:"branches"`
}

type GitCheckoutParams struct {
	Root   string `json:"root,omitempty"`
	Branch string `json:"branch"`
	Create bool   `json:"create,omitempty"`
}

type GitPathsParams struct {
	Root  string   `json:"root,omitempty"`
	Paths []string `json:"paths,omitempty"`
}

type GitCommitParams struct {
	Root     string `json:"root,omitempty"`
	Message  string `json:"message"`
	StageAll bool   `json:"stageAll,omitempty"`
	Amend    bool   `json:"amend,omitempty"`
}

type GitCommitResult struct {
	Commit  string `json:"commit"`
	Subject string `json:"subject"`
}

type GitPushResult struct {
	Output string `json:"output"`
}

type PTYCreateParams struct {
	Cols int    `json:"cols"`
	Rows int    `json:"rows"`
	Cwd  string `json:"cwd,omitempty"`
}

type PTYCreateResult struct {
	PTYID string `json:"ptyId"`
}

type PTYIDParams struct {
	PTYID string `json:"ptyId"`
}

type PTYInputParams struct {
	PTYID string `json:"ptyId"`
	Data  string `json:"data"` // base64
}

type PTYResizeParams struct {
	PTYID string `json:"ptyId"`
	Cols  int    `json:"cols"`
	Rows  int    `json:"rows"`
}

type PTYDataParams struct {
	PTYID string `json:"ptyId"`
	Data  string `json:"data"` // base64
}

type PTYExitParams struct {
	PTYID string `json:"ptyId"`
	Code  int    `json:"code"`
}

// AIAgent is one host-installed AI coding CLI (KubeSpade set).
type AIAgent struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Path string `json:"path"`
}

type AIListResult struct {
	Agents []AIAgent `json:"agents"`
}

type PairRequest struct {
	Secret     string `json:"secret"`
	DeviceName string `json:"deviceName"`
	DeviceID   string `json:"deviceId"`
	DevicePub  string `json:"devicePub,omitempty"` // base64 SPKI P-256 from Secure Enclave
}

type PairResponse struct {
	Token       string `json:"token,omitempty"` // legacy unused; mTLS only
	HostName    string `json:"hostName"`
	Fingerprint string `json:"fingerprint"`
	AgentID     string `json:"agentId,omitempty"`
	AgentPub    string `json:"agentPub,omitempty"` // base64 raw Ed25519
	// RvSecret is the durable blinded-rendezvous key (base64, 32 bytes).
	// Returned once per successful pair; store in Keychain on the iPad.
	RvSecret string `json:"rvSecret,omitempty"`
}

type PairInfo struct {
	HostName    string `json:"hostName"`
	Port        int    `json:"port"`
	Fingerprint string `json:"fingerprint"`
	AgentID     string `json:"agentId,omitempty"`
	Version     string `json:"version"`
	Paired      bool   `json:"paired"` // true if this agent already has devices
}
