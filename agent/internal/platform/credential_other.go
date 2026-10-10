//go:build !unix

package platform

import "os/exec"

func SetCredential(cmd *exec.Cmd, uid, gid uint32, groups []uint32) {}
