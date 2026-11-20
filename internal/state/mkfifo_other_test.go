//go:build !unix

package state

import "errors"

func mkfifo(string) error { return errors.New("no named pipes") }
