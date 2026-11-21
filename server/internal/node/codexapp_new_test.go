//go:build !windows

package node

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/osproc"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// fakeCodexAppSeed stands in for the App's codex starting a chat: it records what it was asked and writes the chat's
// rollout (as thread/name/set has the app-server do) under the App's home, with the originator Persona's chats carry.
type fakeCodexAppSeed struct {
	mu    sync.Mutex
	seeds []codexAppSeed
	fail  error
}

type codexAppSeed struct{ cli, home, dir, title, thread string }

func (f *fakeCodexAppSeed) seeded() []codexAppSeed {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]codexAppSeed(nil), f.seeds...)
}

func writeCodexAppRollout(t *testing.T, home, thread, originator, cwd string) string {
	t.Helper()
	dir := filepath.Join(home, "sessions", "2026", "11", "12")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "rollout-2026-11-12T09-00-00-"+thread+".jsonl")
	meta := `{"timestamp":"2026-11-12T09:00:00Z","type":"session_meta","payload":{"id":"` + thread + `","originator":"` + originator + `","source":"vscode","cwd":"` + cwd + `"}}`
	if err := os.WriteFile(p, []byte(meta+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// codexAppNode is a node whose Codex home has the App's bus (one window owning nothing yet: the App opens a chat at
// codex://threads/<id> and owns it from then on), with the App's codex and its chat start faked.
func codexAppNode(t *testing.T) (*Node, *fakeCodexBus, *fakeCodexAppSeed, *[]string) {
	t.Helper()
	busHome, bus := startFakeCodexBus(t, "")
	n, _ := testNode(t)
	n.dir, n.sessionScope = t.TempDir(), &nodeSessionScope{codex: busHome}
	shellLaunchers(t)
	cli := filepath.Join(t.TempDir(), "codex")
	if err := os.WriteFile(cli, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	seed := &fakeCodexAppSeed{}
	opened := &[]string{}
	oldCLI, oldSeed, oldOpen := codexAppCLI, seedCodexAppChat, openCodexThread
	codexAppCLI = func() (string, error) { return cli, nil }
	seedCodexAppChat = func(_ context.Context, cli, home, dir, title string) (string, error) {
		seed.mu.Lock()
		defer seed.mu.Unlock()
		if seed.fail != nil {
			return "", seed.fail
		}
		thread := newUUID()
		seed.seeds = append(seed.seeds, codexAppSeed{cli, home, dir, title, thread})
		writeCodexAppRollout(t, home, thread, codexAppClient, dir)
		bus.later = thread
		return thread, nil
	}
	var mu sync.Mutex
	openCodexThread = func(thread string) error {
		mu.Lock()
		*opened = append(*opened, thread)
		mu.Unlock()
		bus.opened.Store(true)
		return nil
	}
	t.Cleanup(func() { codexAppCLI, seedCodexAppChat, openCodexThread = oldCLI, oldSeed, oldOpen })
	return n, bus, seed, opened
}

func startCodexApp(t *testing.T, n *Node, params map[string]any) (proto.Session, error) {
	t.Helper()
	b, _ := json.Marshal(params)
	res, err := n.handle(context.Background(), "session.start", b)
	if err != nil {
		return proto.Session{}, err
	}
	out, _ := json.Marshal(res)
	var r struct {
		Session proto.Session `json:"session"`
	}
	if err := json.Unmarshal(out, &r); err != nil {
		t.Fatal(err)
	}
	return r.Session, nil
}

// New Agent with the Codex App's launcher starts the chat with the App's codex (no turn), then the App opens it and
// runs its first message: the chat is the App's, in its sidebar, and Persona lists it as one of the App's chats.
func TestNewAgentWithTheCodexAppLauncherStartsTheChatInTheApp(t *testing.T) {
	n, bus, seed, opened := codexAppNode(t)
	res, err := n.handle(context.Background(), "launchers.list", nil)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := json.Marshal(res); !strings.Contains(string(b), `"name":"codex-app","base_url":"","model":"","source":"codex_app","agent":"codex"`) {
		t.Fatalf("launchers: %s", b)
	}
	dir := t.TempDir()
	s, err := startCodexApp(t, n, map[string]any{"cwd": dir, "launcher": "codex-app", "prompt": "Fix the CSV export\nand test it"})
	if err != nil {
		t.Fatal(err)
	}
	seeds := seed.seeded()
	if len(seeds) != 1 || seeds[0].dir != dir || seeds[0].title != "Fix the CSV export" || seeds[0].home != n.codexHome() {
		t.Fatalf("seeded %+v", seeds)
	}
	thread := seeds[0].thread
	if s.SessionID != thread || s.Cwd != dir || s.Agent != "codex" || s.Title != "Fix the CSV export" {
		t.Fatalf("session %+v", s)
	}
	if len(*opened) != 1 || (*opened)[0] != thread {
		t.Fatalf("opened %v", *opened)
	}
	var p map[string]any
	select {
	case p = <-bus.turns:
	case <-time.After(10 * time.Second):
		t.Fatal("the App ran no turn")
	}
	req := p["turnStart"].(map[string]any)["request"].(map[string]any)
	if in := req["input"].([]any)[0].(map[string]any); req["threadId"] != thread || in["text"] != "Fix the CSV export\nand test it" {
		t.Fatalf("turn %v", req)
	}
	if !codexDesktopChat(codexSource{home: n.codexHome(), thread: thread, path: rolloutPath(n.codexHome(), thread)}) {
		t.Fatal("not listed as one of the App's chats")
	}
}

// With no folder the chat gets one of its own where the App keeps a chat in no project, named after the title.
func TestACodexAppStartWithNoFolderGetsOneWhereTheAppKeepsChats(t *testing.T) {
	n, _, seed, _ := codexAppNode(t)
	day := filepath.Join(n.home, "Documents", "Codex", time.Now().Format("2006-01-02"))
	if err := os.MkdirAll(filepath.Join(day, "csv-export"), 0o755); err != nil { // another chat's
		t.Fatal(err)
	}
	s, err := startCodexApp(t, n, map[string]any{"cwd": "", "launcher": "codex-app", "prompt": "CSV export: 修复编码\nand the rest"})
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(day, "csv-export-2"); s.Cwd != want || seed.seeded()[0].dir != want {
		t.Fatalf("cwd %s, want %s", s.Cwd, want)
	}
	if st, err := os.Stat(s.Cwd); err != nil || !st.IsDir() {
		t.Fatal("its folder was not made")
	}
}

// What the App could not run as asked is refused before a chat is started: no first message (a chat with none is not
// in the App's sidebar), a read-only plan, a folder that is not there, a secret hand-off, the App not running.
func TestACodexAppStartRefusesWhatTheAppCannotRun(t *testing.T) {
	n, _, seed, opened := codexAppNode(t)
	dir := t.TempDir()
	for _, c := range []struct {
		params map[string]any
		code   string
		says   string
	}{
		{map[string]any{"cwd": dir, "launcher": "codex-app", "prompt": "  "}, "", "第一条消息"},
		{map[string]any{"cwd": dir, "launcher": "codex-app", "prompt": "look", "permission_mode": "plan"}, "readonly_unsupported", ""},
		{map[string]any{"cwd": filepath.Join(dir, "a", "b"), "launcher": "codex-app", "prompt": "hi"}, "", "does not exist"},
		{map[string]any{"cwd": dir, "launcher": "codex-app", "prompt": "hi", "secret_ticket": strings.Repeat("a", 64)}, secretEnvAgentCode, ""},
	} {
		_, err := startCodexApp(t, n, c.params)
		var re *rpc.Error
		if err == nil || c.code != "" && (!errors.As(err, &re) || re.Code != c.code) || !strings.Contains(err.Error(), c.says) {
			t.Errorf("%v: %v, want %q %q", c.params, err, c.code, c.says)
		}
	}
	if len(seed.seeded()) != 0 || len(*opened) != 0 {
		t.Fatalf("seeded %v, opened %v", seed.seeded(), *opened)
	}
	n.sessionScope = &nodeSessionScope{codex: t.TempDir()} // a home with no bus: the App is not running for it
	if _, err := startCodexApp(t, n, map[string]any{"cwd": dir, "launcher": "codex-app", "prompt": "hi"}); err == nil || !strings.Contains(err.Error(), "没在运行") {
		t.Fatalf("no App: %v", err)
	}
}

// The launcher is offered where the App runs for the node's Codex home and a Codex CLI is found; a login shell's of
// its name runs instead. Its models are the App's; Settings › Models' Test has nothing to run; no terminal runs it.
func TestTheCodexAppLauncherIsOfferedWhereTheAppRuns(t *testing.T) {
	n, _, _, _ := codexAppNode(t)
	list, added := n.withCodexAppLauncher([]proto.Launcher{{Name: "codex"}})
	if !added || list[1] != (proto.Launcher{Name: "codex-app", Source: "codex_app", Agent: "codex"}) {
		t.Fatalf("list %+v", list)
	}
	if models, err := n.launcherModels(context.Background(), "codex-app", "", false); err != nil || models.Agent != "codex" || len(models.Models) != 0 || models.Locked != "" {
		t.Fatalf("models %+v %v", models, err)
	}
	cacheJSON := `{"models":[
		{"slug":"acme/gpt-5-pro","display_name":"Acme gpt-5-pro","visibility":"list","default_reasoning_level":"medium","supported_reasoning_levels":[{"effort":"low"},{"effort":"medium"},{"effort":"high"}]},
		{"slug":"beta/gpt-5-pro","display_name":"Beta gpt-5-pro","visibility":"list","default_reasoning_level":"high","supported_reasoning_levels":[{"effort":"high"},{"effort":"xhigh"}]},
		{"slug":"hidden/internal","visibility":"hide"}
	]}`
	if err := os.WriteFile(filepath.Join(n.codexHome(), "models_cache.json"), []byte(cacheJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(n.codexHome(), "config.toml"), []byte("model = \"beta/gpt-5-pro\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if models, err := n.launcherModels(context.Background(), "codex-app", "", true); err != nil || models.Locked != "" || len(models.Models) != 2 || models.Models[0].ID != "acme/gpt-5-pro" || models.Models[0].Name != "Acme gpt-5-pro" || !models.Models[1].Default {
		t.Fatalf("cached models %+v %v", models, err)
	}
	if !n.knownLauncher(context.Background(), "codex-app") {
		t.Fatal("not known")
	}
	if c := n.launcherCheck(context.Background(), "codex-app"); c.Error != codexAppCheckMsg {
		t.Fatalf("check %+v", c)
	}
	if _, err := n.runAgentInPane(context.Background(), "w1:p1", "codex-app"); !errors.Is(err, errCodexAppLauncherInTerminal) {
		t.Errorf("Run Agent Here: %v", err)
	}
	if got, err := terminalLauncher("codex-app", sidA); err != nil || got != "codex" {
		t.Errorf("resume: %q %v", got, err)
	}
	if _, err := terminalLauncher("codex-app", ""); !errors.Is(err, errCodexAppLauncherInTerminal) {
		t.Errorf("terminal start: %v", err)
	}
	old := codexAppCLI
	codexAppCLI = func() (string, error) { return "", errors.New("no codex") }
	if _, added := n.withCodexAppLauncher(nil); added {
		t.Error("offered with no Codex CLI")
	}
	codexAppCLI = old
	n.sessionScope = &nodeSessionScope{codex: t.TempDir()}
	if _, added := n.withCodexAppLauncher(nil); added {
		t.Error("offered with the App not running")
	}
	shellLaunchers(t, "codex-app")
	if isCodexAppLauncher("codex-app") {
		t.Error("the login shell's codex-app is not the App's")
	}
}

// A chat Persona started in the App is one of the App's chats without a record in the App's state, and its subagents
// are the App's subagents; Persona's other Codex threads (the assistant's, originator "persona") are not.
func TestPersonasAppChatsAreTheAppsChats(t *testing.T) {
	home, _ := startFakeCodexBus(t, "x")
	chat := writeCodexAppRollout(t, home, "aaaaaaaa-0000-4000-8000-000000000001", codexAppClient, "/w")
	assistant := writeCodexAppRollout(t, home, "aaaaaaaa-0000-4000-8000-000000000002", "persona", "/w")
	sub := filepath.Join(home, "sub.jsonl")
	os.WriteFile(sub, []byte(`{"type":"session_meta","payload":{"originator":"`+codexAppClient+`","source":{"subagent":{"thread_spawn":{"parent_thread_id":"aaaaaaaa-0000-4000-8000-000000000001","depth":1}}}}}`+"\n"), 0o600)
	if !codexDesktopChat(codexSource{home: home, thread: "aaaaaaaa-0000-4000-8000-000000000001", path: chat}) {
		t.Error("Persona's App chat is not the App's")
	}
	if codexDesktopChat(codexSource{home: home, thread: "aaaaaaaa-0000-4000-8000-000000000002", path: assistant}) {
		t.Error("the assistant's thread taken for the App's")
	}
	if o := readCodexOrigin(sub); !o.desktop || o.parent == "" || codexDesktopChat(codexSource{home: home, thread: "sub", path: sub}) {
		t.Errorf("subagent origin %+v", o)
	}
}

// The App's codex starts the chat over stdio as Persona's App client, in its folder, and names it, which writes it; it
// never starts a turn.
func TestCodexAppChatStartsWithTheAppsCodexAndNoTurn(t *testing.T) {
	bin, seen := t.TempDir(), filepath.Join(t.TempDir(), "seen")
	cli := filepath.Join(bin, "codex")
	// A fake app-server: answers the three requests in order, writes what it got and where it ran.
	script := `#!/bin/sh
{ pwd; echo "args:$*"; echo "home:$CODEX_HOME"; } > ` + seen + `
read l1; echo "$l1" >> ` + seen + `; printf '{"id":1,"result":{}}\n'
read l2; echo "$l2" >> ` + seen + `
read l3; echo "$l3" >> ` + seen + `; printf '{"method":"thread/started","params":{}}\n{"id":2,"result":{"thread":{"id":"t-1","path":"/x"}}}\n'
read l4; echo "$l4" >> ` + seen + `; printf '{"id":3,"result":{}}\n'
read l5
`
	if err := os.WriteFile(cli, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	dir, home := t.TempDir(), t.TempDir()
	thread, err := startCodexAppChat(context.Background(), cli, home, dir, "修复导出")
	if err != nil || thread != "t-1" {
		t.Fatalf("%q %v", thread, err)
	}
	b, _ := os.ReadFile(seen)
	got := string(b)
	real, _ := filepath.EvalSymlinks(dir)
	if first := strings.SplitN(got, "\n", 2)[0]; first != dir && first != real {
		t.Errorf("ran in %q", first)
	}
	for _, want := range []string{"args:app-server", "home:" + home, `"name":"persona_codex_app"`, `"method":"initialized"`,
		`"method":"thread/start","params":{"cwd":"` + dir + `"}`, `"method":"thread/name/set","params":{"name":"修复导出","threadId":"t-1"}`} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "turn/start") {
		t.Error("a turn was started")
	}
}

func TestCodexStdioCancellationStopsWrapperChild(t *testing.T) {
	home := t.TempDir()
	cli := filepath.Join(home, "codex")
	// The wrapper waits for a child with inherited stdout. Closing the wrapper alone cannot unblock initialize.
	script := `#!/bin/sh
mkfifo "$CODEX_HOME/block"
cat "$CODEX_HOME/block" &
printf '%s\n' "$!" > "$CODEX_HOME/child"
wait
`
	if err := os.WriteFile(cli, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	finished := make(chan error, 1)
	go func() {
		server, err := startCodexStdio(ctx, cli, home, home)
		if server != nil {
			server.close()
		}
		finished <- err
	}()
	var child int
	waitFor(t, "wrapper child to start", func() bool {
		b, _ := os.ReadFile(filepath.Join(home, "child"))
		child, _ = strconv.Atoi(strings.TrimSpace(string(b)))
		return child > 0
	})
	t.Cleanup(func() { _ = osproc.Kill(child) })
	cancel()
	select {
	case err := <-finished:
		if err == nil {
			t.Fatal("initialize succeeded after cancellation")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("initialize stayed blocked on the wrapper child's stdout")
	}
	waitFor(t, "wrapper child to exit", func() bool { return !osproc.Alive(child) })
}

func TestCodexAppSlugIsTheTitlesASCIIWords(t *testing.T) {
	for _, c := range []struct{ title, want string }{
		{"Fix the CSV export!", "fix-the-csv-export"},
		{"修复 CSV 导出的编码", "csv"},
		{"整理周报", "chat"},
		{strings.Repeat("abcdefghij ", 6), "abcdefghij-abcdefghij-abcdefghij"},
	} {
		if got := codexAppSlug(c.title); got != c.want {
			t.Errorf("%q: %q, want %q", c.title, got, c.want)
		}
	}
}
