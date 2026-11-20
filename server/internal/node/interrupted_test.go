package node

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// A restart takes herdr's panes with it. Sessions Persona had started in one are shown as interrupted, so the apps
// can offer "Continue"; everything else keeps the status it had.
func TestMarkInterrupted(t *testing.T) {
	n, _ := testNode(t)
	n.bootAt = time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC)
	before := n.bootAt.Add(-time.Hour)
	after := n.bootAt.Add(time.Hour)

	n.state.Sessions["cut-off"] = sessionState{Pane: "pane-1", StartedAt: before}
	n.state.Sessions["ended-on-its-own"] = sessionState{Pane: "pane-2", StartedAt: after}
	n.state.Sessions["no-pane"] = sessionState{StartedAt: before} // the user's own terminal: never a pane of ours
	n.state.Sessions["still-running"] = sessionState{Pane: "pane-3", StartedAt: before}
	// "unknown" is simply not in the record.

	rows := []proto.Session{
		{SessionID: "cut-off", Status: proto.StatusOffline},
		{SessionID: "ended-on-its-own", Status: proto.StatusOffline},
		{SessionID: "no-pane", Status: proto.StatusOffline},
		{SessionID: "still-running", Status: proto.StatusOffline},
		{SessionID: "unknown", Status: proto.StatusOffline},
	}
	live := map[string]bool{"still-running": true}

	n.markInterrupted(rows, live)

	want := map[string]string{
		"cut-off":          proto.StatusInterrupted,
		"ended-on-its-own": proto.StatusOffline,
		"no-pane":          proto.StatusOffline,
		"still-running":    proto.StatusOffline, // live: not touched
		"unknown":          proto.StatusOffline,
	}
	for _, r := range rows {
		if r.Status != want[r.SessionID] {
			t.Errorf("%s: status %q, want %q", r.SessionID, r.Status, want[r.SessionID])
		}
	}
}

// A session that is not offline is left alone whatever the record says.
func TestMarkInterruptedLeavesRunningRows(t *testing.T) {
	n, _ := testNode(t)
	n.bootAt = time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC)
	n.state.Sessions["busy"] = sessionState{Pane: "pane-1", StartedAt: n.bootAt.Add(-time.Hour)}

	rows := []proto.Session{
		{SessionID: "busy", Status: proto.StatusWorking},
		{SessionID: "busy2", Status: proto.StatusBlocked},
	}
	n.markInterrupted(rows, nil)

	if rows[0].Status != proto.StatusWorking || rows[1].Status != proto.StatusBlocked {
		t.Fatalf("running rows kept their status: %v", rows)
	}
}

// An Agent last seen running shortly before this node started and gone now was stopped by the restart: its cause is
// recorded as restart, which the assistant may resume (#11441). One last seen long before, one whose cause is known,
// and one with no sighting keep theirs.
func TestMarkInterruptedRecordsARestartStop(t *testing.T) {
	n, _ := testNode(t)
	n.bootAt = time.Date(2026, 11, 19, 18, 12, 0, 0, time.UTC)
	started := n.bootAt.Add(-48 * time.Hour)
	n.state.Sessions["ran-until-restart"] = sessionState{Pane: "p1", StartedAt: started, RanAt: n.bootAt.Add(-3 * time.Minute).Unix()}
	n.state.Sessions["ran-long-before"] = sessionState{Pane: "p2", StartedAt: started, RanAt: n.bootAt.Add(-4 * time.Hour).Unix()}
	n.state.Sessions["user-stopped"] = sessionState{Pane: "p3", StartedAt: started, RanAt: n.bootAt.Add(-time.Minute).Unix(), StopCause: proto.StopUser}
	n.state.Sessions["never-seen"] = sessionState{Pane: "p4", StartedAt: started}
	rows := []proto.Session{
		{SessionID: "ran-until-restart", Status: proto.StatusOffline},
		{SessionID: "ran-long-before", Status: proto.StatusOffline},
		{SessionID: "user-stopped", Status: proto.StatusOffline},
		{SessionID: "never-seen", Status: proto.StatusOffline},
	}
	n.markInterrupted(rows, nil)
	want := map[string]proto.StopCause{"ran-until-restart": proto.StopRestart, "ran-long-before": proto.StopUnknown,
		"user-stopped": proto.StopUser, "never-seen": proto.StopUnknown}
	for id, cause := range want {
		if got := n.stopCause(id); got != cause {
			t.Errorf("%s: stop cause %q, want %q", id, got, cause)
		}
	}
	if ranUntilRestart(n.bootAt.Add(time.Minute).Unix(), n.bootAt) || ranUntilRestart(0, n.bootAt) || !ranUntilRestart(n.bootAt.Add(-restartWindow).Unix(), n.bootAt) {
		t.Fatal("ranUntilRestart")
	}
}

// Seeing an Agent run writes RanAt at most every ranAtEvery, and clears a stop cause it had (#11441).
func TestRecordRunningKeepsRanAt(t *testing.T) {
	n, _ := testNode(t)
	old := time.Now().Add(-time.Hour).Unix()
	n.state.Sessions["s"] = sessionState{Pane: "p", RanAt: old, StopCause: proto.StopRestart}
	n.recordRunning("s")
	st, _ := n.state.get("s")
	if st.RanAt <= old || st.StopCause != proto.StopUnknown {
		t.Fatalf("after running: %+v", st)
	}
	first := st.RanAt
	n.recordRunning("s")
	if st, _ := n.state.get("s"); st.RanAt != first {
		t.Fatal("RanAt written again within ranAtEvery")
	}
	// A later put that does not carry it keeps it.
	n.state.put("s", sessionState{Pane: "p"})
	if st, _ := n.state.get("s"); st.RanAt != first {
		t.Fatal("put dropped RanAt")
	}
}

// The assistant's resume that fails with the Agent not running is recorded as resume_failed, never unknown; a
// resume that went, or one whose Agent runs after all, records nothing (#11441).
func TestNoteResumeFailed(t *testing.T) {
	n, _ := testNode(t)
	n.state.Sessions["gone"] = sessionState{Pane: "p"}
	n.state.Sessions["fine"] = sessionState{Pane: "p"}
	n.state.Sessions["maybe"] = sessionState{Pane: "p"}
	n.state.Sessions["kept"] = sessionState{Pane: "p"}
	n.state.Sessions["late"] = sessionState{Pane: "p"}
	n.state.Sessions["plain"] = sessionState{Pane: "p"}
	n.noteResumeFailed("gone", refusal(codexResumeRefused, "Codex could not find its session", nil))
	n.noteResumeFailed("fine", nil)
	n.noteResumeFailed("maybe", unconfirmedStart(errors.New("no answer from the pane")))
	n.noteResumeFailed("kept", withKeptTab(refusal(proto.CodeAgentStartStalled, "stalled", nil), "w1:p1", time.Now()))
	n.noteResumeFailed("late", context.DeadlineExceeded)
	n.noteResumeFailed("plain", errors.New("claude did not come up")) // a line was typed: it may still
	for _, id := range []string{"maybe", "kept", "late", "plain"} {   // uncertain: it may still come up, so no second resume
		if got := n.stopCause(id); got != proto.StopUnknown {
			t.Fatalf("%s: %q", id, got)
		}
	}
	if got := n.stopCause("gone"); got != proto.StopResumeFailed {
		t.Fatalf("gone: %q", got)
	}
	if got := n.stopCause("fine"); got != proto.StopUnknown {
		t.Fatalf("fine: %q", got)
	}
}
