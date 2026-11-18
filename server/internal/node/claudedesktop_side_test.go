package node

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// A side session of the desktop app (spawnedFrom.sessionId names the local_<id> of the session that started it) is
// drawn under that session (#5935): its row names the parent's transcript in parent_agent_id while both are listed, and
// stays a row of its own. A parent that is archived, missing or not listed, an entry without the id or of another
// shape, a loop, and an entry the user moved to the top level (lineageDetached, #8522) link nothing.
func TestSideSessionsNameTheSessionThatStartedThem(t *testing.T) {
	const (
		parent   = "eeeeeeee-0000-4000-8000-000000000001"
		side     = "eeeeeeee-0000-4000-8000-000000000002" // a /btw side chat of parent
		grand    = "eeeeeeee-0000-4000-8000-000000000003" // a side chat of side: names side, the web nests it under parent
		orphan   = "eeeeeeee-0000-4000-8000-000000000004" // its parent is not in the index
		gone     = "eeeeeeee-0000-4000-8000-000000000005" // archived: not listed
		ofGone   = "eeeeeeee-0000-4000-8000-000000000006" // started from gone
		noID     = "eeeeeeee-0000-4000-8000-000000000007" // an older app's spawnedFrom, without sessionId
		odd      = "eeeeeeee-0000-4000-8000-000000000008" // spawnedFrom of another shape: the entry is still read
		loopA    = "eeeeeeee-0000-4000-8000-000000000009"
		loopB    = "eeeeeeee-0000-4000-8000-00000000000a"
		self     = "eeeeeeee-0000-4000-8000-00000000000b" // started from itself
		unlisted = "eeeeeeee-0000-4000-8000-00000000000c" // started from parentOff, whose transcript is not on this machine
		detached = "eeeeeeee-0000-4000-8000-00000000000d" // started from parent, then moved to the top level: still names it
		kept     = "eeeeeeee-0000-4000-8000-00000000000e" // lineageDetached false: still under parent
		ofDetach = "eeeeeeee-0000-4000-8000-00000000000f" // a side chat of detached: under detached, which is a top row now
	)
	n, home := testNode(t)
	n.metas, n.bgScans = map[string]quickMeta{}, map[string]*bgScan{}
	dir := filepath.Join(home, ".claude", "projects", "-src-app")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for i, id := range []string{parent, side, grand, orphan, gone, ofGone, noID, odd, loopA, loopB, self, unlisted, detached, kept, ofDetach} {
		writeEntrypointTranscript(t, dir, id, "claude-desktop", time.Duration(i+1)*time.Minute)
	}
	_, third := useDesktopDirs(t)
	from := func(local string) string {
		return `,"spawnedFrom":{"sessionId":"local_` + local + `","taskId":"task_1","sideSession":true,"userFork":true,"btw":true}`
	}
	writeDesktopEntry(t, third, parent, parent, false, nil, "")
	writeDesktopEntry(t, third, side, side, false, nil, from(parent))
	writeDesktopEntry(t, third, grand, grand, false, nil, from(side))
	writeDesktopEntry(t, third, orphan, orphan, false, nil, from("ffffffff-0000-4000-8000-000000000001"))
	writeDesktopEntry(t, third, gone, gone, true, nil, "")
	writeDesktopEntry(t, third, ofGone, ofGone, false, nil, from(gone))
	writeDesktopEntry(t, third, noID, noID, false, nil, `,"spawnedFrom":{"btw":true,"sideSession":true,"userFork":true}`)
	writeDesktopEntry(t, third, odd, odd, false, nil, `,"spawnedFrom":"local_`+parent+`"`)
	writeDesktopEntry(t, third, loopA, loopA, false, nil, from(loopB))
	writeDesktopEntry(t, third, loopB, loopB, false, nil, from(loopA))
	writeDesktopEntry(t, third, self, self, false, nil, from(self))
	const parentOff = "ffffffff-0000-4000-8000-000000000002"
	writeDesktopEntry(t, third, parentOff, parentOff, false, nil, "") // named by the index, no transcript here
	writeDesktopEntry(t, third, unlisted, unlisted, false, nil, from(parentOff))
	writeDesktopEntry(t, third, detached, detached, false, nil, from(parent)+`,"lineageDetached":true,"lineageDetachedBy":"user"`)
	writeDesktopEntry(t, third, kept, kept, false, nil, from(parent)+`,"lineageDetached":false`)
	writeDesktopEntry(t, third, ofDetach, ofDetach, false, nil, from(detached))

	want := map[string]string{parent: "", side: parent, grand: side, orphan: "", ofGone: "", noID: "", odd: "", loopA: "",
		loopB: "", self: "", unlisted: "", detached: "", kept: parent, ofDetach: detached}
	for round := range 2 { // the second list comes from the cached scan: the link is set again, on the row's own copy
		rows := map[string]proto.Session{}
		for _, s := range n.sessionList(context.Background(), false) {
			rows[s.SessionID] = s
		}
		if _, ok := rows[gone]; ok {
			t.Fatalf("round %d: the archived session is listed", round)
		}
		for id, p := range want {
			s, ok := rows[id]
			if !ok {
				t.Errorf("round %d: %s is not listed", round, id)
				continue
			}
			if got := s.ParentAgentID(); got != p {
				t.Errorf("round %d: %s parent_agent_id = %q, want %q", round, id, got, p)
			}
			if s.IsChild() || s.IsSubtask() {
				t.Errorf("round %d: %s became a child: a side session stays a session of its own", round, id)
			}
		}
	}
	// The cached scan's rows never carry the link: it is set on each list's copies.
	offlineCache.Lock()
	defer offlineCache.Unlock()
	for _, s := range offlineCache.list {
		if s.ParentAgentID() != "" {
			t.Errorf("the cached row %s carries parent_agent_id %q", s.SessionID, s.ParentAgentID())
		}
	}
}

// A fork made with the desktop app's Fork names the session it came from only in forkedFromSessionId; the app's sidebar
// nests a session under spawnedFrom.sessionId, else forkedFromSessionId, and so does the list (#9376): the fork's row
// names that session's transcript in parent_agent_id, as a side session's does. spawnedFrom wins where both are written;
// a fork moved to the top level (lineageDetached), one whose parent is not in the index or is archived, and a
// forkedFromSessionId of another shape link nothing.
func TestDesktopForksNestUnderTheSessionTheyCameFrom(t *testing.T) {
	const (
		parent   = "f0f0f0f0-0000-4000-8000-000000000001"
		fork     = "f0f0f0f0-0000-4000-8000-000000000002" // forked from parent with the app's Fork
		forkFork = "f0f0f0f0-0000-4000-8000-000000000003" // forked from fork: under fork
		other    = "f0f0f0f0-0000-4000-8000-000000000004"
		both     = "f0f0f0f0-0000-4000-8000-000000000005" // spawnedFrom names other, forkedFromSessionId parent: under other
		split    = "f0f0f0f0-0000-4000-8000-000000000006" // split off with start_session: both name parent
		older    = "f0f0f0f0-0000-4000-8000-000000000007" // spawnedFrom without sessionId: forkedFromSessionId names parent
		detached = "f0f0f0f0-0000-4000-8000-000000000008" // forked from parent, then moved to the top level
		orphan   = "f0f0f0f0-0000-4000-8000-000000000009" // forked from a session not in the index
		gone     = "f0f0f0f0-0000-4000-8000-00000000000a" // archived: not listed
		ofGone   = "f0f0f0f0-0000-4000-8000-00000000000b" // forked from gone
		odd      = "f0f0f0f0-0000-4000-8000-00000000000c" // forkedFromSessionId of another shape: the entry is still read
	)
	n, home := testNode(t)
	n.metas, n.bgScans = map[string]quickMeta{}, map[string]*bgScan{}
	dir := filepath.Join(home, ".claude", "projects", "-src-app")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for i, id := range []string{parent, fork, forkFork, other, both, split, older, detached, orphan, gone, ofGone, odd} {
		writeEntrypointTranscript(t, dir, id, "claude-desktop", time.Duration(i+1)*time.Minute)
	}
	_, third := useDesktopDirs(t)
	forkedFrom := func(local string) string {
		return `,"forkedFromSessionId":"local_` + local + `","forkedAtMessageUuid":"a1"`
	}
	spawnedFrom := func(local string) string {
		return `,"spawnedFrom":{"sessionId":"local_` + local + `","userFork":true}`
	}
	writeDesktopEntry(t, third, parent, parent, false, nil, "")
	writeDesktopEntry(t, third, fork, fork, false, nil, forkedFrom(parent))
	writeDesktopEntry(t, third, forkFork, forkFork, false, nil, forkedFrom(fork))
	writeDesktopEntry(t, third, other, other, false, nil, "")
	writeDesktopEntry(t, third, both, both, false, nil, spawnedFrom(other)+forkedFrom(parent))
	writeDesktopEntry(t, third, split, split, false, nil, spawnedFrom(parent)+forkedFrom(parent))
	writeDesktopEntry(t, third, older, older, false, nil, `,"spawnedFrom":{"btw":true,"sideSession":true}`+forkedFrom(parent))
	writeDesktopEntry(t, third, detached, detached, false, nil, forkedFrom(parent)+`,"lineageDetached":true,"lineageDetachedBy":"user"`)
	writeDesktopEntry(t, third, orphan, orphan, false, nil, forkedFrom("ffffffff-0000-4000-8000-000000000003"))
	writeDesktopEntry(t, third, gone, gone, true, nil, "")
	writeDesktopEntry(t, third, ofGone, ofGone, false, nil, forkedFrom(gone))
	oddPath := writeDesktopEntry(t, third, odd, odd, false, nil, `,"forkedFromSessionId":{"sessionId":"local_`+parent+`"}`)

	if e := readClaudeDesktopEntry(oddPath); e.transcript != odd || e.parent != "" {
		t.Errorf("an entry whose forkedFromSessionId is of another shape reads as transcript %q, parent %q; want %q and none", e.transcript, e.parent, odd)
	}
	rows := map[string]proto.Session{}
	for _, s := range n.sessionList(context.Background(), false) {
		rows[s.SessionID] = s
	}
	if _, ok := rows[gone]; ok {
		t.Fatal("the archived session is listed")
	}
	want := map[string]string{parent: "", fork: parent, forkFork: fork, other: "", both: other, split: parent, older: parent,
		detached: "", orphan: "", ofGone: "", odd: ""}
	for id, p := range want {
		s, ok := rows[id]
		if !ok {
			t.Errorf("%s is not listed", id)
			continue
		}
		if got := s.ParentAgentID(); got != p {
			t.Errorf("%s parent_agent_id = %q, want %q", id, got, p)
		}
		if s.IsChild() || s.IsSubtask() {
			t.Errorf("%s became a child: a fork stays a session of its own", id)
		}
	}
}

// A scheduled task's runs are drawn as the app's sidebar lists them, under the task (#8314): the newest run (createdAt)
// heads it and the others name it in parent_agent_id. A task with one run, a run some session started (its own parent
// wins) and an archived run (not listed) are left as they are.
func TestScheduledTaskRunsFoldUnderTheNewest(t *testing.T) {
	const (
		run1    = "abababab-0000-4000-8000-000000000001"
		run2    = "abababab-0000-4000-8000-000000000002"
		run3    = "abababab-0000-4000-8000-000000000003" // the newest run of daily
		done    = "abababab-0000-4000-8000-000000000004" // an archived run of daily
		alone   = "abababab-0000-4000-8000-000000000005" // the only run of weekly
		starter = "abababab-0000-4000-8000-000000000006"
		spawned = "abababab-0000-4000-8000-000000000007" // a run of daily that starter started: under starter
	)
	n, home := testNode(t)
	n.metas, n.bgScans = map[string]quickMeta{}, map[string]*bgScan{}
	dir := filepath.Join(home, ".claude", "projects", "-src-app")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for i, id := range []string{run3, run2, run1, done, alone, starter, spawned} {
		writeEntrypointTranscript(t, dir, id, "claude-desktop", time.Duration(i+3)*time.Minute)
	}
	_, third := useDesktopDirs(t)
	task := func(id string, created int) string {
		return fmt.Sprintf(`,"scheduledTaskId":%q,"createdAt":%d`, id, created)
	}
	writeDesktopEntry(t, third, run1, run1, false, nil, task("daily", 100))
	writeDesktopEntry(t, third, run3, run3, false, nil, task("daily", 300))
	writeDesktopEntry(t, third, run2, run2, false, nil, task("daily", 200))
	writeDesktopEntry(t, third, done, done, true, nil, task("daily", 400))
	writeDesktopEntry(t, third, alone, alone, false, nil, task("weekly", 100))
	writeDesktopEntry(t, third, starter, starter, false, nil, "")
	writeDesktopEntry(t, third, spawned, spawned, false, nil, task("daily", 500)+`,"spawnedFrom":{"sessionId":"local_`+starter+`"}`)

	rows := map[string]proto.Session{}
	for _, s := range n.sessionList(context.Background(), false) {
		rows[s.SessionID] = s
	}
	if _, ok := rows[done]; ok {
		t.Fatal("the archived run is listed")
	}
	for id, want := range map[string]string{run3: "", run2: run3, run1: run3, alone: "", starter: "", spawned: starter} {
		s, ok := rows[id]
		if !ok {
			t.Errorf("%s is not listed", id)
			continue
		}
		if got := s.ParentAgentID(); got != want {
			t.Errorf("%s parent_agent_id = %q, want %q", id, got, want)
		}
	}
}

// The link goes on the wire as the row's own key, and a change of it changes the list's key (a new list is sent).
func TestParentAgentIDOnTheWireAndInTheListKey(t *testing.T) {
	s := proto.Session{MachineID: "m", SessionID: "side", Title: "btw: why"}
	before := listKey([]proto.Session{s})
	s.SetParentAgentID("main")
	b, err := s.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	var back proto.Session
	if err := back.UnmarshalJSON(b); err != nil {
		t.Fatal(err)
	}
	if back.ParentAgentID() != "main" {
		t.Fatalf("parent_agent_id after a round trip = %q in %s", back.ParentAgentID(), b)
	}
	if listKey([]proto.Session{s}) == before {
		t.Fatal("the list key did not change with parent_agent_id")
	}
	s.SetParentAgentID("")
	if s.Hierarchy != nil {
		t.Fatal("a hierarchy with nothing in it is kept")
	}
}

func TestLoopsBack(t *testing.T) {
	parents := map[string]string{"a": "b", "b": "a", "c": "a", "d": "e"}
	for id, want := range map[string]bool{"a": true, "b": true, "c": false, "d": false, "x": false} {
		if got := loopsBack(id, parents); got != want {
			t.Errorf("loopsBack(%s) = %v, want %v", id, got, want)
		}
	}
}
