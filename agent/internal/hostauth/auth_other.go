//go:build !linux

package hostauth

import "errors"

func Authenticate(username, password string) (*Account, error) {
	_ = username
	_ = password
	return nil, errors.New("unix login is only supported on Linux system installs")
}
