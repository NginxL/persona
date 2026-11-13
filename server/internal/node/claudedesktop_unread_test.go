package node

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// attended has the desktop app in front with someone at it (true) or not, for the test.
func attended(t *testing.T, v *bool) {
	t.Helper()
	old := claudeAppAttended
	claudeAppAttended = func() bool { return *v }
	t.Cleanup(func() { claudeAppAttended = old })
}

func desktopTimes(focused, turnEdge int64) string {
	return fmt.Sprintf(`,"lastFocusedAt":%d,"lastTurnEdgeAt":%d`, focused, turnEdge)
}

// A resting session's last turn is unread when it ended after the app last brought the session into view, unless the
// node finds someone attending it (the app in front, the person active, the session the app shows); that is kept, also
// across a restart, until a later turn ends.
func TestDesktopTurnUnreadUntilSeen(t *testing.T) {
	_, third := useDesktopDirs(t)
	const (
		watched = "dddddddd-1111-4000-8000-000000000001" // the session the app shows
		beside  = "dddddddd-1111-4000-8000-000000000002" // brought into view with it: a split view
		other   = "dddddddd-1111-4000-8000-000000000003" // brought into view a minute before: not shown now
		opened  = "dddddddd-1111-4000-8000-000000000004" // brought into view after its last turn ended
		fresh   = "dddddddd-1111-4000-8000-000000000005" // no turn yet
	)
	now := time.Now().UnixMilli()
	writeDesktopEntry(t, third, "w", watched, false, nil, desktopTimes(now-60_000, now-10_000))
	writeDesktopEntry(t, third, "b", beside, false, nil, desktopTimes(now-63_000, now-10_000))
	writeDesktopEntry(t, third, "o", other, false, nil, desktopTimes(now-120_000, now-10_000))
	writeDesktopEntry(t, third, "p", opened, false, nil, desktopTimes(now-150_000, now-200_000))
	writeDesktopEntry(t, third, "f", fresh, false, nil, desktopTimes(now-200_000, 0))
	dir := t.TempDir()
	n := &Node{dir: dir}
	at := false
	attended(t, &at)
	x := readClaudeDesktopIndex("")

	for id, want := range map[string]bool{watched: true, beside: true, other: true, opened: false, fresh: false} {
		if got := n.desktopTurnUnread(id, x); got != want {
			t.Errorf("%s with nobody at the app: unread %v, want %v", id, got, want)
		}
	}
	// Someone at the app: what it shows is seen (both panes of a split), what it does not show is not.
	at = true
	for id, want := range map[string]bool{watched: false, beside: false, other: true} {
		if got := n.desktopTurnUnread(id, x); got != want {
			t.Errorf("%s with someone at the app: unread %v, want %v", id, got, want)
		}
	}
	// Seen stays seen once they leave, and after a restart (a new node on the same state directory).
	at = false
	for _, m := range []*Node{n, {dir: dir}} {
		if m.desktopTurnUnread(watched, x) || m.desktopTurnUnread(beside, x) {
			t.Error("a turn seen while it was shown became unread again")
		}
	}
	// What is kept is only what still matters: not the sessions whose last turn the app showed anyway.
	b, _ := os.ReadFile(filepath.Join(dir, desktopSeenFile))
	var kept map[string]int64
	if json.Unmarshal(b, &kept) != nil || len(kept) != 2 || kept[watched] == 0 || kept[beside] == 0 {
		t.Errorf("kept %s", b)
	}
	// A later turn that ends while nobody looks is unread again.
	p := writeDesktopEntry(t, third, "w", watched, false, nil, desktopTimes(now-60_000, time.Now().UnixMilli()+1_000))
	later := time.Now().Add(time.Minute)
	_ = os.Chtimes(p, later, later) // read again
	if !n.desktopTurnUnread(watched, readClaudeDesktopIndex("")) {
		t.Error("a later turn nobody saw is not unread")
	}
}

// The desktop app's resting session is listed done (Unread) when its last turn ended unseen, idle once seen; a working
// one stays working, and a claude in another terminal stays idle, even one the app's index names.
func TestOutsideDesktopClaudeListedUnreadUntilSeen(t *testing.T) {
	n, _, home := herdrNode(t)
	noPaneEnv(t)
	_, third := useDesktopDirs(t)
	at := false
	attended(t, &at)
	now := time.Now().UnixMilli()
	for _, id := range []string{sidA, sidB} {
		writeTranscript(t, home, home, id, time.Now(),
			`{"type":"user","uuid":"u1","timestamp":"2026-11-13T09:00:00Z","cwd":"`+home+`","sessionId":"`+id+`","message":{"role":"user","content":"tidy the docs"}}`,
			strings.Repeat(" ", 4096))
	}
	writeDesktopEntry(t, third, "a", sidA, false, nil, desktopTimes(now-60_000, now-5_000))
	// sidB is a desktop session resumed in a terminal (claude --resume): the app's index names it, but it runs there.
	writeDesktopEntry(t, third, "b", sidB, false, nil, desktopTimes(now-90_000, now-5_000))
	desktop := map[string]any{"sessionId": sidA, "cwd": home, "kind": "interactive", "entrypoint": "claude-desktop-3p", "status": "idle"}
	writeClaudeProc(t, home, os.Getpid(), desktop)
	writeClaudeProc(t, home, os.Getppid(), map[string]any{"sessionId": sidB, "cwd": home, "kind": "interactive", "entrypoint": "cli", "status": "idle"})

	rows := byID(listSessions(t, n))
	if s := rows[sidA]; s.SourceKind != proto.SourceClaudeDesktop || s.Status != proto.StatusDone {
		t.Fatalf("an unseen finished turn: %s %s", s.SourceKind, s.Status)
	}
	if s := rows[sidB]; s.Status != proto.StatusIdle {
		t.Errorf("a claude in another terminal: %s", s.Status)
	}
	desktop["status"] = "busy"
	writeClaudeProc(t, home, os.Getpid(), desktop)
	if s := byID(listSessions(t, n))[sidA]; s.Status != proto.StatusWorking {
		t.Errorf("working: %s", s.Status)
	}
	desktop["status"] = "idle"
	writeClaudeProc(t, home, os.Getpid(), desktop)
	at = true // someone comes back to the app, which shows it
	if s := byID(listSessions(t, n))[sidA]; s.Status != proto.StatusIdle {
		t.Errorf("seen in the app: %s", s.Status)
	}
}

// What macOS's commands print: lsappinfo's bundle id line and ioreg's idle time in nanoseconds.
func TestAttendedFromMacOSOutput(t *testing.T) {
	if id := frontBundleID(`"CFBundleIdentifier"="com.anthropic.claudefordesktop"` + "\n"); id != claudeDesktopBundle {
		t.Errorf("bundle id %q", id)
	}
	if id := frontBundleID("ASN:0x0-0x1:\n"); id != "" {
		t.Errorf("no bundle id: %q", id)
	}
	if d, ok := hidIdle(`+-o IOHIDSystem  <class IOHIDSystem>` + "\n" + `  |   "HIDIdleTime" = 6425263791` + "\n"); !ok || d != 6425263791*time.Nanosecond {
		t.Errorf("idle %v %v", d, ok)
	}
	if _, ok := hidIdle("nothing\n"); ok {
		t.Error("an idle time from nothing")
	}
}
