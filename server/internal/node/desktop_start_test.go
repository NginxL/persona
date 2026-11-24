package node

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

func TestDesktopStartsRefuseScopedNode(t *testing.T) {
	n := &Node{sessionScope: &nodeSessionScope{codex: t.TempDir()}}
	oldDirs, oldCLI := claudeDesktopDirs, codexAppCLI
	claudeDesktopDirs = func(string) []string { t.Fatal("scoped node read desktop data"); return nil }
	codexAppCLI = func() (string, error) { t.Fatal("scoped node discovered a global CLI"); return "", nil }
	t.Cleanup(func() { claudeDesktopDirs, codexAppCLI = oldDirs, oldCLI })
	list := []proto.Launcher{{Name: "claude"}}
	if got, added := n.withDesktopLauncher(list); added || len(got) != 1 {
		t.Fatal("scoped node offered Claude desktop")
	}
	if got, added := n.withCodexAppLauncher(list); added || len(got) != 1 {
		t.Fatal("scoped node offered Codex App")
	}
	params := runtimeStartParams{Cwd: filepath.Join(t.TempDir(), "must-not-exist"), Prompt: "hello"}
	for _, start := range []func(context.Context, runtimeStartParams, string) (*proto.Session, error){n.newDesktopSession, n.newCodexAppChat} {
		_, err := start(context.Background(), params, "")
		var re *rpc.Error
		if !errors.As(err, &re) || re.Code != "desktop_scope_unavailable" || deliveredOf(re) != deliveredNone {
			t.Fatalf("scoped creation = %v", err)
		}
	}
}

func TestClaudeDesktopExplicitEnvironmentRefusesBeforeCreationOrResume(t *testing.T) {
	n, home := testNode(t)
	app := newFakeDesktopApp(t, home)
	fakeClaudeOnPath(t)
	shellLaunchers(t)
	id, current := newUUID(), t.TempDir()
	n.state.put(id, sessionState{Launcher: claudeDesktopLauncher, Cwd: current})
	for _, projectID := range []string{
		"claude-environment:profile:aaaaaaaa-0000-4000-8000-000000000001",
		"claude-environment:other-profile:aaaaaaaa-0000-4000-8000-000000000001",
		"codex-project:profile:project-id",
		"unknown-project",
		"claude-project:" + folderKey(t.TempDir()),
	} {
		for _, resume := range []string{"", id} {
			dir := filepath.Join(t.TempDir(), "must-not-be-created")
			_, err := startDesktop(t, n, map[string]any{"cwd": dir, "launcher": claudeDesktopLauncher,
				"desktop_project_id": projectID, "resume_session_id": resume, "prompt": "must not send"})
			var re *rpc.Error
			if !errors.As(err, &re) || deliveredOf(re) != deliveredNone ||
				(re.Code != "claude_desktop_project_unsupported" && re.Code != "claude_desktop_project_mismatch") {
				t.Fatalf("project %q resume %q: %v", projectID, resume, err)
			}
			if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("refused selection created a directory: %v", err)
			}
		}
	}
	if len(app.seeded()) != 0 || len(app.opened()) != 0 {
		t.Fatalf("refused selection created or opened desktop sessions: seeds=%v links=%v", app.seeded(), app.opened())
	}
}

func TestClaudeDesktopLegacyDirectoryProjectMatchesActualSessionDirectory(t *testing.T) {
	n, home := testNode(t)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	app := newFakeDesktopApp(t, home)
	fakeClaudeOnPath(t)
	shellLaunchers(t)
	root := t.TempDir()
	dir := filepath.Join(root, "child")
	projectID := "claude-project:" + folderKey(root)
	s, err := startDesktop(t, n, map[string]any{"cwd": dir, "launcher": claudeDesktopLauncher, "desktop_project_id": projectID})
	if err != nil || s.Cwd != dir || len(app.seeded()) != 1 {
		t.Fatalf("matching legacy directory project: %+v, %v", s, err)
	}
	// Resume validates the stored session directory, not a different cwd supplied by a caller.
	_, err = startDesktop(t, n, map[string]any{"cwd": t.TempDir(), "launcher": claudeDesktopLauncher,
		"desktop_project_id": projectID, "resume_session_id": s.SessionID})
	if err != nil || len(app.seeded()) != 1 || len(app.opened()) != 1 {
		t.Fatalf("legacy directory recovery changed the session: %v, seeds=%v links=%v", err, app.seeded(), app.opened())
	}
}

func checkDesktopCreationError(t *testing.T, err error, id string) {
	t.Helper()
	var re *rpc.Error
	if !errors.As(err, &re) || re.Code != "desktop_start_incomplete" || deliveredOf(re) != deliveredUnknown {
		t.Fatalf("created session failure must prevent creation retry: %v", err)
	}
	var data struct {
		Created   bool          `json:"created"`
		SessionID string        `json:"session_id"`
		Session   proto.Session `json:"session"`
	}
	if json.Unmarshal(re.Data, &data) != nil || !data.Created || data.SessionID != id || data.Session.SessionID != id {
		t.Fatalf("partial session was lost: %s", re.Data)
	}
}

func TestDesktopFirstMessageFailurePreservesCreatedSession(t *testing.T) {
	n, home := testNode(t)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	app := newFakeDesktopApp(t, home)
	fakeClaudeOnPath(t)
	shellLaunchers(t)
	open := openDesktopLink
	openDesktopLink = func(ctx context.Context, link string) error {
		if strings.Contains(link, "/epitaxy/") {
			return errors.New("the desktop app could not open its inbox")
		}
		return open(ctx, link)
	}
	_, err := startDesktop(t, n, map[string]any{"cwd": t.TempDir(), "launcher": claudeDesktopLauncher, "prompt": "hello"})
	seeds := app.seeded()
	if len(seeds) != 1 {
		t.Fatalf("created %d sessions", len(seeds))
	}
	checkDesktopCreationError(t, err, seeds[0].id)
	if state, ok := n.state.get(seeds[0].id); !ok || state.Launcher != claudeDesktopLauncher {
		t.Fatal("native ownership was not retained for retry")
	}
}

func TestCodexAppFirstMessageFailurePreservesCreatedSession(t *testing.T) {
	n, _, seed, _ := codexAppNode(t)
	openCodexThread = func(string) error { return errors.New("desktop window unavailable") }
	_, err := startCodexApp(t, n, map[string]any{"cwd": t.TempDir(), "launcher": codexAppLauncher, "prompt": "hello"})
	seeds := seed.seeded()
	if len(seeds) != 1 {
		t.Fatalf("created %d sessions", len(seeds))
	}
	checkDesktopCreationError(t, err, n.codexPublicID(n.codexHome(), seeds[0].thread))
}

func TestCodexAppMetadataFailurePreservesCreatedSession(t *testing.T) {
	n, bus, _, opened := codexAppNode(t)
	n.codices = newCodexRegistry()
	n.configureCodex(proto.CodexConfig{LegacyHome: n.codexHome()})
	id := newUUID()
	bus.later = id
	seedCodexAppChat = func(context.Context, string, string, string, string) (string, error) {
		return id, errors.New("thread/name/set failed after thread/start succeeded")
	}
	_, err := startCodexApp(t, n, map[string]any{"cwd": t.TempDir(), "launcher": codexAppLauncher, "prompt": "hello"})
	checkDesktopCreationError(t, err, n.codexPublicID(n.codexHome(), id))
	var re *rpc.Error
	if !errors.As(err, &re) || !strings.Contains(string(re.Data), `"first_message_delivered":"none"`) {
		t.Fatal("a pre-send failure claimed that a message might have been sent")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, text := range []string{
		`<persona-context kind="skill" ref="old-selection">review</persona-context>`,
		`<persona-context kind="skill">review</persona-context>`,
	} {
		_, err := request(t, n, "session.send", map[string]any{"session_id": n.codexPublicID(n.codexHome(), id), "skill_owner": "agents:helper", "text": text})
		if code, data := refusalOf(t, err); code != "skill_unavailable" || data["delivered"] != deliveredNone {
			t.Fatalf("desktop retry accepted an unavailable skill: %v (%v)", err, data)
		}
	}
	if len(*opened) != 0 {
		t.Fatalf("refused skill opened the desktop: %v", *opened)
	}
	select {
	case turn := <-bus.turns:
		t.Fatalf("refused skill reached the desktop: %v", turn)
	default:
	}
	if err := n.send(ctx, n.codexPublicID(n.codexHome(), id), "hello", nil, sendOpts{}); err != nil {
		t.Fatalf("retry could not reach the original desktop thread: %v", err)
	}
	select {
	case turn := <-bus.turns:
		if turn["conversationId"] != id {
			t.Fatalf("retry reached another thread: %v", turn)
		}
	case <-ctx.Done():
		t.Fatal("retry sent no turn to the desktop")
	}
}

func TestBlankDesktopCreationRetriesImportWithoutCreatingAnotherSession(t *testing.T) {
	n, home := testNode(t)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	app := newFakeDesktopApp(t, home)
	fakeClaudeOnPath(t)
	shellLaunchers(t)
	app.noImport = true
	oldWait := desktopImportWait
	desktopImportWait = time.Millisecond
	t.Cleanup(func() { desktopImportWait = oldWait })
	dir := t.TempDir()
	_, err := startDesktop(t, n, map[string]any{"cwd": dir, "launcher": claudeDesktopLauncher})
	seeds := app.seeded()
	if len(seeds) != 1 {
		t.Fatalf("created %d transcripts", len(seeds))
	}
	id := seeds[0].id
	checkDesktopCreationError(t, err, id)
	app.mu.Lock()
	app.noImport = false
	app.mu.Unlock()
	s, err := startDesktop(t, n, map[string]any{"cwd": dir, "launcher": claudeDesktopLauncher, "resume_session_id": id})
	if err != nil || s.SessionID != id || len(app.seeded()) != 1 || readClaudeDesktopIndex(home).about[id].local == "" {
		t.Fatalf("same-session import recovery failed: %+v, %v, seeds=%v", s, err, app.seeded())
	}
	if len(app.opened()) != 2 || app.inbox(id) != nil {
		t.Fatal("blank recovery opened an inbox or sent a model message")
	}
}
