package node

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func desktopRetryTerminalCall(f *fakeHerdr) string {
	for _, call := range f.trace() {
		if strings.Contains(call, "workspace") || strings.Contains(call, "tab.create") || strings.Contains(call, "pane.") {
			return call
		}
	}
	return ""
}

func TestClaudeDesktopPendingImportMessageRetriesSameSession(t *testing.T) {
	n, terminal, home := herdrNode(t)
	noPaneEnv(t)
	shortPeerWait(t, 3*time.Second)
	app := newFakeDesktopApp(t, home)
	fakeClaudeOnPath(t)
	shellLaunchers(t)
	terminal.reply("session.snapshot", snapshot(nil, nil, nil, nil))
	app.noImport = true
	oldWait := desktopImportWait
	desktopImportWait = time.Millisecond
	t.Cleanup(func() { desktopImportWait = oldWait })
	_, err := startDesktop(t, n, map[string]any{"cwd": t.TempDir(), "launcher": claudeDesktopLauncher, "prompt": "continue this conversation"})
	seeds := app.seeded()
	if len(seeds) != 1 {
		t.Fatalf("created %d transcripts", len(seeds))
	}
	id := seeds[0].id
	checkDesktopCreationError(t, err, id)
	app.mu.Lock()
	app.noImport = false
	app.mu.Unlock()
	terminal.reset()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	params, _ := json.Marshal(map[string]any{"session_id": id, "text": "continue this conversation", "client_msg_id": newUUID()})
	if _, err := n.handle(ctx, "session.send", params); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if len(app.seeded()) != 1 || app.inbox(id) == nil || len(app.inbox(id).users()) != 1 {
		t.Fatalf("retry did not deliver once to the original transcript: seeds=%v", app.seeded())
	}
	links := app.opened()
	if len(links) != 3 || links[0] != "claude://resume?session="+id || links[1] != links[0] || links[2] != "claude://claude.ai/epitaxy/local_"+id {
		t.Fatalf("retry opened another session: %v", links)
	}
	if call := desktopRetryTerminalCall(terminal); call != "" {
		t.Fatalf("retry used a terminal: %s", call)
	}
}

func TestClaudeDesktopOpenFailureNeverResumesTerminal(t *testing.T) {
	for _, pending := range []bool{false, true} {
		t.Run(fmt.Sprintf("pending-import=%t", pending), func(t *testing.T) {
			n, terminal, home := herdrNode(t)
			noPaneEnv(t)
			first, _ := useDesktopDirs(t)
			n.paths[sidA] = writeTranscript(t, home, home, sidA, time.Now(), fmt.Sprintf(`{"type":"user","cwd":%q,"sessionId":%q,"message":{"role":"user","content":"hello"}}`, home, sidA))
			if pending {
				n.state.put(sidA, sessionState{Launcher: claudeDesktopLauncher, Cwd: home})
			} else {
				writeDesktopEntry(t, first, sidA, sidA, false, nil, "")
			}
			oldOpen := openDesktopLink
			var links []string
			openDesktopLink = func(_ context.Context, link string) error {
				links = append(links, link)
				return errors.New("desktop URI handler unavailable")
			}
			t.Cleanup(func() { openDesktopLink = oldOpen })
			terminal.reply("session.snapshot", snapshot(nil, nil, nil, nil))
			terminal.reset()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := n.send(ctx, sidA, "continue", nil, sendOpts{}); refusalCode(err) != desktopOpenFailed {
				t.Fatalf("opener failure was not refused: %v", err)
			}
			if len(links) != 1 {
				t.Fatalf("opened %d links", len(links))
			}
			if call := desktopRetryTerminalCall(terminal); call != "" {
				t.Fatalf("opener failure used a terminal: %s", call)
			}
		})
	}
}

func TestClaudeDesktopPendingImportChecksBeforeOpening(t *testing.T) {
	for _, tc := range []struct {
		name, text, code string
	}{
		{"command", "/compact", peerCommand},
		{"open elsewhere", "continue", openElsewhere},
		{"archived", "continue", "desktop_session_unavailable"},
		{"replaced", "continue", "desktop_session_unavailable"},
		{"deleted transcript", "continue", "desktop_session_unavailable"},
		{"deleted local session", "continue", "desktop_session_unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n, home := testNode(t)
			noPaneEnv(t)
			first, _ := useDesktopDirs(t)
			n.state.put(sidA, sessionState{Launcher: claudeDesktopLauncher, Cwd: home})
			switch tc.name {
			case "open elsewhere":
				writeOutsideClaude(t, home, sidA, "cli", "idle", nil, nil)
			case "archived":
				writeDesktopEntry(t, first, sidA, sidA, true, nil, "")
			case "replaced":
				writeDesktopEntry(t, first, sidB, sidB, false, []string{sidA}, "")
			case "deleted transcript", "deleted local session":
				local := sidA
				if tc.name == "deleted local session" {
					local = sidB
				}
				entry := writeDesktopEntry(t, first, local, sidA, false, nil, "")
				readClaudeDesktopIndex(home) // observe the local session's exact association before deletion
				if err := os.Remove(entry); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(filepath.Dir(entry), "deleted_"+local), []byte("1"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			oldOpen := openDesktopLink
			openDesktopLink = func(context.Context, string) error {
				t.Error("refused session opened the desktop app")
				return errors.New("unexpected open")
			}
			t.Cleanup(func() { openDesktopLink = oldOpen })
			handled, err := n.sendThroughDesktopApp(context.Background(), sidA, tc.text, nil)
			if !handled || refusalCode(err) != tc.code {
				t.Fatalf("handled=%v err=%v", handled, err)
			}
		})
	}
}

func TestClaudeDesktopPendingImportWaitsForPersistedOwnership(t *testing.T) {
	n, home := testNode(t)
	noPaneEnv(t)
	useDesktopDirs(t)
	n.state.beginLoad()
	defer n.state.endLoad()
	oldOpen := openDesktopLink
	opened := make(chan string, 1)
	openDesktopLink = func(_ context.Context, link string) error {
		opened <- link
		return errors.New("desktop URI handler unavailable")
	}
	t.Cleanup(func() { openDesktopLink = oldOpen })
	type result struct {
		handled bool
		err     error
	}
	done := make(chan result, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() {
		handled, err := n.sendThroughDesktopApp(ctx, sidA, "continue", nil)
		done <- result{handled, err}
	}()
	select {
	case got := <-done:
		t.Fatalf("send chose its route before ownership loaded: %+v", got)
	case <-opened:
		t.Fatal("send opened the app before ownership loaded")
	case <-time.After(100 * time.Millisecond):
	}
	n.state.merge(map[string]sessionState{sidA: {Launcher: claudeDesktopLauncher, Cwd: home}})
	n.state.endLoad()
	select {
	case got := <-done:
		if !got.handled || refusalCode(got.err) != desktopOpenFailed {
			t.Fatalf("persisted desktop ownership was lost: %+v", got)
		}
	case <-ctx.Done():
		t.Fatal("send did not finish after ownership loaded")
	}
	if link := <-opened; link != "claude://resume?session="+sidA {
		t.Fatalf("imported %q", link)
	}
}

// A recovery request is checked as a new start before it can import or send to the app.
func TestClaudeDesktopResumeRejectsUnsupportedSettingsBeforeImport(t *testing.T) {
	for _, tc := range []struct{ name, model, effort, mode, project string }{
		{name: "invalid model", model: "invalid\nmodel"},
		{name: "invalid effort", effort: "unsupported"},
		{name: "plan mode", mode: "plan"},
		{name: "native Codex project", project: "codex-project:profile:workspace"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n, _ := testNode(t)
			oldDirs, oldOpen := claudeDesktopDirs, openDesktopLink
			claudeDesktopDirs = func(string) []string { t.Fatal("an invalid recovery inspected the desktop index"); return nil }
			openDesktopLink = func(context.Context, string) error { t.Fatal("an invalid recovery opened the app"); return nil }
			t.Cleanup(func() { claudeDesktopDirs, openDesktopLink = oldDirs, oldOpen })
			if _, err := n.resumeDesktopSession(context.Background(), runtimeStartParams{Resume: sidA, Model: tc.model, Effort: tc.effort, PermissionMode: tc.mode, DesktopProjectID: tc.project}); err == nil {
				t.Fatal("invalid recovery settings were accepted")
			}
		})
	}
}

func TestClaudeDesktopPartialSeedRecoveryKeepsStartSettings(t *testing.T) {
	for _, mode := range []string{"default", "acceptEdits"} {
		t.Run(mode, func(t *testing.T) {
			n, home := testNode(t)
			n.modes = map[string]sessionModes{}
			t.Setenv("CLAUDE_CONFIG_DIR", "")
			app := newFakeDesktopApp(t, home)
			fakeClaudeOnPath(t)
			seed := seedDesktopSession
			seedDesktopSession = func(ctx context.Context, claude, dir, id, title string) error {
				if err := seed(ctx, claude, dir, id, title); err != nil {
					return err
				}
				return errors.New("the seed process failed after writing the transcript")
			}
			params := runtimeStartParams{Cwd: t.TempDir(), Model: "gateway-opus-5-5[1m]", Effort: "xhigh", PermissionMode: mode, Prompt: "send this after setup"}
			params.DesktopProjectID = projectSpace + params.Cwd
			s, err := n.newDesktopSession(context.Background(), params, "")
			if s == nil {
				t.Fatalf("the partial seed lost the session: %v", err)
			}
			checkDesktopCreationError(t, err, s.SessionID)
			if got := importedDesktopMode(t, n.transcriptPath(s.SessionID)); got != mode {
				t.Fatalf("partial seed mode = %q, want %q", got, mode)
			}
			n.ccPlugins.mu.Lock()
			prepared := n.ccPlugins.queued[s.SessionID].settings
			n.ccPlugins.mu.Unlock()
			if prepared.Model != params.Model || prepared.Effort != params.Effort {
				t.Fatalf("the partial seed lost settings before direct-send recovery: %+v", prepared)
			}
			if len(app.opened()) != 0 {
				t.Fatal("the failed seed imported a session")
			}
			params.Resume, params.Prompt = s.SessionID, ""
			recovered, err := n.resumeDesktopSession(context.Background(), params)
			if err != nil || recovered == nil || recovered.SessionID != s.SessionID {
				t.Fatalf("same-session setup recovery failed: %+v, %v", recovered, err)
			}
			if got := app.importedMode(s.SessionID); got != mode {
				t.Fatalf("imported mode = %q, want %q", got, mode)
			}
			n.ccPlugins.mu.Lock()
			settings := n.ccPlugins.queued[s.SessionID].settings
			n.ccPlugins.mu.Unlock()
			if recovered.Model != params.Model || recovered.Effort != params.Effort || settings.Model != params.Model || settings.Effort != params.Effort {
				t.Fatalf("recovered settings = %+v, queued = %+v", recovered, settings)
			}
			links := app.opened()
			if len(app.seeded()) != 1 || len(links) != 1 || links[0] != "claude://resume?session="+s.SessionID || app.inbox(s.SessionID) != nil {
				t.Fatalf("setup recovery created a transcript or sent a turn: seeds=%v, links=%v", app.seeded(), links)
			}
		})
	}
}
