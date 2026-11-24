package node

import (
	"context"
	"time"
)

// compactRecoveryHookOutput is consumed only after its socket reply is written. A hook that cannot reach the
// manager within the start budget leaves recovery pending for the next platform input.
type compactRecoveryHookOutput struct {
	HookSpecificOutput  map[string]any `json:"hookSpecificOutput"`
	session, checkpoint string
}

func (n *Node) compactRecoveryStart(parent context.Context, req sockRequest, in hookInput) *compactRecoveryHookOutput {
	if req.ReportOnly || in.HookEventName != "SessionStart" || in.Source != "compact" || in.AgentID != "" || in.SessionID == "" || n.peer == nil || !n.isAssistantSession(in.SessionID) {
		return nil
	}
	st, ok := n.state.get(in.SessionID)
	if !ok || st.Guest != nil || st.GuestKind != "" {
		return nil
	}
	if st.RecoveryPending == "" && st.RecoveryHookPending {
		return nil
	}
	if st.RecoveryPending == "" {
		n.state.update(in.SessionID, func(v *sessionState) bool {
			if v.RecoveryPending != "" {
				return false
			}
			v.RecoveryPending = newUUID()
			// Claude can run SessionStart before its compact_boundary reaches the stream reader.
			v.RecoveryHookPending = true
			return true
		})
		st, _ = n.state.get(in.SessionID)
	}
	ctx, cancel := context.WithTimeout(parent, 1500*time.Millisecond)
	defer cancel()
	var answer contextAnswer
	err := n.peer.Call(ctx, "assistant.context", map[string]any{"session_id": in.SessionID, "recovery_id": st.RecoveryPending, "recovery_only": true}, &answer)
	if err != nil || ctx.Err() != nil || answer.Recovery == "" || answer.RecoveryID != st.RecoveryPending {
		return nil
	}
	return &compactRecoveryHookOutput{HookSpecificOutput: map[string]any{"hookEventName": "SessionStart", "additionalContext": answer.Recovery}, session: in.SessionID, checkpoint: answer.RecoveryID}
}

func (n *Node) compactRecoveryHookDelivered(out *compactRecoveryHookOutput) {
	n.state.update(out.session, func(v *sessionState) bool {
		if v.RecoveryPending != out.checkpoint {
			return false
		}
		v.RecoveryPending = ""
		return true
	})
}
