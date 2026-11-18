package node

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

const omzQuestion = "[oh-my-zsh] Would you like to update? [Y/n]"

// stuckAsyncStart has n start startID in the background in w1 and stop on question (the tab w1:p2 kept, shell_waiting),
// as a New Agent start does; it returns the failed start's listed session.
func stuckAsyncStart(t *testing.T, n *Node, f *fakeHerdr, home, question string) proto.Session {
	t.Helper()
	watchShell(n)
	n.shellAsk, n.shellQuiet = 300*time.Millisecond, 200*time.Millisecond
	newShellTab(f, "zsh")
	f.setScreen("w1:p2", "Last login: today\n"+question+" ")
	if _, err := request(t, n, "session.start", map[string]any{"session_id": startID, "cwd": home, "workspace_id": "w1",
		"launcher": "claude", "async": true}); err != nil {
		t.Fatal(err)
	}
	if err := n.starts.wait(t.Context(), startID); rpcCode(err) != proto.CodeShellWaiting {
		t.Fatalf("start: %v", err)
	}
	_, s := n.starts.failed(startID)
	return s
}

// claudeUpOnStart has a claude come up in pane once a start line is typed into it.
func claudeUpOnStart(t *testing.T, f *fakeHerdr, pane string) {
	t.Helper()
	claudeScreen := screenFixture(t, "claude-2.1.312-empty-162.txt")
	var mu sync.Mutex
	up := false
	f.after(func(c herdrCall) {
		if c.Method == "pane.send_input" && c.Typed != "" && str(c.Params["pane_id"]) == pane {
			mu.Lock()
			up = true
			mu.Unlock()
			f.setScreen(pane, claudeScreen)
		}
	})
	f.handle("agent.get", func(map[string]any) any {
		mu.Lock()
		defer mu.Unlock()
		if !up {
			return map[string]any{"agent": map[string]any{"agent": "", "agent_status": "unknown", "pane_id": pane}}
		}
		return map[string]any{"agent": map[string]any{"agent": "claude", "agent_status": proto.StatusIdle, "pane_id": pane,
			"tab_id": "w1:t2", "workspace_id": "w1"}}
	})
}

// keysInto are the keys pressed in pane, in order (each send_keys call's keys joined by "+").
func keysInto(f *fakeHerdr, pane string) []string {
	var out []string
	for _, c := range f.calls() {
		if c.Method == "pane.send_keys" && str(c.Params["pane_id"]) == pane {
			out = append(out, strings.Join(keysOf(c), "+"))
		}
	}
	return out
}

// A New Agent start stopped on oh-my-zsh's question (#10365): the session lists the question and its answers; the
// owner's "n" from the app goes into that tab alone, and the start runs again there under the same id with no new
// tab. The record is starting again at once, so a message sent meanwhile waits for this start and is let through when
// it is up, not refused with the old error.
func TestStartContinueAnswersInTheKeptTab(t *testing.T) {
	withPaneShell(t, "/bin/zsh")
	n, f, home := herdrNode(t)
	withStartWaits(n, 10*time.Second, 10*time.Second)
	s := stuckAsyncStart(t, n, f, home, omzQuestion)
	args := s.StartErrorArgs
	if s.StartErrorCode != proto.CodeShellWaiting || args["line"] != omzQuestion || args["pane"] != "w1:p2" ||
		args["kind"] != "update" || args["choices"] != "y,n,enter" || args["default"] != "y" || args["line_mode"] != "" ||
		args["kept_until"] == "" {
		t.Fatalf("listed %+v", s)
	}
	f.after(func(c herdrCall) {
		if isKeys("n")(c) { // oh-my-zsh read the key (no echo) and the rest of ~/.zshrc drew the prompt
			f.setScreen("w1:p2", "Last login: today\n"+omzQuestion+" \n% ")
		}
	})
	claudeUpOnStart(t, f, "w1:p2")
	f.reset()

	res, err := request(t, n, "start.continue", map[string]any{"session_id": startID, "choice": "n"})
	if err != nil {
		t.Fatal(err)
	}
	if got := decodeSessions(t, []any{res["session"]})[0]; got.SessionID != startID || !got.Starting || got.StartErrorCode != "" {
		t.Errorf("answered %+v", got)
	}
	waited := make(chan error, 1)
	go func() { waited <- n.starts.wait(context.Background(), startID) }() // a message's turn (sendsafe_turns.go)
	select {
	case err := <-waited:
		if err != nil {
			t.Fatalf("a message sent meanwhile got %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the restarted start never ended")
	}
	if got := keysInto(f, "w1:p2"); !slices.Equal(got, []string{"n"}) {
		t.Errorf("keys pressed %v: one key, no Enter after a key read alone", got)
	}
	if got := f.trace("tab.create", "workspace.create"); len(got) != 0 {
		t.Errorf("opened a new tab: %v", got)
	}
	if got := sent(f, "w1:p2"); len(got) != 1 {
		t.Errorf("typed %q into the kept tab", got)
	}
	n.mu.Lock()
	bound := n.panes["w1:p2"]
	_, kept := n.keptStarts["w1:p2"]
	n.mu.Unlock()
	if bound != startID || kept {
		t.Errorf("pane bound to %q, still kept %v", bound, kept)
	}
	if _, err := request(t, n, "start.continue", map[string]any{"session_id": startID, "choice": "n"}); rpcCode(err) != "start_not_waiting" {
		t.Errorf("a second answer: %v", err)
	}
}

// Nothing is typed for an answer the question was not offered, for a tab no longer kept, or for a session whose start
// is not waiting; the start stays as it failed.
func TestStartContinueRefusals(t *testing.T) {
	withPaneShell(t, "/bin/zsh")
	n, f, home := herdrNode(t)
	withStartWaits(n, time.Second, time.Second)
	stuckAsyncStart(t, n, f, home, omzQuestion)
	f.reset()
	for _, tc := range []struct {
		name, session, choice, code string
	}{
		{"not offered", startID, "x", "choice_not_offered"},
		{"text", startID, "yes", "choice_not_offered"},
		{"another session", "0e000000-0000-4000-8000-0000000000bb", "n", "start_not_waiting"},
	} {
		if _, err := request(t, n, "start.continue", map[string]any{"session_id": tc.session, "choice": tc.choice}); rpcCode(err) != tc.code {
			t.Errorf("%s: %v", tc.name, err)
		}
	}
	n.mu.Lock()
	k := n.keptStarts["w1:p2"]
	k.until = time.Now().Add(-time.Second)
	n.keptStarts["w1:p2"] = k
	n.mu.Unlock()
	f.setScreen("w1:p2", "Last login: today\n"+omzQuestion+" ")
	if _, err := request(t, n, "start.continue", map[string]any{"session_id": startID, "choice": "n"}); rpcCode(err) != "start_tab_gone" {
		t.Errorf("an expired tab: %v", err)
	}
	if got := f.trace("pane.send_keys", "pane.send_text", "pane.send_input"); len(got) != 0 {
		t.Errorf("typed %v", got)
	}
	if _, s := n.starts.failed(startID); s.StartErrorCode != proto.CodeShellWaiting {
		t.Errorf("a refused answer changed the start: %+v", s)
	}
}

// The question answered at the computer (#10365): it is not the tab's last line any more, so the app's answer types
// nothing (it could run as a command at the prompt), and the start runs again in that tab all the same and goes on
// once the shell is at rest, rather than being refused with words that leave the card's buttons stuck. Another
// question there since gets nothing typed either: that start ends at once with it and its answers.
func TestStartContinueAfterAnAnswerAtTheComputer(t *testing.T) {
	withPaneShell(t, "/bin/zsh")
	n, f, home := herdrNode(t)
	withStartWaits(n, 10*time.Second, 10*time.Second)
	stuckAsyncStart(t, n, f, home, omzQuestion)
	f.setScreen("w1:p2", "Last login: today\n"+omzQuestion+" \n% ") // answered on the computer: the prompt is back
	claudeUpOnStart(t, f, "w1:p2")
	f.reset()
	res, err := request(t, n, "start.continue", map[string]any{"session_id": startID, "choice": "y"})
	if err != nil {
		t.Fatalf("an answer after the computer's: %v", err)
	}
	if got := decodeSessions(t, []any{res["session"]})[0]; !got.Starting || got.StartErrorCode != "" {
		t.Errorf("answered %+v", got)
	}
	waited := make(chan error, 1)
	go func() { waited <- n.starts.wait(context.Background(), startID) }()
	select {
	case err := <-waited:
		if err != nil {
			t.Fatalf("the start again: %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the restarted start never ended")
	}
	if got := keysInto(f, "w1:p2"); len(got) != 0 {
		t.Errorf("pressed %v over the computer's answer", got)
	}
	if got := sent(f, "w1:p2"); len(got) != 1 {
		t.Errorf("typed %q into the kept tab", got)
	}
	if got := f.trace("tab.create", "workspace.create"); len(got) != 0 {
		t.Errorf("opened a new tab: %v", got)
	}

	n2, f2, home2 := herdrNode(t)
	withStartWaits(n2, 10*time.Second, 10*time.Second)
	stuckAsyncStart(t, n2, f2, home2, omzQuestion)
	const other = "Proceed with the upgrade? [y/N]:"
	f2.setScreen("w1:p2", "Last login: today\n"+omzQuestion+" \n"+other+" ")
	f2.reset()
	if _, err := request(t, n2, "start.continue", map[string]any{"session_id": startID, "choice": "y"}); err != nil {
		t.Fatalf("an answer to a question gone: %v", err)
	}
	if err := n2.starts.wait(t.Context(), startID); rpcCode(err) != proto.CodeShellWaiting {
		t.Fatalf("the start again: %v", err)
	}
	if _, s := n2.starts.failed(startID); s.StartErrorArgs["line"] != other || s.StartErrorArgs["choices"] != "y,n,enter" {
		t.Errorf("listed %v", s.StartErrorArgs)
	}
	if got := f2.trace("pane.send_keys", "pane.send_text", "pane.send_input"); len(got) != 0 {
		t.Errorf("typed %v", got)
	}
}

// compinit's read -q echoes the key after its question, with no newline, and has read it (#10365): no Enter follows,
// which would go to whatever reads next (a later question, which takes its default, or the prompt).
func TestStartContinueSingleKeyGetsNoEnterAfterItsEcho(t *testing.T) {
	withPaneShell(t, "/bin/zsh")
	n, f, home := herdrNode(t)
	withStartWaits(n, 10*time.Second, 10*time.Second)
	const compinit = "Ignore insecure directories and continue [y] or abort compinit [n]?"
	stuckAsyncStart(t, n, f, home, compinit)
	f.after(func(c herdrCall) {
		if isKeys("y")(c) {
			f.setScreen("w1:p2", "Last login: today\n"+compinit+" y")
		}
	})
	claudeUpOnStart(t, f, "w1:p2")
	f.reset()
	if _, err := request(t, n, "start.continue", map[string]any{"session_id": startID, "choice": "y"}); err != nil {
		t.Fatal(err)
	}
	if err := n.starts.wait(t.Context(), startID); err != nil {
		t.Fatalf("the start again: %v", err)
	}
	if got := keysInto(f, "w1:p2"); !slices.Equal(got, []string{"y"}) {
		t.Errorf("keys pressed %v: the key alone", got)
	}
}

// A start that took a hand-off's values (secret_ticket) and stopped on a question cannot run again on an answer: they
// went with it. Its question is listed without answers, so no app offers one it would refuse (secret_env_gone).
func TestSecretStartListsNoAnswers(t *testing.T) {
	withPaneShell(t, "/bin/zsh")
	n, f, home := herdrNode(t)
	withStartWaits(n, time.Second, time.Second)
	n.secrets, n.keeper = newSecretStage(), newAssistantKeeper()
	staged, err := stageForTest(t, n, envVar("GITHUB_TOKEN", handoffTestValue))
	if err != nil {
		t.Fatal(err)
	}
	watchShell(n)
	n.shellAsk, n.shellQuiet = 300*time.Millisecond, 200*time.Millisecond
	newShellTab(f, "zsh")
	f.setScreen("w1:p2", "Last login: today\n"+omzQuestion+" ")
	if _, err := request(t, n, "session.start", map[string]any{"session_id": startID, "cwd": home, "workspace_id": "w1",
		"launcher": "claude", "async": true, "secret_ticket": staged.Ticket}); err != nil {
		t.Fatal(err)
	}
	if err := n.starts.wait(t.Context(), startID); rpcCode(err) != proto.CodeShellWaiting {
		t.Fatalf("start: %v", err)
	}
	_, s := n.starts.failed(startID)
	if args := s.StartErrorArgs; args["line"] != omzQuestion || args["pane"] != "w1:p2" || args["choices"] != "" {
		t.Errorf("listed %v", args)
	}
	f.reset()
	if _, err := request(t, n, "start.continue", map[string]any{"session_id": startID, "choice": "n"}); rpcCode(err) != proto.CodeSecretEnvGone {
		t.Errorf("an answer: %v", err)
	}
	if got := f.trace("pane.send_keys", "pane.send_text", "pane.send_input"); len(got) != 0 {
		t.Errorf("typed %v", got)
	}
}

// A password or passphrase prompt is never typed into from the app: only Ctrl-C is offered and taken.
func TestStartContinueNeverTypesIntoASecretPrompt(t *testing.T) {
	withPaneShell(t, "/bin/zsh")
	n, f, home := herdrNode(t)
	withStartWaits(n, time.Second, time.Second)
	const passphrase = "Enter passphrase for key '/home/me/.ssh/id_ed25519':"
	s := stuckAsyncStart(t, n, f, home, passphrase)
	if s.StartErrorArgs["secret"] != "true" || s.StartErrorArgs["choices"] != "cancel" {
		t.Fatalf("listed %v", s.StartErrorArgs)
	}
	f.reset()
	for _, choice := range []string{"y", "enter", "yes", "no"} {
		if _, err := request(t, n, "start.continue", map[string]any{"session_id": startID, "choice": choice}); rpcCode(err) != "choice_not_offered" {
			t.Errorf("%s: %v", choice, err)
		}
	}
	if got := f.trace("pane.send_keys", "pane.send_text", "pane.send_input"); len(got) != 0 {
		t.Errorf("typed into a secret prompt: %v", got)
	}
	f.after(func(c herdrCall) {
		if isKeys("ctrl+c")(c) {
			f.setScreen("w1:p2", passphrase+" ^C\n% ")
		}
	})
	if _, err := request(t, n, "start.continue", map[string]any{"session_id": startID, "choice": "cancel"}); err != nil {
		t.Fatal(err)
	}
	if got := keysInto(f, "w1:p2"); !slices.Equal(got, []string{"ctrl+c"}) {
		t.Errorf("keys %v", got)
	}
}

// A question that reads a whole line (oh-my-bash's read -p) shows the key after it and waits: Enter follows. A key
// read alone gets none (TestStartContinueAnswersInTheKeptTab).
func TestStartContinueLineModeAddsEnter(t *testing.T) {
	withPaneShell(t, "/bin/zsh")
	n, f, home := herdrNode(t)
	withStartWaits(n, time.Second, time.Second)
	const omb = "[Oh My Bash] Would you like to check for updates? [Y/n]:"
	if s := stuckAsyncStart(t, n, f, home, omb); s.StartErrorArgs["line_mode"] != "true" {
		t.Fatalf("listed %v", s.StartErrorArgs)
	}
	f.after(func(c herdrCall) {
		switch {
		case isKeys("n")(c):
			f.setScreen("w1:p2", "Last login: today\n"+omb+" n")
		case isKeys("enter")(c):
			f.setScreen("w1:p2", "Last login: today\n"+omb+" n\n$ ")
		}
	})
	f.reset()
	if _, err := request(t, n, "start.continue", map[string]any{"session_id": startID, "choice": "n"}); err != nil {
		t.Fatal(err)
	}
	if got := keysInto(f, "w1:p2"); len(got) < 2 || got[0] != "n" || got[1] != "enter" {
		t.Errorf("keys %v", got)
	}
}

// A failed async start's record is swapped for a starting one under the same id in one step: messages wait for the
// new start again, and its outcome is theirs. Only the failed record itself can be swapped, once.
func TestFailedAsyncStartCanRestartUnderItsID(t *testing.T) {
	var l startLog
	t.Cleanup(l.close)
	failed := &startRec{done: make(chan struct{}), session: proto.Session{SessionID: startID, Starting: true}}
	if err := l.add(startID, failed); err != nil {
		t.Fatal(err)
	}
	if err := l.restart(startID, failed, &startRec{done: make(chan struct{})}); !errors.Is(err, errStartNotWaiting) {
		t.Errorf("restarted a start still running: %v", err)
	}
	l.finish(startID, refusal(proto.CodeShellWaiting, "waiting", map[string]any{"line": "q?"}))
	if err := l.wait(context.Background(), startID); rpcCode(err) != proto.CodeShellWaiting {
		t.Fatalf("wait: %v", err)
	}
	if err := l.add(startID, &startRec{done: make(chan struct{})}); err == nil || !strings.Contains(err.Error(), "already starting") {
		t.Errorf("add over a failed start: %v", err)
	}
	again := &startRec{done: make(chan struct{}), session: proto.Session{SessionID: startID, Starting: true}}
	if err := l.restart(startID, failed, again); err != nil {
		t.Fatal(err)
	}
	if err := l.restart(startID, failed, &startRec{done: make(chan struct{})}); !errors.Is(err, errStartNotWaiting) {
		t.Errorf("restarted twice: %v", err)
	}
	if r, _ := l.failed(startID); r != nil {
		t.Error("listed as failed while starting again")
	}
	if got := l.listed(nil); len(got) != 1 || !got[0].Starting || got[0].StartErrorCode != "" {
		t.Errorf("listed %+v", got)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := l.wait(ctx, startID); err == nil || rpcCode(err) != "" {
		t.Errorf("a message did not wait for the new start: %v", err)
	}
	l.finish(startID, nil)
	if err := l.wait(context.Background(), startID); err != nil {
		t.Errorf("after the new start: %v", err)
	}
}

// A Retry named to the kept tab (in_pane) whose question is still open fails at once with the question, its answers
// and a renewed kept_until, rather than wait out the question again; no new tab is opened and nothing is typed.
func TestKeptTabStartFailsFastOnAnOpenQuestion(t *testing.T) {
	withPaneShell(t, "/bin/zsh")
	n, f, home := herdrNode(t)
	withStartWaits(n, time.Minute, time.Minute)
	keptByAQuestion(t, n, f, home)
	n.mu.Lock()
	first := n.keptStarts["w1:p2"].until
	n.mu.Unlock()
	n.shellAsk = 30 * time.Second // what a new tab's question would be waited for
	began := time.Now()
	_, err := request(t, n, "session.start", map[string]any{"cwd": home, "workspace_id": "w1", "launcher": "claude", "in_pane": "w1:p2"})
	if rpcCode(err) != proto.CodeShellWaiting {
		t.Fatalf("start: %v", err)
	}
	if d := time.Since(began); d > 3*time.Second {
		t.Errorf("took %s", d)
	}
	data := startData(t, err)
	until, perr := time.Parse(time.RFC3339, data["kept_until"])
	if data["line"] != omzQuestion || data["choices"] != "y,n,enter" || data["pane"] != "w1:p2" || perr != nil || until.Before(first.Truncate(time.Second)) {
		t.Errorf("data %v", data)
	}
	if got := f.trace("tab.create"); len(got) != 1 {
		t.Errorf("tabs %v", got)
	}
	if got := sent(f, "w1:p2"); len(got) != 0 {
		t.Errorf("typed %q", got)
	}
	if _, ok := n.keptStartTab("w1:p2"); !ok {
		t.Error("the tab is not kept again")
	}
}

// A planned task's start stopped on oh-my-zsh's question (#10365): its start was the hub's, so the answer goes by the
// kept tab's pane (start.answer) and the hub starts the task again in that tab itself. Only the owner's "n" is
// pressed; the start named to the tab (in_pane) then waits out the question's line, which stays the last one written
// until the rest of ~/.zshrc draws the prompt, instead of failing at once as a plain Retry's does; the agent starts
// there and no new tab opens.
func TestStartAnswerAnswersAKeptTabByItsPane(t *testing.T) {
	withPaneShell(t, "/bin/zsh")
	n, f, home := herdrNode(t)
	withStartWaits(n, 10*time.Second, 10*time.Second)
	keptByAQuestion(t, n, f, home)
	n.shellAsk = 10 * time.Second
	f.after(func(c herdrCall) {
		if isKeys("n")(c) { // oh-my-zsh read the key (no echo); the prompt comes after keptQuestionWait
			time.AfterFunc(2*keptQuestionWait, func() { f.setScreen("w1:p2", "Last login: today\n"+omzQuestion+" \n% ") })
		}
	})
	claudeUpOnStart(t, f, "w1:p2")
	f.reset()
	out, err := request(t, n, proto.MethodStartAnswer, map[string]any{"pane": "w1:p2", "line": omzQuestion, "choice": "n"})
	if err != nil {
		t.Fatal(err)
	}
	if out["ok"] != true || out["typed"] != true {
		t.Errorf("answered %v: the hub records the owner's answer only when the node says it typed it", out)
	}
	if got := keysInto(f, "w1:p2"); !slices.Equal(got, []string{"n"}) {
		t.Errorf("keys pressed %v: one key, no Enter after a key read alone", got)
	}
	if got := sent(f, "w1:p2"); len(got) != 0 {
		t.Errorf("typed %q with the answer", got)
	}
	if _, err := request(t, n, "session.start", map[string]any{"cwd": home, "workspace_id": "w1", "launcher": "claude", "in_pane": "w1:p2"}); err != nil {
		t.Fatalf("the start in the answered tab: %v", err)
	}
	if got := f.trace("tab.create", "workspace.create"); len(got) != 0 {
		t.Errorf("opened a new tab: %v", got)
	}
	if got := sent(f, "w1:p2"); len(got) != 1 {
		t.Errorf("typed %q into the kept tab", got)
	}
	if _, ok := n.keptStartTab("w1:p2"); ok {
		t.Error("still kept once an agent runs there")
	}
}

// The assistant's remedy (#2570): start.keys answers the kept tab's question, then a Retry starts in that tab
// (in_pane). The question stays the last line written until the rest of ~/.zshrc draws the prompt, so that start
// waits it out as in a new tab instead of ending at once with shell_waiting, as a Retry with nothing typed does.
func TestStartKeysThenRetryWaitsOutTheAnsweredQuestion(t *testing.T) {
	withPaneShell(t, "/bin/zsh")
	n, f, home := herdrNode(t)
	withStartWaits(n, 10*time.Second, 10*time.Second)
	keptByAQuestion(t, n, f, home)
	n.shellAsk = 10 * time.Second
	f.after(func(c herdrCall) {
		if isKeys("n")(c) { // oh-my-zsh read the key (no echo); the prompt comes after keptQuestionWait
			time.AfterFunc(2*keptQuestionWait, func() { f.setScreen("w1:p2", omzQuestion+" \n% ") })
		}
	})
	claudeUpOnStart(t, f, "w1:p2")
	f.reset()
	if _, err := request(t, n, "start.keys", map[string]any{"pane": "w1:p2", "keys": []string{"n"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := request(t, n, "session.start", map[string]any{"cwd": home, "workspace_id": "w1", "launcher": "claude", "in_pane": "w1:p2"}); err != nil {
		t.Fatalf("the Retry after the keys: %v", err)
	}
	if got := f.trace("tab.create", "workspace.create"); len(got) != 0 {
		t.Errorf("opened a new tab: %v", got)
	}
	if got := sent(f, "w1:p2"); len(got) != 1 {
		t.Errorf("typed %q into the kept tab", got)
	}
}

// start.answer types nothing for an answer the question was not offered, anything but Ctrl-C into a password prompt,
// or a pane that is no failed start's kept tab (any more): refused. A question that is not the tab's last line any
// more (another one there, or answered at the computer) gets nothing typed and the tab is not marked typed, and the
// reply says so (typed false), so the hub does not record the owner's answer: the hub's start there then goes on, or
// ends at once with the question the tab shows.
func TestStartAnswerRefusals(t *testing.T) {
	withPaneShell(t, "/bin/zsh")
	n, f, home := herdrNode(t)
	withStartWaits(n, time.Minute, time.Minute)
	keptByAQuestion(t, n, f, home)
	f.reset()
	const passphrase = "Enter passphrase for key '/home/me/.ssh/id_ed25519':"
	var replies []map[string]any
	answer := func(pane, line, choice string) error {
		out, err := request(t, n, proto.MethodStartAnswer, map[string]any{"pane": pane, "line": line, "choice": choice})
		if err == nil {
			replies = append(replies, out)
		}
		return err
	}
	for _, tc := range []struct {
		name, pane, line, choice, code string
	}{
		{"not offered", "w1:p2", omzQuestion, "x", "choice_not_offered"},
		{"text", "w1:p2", omzQuestion, "yes", "choice_not_offered"},
		{"no question", "w1:p2", "", "n", "start_not_waiting"},
		{"not kept", "w1:p9", omzQuestion, "n", "start_tab_gone"},
		{"a password", "w1:p2", passphrase, "enter", "choice_not_offered"},
		{"a password's yes", "w1:p2", passphrase, "y", "choice_not_offered"},
	} {
		if err := answer(tc.pane, tc.line, tc.choice); rpcCode(err) != tc.code {
			t.Errorf("%s: %v", tc.name, err)
		}
	}
	if err := answer("w1:p2", "Continue? [y/N]", "y"); err != nil { // another question than the tab's
		t.Errorf("another question: %v", err)
	}
	f.setScreen("w1:p2", "Last login: today\n"+omzQuestion+" n\n% ") // answered on the computer
	if err := answer("w1:p2", omzQuestion, "n"); err != nil {
		t.Errorf("a changed screen: %v", err)
	}
	if got := f.trace("pane.send_keys", "pane.send_text", "pane.send_input"); len(got) != 0 {
		t.Errorf("typed %v into a question gone", got)
	}
	for _, out := range replies {
		if typed, said := out["typed"]; out["ok"] != true || !said || typed != false {
			t.Errorf("answered %v with nothing typed: the reply must say typed false", out)
		}
	}
	if len(replies) != 2 {
		t.Errorf("replies %v", replies)
	}
	n.mu.Lock()
	typed := n.keptStarts["w1:p2"].typed
	n.mu.Unlock()
	if typed {
		t.Error("marked typed with nothing typed")
	}
	f.setScreen("w1:p2", "Last login: today\n"+passphrase+" ")
	if err := answer("w1:p2", passphrase, "cancel"); err != nil {
		t.Fatalf("Ctrl-C to a password prompt: %v", err)
	}
	if out := replies[len(replies)-1]; out["typed"] != true {
		t.Errorf("answered %v with Ctrl-C typed", out)
	}
	if got := keysInto(f, "w1:p2"); !slices.Equal(got, []string{"ctrl+c"}) {
		t.Errorf("keys %v: only Ctrl-C", got)
	}
	if got := f.trace("pane.send_text", "pane.send_input"); len(got) != 0 {
		t.Errorf("typed %v", got)
	}
	n.mu.Lock()
	k := n.keptStarts["w1:p2"]
	k.until = time.Now().Add(-time.Second)
	n.keptStarts["w1:p2"] = k
	n.mu.Unlock()
	if err := answer("w1:p2", passphrase, "cancel"); rpcCode(err) != "start_tab_gone" {
		t.Errorf("an expired tab: %v", err)
	}
}
