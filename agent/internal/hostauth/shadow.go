package hostauth

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
)

var (
	ErrAccountLocked   = errors.New("account is locked")
	ErrPasswdChanged   = errors.New("unix password changed")
	ErrAccountRecycled = errors.New("unix account was replaced")
)

// passwordDisabled reports a locked / no-login shadow hash (*, !, !!, or !prefix).
func passwordDisabled(hash string) bool {
	hash = strings.TrimSpace(hash)
	if hash == "" || hash == "*" || hash == "!" || hash == "!!" {
		return true
	}
	return strings.HasPrefix(hash, "!")
}

func passwdIDFromHash(username, hash string) (string, error) {
	username = strings.TrimSpace(username)
	hash = strings.TrimSpace(hash)
	if username == "" {
		return "", ErrNoAccount
	}
	if passwordDisabled(hash) {
		return "", ErrAccountLocked
	}
	sum := sha256.Sum256([]byte(username + "\x00" + hash))
	return hex.EncodeToString(sum[:]), nil
}

// parseShadowHash returns the password hash field for username from one shadow line.
func parseShadowHash(line, username string) (hash string, ok bool) {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return "", false
	}
	parts := strings.SplitN(line, ":", 3)
	if len(parts) < 2 || parts[0] != username {
		return "", false
	}
	return parts[1], true
}
