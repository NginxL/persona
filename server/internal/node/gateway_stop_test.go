package node

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/transcript"
)

// Keep these cases under the existing stop-provenance parent: node's round-robin CI partitions otherwise move
// thousands of unrelated tests between processes when a new top-level test is inserted this early in the listing.
func testGatewayStoppedProvenance(t *testing.T) {
	for _, tc := range []struct {
		name, text string
		want       proto.StopCause
	}{
		{"quota", "Request rejected (429) · upstream account quota usage limit has been reached; an account becomes available again at 18:02 UTC (in 12h7m)", proto.StopUsageLimit},
		{"capacity", "Request rejected (429) · upstream account capacity is temporarily unavailable", proto.StopTransientError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n, f, home := herdrNode(t)
			provenanceHerdr(n)
			cwd := mkdir(t, home+"/project")
			at := time.Now().Add(-time.Minute).UTC().Truncate(time.Second)
			writeTranscript(t, home, cwd, sidA, at,
				`{"type":"user","uuid":"u1","timestamp":"`+at.Format(time.RFC3339)+`","cwd":"`+cwd+`","message":{"role":"user","content":"continue"}}`,
				`{"type":"assistant","uuid":"a1","timestamp":"`+at.Format(time.RFC3339)+`","message":{"role":"assistant","model":"example-model","content":[{"type":"text","text":"`+strings.Repeat("Working. ", 300)+`"}]}}`,
				`{"type":"assistant","uuid":"e1","timestamp":"`+at.Format(time.RFC3339)+`","isApiErrorMessage":true,"error":"rate_limit","apiErrorStatus":429,"message":{"role":"assistant","model":"<synthetic>","content":[{"type":"text","text":"API Error: `+tc.text+`"}]}}`)
			n.state.put(sidA, sessionState{Launcher: "claude", Cwd: cwd, AppMode: "plan", Pane: "w1:p1",
				RanAt: at.Add(-time.Minute).Unix()})
			pane := shellPane("w1:p1", "w1:t1", "w1", cwd, sidA)
			f.reply("session.snapshot", snapshot(nil, nil, nil, []any{pane}))
			offlineCache.Lock()
			offlineCache.at = time.Time{}
			offlineCache.Unlock()
			row := byID(listSessions(t, n))[sidA]
			// An unknown stop, which the assistant may resume on purpose (#11528), not a gateway one.
			if row.Capabilities == nil || row.Capabilities.StopCause != proto.StopUnknown || n.stopCause(sidA) != proto.StopUnknown {
				t.Fatal("unobserved gateway history granted recovery")
			}
			// Observe the failed turn while its Agent still runs, even within RanAt's normal write interval.
			f.reply("session.snapshot", snapshot(nil, nil, []any{
				claudeAgent(sidA, proto.StatusIdle, "w1:p1", "w1:t1", "w1", cwd, ""),
			}, []any{pane}))
			listSessions(t, n)
			if st, _ := n.state.get(sidA); st.RanAt < at.Unix() {
				t.Fatal("live gateway observation was not recorded")
			}
			f.reply("session.snapshot", snapshot(nil, nil, nil, []any{pane}))
			row = byID(listSessions(t, n))[sidA]
			if row.Capabilities == nil || row.Capabilities.StopCause != tc.want || row.Capabilities.Send || row.ReadOnlyReason == "" {
				t.Fatalf("stopped row: status=%q capabilities=%+v reason=%q", row.Status, row.Capabilities, row.ReadOnlyReason)
			}
			if err := n.supervisorResumeAllowed(sidA, "", ""); err != nil {
				t.Fatal(err)
			}
			if tc.want == proto.StopUsageLimit && (row.TurnErrorKind != proto.TurnErrorUsageLimit || row.TurnErrorResetsAt == nil) {
				t.Fatalf("quota has no reset: kind=%q reset=%v", row.TurnErrorKind, row.TurnErrorResetsAt)
			}
			if tc.want == proto.StopTransientError && row.TurnErrorKind != proto.TurnErrorTransient {
				t.Fatalf("capacity kind = %q", row.TurnErrorKind)
			}
			// Closing the terminal outside Persona also revokes an already listed gateway resume.
			f.reply("session.snapshot", snapshot(nil, nil, nil, nil))
			listSessions(t, n)
			if n.supervisorResumeAllowed(sidA, "", "") == nil {
				t.Fatal("closed terminal retained gateway recovery")
			}
			f.reply("session.snapshot", snapshot(nil, nil, nil, []any{pane}))
			row = byID(listSessions(t, n))[sidA]
			if err := n.supervisorResumeAllowed(sidA, "", ""); err != nil {
				t.Fatalf("restored terminal must be eligible before owner-stop checks: %v", err)
			}
			// Explicit stops always win, even while the same gateway error remains in history.
			for _, cause := range []proto.StopCause{proto.StopUser, proto.StopExit} {
				n.state.stopped(sidA, cause)
				row.Capabilities = nil
				n.markStopped(&row)
				if n.stopCause(sidA) != cause || n.supervisorResumeAllowed(sidA, "", "") == nil {
					t.Fatalf("gateway error overrode %s", cause)
				}
			}
			// Consuming a previous resume cannot grant a second start from the old failure: the cause stays unknown, and
			// the uncertain start holds the assistant's next resume off (resumeSettle).
			n.uncertainResumes.begin(sidA, time.Now())(errors.New("no answer"))
			n.state.stopped(sidA, proto.StopUnknown)
			row.Capabilities = nil
			n.markStopped(&row)
			if n.stopCause(sidA) != proto.StopUnknown || n.supervisorResumeAllowed(sidA, "", "") == nil {
				t.Fatal("old error reauthorized an uncertain start")
			}
			// A reply quoting the error is never recovery provenance.
			if kind, _ := transcript.GatewayErrorKind("I saw: "+tc.text, at); kind != "" {
				t.Fatal("ordinary reply classified as a gateway failure")
			}
		})
		t.Run(tc.name+" closed before classification", func(t *testing.T) {
			n, _, home := herdrNode(t)
			provenanceHerdr(n)
			at := time.Now().Add(-time.Minute)
			n.state.put(sidA, sessionState{Launcher: "claude", Cwd: home, Pane: "w1:p1", RanAt: time.Now().Unix()})
			row := proto.Session{SessionID: sidA, Agent: "claude", Status: proto.StatusOffline, TurnError: tc.text, UpdatedAt: at}
			n.markStopped(&row)
			if row.Capabilities == nil || row.Capabilities.StopCause != proto.StopUnknown || n.stopCause(sidA) != proto.StopUnknown {
				t.Fatal("a gateway error authorized a terminal the user closed")
			}
		})
	}
}
