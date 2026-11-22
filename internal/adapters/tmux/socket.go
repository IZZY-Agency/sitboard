package tmux

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// errUnsafeSocket says a socket path from a process's environment is not a
// tmux socket this user can trust: tmux sends its client's environment to
// whatever listens there and obeys it (read or write files, run a shell), and
// tmux itself does not check what -S names.
var errUnsafeSocket = errors.New("tmux socket is not safe to connect to")

// safeSocket checks the socket path before any tmux call and returns the path
// to pass to tmux: the same path with every symlink in its folders resolved.
//
//   - The socket itself is a socket, not a symlink, and is owned by uid.
//   - The socket's own folder is owned by uid and nobody else can write to
//     it (tmux's default /tmp/tmux-<uid>/ is). Not sticky alone: in a shared
//     sticky folder (a custom "tmux -S /tmp/x.sock") another user cannot
//     remove the socket, but can take its name once its owner's server
//     removed it, and tmux trusts whatever listens there.
//   - Every folder above it is owned by uid or by root, and either nobody
//     else can write to it or it is sticky (as /tmp is), so no other user
//     can rename or replace a folder on the way.
//
// tmux then gets the resolved path, so what was checked is what it opens.
// What is left between the check and tmux's connect is a change made by this
// same user (or root): outside what sitboard defends against, and such a
// process could run tmux itself.
func safeSocket(path string, uid int) (string, error) {
	dir, name := filepath.Split(path)
	if name == "" {
		return "", errUnsafeSocket
	}
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", fmt.Errorf("%w: %w", errUnsafeSocket, err)
	}
	for d := real; ; d = filepath.Dir(d) {
		fi, err := os.Lstat(d)
		if err != nil || !dirOK(fi, uid) || d == real && !ownDirOK(fi, uid) {
			return "", errUnsafeSocket
		}
		if d == filepath.Dir(d) {
			break
		}
	}
	sock := filepath.Join(real, name)
	fi, err := os.Lstat(sock)
	if err != nil || !socketOK(fi, uid) {
		return "", errUnsafeSocket
	}
	return sock, nil
}

// socketOK: a socket (Lstat's answer, so never a symlink) owned by uid.
func socketOK(fi fs.FileInfo, uid int) bool {
	return fi.Mode().Type() == fs.ModeSocket && owner(fi) == uid
}

// dirOK: a folder (not a symlink) owned by uid or root that no other user can
// change entries in: not writable by group or others, or sticky.
func dirOK(fi fs.FileInfo, uid int) bool {
	if !fi.IsDir() {
		return false
	}
	if o := owner(fi); o != uid && o != 0 {
		return false
	}
	return fi.Mode().Perm()&0o022 == 0 || fi.Mode()&fs.ModeSticky != 0
}

// ownDirOK: the socket's own folder is uid's, and only uid can write to it.
func ownDirOK(fi fs.FileInfo, uid int) bool {
	return fi.IsDir() && owner(fi) == uid && fi.Mode().Perm()&0o022 == 0
}

// owner is the file's owner, -1 when the platform does not say.
func owner(fi fs.FileInfo) int {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return int(st.Uid)
	}
	return -1
}

// lookBin finds the program name once, as an absolute path with symlinks
// resolved: name itself when it has a slash, else the first file of that
// name in a folder of path (a PATH list) that is absolute and trusted
// (binDirOK), whose resolved file is in a trusted folder too and may be run
// by this process. So nobody else can put a program of that name in the way.
// "" when there is none. Folders above those are not checked: a machine
// where another account owns a folder on the user's PATH, or above one, is
// outside what sitboard defends against.
func lookBin(name, path string, uid int) string {
	if strings.Contains(name, "/") {
		return trustedBin(name, uid)
	}
	for _, dir := range filepath.SplitList(path) {
		if !filepath.IsAbs(dir) {
			continue
		}
		fi, err := os.Stat(dir)
		if err != nil || !binDirOK(fi, uid) {
			continue
		}
		if p := trustedBin(filepath.Join(dir, name), uid); p != "" {
			return p
		}
	}
	return ""
}

// trustedBin returns p with its symlinks resolved when the folder holding the
// resolved file is trusted (binDirOK) and this process may execute it; else
// "".
func trustedBin(p string, uid int) string {
	real, err := filepath.EvalSymlinks(p)
	if err != nil {
		return ""
	}
	if fi, err := os.Stat(filepath.Dir(real)); err != nil || !binDirOK(fi, uid) {
		return ""
	}
	if fi, err := os.Stat(real); err != nil || !fi.Mode().IsRegular() || unix.Access(real, unix.X_OK) != nil {
		return ""
	}
	return real
}

// binDirOK: a folder owned by uid or root that neither group nor others can
// write to.
func binDirOK(fi fs.FileInfo, uid int) bool {
	o := owner(fi)
	return fi.IsDir() && (o == uid || o == 0) && fi.Mode().Perm()&0o022 == 0
}
