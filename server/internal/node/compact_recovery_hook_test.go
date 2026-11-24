package node

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestCompactRecoveryNativeHookSharesDeliveryCheckpoint(t *testing.T) {
	const sid = "11111111-2222-3333-4444-555555555555"
	for _, boundaryFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "hook first", true: "boundary first"}[boundaryFirst], func(t *testing.T) {
			n, _ := closeoutHookNode(t, func(method string, raw json.RawMessage) (any, error) {
				var p struct {
					SessionID    string `json:"session_id"`
					RecoveryID   string `json:"recovery_id"`
					RecoveryOnly bool   `json:"recovery_only"`
				}
				if method != "assistant.context" || json.Unmarshal(raw, &p) != nil || p.SessionID != sid || p.RecoveryID == "" || !p.RecoveryOnly {
					t.Errorf("request: %s %s", method, raw)
				}
				return contextAnswer{RecoveryID: p.RecoveryID, Recovery: "native recovery block"}, nil
			})
			n.procs[sid] = &assistantProc{id: sid, agent: "claude"}
			n.state.put(sid, sessionState{Launcher: "claude", Cwd: "/example"})
			if boundaryFirst {
				n.contextCompacted(sid)
			}
			in := startHook(sid)
			in.Source = "compact"
			out := n.compactRecoveryStart(t.Context(), sockRequest{Kind: "hook"}, in)
			if out == nil || out.HookSpecificOutput["additionalContext"] != "native recovery block" {
				t.Fatalf("hook output: %+v", out)
			}
			st, _ := n.state.get(sid)
			if st.RecoveryPending == "" {
				t.Fatal("an unwritten hook reply consumed recovery")
			}
			n.compactRecoveryHookDelivered(out)
			if !boundaryFirst {
				n.contextCompacted(sid)
			}
			st, _ = n.state.get(sid)
			if st.RecoveryPending != "" || st.RecoveryHookPending {
				t.Fatalf("native hook duplicated the boundary: %+v", st)
			}
			if block, _ := n.turnContext(sid, contextAnswer{RecoveryID: out.checkpoint, Recovery: "native recovery block"}); block != "" {
				t.Fatalf("platform input duplicated native recovery: %q", block)
			}
		})
	}
}

func TestCompactRecoveryNativeHookScopeAndTimeout(t *testing.T) {
	const sid = "11111111-2222-3333-4444-555555555555"
	release := make(chan struct{})
	n, asked := closeoutHookNode(t, func(string, json.RawMessage) (any, error) { <-release; return contextAnswer{}, nil })
	n.procs[sid] = &assistantProc{id: sid, agent: "claude"}
	n.state.put(sid, sessionState{Launcher: "claude", Cwd: "/example"})
	in := startHook(sid)
	in.Source = "compact"
	if out := n.compactRecoveryStart(t.Context(), sockRequest{ReportOnly: true}, in); out != nil {
		t.Fatal("report-only hook received owner context")
	}
	child := in
	child.AgentID = "child"
	if out := n.compactRecoveryStart(t.Context(), sockRequest{}, child); out != nil {
		t.Fatal("subagent received parent context")
	}
	startup := in
	startup.Source = "startup"
	if out := n.compactRecoveryStart(t.Context(), sockRequest{}, startup); out != nil {
		t.Fatal("ordinary startup received recovery")
	}
	if asked.Load() != 0 {
		t.Fatal("an excluded hook contacted the manager")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	if out := n.compactRecoveryStart(ctx, sockRequest{}, in); out != nil {
		t.Fatal("timed out hook returned context")
	}
	close(release)
	st, _ := n.state.get(sid)
	if st.RecoveryPending == "" {
		t.Fatal("timed out hook lost fallback recovery")
	}
	n.contextCompacted(sid)
	st, _ = n.state.get(sid)
	if st.RecoveryPending == "" || st.RecoveryHookPending {
		t.Fatal("stream boundary lost the timed-out hook checkpoint")
	}
}
