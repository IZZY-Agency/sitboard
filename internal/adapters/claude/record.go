package claude

import (
	"regexp"
	"strings"
	"time"

	"github.com/IZZY-Agency/sitboard/internal/model"
)

// record is one live-session file, <Home>/.claude/sessions/<pid>.json, as
// written by Claude Code (seen on 2.1.322 to 2.1.326). Unknown fields are ignored.
type record struct {
	PID             int    `json:"pid"`
	SessionID       string `json:"sessionId"`
	Cwd             string `json:"cwd"`
	Name            string `json:"name"`
	Kind            string `json:"kind"`
	Status          string `json:"status"`
	StatusUpdatedAt int64  `json:"statusUpdatedAt"` // ms since the epoch
	StartedAt       int64  `json:"startedAt"`
	UpdatedAt       int64  `json:"updatedAt"`
	ProcStart       string `json:"procStart"` // `LC_ALL=C TZ=UTC ps -o lstart= -p <pid>` at start
	Version         string `json:"version"`
}

// sessionIDRe is the shape a session id must have to be used at all. Ids made
// only of dots are refused too: an id becomes part of a path.
var sessionIDRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)

func validSessionID(id string) bool {
	return sessionIDRe.MatchString(id) && strings.Trim(id, ".") != ""
}

// parseRecord reads a session file. It returns false for a file that is not a
// usable record; one bad file never stops the others.
func parseRecord(b []byte) (record, bool) {
	var r record
	if !decode(b, &r) {
		return record{}, false
	}
	if r.PID <= 0 || !validSessionID(r.SessionID) {
		return record{}, false
	}
	if len(r.Name) > 1024 {
		r.Name = r.Name[:1024]
	}
	if len(r.Version) > 64 {
		r.Version = ""
	}
	return r, true
}

// mapStatus follows Claude Code's own reading of its status values, as its
// session list shows them: busy and shell are working (shell is set while a
// background shell task still runs, and the agent resumes by itself when it
// reports), waiting is blocked on the user, idle has finished its turn.
// Anything else is unknown.
func mapStatus(s string) model.Known[model.Status] {
	switch s {
	case "busy", "shell":
		return model.Of(model.StatusBusy)
	case "waiting":
		return model.Of(model.StatusBlocked)
	case "idle":
		return model.Of(model.StatusFinished)
	}
	return model.Unknown[model.Status]()
}

// msTime turns a millisecond timestamp into a time; 0, negative or absurd
// values are unknown.
func msTime(ms int64) time.Time {
	if ms <= 0 || ms > 4_000_000_000_000 {
		return time.Time{}
	}
	return time.UnixMilli(ms)
}
