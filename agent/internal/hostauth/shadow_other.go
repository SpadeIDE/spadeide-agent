//go:build !linux

package hostauth

func PasswdID(username string) (string, error) {
	_ = username
	return "", ErrNoAccount
}
