// Package procs tells which folders this user's running processes work in.
// It reads only each process's parent and working folder: no arguments, no
// environment, no open files. The folders are kept in memory by the caller only.
package procs

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/IZZY-Agency/sitboard/internal/adapters/internal/lsofname"
	"github.com/IZZY-Agency/sitboard/internal/adapters/internal/runner"
	"github.com/IZZY-Agency/sitboard/internal/model"
	"github.com/IZZY-Agency/sitboard/internal/source"
)

const callTimeout = 10 * time.Second

type lister struct {
	goos    string
	uid     int
	run     runner.Func                             // nil: real processes
	cwdOf   func(pid string) (string, error)        // Linux: /proc/<pid>/cwd
	ppidOf  func(pid string) (int, error)           // Linux: from /proc/<pid>/stat
	procs   func(context.Context) ([]string, error) // Linux: the pids under /proc owned by uid
	isDir   func(path string) bool                  // nil: os.Stat
	entries func(dir string) []string               // nil: os.ReadDir's names
}

// errUnreadable: lsof named a process without a working folder it could read.
var errUnreadable = errors.New("procs: a working folder could not be read")

// errAmbiguous: lsof printed a working folder that can be read as more than
// one existing folder, or as none.
var errAmbiguous = errors.New("procs: a working folder could be read more than one way")

// maxReadings caps the readings of one printed folder alive at once, after
// those that name nothing on disk are dropped.
const maxReadings = 16

// New returns the folder-use source for this machine. It sees only this
// user's own processes: other users' are not listed.
func New() source.FolderUse {
	uid := os.Getuid()
	return &lister{goos: runtime.GOOS, uid: uid, cwdOf: procCwd, ppidOf: procPPID, procs: func(ctx context.Context) ([]string, error) {
		return pidsOwnedBy(ctx, "/proc", uid, owner)
	}}
}

func (l *lister) Procs(ctx context.Context) ([]model.Proc, error) {
	if l.goos == "linux" {
		return l.linux(ctx)
	}
	return l.lsof(ctx)
}

// lsof lists every process of this user with its parent and working folder.
// Any failure, even a partial answer, means "cannot tell": lsof -F prints a
// "p<pid>" line for each process, its "R<parent>", then its "f" and "n"
// lines, and every process must come with exactly one absolute folder.
func (l *lister) lsof(ctx context.Context) ([]model.Proc, error) {
	run := l.run
	if run == nil {
		run = runner.Exec(callTimeout, "LC_ALL=C")
	}
	out, err := run(ctx, "lsof", "-a", "-d", "cwd", "-u", strconv.Itoa(l.uid), "-F", "pRn", "-w", "-n", "-P")
	if err != nil {
		return nil, err
	}
	var procs []model.Proc
	var cur model.Proc
	waiting := false // a process named, its folder not yet
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		switch line[:min(1, len(line))] {
		case "p":
			pid, err := strconv.Atoi(line[1:])
			if waiting || err != nil || pid <= 0 {
				return nil, errUnreadable
			}
			cur, waiting = model.Proc{PID: pid}, true
		case "R":
			ppid, err := strconv.Atoi(line[1:])
			if !waiting || err != nil || ppid < 0 {
				return nil, errUnreadable
			}
			cur.PPID = ppid
		case "f":
		case "n":
			if !waiting {
				return nil, errUnreadable
			}
			dir, err := l.folder(line[1:])
			if err != nil {
				return nil, err
			}
			cur.Cwd = dir
			procs = append(procs, cur)
			waiting = false
		default:
			return nil, errors.New("procs: unexpected lsof output")
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if waiting {
		return nil, errUnreadable
	}
	return procs, nil
}

// folder reads a working folder as lsof printed it. A name with no escape
// in it is the folder. One with escapes is the one reading that names an
// existing folder, followed through the file system one name at a time;
// none or several is an error: the answer is unknown.
func (l *lister) folder(printed string) (string, error) {
	entries := l.entries
	if entries == nil {
		entries = func(dir string) []string {
			es, err := os.ReadDir(dir)
			if err != nil {
				return nil
			}
			names := make([]string, len(es))
			for i, e := range es {
				names[i] = e.Name()
			}
			return names
		}
	}
	names, ok := lsofname.Resolve(printed, maxReadings, entries)
	if !ok {
		return "", errAmbiguous
	}
	if len(names) == 0 {
		return "", errAmbiguous // no reading exists
	}
	var abs []string
	for _, n := range names {
		if filepath.IsAbs(n) {
			abs = append(abs, filepath.Clean(n))
		}
	}
	switch {
	case len(abs) == 0:
		return "", errUnreadable
	case len(names) == 1:
		return abs[0], nil
	}
	isDir := l.isDir
	if isDir == nil {
		isDir = func(p string) bool {
			fi, err := os.Stat(p)
			return err == nil && fi.IsDir()
		}
	}
	found := ""
	for _, n := range abs {
		if !isDir(n) {
			continue
		}
		if found != "" && found != n {
			return "", errAmbiguous
		}
		found = n
	}
	if found == "" {
		return "", errAmbiguous
	}
	return found, nil
}

func (l *lister) linux(ctx context.Context) ([]model.Proc, error) {
	pids, err := l.procs(ctx)
	if err != nil {
		return nil, err
	}
	var procs []model.Proc
	for _, pid := range pids {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		n, err := strconv.Atoi(pid)
		if err != nil {
			continue
		}
		d, err := l.cwdOf(pid)
		ppid := 0
		if err == nil {
			ppid, err = l.ppidOf(pid)
		}
		if os.IsNotExist(err) {
			continue // the process ended meanwhile
		}
		if err != nil {
			return nil, err
		}
		if !filepath.IsAbs(d) {
			continue
		}
		procs = append(procs, model.Proc{PID: n, PPID: ppid, Cwd: filepath.Clean(d)})
		// A folder removed while the process works in it reads "<folder>
		// (deleted)", and a live folder can have that name too: both count.
		if s, ok := strings.CutSuffix(d, " (deleted)"); ok && filepath.IsAbs(s) {
			procs = append(procs, model.Proc{PID: n, PPID: ppid, Cwd: filepath.Clean(s)})
		}
	}
	return procs, nil
}

func procCwd(pid string) (string, error) { return os.Readlink(filepath.Join("/proc", pid, "cwd")) }

// procPPID reads the parent from /proc/<pid>/stat: "pid (name) state ppid
// ...", where the name may hold spaces and parentheses.
func procPPID(pid string) (int, error) {
	b, err := os.ReadFile(filepath.Join("/proc", pid, "stat"))
	if err != nil {
		return 0, err
	}
	return parseStatPPID(string(b))
}

func parseStatPPID(stat string) (int, error) {
	i := strings.LastIndexByte(stat, ')')
	if i < 0 {
		return 0, errors.New("procs: unexpected stat")
	}
	f := strings.Fields(stat[i+1:])
	if len(f) < 2 {
		return 0, errors.New("procs: unexpected stat")
	}
	return strconv.Atoi(f[1])
}

// pidsOwnedBy lists the processes under root (/proc) that uid owns. An owner
// it cannot read makes the whole list unknown; a process that ended meanwhile
// is skipped.
func pidsOwnedBy(ctx context.Context, root string, uid int, ownerOf func(string) (int, error)) ([]string, error) {
	ents, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	var pids []string
	for _, e := range ents {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if _, err := strconv.Atoi(e.Name()); err != nil {
			continue
		}
		u, err := ownerOf(filepath.Join(root, e.Name()))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if u == uid {
			pids = append(pids, e.Name())
		}
	}
	return pids, nil
}
