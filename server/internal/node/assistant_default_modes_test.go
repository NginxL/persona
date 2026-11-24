package node

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/proto"
)

// Exercise the real process startup, mode persistence, and acknowledged Claude control protocol.
func TestAssistantResumeUsesCurrentDefaultUnlessModeExplicit(t *testing.T) {
	f := cliTestBash(t)
	raw, err := nodeCall(t, f.n, "assistant.send", map[string]any{
		"text": "first", "cwd": t.TempDir(), "launcher": "claude",
		"permission_mode": "auto", "default_permission_mode": "auto",
	})
	if err != nil {
		t.Fatal(err)
	}
	var started struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(raw, &started); err != nil || started.SessionID == "" {
		t.Fatalf("start result=%s err=%v", raw, err)
	}
	f.idle(t, started.SessionID)
	f.n.stopAssistantProcess(started.SessionID)

	// An older manager supplies no default: the legacy mode still wins on this path.
	if _, err := nodeCall(t, f.n, "assistant.send", map[string]any{
		"session_id": started.SessionID, "text": "resume from an older manager",
	}); err != nil {
		t.Fatal(err)
	}
	f.idle(t, started.SessionID)
	if st, _ := f.n.state.get(started.SessionID); st.AppMode != "auto" {
		t.Fatalf("legacy manager lost its previous mode; state=%+v", st)
	}
	f.n.stopAssistantProcess(started.SessionID)

	if _, err := nodeCall(t, f.n, "assistant.send", map[string]any{
		"session_id": started.SessionID, "text": "resume with the current default", "default_permission_mode": proto.ModeBypass,
	}); err != nil {
		t.Fatal(err)
	}
	f.idle(t, started.SessionID)
	if st, _ := f.n.state.get(started.SessionID); st.AppMode != proto.ModeBypass || st.AppModeExplicit {
		t.Fatalf("legacy mode %q overrode the current default; state=%+v", st.AppMode, st)
	}

	// A different default does not interrupt a live process; it applies on the next stopped-process resume.
	if _, err := nodeCall(t, f.n, "assistant.send", map[string]any{
		"session_id": started.SessionID, "text": "keep the live process", "default_permission_mode": "default",
	}); err != nil {
		t.Fatal(err)
	}
	f.idle(t, started.SessionID)
	if st, _ := f.n.state.get(started.SessionID); st.AppMode != proto.ModeBypass {
		t.Fatalf("live process changed with the default; state=%+v", st)
	}

	if err := f.n.setModes(context.Background(), started.SessionID, "plan", ""); err != nil {
		t.Fatal(err)
	}
	f.n.stopAssistantProcess(started.SessionID)
	if _, err := nodeCall(t, f.n, "assistant.send", map[string]any{
		"session_id": started.SessionID, "text": "resume with an explicit choice", "default_permission_mode": proto.ModeBypass,
	}); err != nil {
		t.Fatal(err)
	}
	f.idle(t, started.SessionID)
	if st, _ := f.n.state.get(started.SessionID); st.AppMode != "plan" || !st.AppModeExplicit {
		t.Fatalf("current default overrode the explicit conversation mode; state=%+v", st)
	}
}

func TestSessionSendDoesNotMarkFallbackModeExplicit(t *testing.T) {
	f := cliTestBash(t)
	raw, err := nodeCall(t, f.n, "assistant.send", map[string]any{
		"text": "legacy start", "cwd": t.TempDir(), "launcher": "claude",
		"permission_mode": "auto", "default_permission_mode": "auto",
	})
	if err != nil {
		t.Fatal(err)
	}
	var started struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(raw, &started); err != nil || started.SessionID == "" {
		t.Fatalf("start result=%s err=%v", raw, err)
	}
	f.idle(t, started.SessionID)

	if _, err := nodeCall(t, f.n, "session.send", map[string]any{
		"session_id": started.SessionID, "text": "session.send drops its generic mode",
		"permission_mode": "plan", "permission_mode_explicit": true,
	}); err != nil {
		t.Fatal(err)
	}
	f.idle(t, started.SessionID)
	if st, _ := f.n.state.get(started.SessionID); st.AppModeExplicit {
		t.Fatalf("fallback mode %q was pinned as an explicit choice; state=%+v", st.AppMode, st)
	}
}

func TestAssistantExplicitStartModeSurvivesDefaultResume(t *testing.T) {
	f := cliTestBash(t)
	raw, err := nodeCall(t, f.n, "assistant.send", map[string]any{
		"text": "explicit start", "cwd": t.TempDir(), "launcher": "claude",
		"permission_mode": "plan", "permission_mode_explicit": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	var started struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(raw, &started); err != nil || started.SessionID == "" {
		t.Fatalf("start result=%s err=%v", raw, err)
	}
	f.idle(t, started.SessionID)
	f.n.stopAssistantProcess(started.SessionID)

	if _, err := nodeCall(t, f.n, "assistant.send", map[string]any{
		"session_id": started.SessionID, "text": "resume the explicit start", "default_permission_mode": proto.ModeBypass,
	}); err != nil {
		t.Fatal(err)
	}
	f.idle(t, started.SessionID)
	if st, _ := f.n.state.get(started.SessionID); st.AppMode != "plan" || !st.AppModeExplicit {
		t.Fatalf("explicit start did not survive resume; state=%+v", st)
	}
}

func TestNonClaudeStoppedResumeKeepsLegacyMode(t *testing.T) {
	n, hub := runtimeNode(t)
	assistantHub(hub)
	hello := assistantRuntimeHello()
	hello["launcher"] = "custom-runtime"
	hello["caps"].(map[string]any)["modes"] = true
	host := dialRuntime(t, n, hello)
	cwd := identity.AssistantDir(n.home)
	host.answer = func(method string, params json.RawMessage) (any, error) {
		if method == "session.start" {
			return map[string]any{"session": map[string]any{
				"session_id": "session-paseo-default", "cwd": cwd, "status": "idle", "agent": "paseo",
				"capabilities": map[string]any{"permission_modes": []string{proto.ModeDefault, proto.ModePlan}},
			}}, nil
		}
		return map[string]any{"ok": true}, nil
	}
	if _, err := nodeCall(t, n, "assistant.send", map[string]any{
		"cwd": cwd, "launcher": "custom-runtime", "text": "first",
		"permission_mode": proto.ModeDefault, "default_permission_mode": proto.ModeDefault,
	}); err != nil {
		t.Fatal(err)
	}
	if st, _ := n.state.get("session-paseo-default"); st.AppMode != proto.ModeDefault || st.AppModeExplicit {
		t.Fatalf("start did not retain the legacy inherited mode: %+v", st)
	}
	if err := host.peer.Notify("runtime.sessions", map[string]any{"sessions": []map[string]any{{
		"session_id": "session-paseo-default", "cwd": cwd, "status": "offline", "agent": "paseo",
		"capabilities": map[string]any{"permission_modes": []string{proto.ModeDefault, proto.ModePlan}},
	}}}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "stopped runtime session", func() bool { return listed(t, n)["session-paseo-default"].Status == proto.StatusOffline })

	if _, err := nodeCall(t, n, "assistant.send", map[string]any{
		"session_id": "session-paseo-default", "text": "resume", "default_permission_mode": proto.ModePlan,
	}); err != nil {
		t.Fatal(err)
	}
	sends := host.got("session.send")
	if len(sends) != 2 {
		t.Fatalf("runtime sends: %s", sends)
	}
	var resumed struct {
		Mode string `json:"mode"`
	}
	if err := json.Unmarshal(sends[1], &resumed); err != nil {
		t.Fatal(err)
	}
	if resumed.Mode != proto.ModeDefault {
		t.Fatalf("stopped non-Claude resume mode = %q, want saved %q; send=%s", resumed.Mode, proto.ModeDefault, sends[1])
	}
	if st, _ := n.state.get("session-paseo-default"); st.AppMode != proto.ModeDefault {
		t.Fatalf("manager default replaced the saved non-Claude mode: %+v", st)
	}
}

func TestClaudeRuntimeResumeDefaultOnlyWhenStopped(t *testing.T) {
	n, hub := runtimeNode(t)
	assistantHub(hub)
	hello := assistantRuntimeHello()
	hello["launcher"], hello["agent"] = "custom-claude-runtime", "claude"
	hello["caps"].(map[string]any)["modes"] = true
	host := dialRuntime(t, n, hello)
	cwd := identity.AssistantDir(n.home)
	host.answer = func(method string, params json.RawMessage) (any, error) {
		if method == "session.start" {
			return map[string]any{"session": map[string]any{
				"session_id": "session-claude-default", "cwd": cwd, "status": "idle", "agent": "claude",
				"capabilities": map[string]any{"permission_modes": []string{proto.ModeDefault, proto.ModePlan}},
			}}, nil
		}
		return map[string]any{"ok": true}, nil
	}
	if _, err := nodeCall(t, n, "assistant.send", map[string]any{
		"cwd": cwd, "launcher": "custom-claude-runtime", "text": "first",
		"permission_mode": proto.ModeDefault, "default_permission_mode": proto.ModeDefault,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := nodeCall(t, n, "assistant.send", map[string]any{
		"session_id": "session-claude-default", "text": "keep the live mode", "default_permission_mode": proto.ModePlan,
	}); err != nil {
		t.Fatal(err)
	}
	if err := host.peer.Notify("runtime.sessions", map[string]any{"sessions": []map[string]any{{
		"session_id": "session-claude-default", "cwd": cwd, "status": "offline", "agent": "claude",
		"capabilities": map[string]any{"permission_modes": []string{proto.ModeDefault, proto.ModePlan}},
	}}}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "stopped Claude runtime session", func() bool { return listed(t, n)["session-claude-default"].Status == proto.StatusOffline })
	if _, err := nodeCall(t, n, "assistant.send", map[string]any{
		"session_id": "session-claude-default", "text": "resume with the current default", "default_permission_mode": proto.ModePlan,
	}); err != nil {
		t.Fatal(err)
	}

	sends := host.got("session.send")
	if len(sends) != 3 {
		t.Fatalf("runtime sends: %s", sends)
	}
	for i, want := range []string{proto.ModeDefault, proto.ModeDefault, proto.ModePlan} {
		var sent struct {
			Mode string `json:"mode"`
		}
		if err := json.Unmarshal(sends[i], &sent); err != nil {
			t.Fatal(err)
		}
		if sent.Mode != want {
			t.Fatalf("send %d mode = %q, want %q; send=%s", i+1, sent.Mode, want, sends[i])
		}
	}
}

func TestClaudeRuntimeUUIDLiveSendKeepsMode(t *testing.T) {
	n, hub := runtimeNode(t)
	assistantHub(hub)
	hello := assistantRuntimeHello()
	hello["launcher"], hello["agent"] = "custom-claude-uuid", "claude"
	host := dialRuntime(t, n, hello)
	cwd := t.TempDir()
	const id = "dddddddd-0000-4000-8000-0000000000aa"
	sdkTranscript(t, n, id, "existing transcript")
	host.answer = func(method string, params json.RawMessage) (any, error) {
		if method == "session.start" {
			return map[string]any{"session": map[string]any{"session_id": id, "cwd": cwd, "status": "idle", "agent": "claude"}}, nil
		}
		return map[string]any{"ok": true}, nil
	}
	if _, err := nodeCall(t, n, "assistant.send", map[string]any{
		"cwd": cwd, "launcher": "custom-claude-uuid", "text": "first", "task_execution": true,
		"permission_mode": proto.ModeDefault, "default_permission_mode": proto.ModeDefault,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := nodeCall(t, n, "assistant.send", map[string]any{
		"session_id": id, "cwd": cwd, "text": "keep the live mode", "task_execution": true, "default_permission_mode": proto.ModePlan,
	}); err != nil {
		t.Fatal(err)
	}
	sends := host.got("session.send")
	if len(sends) != 2 {
		t.Fatalf("runtime sends: %s", sends)
	}
	var live struct {
		Mode string `json:"mode"`
	}
	if err := json.Unmarshal(sends[1], &live); err != nil {
		t.Fatal(err)
	}
	if live.Mode != proto.ModeDefault {
		t.Fatalf("live UUID runtime mode = %q, want saved %q; send=%s", live.Mode, proto.ModeDefault, sends[1])
	}
}

func TestClaudeRuntimeExplicitModeSurvivesStoppedResume(t *testing.T) {
	n, hub := runtimeNode(t)
	assistantHub(hub)
	hello := assistantRuntimeHello()
	hello["launcher"], hello["agent"] = "custom-claude-explicit", "claude"
	host := dialRuntime(t, n, hello)
	cwd := identity.AssistantDir(n.home)
	const id = "session-claude-explicit"
	host.answer = func(method string, params json.RawMessage) (any, error) {
		if method == "session.start" {
			return map[string]any{"session": map[string]any{"session_id": id, "cwd": cwd, "status": "idle", "agent": "claude"}}, nil
		}
		return map[string]any{"ok": true}, nil
	}
	if _, err := nodeCall(t, n, "assistant.send", map[string]any{
		"cwd": cwd, "launcher": "custom-claude-explicit", "text": "explicit start",
		"permission_mode": proto.ModePlan, "permission_mode_explicit": true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := host.peer.Notify("runtime.sessions", map[string]any{"sessions": []map[string]any{{
		"session_id": id, "cwd": cwd, "status": "offline", "agent": "claude",
	}}}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "stopped explicit Claude runtime", func() bool { return listed(t, n)[id].Status == proto.StatusOffline })
	if _, err := nodeCall(t, n, "assistant.send", map[string]any{
		"session_id": id, "text": "resume the explicit mode", "default_permission_mode": proto.ModeBypass,
	}); err != nil {
		t.Fatal(err)
	}
	sends := host.got("session.send")
	if len(sends) != 2 {
		t.Fatalf("runtime sends: %s", sends)
	}
	var resumed struct {
		Mode string `json:"mode"`
	}
	if err := json.Unmarshal(sends[1], &resumed); err != nil {
		t.Fatal(err)
	}
	if resumed.Mode != proto.ModePlan {
		t.Fatalf("explicit runtime mode = %q, want %q; send=%s", resumed.Mode, proto.ModePlan, sends[1])
	}
	if st, _ := n.state.get(id); !st.AppModeExplicit {
		t.Fatalf("explicit runtime choice was not persisted: %+v", st)
	}
}

func TestClaudeNewConversationPermissionModes(t *testing.T) {
	for _, mode := range proto.PermissionModes {
		t.Run(mode, func(t *testing.T) {
			f := cliTest(t)
			id, err := f.n.assistantSend(context.Background(), "", t.TempDir(), "claude", "hello", nil, mode, "", cliExec())
			if err != nil {
				t.Fatal(err)
			}
			f.idle(t, id)
			st, ok := f.n.state.get(id)
			if !ok || st.AppMode != mode {
				t.Fatalf("persisted mode: %+v", st)
			}
			p := f.proc(id)
			p.mu.Lock()
			applied := p.mode
			p.mu.Unlock()
			if applied != mode {
				t.Fatalf("applied mode: %q, want %q", applied, mode)
			}
			if mode == proto.ModeBypass {
				if argv := f.file("cli-argv"); !strings.Contains(argv, "--permission-mode "+mode) {
					t.Fatalf("start args: %q", argv)
				}
			} else if !strings.Contains(f.file("stdin.jsonl"), `"mode":"`+mode+`"`) {
				t.Fatalf("Claude did not receive mode %s: %s", mode, f.file("stdin.jsonl"))
			}
		})
	}
}
