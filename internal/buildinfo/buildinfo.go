// Package buildinfo carries the version and commit, set by linker flags:
//
//	-X github.com/IZZY-Agency/sitboard/internal/buildinfo.Version=...
//	-X github.com/IZZY-Agency/sitboard/internal/buildinfo.Commit=...
package buildinfo

var (
	Version = "dev"
	Commit  = "unknown"
)
