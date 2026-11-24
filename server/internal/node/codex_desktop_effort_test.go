package node

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// withCodexAppModels has the App's codex list models for home, as read just now.
func withCodexAppModels(t *testing.T, home string, models map[string]codexModelEfforts) {
	t.Helper()
	codexAppModelLists.Lock()
	codexAppModelLists.at[home], codexAppModelLists.got[home] = time.Now(), models
	codexAppModelLists.Unlock()
	t.Cleanup(func() {
		codexAppModelLists.Lock()
		delete(codexAppModelLists.at, home)
		delete(codexAppModelLists.got, home)
		codexAppModelLists.Unlock()
	})
}

// appEffortChat is a chat of a running Codex App (appChat, in no project) whose last turn ran gpt-x at high an hour
// ago, owned by a window of the fake bus; the App's codex lists gpt-x with three levels.
func appEffortChat(t *testing.T) (n *Node, id string, bus *fakeCodexBus, rollout string) {
	return appEffortChatOwned(t, appChat, "")
}

// appEffortChatOwned is appEffortChat with the bus's window owning owned, and later once the App is asked to open it.
func appEffortChatOwned(t *testing.T, owned, later string) (n *Node, id string, bus *fakeCodexBus, rollout string) {
	t.Helper()
	home, bus := startFakeCodexBus(t, owned)
	bus.later = later
	if err := os.WriteFile(filepath.Join(home, ".codex-global-state.json"), []byte(`{"projectless-thread-ids":["`+appChat+`"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cwd := t.TempDir()
	rollout = writeAppRollout(t, home, appChat, cwd, time.Now())
	at := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano)
	cwdJSON, err := json.Marshal(cwd)
	if err != nil {
		t.Fatal(err)
	}
	appendLines(t, rollout,
		`{"timestamp":"`+at+`","type":"turn_context","payload":{"cwd":`+string(cwdJSON)+`,"model":"gpt-x","effort":"high","approval_policy":"never","sandbox_policy":{"type":"danger-full-access"}}}`,
		`{"timestamp":"`+at+`","type":"event_msg","payload":{"type":"task_started"}}`,
		`{"timestamp":"`+at+`","type":"event_msg","payload":{"type":"task_complete"}}`)
	n = remoteTestNode(t)
	n.configureCodex(proto.CodexConfig{Homes: []string{home}})
	id = n.registerCodex(home, appChat, rollout)
	src, _ := n.codexSource(id) // the home as the node spells it
	withCodexAppModels(t, src.home, map[string]codexModelEfforts{"gpt-x": {levels: []string{"low", "high", "ultra"}, def: "low"}})
	return n, id, bus, rollout
}

func appChatRow(t *testing.T, n *Node) proto.Session {
	t.Helper()
	for _, s := range n.codexSessions(map[string]bool{}) {
		if s.SourceThreadID == appChat {
			return s
		}
	}
	t.Fatal("the App's chat is not listed")
	return proto.Session{}
}

func modesCall(t *testing.T, n *Node, id string, params map[string]any) error {
	t.Helper()
	params["session_id"] = id
	b, _ := json.Marshal(params)
	_, err := n.handle(context.Background(), "session.modes", b)
	return err
}

// A chat of the App offers its model's levels and the effort its last turn ran with; the phone's choice goes to the
// window that owns it for the next turn, as the App's picker does, and shows on the row until a turn starts after it.
func TestCodexAppChatEffortIsSetInTheOwningWindowForItsNextTurn(t *testing.T) {
	n, id, bus, rollout := appEffortChat(t)
	s := appChatRow(t, n)
	// As a thread on an app-server: efforts, and no modes capability (the apps would offer the permission mode too).
	if !slices.Equal(s.Efforts, []string{"low", "high", "ultra"}) || s.Effort != "high" || !s.Capabilities.Send || s.Capabilities.Modes != nil {
		t.Fatalf("row: efforts %v, effort %q, send %v, modes %v", s.Efforts, s.Effort, s.Capabilities.Send, s.Capabilities.Modes)
	}
	if err := modesCall(t, n, id, map[string]any{"effort": "ultra"}); err != nil {
		t.Fatal(err)
	}
	select {
	case p := <-bus.settings:
		want := `{"conversationId":"` + appChat + `","threadSettings":{"effort":"ultra"}}`
		if b, _ := json.Marshal(p); string(b) != want { // no activeTurnId: the next turn, not the running one
			t.Fatalf("update = %s, want %s", b, want)
		}
	default:
		t.Fatal("no update reached the owning window")
	}
	if s := appChatRow(t, n); s.Effort != "ultra" {
		t.Fatalf("after the change the row shows %q", s.Effort)
	}
	// A turn after it records what it ran with: that tells from then on (here the App's picker was changed since).
	at := time.Now().Add(time.Second).UTC().Format(time.RFC3339Nano)
	appendLines(t, rollout, `{"timestamp":"`+at+`","type":"turn_context","payload":{"model":"gpt-x","effort":"low"}}`)
	n.mu.Lock()
	clear(n.metas) // read again now, not after metaMinInterval
	n.mu.Unlock()
	if s := appChatRow(t, n); s.Effort != "low" {
		t.Fatalf("after a later turn the row shows %q, want the turn's", s.Effort)
	}
}

// A change the App does not confirm is not shown as made: refused by the window, not applied, read back otherwise,
// or for a level the model does not take (nothing sent).
func TestCodexAppChatEffortNotConfirmedIsNotShown(t *testing.T) {
	for _, c := range []struct {
		name   string
		set    func(b *fakeCodexBus)
		effort string
		code   string // a refusal's code; "" an error
		sent   bool
	}{
		{"refused", func(b *fakeCodexBus) { b.refuse = "thread-stream-owner-unavailable" }, "ultra", "codex_refused", true},
		{"not applied", func(b *fakeCodexBus) { b.notApplied = true }, "ultra", "codex_refused", true},
		{"read back unchanged", func(b *fakeCodexBus) { b.unchanged = true }, "ultra", "", true},
		{"a level the model lacks", func(*fakeCodexBus) {}, "max", "", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			n, id, bus, _ := appEffortChat(t)
			bus.mu.Lock()
			c.set(bus)
			bus.mu.Unlock()
			err := modesCall(t, n, id, map[string]any{"effort": c.effort})
			if err == nil {
				t.Fatal("taken")
			}
			if c.code != "" {
				if code, _ := refusalOf(t, err); code != c.code {
					t.Fatalf("refusal %s (%v), want %s", code, err, c.code)
				}
			}
			select {
			case p := <-bus.settings:
				if !c.sent {
					t.Fatalf("sent %v", p)
				}
			default:
				if c.sent {
					t.Fatal("nothing reached the window")
				}
			}
			if s := appChatRow(t, n); s.Effort != "high" {
				t.Fatalf("the row shows %q, want the last turn's", s.Effort)
			}
		})
	}
}

// Model and effort change in the owning window (preserving provider-prefixed slugs and reconciling incompatible
// efforts), while permission modes and invalid model IDs are refused without reaching the App.
func TestCodexAppChatSwitchesModelAndEffort(t *testing.T) {
	n, id, bus, rollout := appEffortChat(t)
	src, _ := n.codexSource(id)
	withCodexAppModels(t, src.home, map[string]codexModelEfforts{
		"gpt-x":          {levels: []string{"low", "high", "ultra"}, def: "low"},
		"acme/gpt-5-pro": {levels: []string{"low", "medium"}, def: "medium"},
		"beta/gpt-5-pro": {levels: []string{"low", "high", "xhigh"}, def: "high"},
	})
	if s := appChatRow(t, n); s.Capabilities.Model == nil || !*s.Capabilities.Model {
		t.Fatalf("row should advertise model capability: %+v", s.Capabilities)
	}
	for _, p := range []map[string]any{{"permission_mode": "plan"}, {"model": "-bad"}} {
		if err := modesCall(t, n, id, p); err == nil {
			t.Fatalf("%v taken", p)
		}
	}
	select {
	case p := <-bus.settings:
		t.Fatalf("sent %v", p)
	default:
	}

	// Switching from gpt-x (effort=high) to acme/gpt-5-pro (levels=[low, medium], def=medium) reconciles effort.
	if err := modesCall(t, n, id, map[string]any{"model": "acme/gpt-5-pro"}); err != nil {
		t.Fatal(err)
	}
	p := <-bus.settings
	if b, _ := json.Marshal(p); string(b) != `{"conversationId":"`+appChat+`","threadSettings":{"effort":"medium","model":"acme/gpt-5-pro"}}` {
		t.Fatalf("reconciled update = %s", b)
	}
	if s := appChatRow(t, n); s.Model != "acme/gpt-5-pro" || s.Effort != "medium" || !slices.Equal(s.Efforts, []string{"low", "medium"}) {
		t.Fatalf("after model switch: model=%q effort=%q efforts=%v", s.Model, s.Effort, s.Efforts)
	}

	// Sequential SessionModelPanel two-step pick: {model} then {effort} validates against the newly picked model.
	if err := modesCall(t, n, id, map[string]any{"model": "beta/gpt-5-pro"}); err != nil {
		t.Fatal(err)
	}
	<-bus.settings
	if err := modesCall(t, n, id, map[string]any{"effort": "xhigh"}); err != nil {
		t.Fatalf("second step effort against picked model failed: %v", err)
	}
	<-bus.settings
	if s := appChatRow(t, n); s.Model != "beta/gpt-5-pro" || s.Effort != "xhigh" {
		t.Fatalf("after two-step pick: model=%q effort=%q", s.Model, s.Effort)
	}

	// A subsequent turn_context in the rollout supersedes the picked model even when effort is omitted.
	at := time.Now().Add(time.Second).UTC().Format(time.RFC3339Nano)
	appendLines(t, rollout, `{"timestamp":"`+at+`","type":"turn_context","payload":{"model":"gpt-x"}}`)
	n.mu.Lock()
	clear(n.metas)
	n.mu.Unlock()
	if s := appChatRow(t, n); s.Model != "gpt-x" {
		t.Fatalf("after later turn_context model=%q, want gpt-x", s.Model)
	}
}

// A chat no window has loaded is opened in the App first, as for a message; one the App does not load is refused.
func TestCodexAppChatEffortOpensAChatTheAppHasNotLoaded(t *testing.T) {
	n, id, bus, _ := appEffortChatOwned(t, "another-thread", appChat)
	defer func(f func(string) error, w time.Duration) { openCodexThread, codexDesktopLoadWait = f, w }(openCodexThread, codexDesktopLoadWait)
	codexDesktopLoadWait = 2 * time.Second
	openCodexThread = func(string) error { return nil } // the App does not load it
	if code, _ := refusalOf(t, modesCall(t, n, id, map[string]any{"effort": "ultra"})); code != "codex_desktop_not_loaded" {
		t.Fatalf("refusal %s", code)
	}
	var opened string
	openCodexThread = func(thread string) error { opened = thread; bus.opened.Store(true); return nil }
	if err := modesCall(t, n, id, map[string]any{"effort": "ultra"}); err != nil || opened != appChat {
		t.Fatalf("%v, opened %q", err, opened)
	}
	if p := <-bus.settings; p["conversationId"] != appChat {
		t.Fatalf("update for %v", p["conversationId"])
	}
}

// The levels are the chat's model's on the App's codex, read in the background: Codex's whole list until then and for a
// model the App does not list; none for a model that takes no effort, and then the row offers no change.
func TestCodexAppEffortsAreTheModelsOnTheAppsCodex(t *testing.T) {
	home := t.TempDir()
	defer func(f func(context.Context, string) (map[string]codexModelEfforts, error)) { readCodexAppModels = f }(readCodexAppModels)
	asked := make(chan string, 4)
	readCodexAppModels = func(_ context.Context, h string) (map[string]codexModelEfforts, error) {
		asked <- h
		return map[string]codexModelEfforts{"gpt-x": {levels: []string{"low", "ultra"}}, "gpt-plain": {levels: []string{}}}, nil
	}
	t.Cleanup(func() {
		codexAppModelLists.Lock()
		delete(codexAppModelLists.at, home)
		delete(codexAppModelLists.got, home)
		codexAppModelLists.Unlock()
	})
	n := remoteTestNode(t)
	if got := n.codexAppEfforts(home, "gpt-x"); !slices.Equal(got, codexEffortLevels) {
		t.Fatalf("before the list: %v", got)
	}
	select {
	case h := <-asked:
		if h != home {
			t.Fatalf("asked for %q", h)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the App's codex was not asked")
	}
	waitFor(t, "the model's own levels", func() bool { return slices.Equal(n.codexAppEfforts(home, "gpt-x"), []string{"low", "ultra"}) })
	if got := n.codexAppEfforts(home, "gpt-unlisted"); !slices.Equal(got, codexEffortLevels) {
		t.Fatalf("an unlisted model: %v", got)
	}
	if got := n.codexAppEfforts(home, "gpt-plain"); len(got) != 0 {
		t.Fatalf("a model without efforts: %v", got)
	}
	s := proto.Session{SessionID: "x", Model: "gpt-plain", Capabilities: &proto.SessionCapabilities{Send: true}}
	n.codexDesktopEffortRow(&s, codexSource{home: home}, quickMeta{model: "gpt-plain"})
	if s.Efforts != nil || s.Effort != "" {
		t.Fatalf("a model without efforts offers %v (%q)", s.Efforts, s.Effort)
	}
	select {
	case h := <-asked:
		t.Fatalf("asked again within the TTL (%s)", h)
	default:
	}
}

// The App's codex is asked over stdio as the App's client, hidden models included, its efforts per model.
func TestReadCodexAppModelsAsksTheAppsCodex(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix shell fixture; Windows coverage uses TestWindowsReadCodexAppModelsAsksTheAppsCodex")
	}
	bin, seen := t.TempDir(), filepath.Join(t.TempDir(), "seen")
	cli := filepath.Join(bin, "codex")
	list := `{"id":2,"result":{"data":[{"model":"gpt-x","hidden":true,"defaultReasoningEffort":"low","supportedReasoningEfforts":[{"reasoningEffort":"low"},{"reasoningEffort":"ultra"}]}],"nextCursor":null}}`
	script := `#!/bin/sh
echo "args:$* home:$CODEX_HOME" > ` + seen + `
read l1; echo "$l1" >> ` + seen + `; printf '{"id":1,"result":{}}\n'
read l2
read l3; echo "$l3" >> ` + seen + `; printf '%s\n' '` + list + `'
read l4
`
	if err := os.WriteFile(cli, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	defer func(f func() (string, error)) { codexAppCLI = f }(codexAppCLI)
	codexAppCLI = func() (string, error) { return cli, nil }
	home := t.TempDir()
	models, err := readCodexAppModels(context.Background(), home)
	if err != nil {
		t.Fatal(err)
	}
	if m := models["gpt-x"]; !slices.Equal(m.levels, []string{"low", "ultra"}) || m.def != "low" {
		t.Fatalf("models = %+v", models)
	}
	b, _ := os.ReadFile(seen)
	for _, want := range []string{"args:app-server home:" + home, `"name":"persona_codex_app"`, `"method":"model/list","params":{"includeHidden":true}`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("missing %s in:\n%s", want, b)
		}
	}
}

// A home whose App's codex cannot list its models is asked again after each TTL, but the same failure is logged once
// (#9845): again only when it changes, or after a read that worked.
func TestCodexAppModelListFailureIsLoggedOnce(t *testing.T) {
	home := t.TempDir()
	defer func(f func(context.Context, string) (map[string]codexModelEfforts, error)) { readCodexAppModels = f }(readCodexAppModels)
	var mu sync.Mutex
	answer := func() (map[string]codexModelEfforts, error) { return nil, errors.New("codex not found in PATH") }
	asks := 0
	readCodexAppModels = func(context.Context, string) (map[string]codexModelEfforts, error) {
		mu.Lock()
		defer mu.Unlock()
		asks++
		return answer()
	}
	t.Cleanup(func() {
		codexAppModelLists.Lock()
		delete(codexAppModelLists.at, home)
		delete(codexAppModelLists.got, home)
		delete(codexAppModelLists.failed, home)
		codexAppModelLists.Unlock()
	})
	n := remoteTestNode(t)
	var logs syncBuffer
	n.logger = log.New(&logs, "", 0)
	read := func(want int, answering func() (map[string]codexModelEfforts, error)) {
		t.Helper()
		mu.Lock()
		answer = answering
		mu.Unlock()
		codexAppModelLists.Lock()
		if _, ok := codexAppModelLists.at[home]; ok {
			codexAppModelLists.at[home] = time.Now().Add(-codexAppModelsTTL) // its TTL is over
		}
		codexAppModelLists.Unlock()
		n.codexAppEfforts(home, "gpt-x")
		waitFor(t, "the App's codex asked", func() bool {
			mu.Lock()
			defer mu.Unlock()
			codexAppModelLists.Lock()
			defer codexAppModelLists.Unlock()
			at, ok := codexAppModelLists.at[home]
			return asks == want && ok && time.Since(at) < codexAppModelsTTL
		})
	}
	failing := func(msg string) func() (map[string]codexModelEfforts, error) {
		return func() (map[string]codexModelEfforts, error) { return nil, errors.New(msg) }
	}
	logged := func() int { return strings.Count(logs.String(), "its chats offer every effort level") }
	read(1, failing("codex not found in PATH"))
	read(2, failing("codex not found in PATH"))
	read(3, failing("codex not found in PATH"))
	if got := logged(); got != 1 {
		t.Fatalf("the same failure three times was logged %d times:\n%s", got, logs.String())
	}
	read(4, failing("timed out"))
	if got := logged(); got != 2 {
		t.Fatalf("a different failure was not logged (%d lines):\n%s", got, logs.String())
	}
	read(5, func() (map[string]codexModelEfforts, error) {
		return map[string]codexModelEfforts{"gpt-x": {levels: []string{"low"}}}, nil
	})
	read(6, failing("timed out"))
	if got := logged(); got != 3 {
		t.Fatalf("a failure after a read that worked was not logged (%d lines):\n%s", got, logs.String())
	}
}
