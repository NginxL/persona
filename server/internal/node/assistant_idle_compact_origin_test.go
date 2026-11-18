package node

import (
	"encoding/json"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

func TestIdleCompactionRPCPreservesConversationOrigin(t *testing.T) {
	for _, tc := range []struct {
		name, origin, mode string
		main               bool
	}{
		{"main", "user", proto.ModeBypass, true},
		{"inbox", originInbox, proto.ModePlan, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := cliTest(t)
			raw, err := nodeCall(t, f.n, "assistant.send", map[string]any{
				"launcher": "claude", "cwd": t.TempDir(), "text": "hello",
				"origin": tc.origin, "is_main": tc.main,
				"permission_mode": proto.ModeBypass, "idle_compaction": idleCompactPolicy,
			})
			if err != nil {
				t.Fatal(err)
			}
			var out struct {
				SessionID string `json:"session_id"`
			}
			if err := json.Unmarshal(raw, &out); err != nil {
				t.Fatal(err)
			}
			f.idle(t, out.SessionID)
			st, ok := f.n.state.get(out.SessionID)
			if !ok || st.Origin != tc.origin || st.IsMain != tc.main || st.AppMode != tc.mode {
				t.Fatalf("RPC lost origin or scope: state=%+v, present=%v", st, ok)
			}
			if got := f.n.assistantOrigin(out.SessionID); got != tc.origin {
				t.Fatalf("RPC process origin=%q, want %q", got, tc.origin)
			}
			if tc.main {
				if st.IdleCompaction == nil || st.IdleCompaction.Policy != idleCompactPolicy {
					t.Fatalf("RPC lost the main conversation's idle policy: %+v", st.IdleCompaction)
				}
			} else if st.IdleCompaction != nil {
				t.Fatal("Inbox conversation received the main conversation's idle state")
			}
		})
	}
}
