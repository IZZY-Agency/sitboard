//go:build !unix

package procs

import "errors"

func owner(string) (int, error) { return -1, errors.New("procs: no owner here") }
