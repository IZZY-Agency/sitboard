package claude

import (
	"bytes"
	"slices"
	"testing"
)

// The terminal id printed by a spawn inside a script is recorded as well.
func TestSpawnInsideShellScriptRecordsTerminalID(t *testing.T) {
	id := "33333333-3333-4333-8333-333333333333"
	var b bytes.Buffer
	toolCall(&b, "t1", `bash -lc 'agtermctl session new --name worker-w --no-select; date -u'`, id+"\nTue Nov 17 12:00:00 UTC 2026\n", false)
	tl := tailOf(t, b.Bytes())
	if !slices.Equal(tl.d.SpawnedNames, []string{"worker-w"}) || !slices.Equal(tl.d.SpawnedTerminalIDs, []string{id}) {
		t.Fatalf("spawn = %v %v", tl.d.SpawnedNames, tl.d.SpawnedTerminalIDs)
	}
}

// One command can both open a pull request and spawn a session; both count.
func TestSpawnAndPRInOneCommand(t *testing.T) {
	id := "33333333-3333-4333-8333-333333333333"
	url := "https://github.com/example-org/example-repo/pull/301"
	var b bytes.Buffer
	toolCall(&b, "t1", `gh pr create --fill && agtermctl session new --name worker-m`, url+"\n"+id+"\n", false)
	tl := tailOf(t, b.Bytes())
	if len(tl.d.CreatedPRs) != 1 || tl.d.CreatedPRs[0].Number != 301 {
		t.Errorf("CreatedPRs = %+v", tl.d.CreatedPRs)
	}
	if !slices.Equal(tl.d.SpawnedNames, []string{"worker-m"}) || !slices.Equal(tl.d.SpawnedTerminalIDs, []string{id}) {
		t.Errorf("spawn = %v %v", tl.d.SpawnedNames, tl.d.SpawnedTerminalIDs)
	}
}

// A spawn in a loop with names from a variable: the names stay unknown, the
// printed terminal ids are recorded.
func TestSpawnInLoopRecordsTerminalIDs(t *testing.T) {
	u1, u2 := "33333333-3333-4333-8333-333333333333", "44444444-4444-4444-8444-444444444444"
	var b bytes.Buffer
	toolCall(&b, "t1", `cd /x; for n in a b; do agtermctl session new --name worker-$n --no-select; done; git log --oneline | head -3`, u1+"\n"+u2+"\n== main abc123\nlong output line one two three four five\n", false)
	tl := tailOf(t, b.Bytes())
	if len(tl.d.SpawnedNames) != 0 || !slices.Equal(tl.d.SpawnedTerminalIDs, []string{u1, u2}) {
		t.Fatalf("spawn = %v %v", tl.d.SpawnedNames, tl.d.SpawnedTerminalIDs)
	}
}
