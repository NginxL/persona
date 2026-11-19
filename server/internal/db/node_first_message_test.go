package db

import (
	"context"
	"testing"
)

// Empty creation is written by the node before Android sends its outbox. Both
// that pending state and successful first delivery must survive state.load.
func TestNodeAwaitingFirstMessageIsPersisted(t *testing.T) {
	d, ctx := open(t), context.Background()
	s := NodeSession{Launcher: "example", Cwd: "/tmp/assistant", AwaitingFirstMessage: true}
	for _, pending := range []bool{true, false} {
		s.AwaitingFirstMessage = pending
		if err := d.PutNodeSession(ctx, "machine", "session", s); err != nil {
			t.Fatal(err)
		}
		out, err := d.NodeSessions(ctx, "machine")
		if err != nil {
			t.Fatal(err)
		}
		if got, ok := out["session"]; !ok || got.AwaitingFirstMessage != pending {
			t.Fatalf("pending first message = %v (present=%v), want %v", got.AwaitingFirstMessage, ok, pending)
		}
	}
	// Settings written by an older node have no flag; they are not empty starts.
	if err := d.PutSetting(ctx, "node_model.machine.session", map[string]any{"model": "example"}); err != nil {
		t.Fatal(err)
	}
	if out, err := d.NodeSessions(ctx, "machine"); err != nil || out["session"].AwaitingFirstMessage {
		t.Fatalf("legacy session became pending: %+v %v", out, err)
	}
}
