//go:build linux

package hostauth

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// PasswdID is a hex SHA-256 of the account's shadow password hash (not the
// password). It changes when the password is changed or the account is locked.
//
// Reads /etc/shadow when possible, else `getent shadow` (NSS / LDAP-aware).
func PasswdID(username string) (string, error) {
	hash, err := readShadowHash(username)
	if err != nil {
		return "", err
	}
	return passwdIDFromHash(username, hash)
}

func readShadowHash(username string) (string, error) {
	if hash, err := readShadowFile(username); err == nil {
		return hash, nil
	}
	return readShadowGetent(username)
}

func readShadowFile(username string) (string, error) {
	f, err := os.Open("/etc/shadow")
	if err != nil {
		return "", err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if hash, ok := parseShadowHash(sc.Text(), username); ok {
			return hash, nil
		}
	}
	return "", ErrNoAccount
}

func readShadowGetent(username string) (string, error) {
	out, err := exec.Command("getent", "shadow", username).Output()
	if err != nil {
		return "", fmt.Errorf("%w: shadow entry unavailable", ErrNoAccount)
	}
	line := string(bytes.TrimSpace(out))
	if hash, ok := parseShadowHash(line, username); ok {
		return hash, nil
	}
	// Some getent implementations omit the username when keyed; accept "hash:…"
	parts := strings.SplitN(line, ":", 3)
	if len(parts) >= 1 && !strings.Contains(parts[0], "/") {
		// "user:hash:…" already handled; lone hash line is rare — try as hash field
		if len(parts) >= 2 {
			return parts[1], nil
		}
	}
	return "", ErrNoAccount
}
