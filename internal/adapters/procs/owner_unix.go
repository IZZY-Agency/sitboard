//go:build unix

package procs

import (
	"errors"
	"os"
	"syscall"
)

// owner is the uid that owns path.
func owner(path string) (int, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return -1, err
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return int(st.Uid), nil
	}
	return -1, errors.New("procs: no owner")
}
