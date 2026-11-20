package node

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// Stop always presses Escape. On a Claude Code session whose turn is over while a background command still runs, the
// Escape interrupts nothing, and the answer says so instead of claiming an interruption; a turn that runs, or one with
// no background work left, is answered as before (#10828).
func TestInterruptWithOnlyBackgroundWorkSaysSo(t *testing.T) {
	now := time.Now().UTC().Format(time.RFC3339)
	started := []string{
		`{"type":"assistant","uuid":"a2","timestamp":"` + now + `","message":{"role":"assistant","content":[{"type":"tool_use","id":"tu1","name":"Bash","input":{"command":"sleep 600","run_in_background":true}}]}}`,
		`{"type":"user","uuid":"u2","timestamp":"` + now + `","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu1","content":"Command running in background with ID: b1. Output is being written to: /tmp/tasks/b1.output"}]}}`,
		`{"type":"system","subtype":"turn_duration","uuid":"d2","parentUuid":"u2","durationMs":1000}`,
	}
	// The command's notification woke the session, which answered and ended that turn too: nothing runs any more.
	finished := []string{
		`{"type":"user","uuid":"u3","timestamp":"` + now + `","message":{"role":"user","content":"<task-notification>\n<task-id>b1</task-id>\n<status>completed</status>\n<summary>Background command \"sleep\" completed (exit code 0)</summary>\n</task-notification>"}}`,
		`{"type":"assistant","uuid":"a3","parentUuid":"u3","timestamp":"` + now + `","message":{"role":"assistant","content":[{"type":"text","text":"done"}]}}`,
		`{"type":"system","subtype":"turn_duration","uuid":"d3","parentUuid":"a3","durationMs":1000}`,
	}
	for _, tc := range []struct {
		name    string
		status  string
		lines   []string
		refused bool
	}{
		{"turn over, the command still runs", proto.StatusIdle, started, true},
		{"turn over and done, the command still runs", proto.StatusDone, started, true},
		{"working, with the command in the background", proto.StatusWorking, started, false},
		{"waiting on the user", proto.StatusBlocked, started, false},
		{"turn over, nothing in the background", proto.StatusIdle, nil, false},
		{"turn over, the command has finished", proto.StatusIdle, append(append([]string{}, started...), finished...), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n, f, home := herdrNode(t)
			seedAgents(t, n, f, claudeAgent(sidA, tc.status, "w1:p1", "w1:t1", "w1", "/tmp/m", ""))
			writeTranscript(t, home, "/tmp/m", sidA, time.Now(), append(finishedTurn("/tmp/m"), tc.lines...)...)
			f.reset()
			_, err := request(t, n, "session.interrupt", map[string]any{"session_id": sidA})
			keys := f.trace("pane.send_keys", "pane.send_input")
			if len(keys) != 1 || !strings.Contains(keys[0], "esc") {
				t.Fatalf("keys %q: want the Escape, whatever the answer", keys)
			}
			if !tc.refused {
				if err != nil {
					t.Fatalf("err = %v, want the stop taken", err)
				}
				return
			}
			var re *rpc.Error
			if !errors.As(err, &re) || re.Code != proto.CodeTurnOverBackground {
				t.Fatalf("err = %v, want %s", err, proto.CodeTurnOverBackground)
			}
		})
	}
}

// The words exist in every language, and are not another language's (a missing key falls back).
func TestTurnOverBackgroundWords(t *testing.T) {
	seen := map[string]i18n.Lang{}
	for _, l := range []i18n.Lang{i18n.En, i18n.ZhHans, i18n.Ja} {
		got := i18n.T(l, "rpc-errors."+proto.CodeTurnOverBackground)
		if got == "" || strings.Contains(got, "rpc-errors.") {
			t.Errorf("%s: no words: %q", l, got)
		}
		if other, dup := seen[got]; dup {
			t.Errorf("%s says the same as %s: a missing translation", l, other)
		}
		seen[got] = l
	}
}
