// Package platform isolates OS differences for the host agent.
//
// Current status:
//   - darwin: full PTY; ships only as SpadeIDE Agent.app (make mac)
//   - linux:  same unix PTY — Ubuntu/Debian .deb (desktop) and server
//     tarballs for LTS/headless (amd64 + arm64). See docs/LINUX.md.
//   - windows: stub until ConPTY — docs/plans/windows-agent.md
package platform
