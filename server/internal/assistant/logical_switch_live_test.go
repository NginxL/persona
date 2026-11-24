package assistant

import (
	"encoding/json"
	"testing"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

// The node lists a native Claude Code conversation without an agent. Its first explicit switch adopts it as a
// Claude Code segment instead of refusing it as an unknown engine (live acceptance, 2026-11-21).
func TestLogicalSwitchAdoptsANativeClaudeRowWithoutAnAgent(t *testing.T) {
	s, f, d, _ := historyFixture(t, func(string, ...any) {})
	ctx := t.Context()
	f.known["fresh-chat"] = true
	f.setSessions("server", proto.Session{SessionID: "fresh-chat", Assistant: true, Rev: 100})
	f.answerNode("assistant.segment.check", func(string, json.RawMessage) (any, error) {
		return segmentTarget{Registered: true, Engine: "codex", SupervisionPolicy: true, Prepare: true}, nil
	})
	if _, err := s.beginLogicalSwitch(ctx, "server", "fresh-chat", logicalSwitchRequest{OperationID: "first", ExpectedEpoch: 1, MachineID: "server", Launcher: "codex"}); err != nil {
		t.Fatalf("native Claude Code source refused: %v", err)
	}
	c, err := d.LogicalConversation(ctx, "server", "fresh-chat")
	if err != nil || c == nil || len(c.Segments) != 1 || c.Segments[0].Engine != "claude" || c.Switch == nil || c.Switch.OperationID != "first" {
		t.Fatalf("adopted source: %+v %v", c, err)
	}
	// A runtime's row without an agent is still unknown: nothing is guessed for it.
	if got := sessionEngineAgent(proto.Session{SourceKind: proto.SourceRuntime}); got != "" {
		t.Fatalf("runtime without agent guessed as %q", got)
	}
	if got := sessionEngineAgent(proto.Session{Agent: "codex"}); got != "codex" {
		t.Fatalf("listed agent replaced: %q", got)
	}
}

// An adopted conversation's row is still read from the manager's fleet, not sent to a node as session.get (which
// no node knows): otherwise the conversation looks gone and its segments state and every later switch refuse it.
func TestLogicalAdoptedConversationRowIsReadFromTheFleet(t *testing.T) {
	s, _, st, _ := logicalPair(t, "codex")
	ctx := t.Context()
	raw := newFleetDevs()
	raw.add("server", "Computer")
	raw.setSessions("server", []proto.Session{
		{SessionID: "side-chat", Cwd: "/home/u/.persona/assistant", Assistant: true, Agent: "claude", Rev: 100},
		{SessionID: "target-chat", Cwd: "/home/u/.persona/assistant", Assistant: true, Agent: "codex", Rev: 100},
	})
	s.devs = &logicalDevices{Devices: raw, s: s}
	s.logicalChanged()
	row, ok := s.session(ctx, "server", "side-chat")
	if !ok || !row.Assistant || row.SessionID != "side-chat" {
		t.Fatalf("adopted conversation row: %+v %v", row, ok)
	}
	if calls := raw.callsTo("server", DeviceSessionGet); len(calls) != 0 {
		t.Fatalf("session.get reached the node: %s", calls)
	}
	if !s.keepsPickOn(ctx, "side-chat", "server") {
		t.Fatal("adopted conversation lost its pick (segments state refuses it)")
	}
	// Switched: the public root reads as its active segment, and the private segment stays hidden.
	activatePair(t, st)
	s.logicalChanged()
	row, ok = s.session(ctx, "server", "side-chat")
	if !ok || row.SessionID != "side-chat" || row.Agent != "codex" {
		t.Fatalf("switched conversation row: %+v %v", row, ok)
	}
	if !s.keepsPickOn(ctx, "side-chat", "server") {
		t.Fatal("switched conversation lost its pick (a switch back is refused)")
	}
	if _, ok := s.session(ctx, "server", "target-chat"); ok {
		t.Fatal("private segment readable as a session")
	}
	if calls := raw.callsTo("server", DeviceSessionGet); len(calls) != 0 {
		t.Fatalf("session.get reached the node: %s", calls)
	}
}

// A supervisor's switch prepares the target with the role's policy, as the first input will carry it: a Codex thread
// with no turn yet cannot be resumed, so a policy arriving only with that input restarted it under a new thread id.
func TestLogicalPreparationCarriesTheSupervisorPolicy(t *testing.T) {
	s, f, d, _ := logicalPair(t, "codex")
	ctx := t.Context()
	setSettings(t, s, func(cfg *Settings) { cfg.Leaders = append(cfg.Leaders, "side-chat") })
	if err := d.ResolveConversationSwitch(ctx, "server", "side-chat", "operation", "rejected", ""); err != nil {
		t.Fatal(err)
	}
	request, _ := json.Marshal(logicalSwitchRequest{OperationID: "lead", ExpectedEpoch: 1, MachineID: "server", Launcher: "codex"})
	if err := d.BeginConversationSwitch(ctx, "server", "side-chat", db.ConversationSwitch{OperationID: "lead", ExpectedEpoch: 1, SourceSegment: "one", Request: request}); err != nil {
		t.Fatal(err)
	}
	if err := d.PutConversationHandoff(ctx, "server", "side-chat", "lead", json.RawMessage(`{"summary":"saved summary"}`)); err != nil {
		t.Fatal(err)
	}
	f.answerNode("assistant.segment.check", func(string, json.RawMessage) (any, error) {
		return segmentTarget{Registered: true, Engine: "codex", Prepare: true, SupervisionPolicy: true}, nil
	})
	var prepared map[string]any
	f.answerNode("assistant.segment.prepare", func(_ string, raw json.RawMessage) (any, error) {
		_ = json.Unmarshal(raw, &prepared)
		return map[string]any{"session_id": "lead-target", "prepared": true}, nil
	})
	want, err := s.supervisionInstructions(ctx, "server", "side-chat")
	if err != nil || want == "" {
		t.Fatalf("leader policy: %q %v", want, err)
	}
	c, _ := d.LogicalConversation(ctx, "server", "side-chat")
	_ = s.advanceLogicalConversation(ctx, d, c)
	if prepared == nil {
		t.Fatal("no preparation")
	}
	if prepared["supervision_instructions"] != want {
		t.Fatalf("preparation policy: %v", prepared["supervision_instructions"])
	}
	for _, key := range []string{"text", "prompt", "images", "client_msg_id"} {
		if _, ok := prepared[key]; ok {
			t.Fatalf("preparation carried input %q", key)
		}
	}
}
