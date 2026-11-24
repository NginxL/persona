package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/conversationlog"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

func permissionSettingsRequest(t *testing.T, s *Service, method, path, target, role, body string) (*httptest.ResponseRecorder, assistantView) {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if target != "" {
		r.Header.Set(assistantwire.ManagerHeader, target)
	}
	if role != "" {
		r.Header.Set(RoleHeader, role)
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	var out assistantView
	if w.Code == http.StatusOK {
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
	}
	return w, out
}

func TestDefaultPermissionSettingsScopeAndReload(t *testing.T) {
	ctx := context.Background()
	st := &memStore{}
	f := newFakeHub().withMachine()
	s := newServiceOn(t, f, st)
	setSettings(t, s, func(v *Settings) {
		v.Main, v.Launcher = "existing-main", "claude"
		v.Former = []FormerMain{{MachineID: "server", SessionID: "old-main"}}
	})
	w, view := permissionSettingsRequest(t, s, "GET", "/api/v1/assistant", "assistant", "full", "")
	if w.Code != 200 || view.Manager != "assistant" || view.DefaultPermissionMode != "" || view.PermissionDefaults.Version != 1 ||
		view.PermissionDefaults.Manager != view.Manager || !reflect.DeepEqual(view.PermissionDefaults.Modes, proto.PermissionModes) {
		t.Fatalf("legacy defaults: %d %+v", w.Code, view)
	}
	before, _ := s.savedSettings(ctx)
	w, _ = permissionSettingsRequest(t, s, "PUT", "/api/v1/assistant?manager=m-other", "assistant", "full", `{"default_permission_mode":"bypassPermissions","main":"-"}`)
	if got, _ := s.savedSettings(ctx); w.Code != 400 || !reflect.DeepEqual(got, before) {
		t.Fatalf("conflicting targets changed settings: %d %+v", w.Code, got)
	}
	for _, tc := range []struct {
		target, role, body string
		code               int
	}{
		{"", "full", `{"default_permission_mode":"bypassPermissions","main":"-"}`, 400},
		{"m-other", "full", `{"default_permission_mode":"bypassPermissions","main":"-"}`, 403},
		{"assistant", "answer", `{"default_permission_mode":"bypassPermissions"}`, 403},
		{"assistant", "extension", `{"default_permission_mode":"bypassPermissions"}`, 403},
		{"assistant", "full", `{"default_permission_mode":"","main":"-"}`, 400},
		{"assistant", "full", `{"default_permission_mode":null,"main":"-"}`, 400},
		{"assistant", "full", `{"default_permission_mode":false,"main":"-"}`, 400},
		{"assistant", "full", `{"default_permission_mode":"unsafe","launcher":"codex-l","main":"-"}`, 400},
	} {
		puts := st.puts
		w, _ := permissionSettingsRequest(t, s, "PUT", "/api/v1/assistant", tc.target, tc.role, tc.body)
		got, _ := s.savedSettings(ctx)
		if w.Code != tc.code || !reflect.DeepEqual(got, before) || st.puts != puts || len(f.sends) != 0 {
			t.Fatalf("non-atomic refusal: %+v -> %d %s; %+v", tc, w.Code, w.Body, got)
		}
	}
	for _, mode := range proto.PermissionModes {
		w, view := permissionSettingsRequest(t, s, "PUT", "/api/v1/assistant?manager=assistant", "", "full", `{"default_permission_mode":"`+mode+`"}`)
		if w.Code != 200 || view.DefaultPermissionMode != mode || view.Main != before.Main || view.Launcher != before.Launcher || !reflect.DeepEqual(view.Former, before.Former) {
			t.Fatalf("save %s: %d %+v", mode, w.Code, view)
		}
	}
	// Reconstruct the service with only its serialized store; no preferences cache survives.
	reloaded := newServiceOn(t, f, st)
	w, view = permissionSettingsRequest(t, reloaded, "GET", "/api/v1/assistant", "assistant", "full", "")
	if w.Code != 200 || view.DefaultPermissionMode != proto.ModeBypass || view.Main != before.Main {
		t.Fatalf("reload: %d %+v", w.Code, view)
	}
	w, view = permissionSettingsRequest(t, reloaded, "PUT", "/api/v1/assistant", "", "full", `{"main_tasks_quiet":false}`)
	if w.Code != 200 || view.DefaultPermissionMode != proto.ModeBypass {
		t.Fatalf("sparse legacy save reset permission: %d %+v", w.Code, view)
	}
	w, view = permissionSettingsRequest(t, reloaded, "PUT", "/api/v1/assistant", "assistant", "full", `{"default_permission_mode":"default"}`)
	if w.Code != 200 || view.DefaultPermissionMode != "default" {
		t.Fatalf("explicit default: %d %+v", w.Code, view)
	}
}

func TestDefaultPermissionNewConversationInheritance(t *testing.T) {
	for _, mode := range append([]string{""}, proto.PermissionModes...) {
		t.Run(mode, func(t *testing.T) {
			f := newFakeHub().withMachine()
			s := newTestService(t, f)
			setSettings(t, s, func(v *Settings) { v.DefaultPermissionMode = mode })
			wantMode := mode
			if wantMode == "" {
				wantMode = proto.ModeBypass
			}
			for _, p := range []StartParams{{}, {Main: true}, {Space: SpaceHealth}, {Purpose: "assistant"}} {
				p.Untitled = true
				sess, err := s.Start(context.Background(), p)
				if err != nil {
					t.Fatal(err)
				}
				if sent := f.sends[len(f.sends)-1]; sent["permission_mode"] != wantMode || sent["default_permission_mode"] != wantMode ||
					sent["permission_mode_explicit"] != nil || sess.PermissionMode != wantMode {
					t.Fatalf("%+v inherited %v / %s, want %s", p, sent, sess.PermissionMode, wantMode)
				}
			}
			for _, override := range proto.PermissionModes {
				sess, err := s.Start(context.Background(), StartParams{PermissionMode: override, Untitled: true})
				if err != nil || sess.PermissionMode != override || f.sends[len(f.sends)-1]["permission_mode"] != override ||
					f.sends[len(f.sends)-1]["default_permission_mode"] != nil || f.sends[len(f.sends)-1]["permission_mode_explicit"] != true {
					t.Fatalf("explicit %s lost to %s: %+v %v", override, mode, sess, err)
				}
			}
			// A conversation the manager does not know returns before model/default resolution and keeps the caller's params.
			for _, override := range []string{"", "default", "plan"} {
				params := map[string]any{"text": "next"}
				if override != "" {
					params["permission_mode"] = override
				}
				got, err := s.SendParams(context.Background(), SendParams{MachineID: "server", SessionID: "existing", Params: params})
				var want any
				if override != "" {
					want = override
				}
				if err != nil || got["permission_mode"] != want || got["default_permission_mode"] != nil || got["permission_mode_explicit"] != nil {
					t.Fatalf("unknown conversation was resolved: %v %v", got, err)
				}
			}
			before := len(f.sends)
			w, _ := permissionSettingsRequest(t, s, "POST", "/api/v1/assistant/main/new", "assistant", "full", `{"carry":"none"}`)
			if w.Code != 200 || len(f.sends) != before {
				t.Fatalf("main/new started a turn: %d %s", w.Code, w.Body)
			}
			_, err := s.Start(context.Background(), StartParams{Main: true, Untitled: true})
			if err != nil || f.sends[len(f.sends)-1]["permission_mode"] != wantMode {
				t.Fatalf("replacement main lost default: %v", err)
			}
		})
	}
}

func TestDefaultPermissionExistingConversationPrecedence(t *testing.T) {
	f := newFakeHub().withMachine()
	s := newTestService(t, f)
	setSettings(t, s, func(v *Settings) {
		v.DefaultPermissionMode = proto.ModeBypass
		v.Main, v.MainMachineID = "existing", "server"
	})

	got, err := s.SendParams(context.Background(), SendParams{MachineID: "server", SessionID: "existing", Params: map[string]any{"text": "next"}})
	if err != nil {
		t.Fatal(err)
	}
	if got["default_permission_mode"] != proto.ModeBypass || got["permission_mode"] != nil {
		t.Fatalf("current default was not sent separately: %v", got)
	}

	got, err = s.SendParams(context.Background(), SendParams{MachineID: "server", SessionID: "existing", Params: map[string]any{
		"text": "explicit", "permission_mode": "plan", "default_permission_mode": "default",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if got["permission_mode"] != "plan" || got["default_permission_mode"] != nil {
		t.Fatalf("explicit mode was replaced by the default: %v", got)
	}
}

func TestDefaultPermissionLogicalInheritedModeIsNotExplicit(t *testing.T) {
	s, f, d, _ := logicalPair(t, "claude")
	activatePair(t, d)
	if err := d.ResolveConversationSwitch(t.Context(), "server", "side-chat", "operation", "accepted", ""); err != nil {
		t.Fatal(err)
	}
	f.setSessions("server",
		proto.Session{SessionID: "side-chat", Assistant: true, Agent: "claude", Status: proto.StatusIdle},
		proto.Session{SessionID: "target-chat", Assistant: true, Agent: "claude", Status: proto.StatusIdle})
	f.answerNode("session.modes", func(string, json.RawMessage) (any, error) { return map[string]bool{"ok": true}, nil })
	_, handled, err := s.logicalCall(t.Context(), "server", "session.modes", json.RawMessage(`{"session_id":"side-chat","permission_mode":"bypassPermissions"}`))
	if err != nil || !handled {
		t.Fatalf("seed the inherited segment mode: handled=%v err=%v", handled, err)
	}
	conversation, _ := d.LogicalConversation(t.Context(), "server", "side-chat")
	active, activeOK := conversation.Active()
	if !activeOK || active.Ordinal < 2 || active.PermissionMode != proto.ModeBypass {
		t.Fatalf("logical fixture did not activate the inherited mode: epoch=%d active=%+v", conversation.Epoch, active)
	}
	setSettings(t, s, func(v *Settings) { v.DefaultPermissionMode = proto.ModePlan })

	got, err := s.SendParams(t.Context(), SendParams{MachineID: "server", SessionID: "side-chat", Params: map[string]any{"text": "next"}})
	if err != nil {
		t.Fatal(err)
	}
	if got["permission_mode"] != nil || got["permission_mode_explicit"] != nil || got["default_permission_mode"] != proto.ModePlan {
		t.Fatalf("inherited logical mode became an explicit pin: %v", got)
	}

	got, err = s.SendParams(t.Context(), SendParams{MachineID: "server", SessionID: "side-chat", Params: map[string]any{
		"text": "explicit", "permission_mode": "acceptEdits",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if got["permission_mode"] != "acceptEdits" || got["permission_mode_explicit"] != true || got["default_permission_mode"] != nil {
		t.Fatalf("explicit logical mode changed: %v", got)
	}
}

func TestDefaultPermissionLogicalUsesExecutionMachine(t *testing.T) {
	s, f, d, _ := historyFixture(t, func(string, ...any) {})
	ctx := t.Context()
	f.setSessions("server", proto.Session{SessionID: "side-chat", Assistant: true, Agent: "claude", Launcher: "shared"})
	f.setSessions("other", proto.Session{SessionID: "target-chat", Assistant: true, Agent: "claude", Launcher: "shared"})
	f.zshrc = map[string][]proto.Launcher{
		"server": {{Name: "shared", Agent: "paseo", Source: "paseo", Assistant: true, PermissionModes: proto.PermissionModeSetOf(proto.ModeDefault, proto.ModePlan)}},
		"other":  {{Name: "shared", Agent: "claude", Source: "zshrc", PermissionModes: proto.PermissionModeSetOf(proto.ModeDefault)}},
	}
	if err := d.AdoptConversation(ctx, "server", "side-chat", "one", "claude", "h1"); err != nil {
		t.Fatal(err)
	}
	request, _ := json.Marshal(logicalSwitchRequest{OperationID: "operation", ExpectedEpoch: 1, MachineID: "other", Launcher: "shared"})
	if err := d.BeginConversationSwitch(ctx, "server", "side-chat", db.ConversationSwitch{OperationID: "operation", ExpectedEpoch: 1, SourceSegment: "one", Request: request}); err != nil {
		t.Fatal(err)
	}
	if err := d.PutConversationHandoff(ctx, "server", "side-chat", "operation", json.RawMessage(`{"summary":"summary"}`)); err != nil {
		t.Fatal(err)
	}
	segment := conversationlog.Segment{ID: "two", MachineID: "other", SessionID: "target-chat", Engine: "claude", Launcher: "shared"}
	if err := d.PrepareConversationSegment(ctx, "server", "side-chat", "operation", segment, 6); err != nil {
		t.Fatal(err)
	}
	activatePair(t, d)
	conversation, _ := d.LogicalConversation(ctx, "server", "side-chat")
	active, activeOK := conversation.Active()
	if !activeOK || active.Ordinal < 2 || active.MachineID != "other" {
		t.Fatalf("logical fixture did not activate the other machine: epoch=%d active=%+v", conversation.Epoch, active)
	}
	setSettings(t, s, func(v *Settings) { v.DefaultPermissionMode = proto.ModePlan })

	got, err := s.SendParams(ctx, SendParams{MachineID: "server", SessionID: "side-chat", Params: map[string]any{"text": "next"}})
	if err != nil {
		t.Fatal(err)
	}
	if got["launcher"] != "shared" || got["default_permission_mode"] != proto.ModePlan {
		t.Fatalf("logical root machine hid the active Claude segment's default: %v", got)
	}
}

func TestDefaultPermissionExistingCustomClaudeConversationUsesManagerDefault(t *testing.T) {
	f := newFakeHub().withMachine()
	f.setSessions("server", proto.Session{SessionID: "existing", Assistant: true, Agent: "claude", Launcher: "custom-claude"})
	f.zshrc = map[string][]proto.Launcher{"server": {
		{Name: "custom-claude", Agent: "claude", Source: "zshrc", PermissionModes: proto.PermissionModeSetOf(proto.ModeDefault)},
	}}
	s := newTestService(t, f)
	setSettings(t, s, func(v *Settings) {
		v.DefaultPermissionMode = proto.ModePlan
		v.Main, v.MainMachineID = "existing", "server"
	})
	setModel(t, s, purposeMain, ModelBinding{Provider: "launcher.custom-claude"})

	got, err := s.SendParams(context.Background(), SendParams{MachineID: "server", SessionID: "existing", Params: map[string]any{"text": "next"}})
	if err != nil {
		t.Fatal(err)
	}
	if got["launcher"] != "custom-claude" || got["default_permission_mode"] != proto.ModePlan || got["permission_mode"] != nil {
		t.Fatalf("custom Claude resume lost the manager default: %v", got)
	}
}

func TestDefaultPermissionExistingNonClaudeConversationKeepsLegacyMode(t *testing.T) {
	f := newFakeHub().withMachine()
	f.setSessions("server", proto.Session{SessionID: "existing", Assistant: true, Agent: "paseo", Launcher: "custom-runtime", SourceKind: proto.SourceRuntime, SourceInstanceID: "paseo"})
	f.zshrc = map[string][]proto.Launcher{"server": {
		{Name: "custom-runtime", Agent: "paseo", Source: "paseo", Assistant: true, PermissionModes: proto.PermissionModeSetOf(proto.ModeDefault, proto.ModePlan)},
	}}
	s := newTestService(t, f)
	setSettings(t, s, func(v *Settings) {
		v.DefaultPermissionMode = proto.ModePlan
		v.Main, v.MainMachineID = "existing", "server"
	})
	setModel(t, s, purposeMain, ModelBinding{Provider: "launcher.custom-runtime"})

	t.Run("manager default is not emitted", func(t *testing.T) {
		got, err := s.SendParams(context.Background(), SendParams{MachineID: "server", SessionID: "existing", Params: map[string]any{"text": "next"}})
		if err != nil {
			t.Fatal(err)
		}
		if got["launcher"] != "custom-runtime" || got["default_permission_mode"] != nil || got["permission_mode"] != nil {
			t.Fatalf("non-Claude resume received the manager default: %v", got)
		}
	})
	t.Run("explicit generic mode is unchanged", func(t *testing.T) {
		got, err := s.SendParams(context.Background(), SendParams{MachineID: "server", SessionID: "existing", Params: map[string]any{
			"text": "explicit", "permission_mode": proto.ModeDefault,
		}})
		if err != nil {
			t.Fatal(err)
		}
		if got["launcher"] != "custom-runtime" || got["permission_mode"] != proto.ModeDefault ||
			got["permission_mode_explicit"] != true || got["default_permission_mode"] != nil {
			t.Fatalf("explicit non-Claude mode changed: %v", got)
		}
	})
}

func TestDefaultPermissionCustomClaudeLauncherKeepsBypass(t *testing.T) {
	f := newFakeHub().withMachine()
	f.zshrc = map[string][]proto.Launcher{"server": {
		{Name: "custom-claude", Agent: "claude", Source: "zshrc", PermissionModes: proto.PermissionModeSetOf(proto.ModeDefault)},
		{Name: "custom-runtime", Agent: "paseo", Source: "paseo", Assistant: true, PermissionModes: proto.PermissionModeSetOf(proto.ModeDefault)},
	}}
	s := newTestService(t, f)
	setSettings(t, s, func(v *Settings) { v.DefaultPermissionMode = proto.ModeBypass })

	setModel(t, s, "assistant", ModelBinding{Provider: "launcher.custom-claude"})
	sess, err := s.Start(context.Background(), StartParams{Text: "start", Untitled: true})
	if err != nil {
		t.Fatal(err)
	}
	if sent := f.sends[len(f.sends)-1]; sent["permission_mode"] != proto.ModeBypass || sess.PermissionMode != proto.ModeBypass {
		t.Fatalf("custom Claude launcher lost inherited bypass: sent=%v session=%+v", sent, sess)
	}

	// The mode list remains authoritative for a non-Claude runtime.
	setModel(t, s, "assistant", ModelBinding{Provider: "launcher.custom-runtime"})
	sess, err = s.Start(context.Background(), StartParams{Text: "start", Untitled: true})
	if err != nil {
		t.Fatal(err)
	}
	if sent := f.sends[len(f.sends)-1]; sent["permission_mode"] != nil || sess.PermissionMode != "" {
		t.Fatalf("non-Claude runtime received an undeclared mode: sent=%v session=%+v", sent, sess)
	}
}

func TestDefaultPermissionGuestDoesNotInherit(t *testing.T) {
	f, s, _ := guestService(t)
	setSettings(t, s, func(v *Settings) { v.DefaultPermissionMode = proto.ModeBypass })
	w := postConversation(t, s, `{"prompt":"hello","guest":{"tools":["lark_agent_reply"]}}`)
	if w.Code != 200 || f.sends[len(f.sends)-1]["permission_mode"] != "" || f.sends[len(f.sends)-1]["default_permission_mode"] != nil ||
		f.sends[len(f.sends)-1]["permission_mode_explicit"] != nil {
		t.Fatalf("guest inherited permissions: %d %s %v", w.Code, w.Body, f.sends)
	}
}

func TestDefaultPermissionManagersKeepIndependentStores(t *testing.T) {
	stores := []*memStore{{}, {}}
	ids := []string{"assistant", "m-example"}
	modes := []string{proto.ModeBypass, "plan"}
	f := newFakeHub().withMachine()
	for pass := 0; pass < 2; pass++ {
		for i, id := range ids {
			st := stores[i]
			s := withSettings(t, f, New(Options{ID: id, Hub: f, Store: func() Store { return st }, Logf: testLogf(t)}))
			if pass == 0 {
				w, _ := permissionSettingsRequest(t, s, "PUT", "/api/v1/assistant", id, "full", `{"default_permission_mode":"`+modes[i]+`"}`)
				if w.Code != 200 {
					t.Fatalf("save %s: %d %s", id, w.Code, w.Body)
				}
			}
			w, view := permissionSettingsRequest(t, s, "GET", "/api/v1/assistant", id, "full", "")
			if w.Code != 200 || view.Manager != id || view.DefaultPermissionMode != modes[i] || view.PermissionDefaults.Manager != id || view.PermissionDefaults.Version != 1 {
				t.Fatalf("pass %d %s: %d %+v", pass, id, w.Code, view)
			}
		}
	}
}

// The saved preference reaches new conversations regardless of the chosen native backend.
func TestDefaultPermissionSavedForBothLaunchers(t *testing.T) {
	for _, launcher := range []string{"claude", "codex-l"} {
		for _, mode := range proto.PermissionModes {
			t.Run(launcher+"/"+mode, func(t *testing.T) {
				f := newFakeHub().withMachine()
				st := &memStore{}
				s := newServiceOn(t, f, st)
				setSettings(t, s, func(v *Settings) { v.Launcher = launcher; v.Main = "existing-main" })
				w, _ := permissionSettingsRequest(t, s, "PUT", "/api/v1/assistant", "assistant", "full", `{"default_permission_mode":"`+mode+`"}`)
				if w.Code != http.StatusOK {
					t.Fatalf("save: %d %s", w.Code, w.Body)
				}
				s = newServiceOn(t, f, st)
				w, view := permissionSettingsRequest(t, s, "GET", "/api/v1/assistant", "assistant", "full", "")
				if w.Code != http.StatusOK || view.DefaultPermissionMode != mode || view.Main != "existing-main" {
					t.Fatalf("reload: %d %+v", w.Code, view)
				}
				session, err := s.Start(context.Background(), StartParams{Untitled: true})
				if err != nil {
					t.Fatal(err)
				}
				sent := f.sends[len(f.sends)-1]
				if sent["launcher"] != launcher || sent["permission_mode"] != mode || session.PermissionMode != mode {
					t.Fatalf("new %s session: %v %+v", launcher, sent, session)
				}
			})
		}
	}
}
