package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

// The claude_interactive setting (#10058) is off by default and, once on, goes with every message to the node: the
// manager's own sends, an app's send and a warm, so the node never switches a conversation between the two kinds
// because one message lacked it.
func TestSendParamsClaudeInteractive(t *testing.T) {
	ctx := context.Background()
	f := newFakeHub().withMachine()
	s := newTestService(t, f)
	sess, err := s.start(ctx, StartParams{Main: true, Text: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if _, set := f.lastSend(t)["claude_interactive"]; set {
		t.Fatal("sent while off (the default)")
	}
	if v := s.view(ctx, false); v.ClaudeInteractive {
		t.Fatal("on by default")
	}
	f.setSessions("server", proto.Session{SessionID: sess.SessionID, Assistant: true})
	on := true
	setSettings(t, s, func(v *Settings) { v.ClaudeInteractive = &on })
	params, err := s.SendParams(ctx, SendParams{MachineID: "server", SessionID: sess.SessionID, Params: map[string]any{"text": "next"}})
	if err != nil || params["claude_interactive"] != true {
		t.Fatalf("app send while on: %v %v", params, err)
	}
	// assistant.model, what the node asks for a start no send makes (a compaction's), says it too
	raw, _ := json.Marshal(map[string]any{"session_id": sess.SessionID})
	if out, _, err := s.NodeAnswer(ctx, "server", "assistant.model", raw); err != nil || out.(map[string]any)["claude_interactive"] != true {
		t.Fatalf("assistant.model while on: %v %v", out, err)
	}
	if _, err := s.start(ctx, StartParams{Text: "side"}); err != nil {
		t.Fatal(err)
	}
	if got := f.lastSend(t)["claude_interactive"]; got != true {
		t.Fatalf("assistant.send while on: %v", got)
	}
	if v := s.view(ctx, false); !v.ClaudeInteractive {
		t.Fatal("the view does not say it is on")
	}
}

// The assistant's settings tool lists the switch with what it does and its risk, and changes it.
func TestClaudeInteractiveSetting(t *testing.T) {
	ctx := context.Background()
	f := newFakeHub().withMachine()
	s := newTestService(t, f)
	var found *Setting
	for _, d := range s.ownSettings() {
		if d.Key == "assistant.claude_interactive" {
			found = &d.Setting
		}
	}
	if found == nil {
		t.Fatal("assistant.claude_interactive is not a setting")
	}
	for _, w := range []string{"off (the default)", "5-hour", "Risk", "print mode"} {
		if !strings.Contains(found.About, w) {
			t.Errorf("About does not say %q: %s", w, found.About)
		}
	}
	if s.claudeInteractive(ctx) {
		t.Fatal("on before it was set")
	}
	change, err := s.setClaudeInteractive(ctx, []byte("true"))
	if err != nil || change.apply == nil {
		t.Fatalf("change %+v %v", change, err)
	}
	if _, err := change.apply(ctx); err != nil {
		t.Fatal(err)
	}
	if !s.claudeInteractive(ctx) {
		t.Fatal("set did not turn it on")
	}
}
