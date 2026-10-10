// Package hostauth binds an iPad session to a Unix account (system-mode agent).
package hostauth

import (
	"errors"
	"os/user"
	"strconv"
	"strings"
)

var (
	ErrRefusedRoot = errors.New("root login is not allowed")
	ErrBadPassword = errors.New("invalid username or password")
	ErrNoAccount   = errors.New("unknown user")
	ErrNoHome      = errors.New("user has no home directory")
)

// Account is a non-root Unix user the session may run as.
type Account struct {
	Name   string
	UID    uint32
	GID    uint32
	Groups []uint32 // existing supplementary GIDs (incl. primary)
	Home   string
	Shell  string
}

// LookupAccount resolves name and rejects root / missing home.
func LookupAccount(name string) (*Account, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, ErrNoAccount
	}
	if name == "root" {
		return nil, ErrRefusedRoot
	}
	u, err := user.Lookup(name)
	if err != nil {
		return nil, ErrNoAccount
	}
	uid, err := strconv.ParseUint(u.Uid, 10, 32)
	if err != nil {
		return nil, ErrNoAccount
	}
	if uid == 0 {
		return nil, ErrRefusedRoot
	}
	gid, err := strconv.ParseUint(u.Gid, 10, 32)
	if err != nil {
		return nil, ErrNoAccount
	}
	home := strings.TrimSpace(u.HomeDir)
	if home == "" || home == "/" {
		return nil, ErrNoHome
	}
	acct := &Account{
		Name:   u.Username,
		UID:    uint32(uid),
		GID:    uint32(gid),
		Groups: SupplementaryGroups(u.Username, uint32(gid)),
		Home:   home,
		Shell:  passwdShell(u.Username),
	}
	return acct, nil
}
