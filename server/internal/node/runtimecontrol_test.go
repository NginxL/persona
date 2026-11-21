package node

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// paseoEngineHello is the Paseo runtime with a launcher per provider, the phone's modes and session takeovers.
func paseoEngineHello() map[string]any {
	h := paseoHello()
	h["caps"] = map[string]any{"start": true, "send": true, "interrupt": true, "rename": true, "close": true, "modes": true, "resume": true}
	h["launchers"] = []map[string]any{
		{"name": "paseo-claude", "agent": "claude", "model": "Opus 5.5", "models": []map[string]any{
			{"id": "claude-opus-5-5", "name": "Opus 5.5", "default": true, "efforts": []string{"low", "high", "max", "turbo"}},
			{"id": "", "name": "no id"}}},
		{"name": "paseo-codex", "agent": "paseo", "model": "GPT"},
		{"name": "bad name!", "agent": "claude"},
		{"name": "paseo-claude", "agent": "claude"},
	}
	return h
}

// A runtime running several agents offers a launcher for each, with its agent and models; what the form chose with
// one reaches the runtime when the session starts; runtime.launchers replaces them.
func TestRuntimeLaunchers(t *testing.T) {
	n, _ := runtimeNode(t)
	host := dialRuntime(t, n, paseoEngineHello())
	enablePaseo(n)
	raw, err := nodeCall(t, n, "launchers.list", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	var res struct {
		Launchers []struct {
			proto.Launcher
			AgentType string `json:"agent_type"`
		} `json:"launchers"`
	}
	_ = json.Unmarshal(raw, &res)
	got := map[string]proto.Launcher{}
	for _, l := range res.Launchers {
		if l.Source == "paseo" {
			got[l.Name] = l.Launcher
		}
	}
	if len(got) != 2 || got["paseo-claude"].Agent != "" || got["paseo-claude"].Model != "Opus 5.5" || got["paseo-codex"].Agent != "paseo" {
		t.Fatalf("launchers %s", raw)
	}
	raw, err = nodeCall(t, n, "launchers.models", map[string]any{"name": "paseo-claude"})
	if err != nil {
		t.Fatal(err)
	}
	var models proto.LauncherModels
	_ = json.Unmarshal(raw, &models)
	if models.Agent != "claude" || len(models.Models) != 1 || !models.Models[0].Default || len(models.Models[0].Efforts) != 3 {
		t.Fatalf("models %s", raw)
	}

	host.answer = func(method string, params json.RawMessage) (any, error) {
		if method == "session.start" {
			return map[string]any{"session": map[string]any{"session_id": "paseo-n1", "status": "idle", "cwd": "/w/app"}}, nil
		}
		return map[string]any{"ok": true}, nil
	}
	if _, err := nodeCall(t, n, "session.start", map[string]any{"cwd": "/w/app", "launcher": "paseo-codex", "model": "gpt-6",
		"permission_mode": "default", "effort": "high", "workspace_id": "wks_1", "async": true}); err != nil {
		t.Fatal(err)
	}
	var start map[string]any
	_ = json.Unmarshal(host.got("session.start")[0], &start)
	if start["launcher"] != "paseo-codex" || start["model"] != "gpt-6" || start["permission_mode"] != "default" || start["effort"] != "high" ||
		start["workspace_id"] != "wks_1" || start["shell_launcher"] != nil || start["resume"] != nil {
		t.Fatalf("started with %v", start)
	}

	_ = host.peer.Notify("runtime.launchers", map[string]any{"launchers": []map[string]any{{"name": "paseo-grok", "agent": "paseo"}}})
	waitFor(t, "the new launchers", func() bool {
		return n.runtimes.launcherRuntime("paseo-grok") != nil && n.runtimes.launcherRuntime("paseo-codex") == nil
	})
}

// A runtime's launcher carries the name the runtime gives its provider and, when its agent is not Claude Code, the
// permission modes a start on it takes (#8261): Persona's own, whatever else the runtime sent. A Claude Code launcher's
// are the apps' own five; an older runtime's launcher says neither.
func TestRuntimeLauncherNamesAndPermissionModes(t *testing.T) {
	n, _ := runtimeNode(t)
	h := paseoEngineHello()
	h["launchers"] = []map[string]any{
		{"name": "my-claude", "agent": "claude", "display_name": "My Claude", "permission_modes": []string{"default", "bypassPermissions"}},
		{"name": "grok", "agent": "paseo", "display_name": "  Grok  ", "permission_modes": []string{"bypassPermissions", "yolo", "default"}},
		{"name": "kimi", "agent": "paseo", "display_name": strings.Repeat("π", 80), "permission_modes": []string{"default"}},
		{"name": "older", "agent": "paseo"},
	}
	dialRuntime(t, n, h)
	enablePaseo(n)
	raw, err := nodeCall(t, n, "launchers.list", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	var res struct {
		Launchers []proto.Launcher `json:"launchers"`
	}
	_ = json.Unmarshal(raw, &res)
	got := map[string]proto.Launcher{}
	for _, l := range res.Launchers {
		if l.Source == "paseo" {
			got[l.Name] = l
		}
	}
	both := proto.PermissionModeSetOf("default", "bypassPermissions")
	for name, want := range map[string]struct {
		display string
		modes   proto.PermissionModeSet
	}{
		"my-claude": {"My Claude", 0},
		"grok":      {"Grok", both},
		"kimi":      {strings.Repeat("π", 64), proto.PermissionModeSetOf("default")},
		"older":     {"", 0},
	} {
		if l := got[name]; l.DisplayName != want.display || l.PermissionModes != want.modes {
			t.Errorf("%s: %q %v, want %q %v", name, l.DisplayName, l.PermissionModes.List(), want.display, want.modes.List())
		}
	}
}

// A launcher whose agents' approval is a switch Allow all turns on (Paseo's Auto Accept) says so in launchers.list, the
// runtime's own launcher too: the manager starts its own Agents there with Allow all (#8141). One the runtime does not
// say it of has no such field, and runtime.launchers carries it as hello does.
func TestRuntimeLauncherAutoAccept(t *testing.T) {
	n, _ := runtimeNode(t)
	h := paseoEngineHello()
	h["launcher"] = "paseo-grok"
	h["launchers"] = []map[string]any{
		{"name": "paseo-claude", "agent": "claude"},
		{"name": "paseo-grok", "agent": "paseo", "auto_accept": true},
		{"name": "paseo-pi", "agent": "paseo"},
	}
	host := dialRuntime(t, n, h)
	enablePaseo(n)
	autoAccept := func() map[string]bool {
		raw, err := nodeCall(t, n, "launchers.list", map[string]any{})
		if err != nil {
			t.Fatal(err)
		}
		var res struct {
			Launchers []proto.Launcher `json:"launchers"`
		}
		_ = json.Unmarshal(raw, &res)
		got := map[string]bool{}
		for _, l := range res.Launchers {
			if l.Source == "paseo" {
				got[l.Name] = l.AutoAccept
			}
		}
		return got
	}
	if got := autoAccept(); len(got) != 3 || !got["paseo-grok"] || got["paseo-claude"] || got["paseo-pi"] {
		t.Fatalf("auto_accept by launcher: %v", got)
	}
	_ = host.peer.Notify("runtime.launchers", map[string]any{"launchers": []map[string]any{
		{"name": "paseo-grok", "agent": "paseo"}, {"name": "paseo-pi", "agent": "paseo", "auto_accept": true},
	}})
	waitFor(t, "the new launchers", func() bool { got := autoAccept(); return got["paseo-pi"] && !got["paseo-grok"] })
}

// The phone's permission mode and effort reach a runtime that takes them; one that does not refuses them.
func TestRuntimeModes(t *testing.T) {
	n, _ := runtimeNode(t)
	host := dialRuntime(t, n, paseoEngineHello())
	_ = host.peer.Notify("runtime.sessions", map[string]any{"sessions": []map[string]any{{"session_id": "paseo-m1", "agent": "claude", "status": "idle"}}})
	waitFor(t, "listed", func() bool { _, ok := listed(t, n)["paseo-m1"]; return ok })
	if _, err := nodeCall(t, n, "session.modes", map[string]any{"session_id": "paseo-m1", "permission_mode": "acceptEdits", "effort": "max"}); err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	_ = json.Unmarshal(host.got("session.modes")[0], &m)
	if m["permission_mode"] != "acceptEdits" || m["effort"] != "max" {
		t.Fatalf("modes %v", m)
	}
	if _, err := nodeCall(t, n, "session.modes", map[string]any{"session_id": "paseo-m1", "permission_mode": "yolo"}); err == nil {
		t.Fatal("an unknown mode was passed on")
	}

	dsh := dialRuntime(t, n, dshHello()) // no caps.modes
	_ = dsh.peer.Notify("runtime.sessions", map[string]any{"sessions": []map[string]any{{"session_id": "session-m2", "status": "idle"}}})
	waitFor(t, "listed", func() bool { _, ok := listed(t, n)["session-m2"]; return ok })
	_, err := nodeCall(t, n, "session.modes", map[string]any{"session_id": "session-m2", "effort": "high"})
	var re *rpc.Error
	if !errors.As(err, &re) || re.Code != "unsupported" || len(dsh.got("session.modes")) != 0 {
		t.Fatalf("a runtime without modes: %v", err)
	}
}

// On a machine whose engine is Paseo every start goes to it: its own launchers, a launcher of the login shell's (Paseo
// picks its provider of that name), a session to continue; with Paseo away nothing starts.
func TestEngineRuntimeStarts(t *testing.T) {
	n, _ := runtimeNode(t)
	if err := n.useEngine(context.Background(), proto.EnginePaseo); err != nil {
		t.Fatal(err)
	}
	_, err := nodeCall(t, n, "session.start", map[string]any{"cwd": "/w/app", "launcher": "claude", "async": true})
	if rpcCode(err) != engineMissing {
		t.Fatalf("Paseo away: %v", err)
	}
	host := dialRuntime(t, n, paseoEngineHello())
	host.answer = func(method string, params json.RawMessage) (any, error) {
		if method == "session.start" {
			return map[string]any{"session": map[string]any{"session_id": "paseo-s1", "status": "idle", "cwd": "/w/app"}}, nil
		}
		return map[string]any{"ok": true}, nil
	}
	raw, err := nodeCall(t, n, "session.start", map[string]any{"cwd": "/w/app", "launcher": "claude", "async": true, "effort": "high"})
	if err != nil {
		t.Fatal(err)
	}
	var res struct {
		Session proto.Session `json:"session"`
	}
	_ = json.Unmarshal(raw, &res)
	if res.Session.SessionID != "paseo-s1" || res.Session.SourceKind != proto.SourceRuntime {
		t.Fatalf("started %s", raw)
	}
	var start map[string]any
	_ = json.Unmarshal(host.got("session.start")[0], &start)
	if start["launcher"] != "claude" || start["shell_launcher"] != true || start["effort"] != "high" {
		t.Fatalf("started with %v", start)
	}
}

// A message to a Claude session not running anywhere, on a machine whose engine is Paseo: Paseo takes it over (the
// same id, its transcript the conversation) and the message goes to it there — never a claude --resume in a terminal.
func TestEngineRuntimeContinuesASession(t *testing.T) {
	n, _ := runtimeNode(t)
	sdkTranscript(t, n, sdkSession, "where we left off")
	if err := n.useEngine(context.Background(), proto.EnginePaseo); err != nil {
		t.Fatal(err)
	}
	host := dialRuntime(t, n, paseoEngineHello())
	host.answer = func(method string, params json.RawMessage) (any, error) {
		if method == "session.start" {
			return map[string]any{"session": map[string]any{"session_id": sdkSession, "transcript": sdkSession, "agent": "claude",
				"status": "idle", "cwd": "/w/app"}}, nil
		}
		return map[string]any{"ok": true}, nil
	}
	if _, err := nodeCall(t, n, "session.send", map[string]any{"session_id": sdkSession, "text": "carry on", "client_msg_id": "m-c1"}); err != nil {
		t.Fatal(err)
	}
	var start map[string]any
	_ = json.Unmarshal(host.got("session.start")[0], &start)
	if start["resume"] != sdkSession || start["cwd"] != "/w/app" {
		t.Fatalf("taken over with %v", start)
	}
	var send map[string]any
	_ = json.Unmarshal(host.got("session.send")[0], &send)
	if send["session_id"] != sdkSession || send["text"] != "carry on" || send["client_msg_id"] != "m-c1" {
		t.Fatalf("sent %v", send)
	}
	// From now on it is Paseo's session: the next message goes straight to it.
	_ = host.peer.Notify("runtime.sessions", map[string]any{"sessions": []map[string]any{{
		"session_id": sdkSession, "transcript": sdkSession, "agent": "claude", "status": "idle", "cwd": "/w/app"}}})
	waitFor(t, "listed by Paseo", func() bool { return listed(t, n)[sdkSession].SourceKind == proto.SourceRuntime })
	if _, err := nodeCall(t, n, "session.send", map[string]any{"session_id": sdkSession, "text": "and more", "client_msg_id": "m-c2"}); err != nil {
		t.Fatal(err)
	}
	if len(host.got("session.start")) != 1 || len(host.got("session.send")) != 2 {
		t.Fatalf("starts %d sends %d", len(host.got("session.start")), len(host.got("session.send")))
	}
}

// A runtime that cannot take sessions over: a message to one not running is refused, nothing sent.
func TestEngineRuntimeWithoutResume(t *testing.T) {
	n, _ := runtimeNode(t)
	sdkTranscript(t, n, sdkSession, "old")
	if err := n.useEngine(context.Background(), proto.EnginePaseo); err != nil {
		t.Fatal(err)
	}
	hello := paseoEngineHello()
	hello["caps"] = map[string]any{"start": true, "send": true}
	host := dialRuntime(t, n, hello)
	_, err := nodeCall(t, n, "session.send", map[string]any{"session_id": sdkSession, "text": "x", "client_msg_id": "m-r"})
	if rpcCode(err) != notRunning || len(host.got("session.start")) != 0 || len(host.got("session.send")) != 0 {
		t.Fatalf("without resume: %v", err)
	}
}

// A session a running claude has open: Paseo is told, takes it only when that claude is its own, and refuses
// otherwise — the refusal says where it is open, nothing is sent.
func TestEngineRuntimeSessionOpenElsewhere(t *testing.T) {
	n, _ := runtimeNode(t)
	sdkTranscript(t, n, sdkSession, "open in a terminal")
	dir := filepath.Join(n.home, ".claude", "sessions")
	_ = os.MkdirAll(dir, 0o700)
	_ = os.WriteFile(filepath.Join(dir, strconv.Itoa(os.Getpid())+".json"), []byte(`{"sessionId":"`+sdkSession+`","entrypoint":"cli"}`), 0o600)
	if err := n.useEngine(context.Background(), proto.EnginePaseo); err != nil {
		t.Fatal(err)
	}
	host := dialRuntime(t, n, paseoEngineHello())
	host.answer = func(method string, params json.RawMessage) (any, error) {
		if method == "session.start" {
			return nil, &rpc.Error{Message: "not ours", Code: "open_elsewhere"}
		}
		return map[string]any{"ok": true}, nil
	}
	_, err := nodeCall(t, n, "session.send", map[string]any{"session_id": sdkSession, "text": "x", "client_msg_id": "m-e"})
	if rpcCode(err) != openElsewhere || !strings.Contains(err.Error(), "电脑上的一个终端") {
		t.Fatalf("open elsewhere: %v", err)
	}
	var start map[string]any
	_ = json.Unmarshal(host.got("session.start")[0], &start)
	if start["open_elsewhere"] != true || len(host.got("session.send")) != 0 {
		t.Fatalf("started with %v, sends %d", start, len(host.got("session.send")))
	}
}

// The regression check of #10941's resume rule on a computer whose engine is Paseo. Paseo lists an archived Agent only
// among the 80 newest archived in the last 7 days, and the node has no transcript under the id Persona named an Agent
// by, so once it was not listed the node had nothing of it and refused an extension's resume (marked
// resume_outside_health) that Paseo takes back through import_agent; while Paseo's bridge reconnected, the extension
// got that refusal where the owner gets engine_missing. Now a start through the engine keeps the node's record of the
// Agent's directory, which the check reads, and with the engine away any resume is engine_missing before the check.
// One started in the health space, or an id the computer has nothing of, is still refused before Paseo is asked.
func TestEngineRuntimeAnExtensionsResumeOfAnUnlistedAgent(t *testing.T) {
	const (
		own    = "bbbbbbbb-0000-4000-8000-000000000077"
		health = "bbbbbbbb-0000-4000-8000-000000000078"
		forged = "bbbbbbbb-0000-4000-8000-000000000079"
	)
	n, _ := runtimeNode(t)
	if err := n.useEngine(context.Background(), proto.EnginePaseo); err != nil {
		t.Fatal(err)
	}
	work := mkdir(t, filepath.Join(n.home, "errands"))
	charts := mkdir(t, filepath.Join(identity.HealthDir(n.home), "charts"))
	host := dialRuntime(t, n, paseoEngineHello())
	host.answer = func(method string, params json.RawMessage) (any, error) {
		var p struct {
			SessionID string `json:"session_id"`
			Resume    string `json:"resume"`
			Cwd       string `json:"cwd"`
		}
		_ = json.Unmarshal(params, &p)
		if method != "session.start" {
			return map[string]any{"ok": true}, nil
		}
		id := p.SessionID
		if p.Resume != "" {
			id = p.Resume
		}
		return map[string]any{"session": map[string]any{"session_id": id, "status": "idle", "cwd": p.Cwd, "launcher": "paseo-claude",
			"started_by_persona": true}}, nil
	}
	for id, cwd := range map[string]string{own: work, health: charts} {
		if _, err := nodeCall(t, n, "session.start", map[string]any{"cwd": cwd, "launcher": "paseo-claude", "session_id": id, "prompt": "go"}); err != nil {
			t.Fatal(err)
		}
		if st, ok := n.state.get(id); !ok || st.Cwd != cwd || st.Launcher != "paseo-claude" || st.EngineRuntime != proto.EnginePaseo {
			t.Errorf("the record of %s started through Paseo: launcher %q, directory %q, engine runtime %q, %v", id, st.Launcher, st.Cwd, st.EngineRuntime, ok)
		}
	}
	// Archived a while ago: Paseo lists neither any more.
	_ = host.peer.Notify("runtime.sessions", map[string]any{"sessions": []map[string]any{}})
	waitFor(t, "Paseo's list emptied", func() bool { _, ok := listed(t, n)[own]; return !ok })
	resume := func(id string, marked bool) error {
		params := map[string]any{"cwd": work, "launcher": "paseo-claude", "resume_session_id": id}
		if marked {
			params["resume_outside_health"] = true
		}
		_, err := nodeCall(t, n, "session.start", params)
		return err
	}
	starts := len(host.got("session.start"))
	for _, id := range []string{health, forged} {
		if err := resume(id, true); rpcCode(err) != proto.CodeResumeUnknown {
			t.Errorf("the extension's resume of %s: %v (code %q), want %s", id, err, rpcCode(err), proto.CodeResumeUnknown)
		}
	}
	if got := len(host.got("session.start")); got != starts {
		t.Fatalf("a refused resume reached Paseo: %d starts, want %d", got, starts)
	}
	if err := resume(own, true); err != nil {
		t.Fatalf("the extension's resume of its own Agent Paseo no longer lists: %v", err)
	}
	var start map[string]any
	_ = json.Unmarshal(host.got("session.start")[starts], &start)
	if start["resume"] != own {
		t.Fatalf("taken back with %v", start)
	}
	if err := resume(health, false); err != nil {
		t.Fatalf("the owner's resume of the health Agent: %v", err)
	}

	// The bridge restarts: every resume, the extension's of any id and the owner's, is engine_missing, as the owner's was.
	_ = host.conn.Close()
	waitFor(t, "Paseo away", func() bool { return n.engineRuntime(context.Background()) == nil })
	for _, id := range []string{own, health, forged} {
		for _, marked := range []bool{true, false} {
			if err := resume(id, marked); rpcCode(err) != engineMissing {
				t.Errorf("resume of %s (marked %v) while Paseo is away: %v (code %q), want %s", id, marked, err, rpcCode(err), engineMissing)
			}
		}
	}
}

func TestRuntimeNativeModelsAndModelControl(t *testing.T) {
	n, _ := runtimeNode(t)
	hello := paseoEngineHello()
	hello["caps"].(map[string]any)["model"] = true
	hello["caps"].(map[string]any)["models"] = true
	host := dialRuntime(t, n, hello)
	enablePaseo(n)
	host.mu.Lock()
	host.answer = func(method string, params json.RawMessage) (any, error) {
		if method == "launchers.models" {
			return proto.LauncherModels{Models: []proto.AgentModel{{ID: "future-model", Name: "Future Model"}}}, nil
		}
		return map[string]any{"ok": true}, nil
	}
	host.mu.Unlock()
	got, err := n.launcherModels(context.Background(), "paseo-claude", "", true)
	if err != nil || len(got.Models) != 1 || got.Models[0].ID != "future-model" {
		t.Fatalf("native models: %+v %v", got, err)
	}
	var discovery map[string]any
	_ = json.Unmarshal(host.got("launchers.models")[0], &discovery)
	if discovery["refresh"] != true {
		t.Fatalf("refresh lost: %v", discovery)
	}
	_ = host.peer.Notify("runtime.sessions", map[string]any{"sessions": []map[string]any{{"session_id": "paseo-model", "agent": "claude", "status": "idle"}}})
	waitFor(t, "listed", func() bool { _, ok := listed(t, n)["paseo-model"]; return ok })
	row := listed(t, n)["paseo-model"]
	if row.Capabilities.Model == nil || !*row.Capabilities.Model {
		t.Fatal("model capability missing")
	}
	if _, err := nodeCall(t, n, "session.modes", map[string]any{"session_id": row.SessionID, "model": "future-model"}); err != nil {
		t.Fatal(err)
	}
	var change map[string]any
	_ = json.Unmarshal(host.got("session.modes")[0], &change)
	if change["model"] != "future-model" {
		t.Fatalf("model lost: %v", change)
	}
	host.mu.Lock()
	host.answer = func(string, json.RawMessage) (any, error) { return nil, errors.New("native catalog unavailable") }
	host.mu.Unlock()
	if _, err := n.launcherModels(context.Background(), "paseo-claude", "", true); err == nil {
		t.Fatal("native failure was hidden")
	}
}

func TestRuntimeWithoutModelCapabilityRejectsModel(t *testing.T) {
	n, _ := runtimeNode(t)
	host := dialRuntime(t, n, paseoEngineHello())
	_ = host.peer.Notify("runtime.sessions", map[string]any{"sessions": []map[string]any{{"session_id": "paseo-model", "agent": "claude", "status": "idle"}}})
	waitFor(t, "listed", func() bool { _, ok := listed(t, n)["paseo-model"]; return ok })
	if _, err := nodeCall(t, n, "session.modes", map[string]any{"session_id": "paseo-model", "model": "future"}); err == nil {
		t.Fatal("unadvertised model switch passed")
	}
	if len(host.got("session.modes")) != 0 {
		t.Fatal("unsupported model reached runtime")
	}
}

func TestRuntimeLauncherAvailabilityCheck(t *testing.T) {
	n, _ := runtimeNode(t)
	hello := paseoEngineHello()
	hello["caps"].(map[string]any)["check"] = true
	host := dialRuntime(t, n, hello)
	enablePaseo(n)
	var workspace string
	host.mu.Lock()
	host.answer = func(method string, params json.RawMessage) (any, error) {
		if method != "launchers.check" {
			return nil, errors.New("unexpected shell fallback")
		}
		var p struct{ Launcher, Cwd, Prompt, Model string }
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		workspace = p.Cwd
		if _, err := os.Stat(p.Cwd); err != nil {
			return nil, err
		}
		if p.Launcher != "paseo-claude" || p.Prompt == "" || p.Model != "opus" {
			return nil, errors.New("missing check parameters")
		}
		return proto.LauncherCheck{OK: true, Model: "native-model", Answer: "OK"}, nil
	}
	host.mu.Unlock()
	result := n.launcherCheck(context.Background(), "paseo-claude", "opus")
	if !result.OK || result.Model != "native-model" || result.Agent != "claude" || result.Answer != "OK" {
		t.Fatalf("native check: %+v", result)
	}
	if _, err := os.Stat(workspace); !os.IsNotExist(err) {
		t.Fatalf("check workspace survived: %v", err)
	}
	host.mu.Lock()
	host.answer = func(string, json.RawMessage) (any, error) {
		return nil, errors.New("native authentication unavailable")
	}
	host.mu.Unlock()
	result = n.launcherCheck(context.Background(), "paseo-claude")
	if result.OK || !strings.Contains(result.Error, "native authentication unavailable") {
		t.Fatalf("native error lost: %+v", result)
	}
	if len(host.got("launchers.check")) != 2 {
		t.Fatal("runtime check did not own the result")
	}
}

func TestRuntimeAvailabilityCheckRequiresOptIn(t *testing.T) {
	n, _ := runtimeNode(t)
	host := dialRuntime(t, n, paseoEngineHello())
	if _, handled := n.runtimeLauncherCheck(context.Background(), n.runtimes.launcherRuntime("paseo-claude"), "paseo-claude", t.TempDir(), "probe", ""); handled {
		t.Fatal("older runtime intercepted the shell check")
	}
	if len(host.got("launchers.check")) != 0 {
		t.Fatal("unsupported check reached runtime")
	}
}

// The launcher a runtime runs the assistant on carries the Agent launcher it was set up to run it for
// (proto.Launcher.AssistantFor: integrations/grok's grok-chat for the Paseo provider grok); another launcher of it, a
// name no launcher may have and its own name do not.
func TestRuntimeAssistantLauncherSaysWhatItRunsTheAssistantFor(t *testing.T) {
	n, _ := runtimeNode(t)
	h := map[string]any{"runtime": "grok-chat", "name": "Grok", "agent": "grok", "launcher": "grok-chat",
		"caps": map[string]any{"start": true, "send": true, "interrupt": true, "assistant": true},
		"launchers": []map[string]any{
			{"name": "grok-chat", "agent": "grok", "display_name": "Grok", "assistant_for": "grok"},
			{"name": "grok-side", "agent": "grok", "assistant_for": "grok"},
		}}
	host := dialRuntime(t, n, h)
	got := func() map[string]proto.Launcher {
		raw, err := nodeCall(t, n, "launchers.list", map[string]any{})
		if err != nil {
			t.Fatal(err)
		}
		var res struct {
			Launchers []proto.Launcher `json:"launchers"`
		}
		_ = json.Unmarshal(raw, &res)
		out := map[string]proto.Launcher{}
		for _, l := range res.Launchers {
			out[l.Name] = l
		}
		return out
	}
	ls := got()
	if l := ls["grok-chat"]; !l.Assistant || l.AssistantFor != "grok" || l.DisplayName != "Grok" {
		t.Fatalf("grok-chat: %+v", l)
	}
	if l := ls["grok-side"]; l.Assistant || l.AssistantFor != "" {
		t.Fatalf("a launcher that does not run the assistant says what it runs it for: %+v", l)
	}
	for _, bad := range []string{"bad name!", "grok-chat"} {
		_ = host.peer.Notify("runtime.launchers", map[string]any{"launchers": []map[string]any{{"name": "grok-chat", "agent": "grok", "assistant_for": bad}}})
		waitFor(t, "the new launchers "+bad, func() bool { l := got()["grok-chat"]; return l.Assistant && l.AssistantFor == "" })
	}
}

// The record an engine runtime's start keeps says whose start its directory is (EngineRuntime), and only while it is:
// a launcher switch carried out elsewhere moves the directory and carries the switch's (none for a terminal), a start
// that writes the record anew has none, and putting back the record a failed switch wrote over puts back its own. An
// extension's resume of an unlisted Agent passes on such a record alone (resumeOutsideHealth, #10941), so a directory
// a resume or a switch wrote must never keep it. It goes to the hub's copy under engine_runtime (db.NodeSession).
func TestEngineRuntimeRecordFollowsItsDirectory(t *testing.T) {
	s := newState()
	agent := sessionState{Launcher: "paseo-claude", Cwd: "/work/errands", EngineRuntime: proto.EnginePaseo}
	s.put("a", agent)
	s.adoptSwitch("a", sessionState{Launcher: "claude", Cwd: "/work/elsewhere"})
	if st, _ := s.get("a"); st.Cwd != "/work/elsewhere" || st.EngineRuntime != "" {
		t.Fatalf("after a switch carried out elsewhere: %+v", st)
	}
	s.put("b", agent)
	s.put("b", sessionState{Launcher: "claude", Cwd: "/work/elsewhere"}) // a terminal start writes the record anew
	if st, _ := s.get("b"); st.EngineRuntime != "" {
		t.Fatalf("after a terminal start: %+v", st)
	}
	s.restoreLaunch("b", agent, true, "")
	if st, _ := s.get("b"); st.Cwd != agent.Cwd || st.EngineRuntime != proto.EnginePaseo {
		t.Fatalf("after putting the record back: %+v", st)
	}
	raw, err := json.Marshal(agent)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"engine_runtime":"paseo"`) {
		t.Fatalf("the record as the hub keeps it: %s", raw)
	}
}
