package node

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

func TestCodexHistoryAsksAfterUserReply(test *testing.T) {
	const question = "Where should updates go?"
	turnEvent := func(second int, kind string) string {
		return fmt.Sprintf(`{"timestamp":"2026-01-01T00:00:%02dZ","type":"event_msg","payload":{"type":%q}}`, second, kind)
	}
	const ask = `{"timestamp":"2026-01-01T00:00:02Z","type":"history_mutation","payload":{"display_completions":[{"item":{"type":"AgentMessage","id":"ask-1","delivery":"async","content":[{"type":"Text","text":"` + question + `"}],"questions":[{"title":"` + question + `","options":["Dashboard","Inbox"]}]}}]}}`
	const asyncReply = `{"timestamp":"2026-01-01T00:00:05Z","type":"history_mutation","payload":{"display_completions":[{"item":{"type":"UserMessage","id":"user-1","content":[{"type":"text","text":"<send_user_message_question_reply>[{\"questionItemId\":\"ask-1\",\"question\":\"` + question + `\",\"answer\":\"Dashboard\"}]</send_user_message_question_reply>"}]}}]}}`
	const ordinaryReply = `{"timestamp":"2026-01-01T00:00:05Z","type":"event_msg","payload":{"type":"item_completed","item":{"type":"UserMessage","id":"user-1","content":[{"type":"text","text":"Use the dashboard."}]}}}`
	toolResult := func(second, size int) string {
		return fmt.Sprintf(`{"timestamp":"2026-01-01T00:00:%02dZ","type":"event_msg","payload":{"type":"item_completed","item":{"type":"CommandExecution","id":"tool-%d","command":["ls"],"status":"completed","aggregated_output":%q,"exit_code":0}}}`, second, second, strings.Repeat("x", size))
	}
	for _, scenario := range []struct {
		name, reply string
		long        bool
	}{
		{name: "async reply", reply: asyncReply},
		{name: "async reply outside quick tail", reply: asyncReply, long: true},
		{name: "later ordinary prompt outside quick tail", reply: ordinaryReply, long: true},
	} {
		test.Run(scenario.name, func(test *testing.T) {
			node := remoteTestNode(test)
			home := node.codexHome()
			path := realPath(writeAppRollout(test, home, appChat, test.TempDir(), time.Now()))
			appendEvents := func(lines ...string) {
				test.Helper()
				file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
				if err != nil {
					test.Fatal(err)
				}
				_, writeErr := file.WriteString(strings.Join(lines, "\n") + "\n")
				if err := file.Close(); err != nil || writeErr != nil {
					test.Fatalf("append rollout: write %v, close %v", writeErr, err)
				}
				node.mu.Lock()
				if meta, ok := node.metas[path]; ok {
					meta.readAt = time.Time{}
					node.metas[path] = meta
				}
				node.mu.Unlock()
			}
			check := func(stage string, node *Node, want string) {
				test.Helper()
				row := rowsByThread(cachedSessions(node))[appChat]
				if row.SourceKind != "codex_history" || row.LastText != question {
					test.Fatalf("%s: source = %q, last text = %q; want history row with the original question", stage, row.SourceKind, row.LastText)
				}
				if !row.AsksRead || row.Asks != want {
					test.Errorf("%s: AsksRead = %t, Asks = %q; want true, %q", stage, row.AsksRead, row.Asks, want)
				}
			}
			appendEvents(turnEvent(1, "task_started"), ask)
			if scenario.long {
				appendEvents(toolResult(3, quickTail+1))
			}
			if scenario.reply == ordinaryReply {
				appendEvents(turnEvent(3, "task_complete"))
			}
			check("initial discovery, unanswered", node, question)
			if scenario.reply == ordinaryReply {
				appendEvents(turnEvent(4, "task_started"))
			}
			appendEvents(scenario.reply, toolResult(6, 16))
			check("reread while the agent continues", node, "")
			if scenario.long {
				appendEvents(toolResult(7, quickTail+1))
			}
			appendEvents(turnEvent(8, "task_complete"))
			check("reread after completion", node, "")
			restarted := remoteTestNode(test)
			restarted.configureCodex(proto.CodexConfig{Homes: []string{home}, LegacyHome: home})
			check("rediscovery after restart", restarted, "")
		})
	}
}

func TestCodexHistoryAsksUnknownWithoutReadableMetadata(test *testing.T) {
	node := remoteTestNode(test)
	path := realPath(writeAppRollout(test, node.codexHome(), appChat, test.TempDir(), time.Now()))
	if err := os.WriteFile(path, []byte("not a rollout\n"), 0o600); err != nil {
		test.Fatal(err)
	}
	check := func(stage string, rows []proto.Session) {
		test.Helper()
		row := rowsByThread(rows)[appChat]
		if row.SourceKind != "codex_history" || row.AsksRead || row.Asks != "" {
			test.Errorf("%s: source = %q, AsksRead = %t, Asks = %q; want history row with unknown asks", stage, row.SourceKind, row.AsksRead, row.Asks)
		}
	}
	check("unparseable rollout", cachedSessions(node))
	pass := newCodexListPass()
	pass.stats[path] = codexStat{err: os.ErrPermission}
	check("failed stat", node.sessionListIn(context.Background(), false, pass))
}

const (
	appChat     = "019a0000-0000-7000-8000-000000000001" // in a project; its rollout is gone
	appOld      = "019a0000-0000-7000-8000-000000000002" // without a project; its rollout is older than two weeks
	appDeleted  = "019a0000-0000-7000-8000-000000000003" // still in the sidebar's list, deleted from the database
	appArchived = "019a0000-0000-7000-8000-000000000004"
	appExec     = "019a0000-0000-7000-8000-000000000005" // in the database, not in the sidebar
	appMigrated = "019a0000-0000-7000-8000-000000000006" // in the database only: the migrated App writes no legacy keys
)

const codexAppSchema = `CREATE TABLE threads (id TEXT PRIMARY KEY, rollout_path TEXT NOT NULL, created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL, source TEXT NOT NULL, cwd TEXT NOT NULL, title TEXT NOT NULL,
	archived INTEGER NOT NULL DEFAULT 0, updated_at_ms INTEGER, name TEXT, originator TEXT);`

// writeCodexApp writes the App's sidebar (appChat in project Alpha at project; appOld, appDeleted and appArchived without
// one) and runs sql against its state database in home.
func writeCodexApp(t *testing.T, home, project, sql string) {
	t.Helper()
	sqlite := sqlite3WithJSON(t)
	state := fmt.Sprintf(`{"projectless-thread-ids": [%q, %q, %q, "not-an-id"],
		"thread-project-assignments": {%q: {"projectKind": "local", "projectId": "p1"}},
		"local-projects": {"p1": {"name": "Alpha", "rootPaths": [%q]}}}`, appOld, appDeleted, appArchived, appChat, project)
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".codex-global-state.json"), []byte(state), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(sqlite, filepath.Join(home, "state_5.sqlite"), sql).CombinedOutput(); err != nil {
		t.Fatalf("sqlite3: %v: %s", err, out)
	}
	codexAppDBs.Lock()
	clear(codexAppDBs.byHome) // read the database again now, not after codexAppDBRefresh (under any spelling of home)
	codexAppDBs.Unlock()
}

func codexAppRow(id, rollout, cwd, name string, updated time.Time, archived int, originator ...string) string {
	q := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
	o := "Codex Desktop"
	if len(originator) > 0 {
		o = originator[0]
	}
	return fmt.Sprintf("INSERT INTO threads VALUES (%s, %s, 0, %d, 'vscode', %s, 'auto title', %d, %d, %s, %s);",
		q(id), q(rollout), updated.Unix(), q(cwd), archived, updated.UnixMilli(), q(name), q(o))
}

// writeAppRollout writes a Codex App rollout (originator Codex Desktop) under home, last written at written.
func writeAppRollout(t *testing.T, home, id, cwd string, written time.Time) string {
	t.Helper()
	dir := filepath.Join(home, "sessions", written.Format("2006"), written.Format("01"), written.Format("02"))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "rollout-"+written.Format("2006-01-02T15-04-05")+"-"+id+".jsonl")
	meta := fmt.Sprintf(`{"timestamp":"2026-01-01T00:00:00Z","type":"session_meta","payload":{"id":%q,"cwd":%q,"originator":"Codex Desktop","source":"vscode"}}`, id, cwd)
	if err := os.WriteFile(p, []byte(meta+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, written, written); err != nil {
		t.Fatal(err)
	}
	return p
}

func rowsByThread(rows []proto.Session) map[string]proto.Session {
	out := map[string]proto.Session{}
	for _, s := range rows {
		out[s.SourceThreadID] = s
	}
	return out
}

// The App's sidebar lists chats whose rollout it deleted after moving their history into its database, and chats whose
// rollout is older than the two weeks rollouts looks at: both are listed, as the sidebar lists them (#3305).
func TestCodexAppChatsAreListedLikeTheSidebar(t *testing.T) {
	noFakeCodexBus(t)
	n := remoteTestNode(t)
	home := n.codexHome()
	project := t.TempDir()
	updated := time.Now().Add(-20 * 24 * time.Hour).Truncate(time.Millisecond)
	elsewhere := t.TempDir()
	old := writeAppRollout(t, home, appOld, elsewhere, updated)
	writeCodexApp(t, home, project, codexAppSchema+
		codexAppRow(appChat, filepath.Join(home, "sessions", "gone.jsonl"), filepath.Join(project, "sub"), "Alpha chat", updated, 0)+
		codexAppRow(appOld, old, elsewhere, "Old chat", updated, 0)+
		codexAppRow(appArchived, "", project, "Archived", updated, 1)+
		codexAppRow(appExec, "", project, "A run", updated, 0, "codex_exec"))

	rows := rowsByThread(n.codexSessions(map[string]bool{}))
	if len(rows) != 2 {
		t.Fatalf("rows = %v, want the two chats the sidebar shows", rows)
	}
	chat, ok := rows[appChat]
	if !ok {
		t.Fatalf("the chat without a rollout is missing: %v", rows)
	}
	if chat.Title != "Alpha chat" || chat.Cwd != filepath.Join(project, "sub") || !chat.UpdatedAt.Equal(updated) || chat.Status != proto.StatusOffline {
		t.Fatalf("chat = %+v", chat)
	}
	if chat.AsksRead || chat.Asks != "" {
		t.Fatalf("chat without a rollout: AsksRead = %t, Asks = %q; want unknown asks", chat.AsksRead, chat.Asks)
	}
	if chat.AgentName != "Codex App" || chat.SpaceKind != "codex_app" || chat.SpaceID != codexDesktopProjectID(canonicalCodexHome(home, ""), "p1") {
		t.Fatalf("chat placed as %q %q %q, want in its project", chat.AgentName, chat.SpaceKind, chat.SpaceID)
	}
	if chat.Capabilities.Send {
		t.Fatal("without the App running there is no way to send")
	}
	if o, ok := rows[appOld]; !ok || o.AgentName != "Codex App" || o.UpdatedAt.Before(updated.Add(-time.Second)) {
		t.Fatalf("the chat with an old rollout = %+v", o)
	}
	// It is in no project in the App: under "Codex chats", not a project named by its folder.
	if o := rows[appOld]; !strings.HasPrefix(o.SpaceID, "codex-chats:") || o.SpaceName != "Codex 对话" { // i18n-ignore: the node's space name
		t.Fatalf("the chat in no project is placed in %q %q", o.SpaceID, o.SpaceName)
	}
	if src, _ := n.codexSource(rows[appOld].SessionID); src.path != resolved(t, old) { // the node names it resolved (#9138)
		t.Fatalf("the old chat reads %q, want its rollout", src.path)
	}
}

func TestPaginatedOpeningPromptIsOnlyAListTitle(t *testing.T) {
	n := remoteTestNode(t)
	home, project := n.codexHome(), t.TempDir()
	updated := time.Now()
	want := strings.Repeat("界", 255) + "🙂"
	prompt := want + strings.Repeat("x", 65<<10)
	writeCodexApp(t, home, project, codexAppSchema+
		codexAppRow(appChat, "", project, "", updated, 0)+
		codexAppRow(appOld, "", project, "Chosen name", updated, 0)+
		`ALTER TABLE threads ADD COLUMN history_mode TEXT DEFAULT 'paginated';
		UPDATE threads SET title = '`+prompt+`';`)
	writeCodexHistory(t, home, []historyTurn{{thread: appChat, id: "turn-1", status: "completed",
		ordinal: 1, items: [][2]string{userItem("u1", prompt)}}})

	live, ok := codexPaginatedThreadOf(codexSource{home: home, thread: appChat})
	if !ok || live.Title != want {
		t.Errorf("live title: found %t, got %d bytes, want %d bytes", ok, len(live.Title), len(want))
	}
	rows := rowsByThread(n.codexSessions(map[string]bool{}))
	if got := rows[appChat].Title; got != want {
		t.Errorf("stored title: got %d bytes, want %d bytes", len(got), len(want))
	}
	if got := rows[appOld].Title; got != "Chosen name" {
		t.Errorf("stored name = %q", got)
	}
	f, err := n.file(rows[appChat].SessionID)
	if err != nil {
		t.Fatal(err)
	}
	items, err := f.Refresh()
	if err != nil || !strings.Contains(historyText(items), "user:text:"+want) {
		t.Fatalf("opening prompt missing from history: %v", err)
	}
	raw, err := os.ReadFile(f.Where())
	if err != nil || !strings.Contains(string(raw), prompt) {
		t.Fatalf("full opening prompt missing from materialized history: %v", err)
	}
}

// One list reads the App's state once per home, not twice for each of its chats: each read globbed the home for the
// App's state database, about 1,100 globs a second for 550 chats.
func TestCodexAppStateReadOncePerList(t *testing.T) {
	home, _ := startFakeCodexBus(t, "")
	sqlite := sqlite3WithJSON(t)
	project := t.TempDir()
	var ids, quoted []string
	sql := codexAppSchema
	for i := range 20 {
		id := fmt.Sprintf("019a0000-0000-7000-8000-%012d", 100+i)
		ids, quoted = append(ids, id), append(quoted, fmt.Sprintf("%q", id))
		// Its rollout is gone (Codex compressed it): listed from its row, placed through the App's state.
		sql += codexAppRow(id, filepath.Join(home, "sessions", "rollout-"+id+".jsonl"), project, fmt.Sprintf("Chat %d", i), time.Now().Add(-time.Hour), 0)
	}
	state := `{"projectless-thread-ids": [` + strings.Join(quoted, ", ") + `]}`
	if err := os.WriteFile(filepath.Join(home, ".codex-global-state.json"), []byte(state), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(sqlite, filepath.Join(home, "state_5.sqlite"), sql).CombinedOutput(); err != nil {
		t.Fatalf("sqlite3: %v: %s", err, out)
	}
	codexAppDBs.Lock()
	clear(codexAppDBs.byHome)
	codexAppDBs.Unlock()
	n := remoteTestNode(t)
	n.configureCodex(proto.CodexConfig{Homes: []string{home}})
	if rows := rowsByThread(n.codexSessions(map[string]bool{})); len(rows) != len(ids) {
		t.Fatalf("rows = %v, want the %d chats", rows, len(ids))
	}

	var globs atomic.Int64
	glob := codexAppDBGlob
	codexAppDBGlob = func(pattern string) ([]string, error) {
		if dir := filepath.Dir(pattern); dir == home || dir == realPath(home) { // the node keeps the home's real path
			globs.Add(1)
		}
		return glob(pattern)
	}
	t.Cleanup(func() { codexAppDBGlob = glob })
	rows := rowsByThread(n.codexSessions(map[string]bool{}))
	if len(rows) != len(ids) || rows[ids[0]].SpaceKind != "codex_app" || !rows[ids[0]].Capabilities.Send {
		t.Fatalf("rows = %v", rows)
	}
	// Once for the list; the App's database rows are looked at again every 5 s (codexAppThreads), twice more then.
	// Before, every one of the 20 chats asked twice: 40.
	if got := globs.Load(); got > 3 {
		t.Fatalf("one list globbed the home %d times for the App's state database, want a few whatever the number of chats", got)
	}
}

// A chat whose rollout is gone is sent to through the App's bus, as one with a rollout.
func TestCodexAppChatWithoutRolloutSendsThroughTheApp(t *testing.T) {
	home, bus := startFakeCodexBus(t, appChat)
	project := t.TempDir()
	writeCodexApp(t, home, project, codexAppSchema+codexAppRow(appChat, "", project, "Alpha chat", time.Now(), 0))
	n := remoteTestNode(t)
	n.configureCodex(proto.CodexConfig{Homes: []string{home}})

	var chat proto.Session
	for _, s := range n.codexSessions(map[string]bool{}) {
		if s.SourceThreadID == appChat {
			chat = s
		}
	}
	if chat.SessionID == "" || !chat.Capabilities.Send || chat.ReadOnlyReason != "" || chat.SpaceKind != "codex_app" {
		t.Fatalf("chat = %+v", chat)
	}
	if err := n.sendCodex(context.Background(), chat.SessionID, "hello", nil); err != nil {
		t.Fatal(err)
	}
	select {
	case params := <-bus.turns:
		if params["conversationId"] != appChat {
			t.Fatalf("turn for %v", params["conversationId"])
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no turn reached the App")
	}
}

// Without the database, or with one whose schema has other columns, nothing more is listed than before.
func TestCodexAppDatabaseItCannotReadListsNothingMore(t *testing.T) {
	for name, sql := range map[string]string{
		"no database":  "",
		"other schema": `CREATE TABLE threads (id TEXT PRIMARY KEY, rollout_path TEXT NOT NULL); INSERT INTO threads VALUES ('` + appChat + `', '');`,
	} {
		t.Run(name, func(t *testing.T) {
			n := remoteTestNode(t)
			home := n.codexHome()
			writeCodexApp(t, home, t.TempDir(), sql)
			if sql == "" {
				os.Remove(filepath.Join(home, "state_5.sqlite"))
			}
			if rows := n.codexSessions(map[string]bool{}); len(rows) != 0 {
				t.Fatalf("rows = %v", rows)
			}
		})
	}
}

func TestCodexAppDBPathTakesTheNewestSchema(t *testing.T) {
	home := t.TempDir()
	for _, name := range []string{"state_5.sqlite", "state_12.sqlite", "state_x.sqlite", "logs_2.sqlite"} {
		os.WriteFile(filepath.Join(home, name), nil, 0o600)
	}
	if got := codexAppDBPath(home); !strings.HasSuffix(got, "state_12.sqlite") {
		t.Fatalf("db = %s", got)
	}
	if got := codexAppDBPath(t.TempDir()); got != "" {
		t.Fatalf("db without one = %q", got)
	}
}

// A chat the App keeps in no project, in the folder the App made for it, is under "Codex chats" as in the App's sidebar
// (placeCodexDesktop, docs/integrations/codex.md), and so are its subagents; a chat in a project stays in it.
func TestCodexAppChatsInNoProjectAreUnderCodexChats(t *testing.T) {
	n := remoteTestNode(t)
	home := n.codexHome()
	project := t.TempDir()
	scratch := filepath.Join(t.TempDir(), "Documents", "Codex", "2026-11-06", "w-i") // the App's folder for such a chat
	if err := os.MkdirAll(scratch, 0o755); err != nil {
		t.Fatal(err)
	}
	const sub = "019a0000-0000-7000-8000-0000000000c1"
	now := time.Now().Truncate(time.Second)
	chat := writeAppRollout(t, home, appOld, scratch, now)
	subPath := filepath.Join(filepath.Dir(chat), "rollout-"+now.Format("2006-01-02T15-04-05")+"-"+sub+".jsonl")
	meta := fmt.Sprintf(`{"timestamp":"2026-01-01T00:00:00Z","type":"session_meta","payload":{"id":%q,"cwd":%q,"originator":"Codex Desktop","source":{"subagent":{"thread_spawn":{"parent_thread_id":%q,"depth":1}}}}}`, sub, scratch, appOld)
	if err := os.WriteFile(subPath, []byte(meta+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	inProject := writeAppRollout(t, home, appChat, project, now)
	writeCodexApp(t, home, project, codexAppSchema+
		codexAppRow(appOld, chat, scratch, "Scratch chat", now, 0)+
		codexAppRow(appChat, inProject, project, "Alpha chat", now, 0))

	rows := rowsByThread(n.codexSessions(map[string]bool{}))
	for _, id := range []string{appOld, sub} {
		if r, ok := rows[id]; !ok || !strings.HasPrefix(r.SpaceID, "codex-chats:") || r.SpaceName != "Codex 对话" { // i18n-ignore: the node's space name
			t.Fatalf("%s placed in %q %q (listed %v), want under Codex chats", id, r.SpaceID, r.SpaceName, ok)
		}
	}
	if !rows[sub].IsSubagent {
		t.Fatal("the subagent is not marked as one")
	}
	if r := rows[appChat]; r.SpaceID != codexDesktopProjectID(canonicalCodexHome(home, ""), "p1") {
		t.Fatalf("the chat in a project is placed in %q", r.SpaceID)
	}
}

// The migrated inventory includes subagent origins needed for hierarchy and guardian filtering, while one-shot
// desktop runs and unrelated CLI rows do not become sidebar conversations.
func TestCodexAppDatabaseRowsKeepSubagentsAndExcludeProgramRuns(t *testing.T) {
	n := remoteTestNode(t)
	home := n.codexHome()
	project := t.TempDir()
	const unknownSource = "019a0000-0000-7000-8000-000000009999"
	updated := time.Now().Add(-time.Hour).Truncate(time.Millisecond)
	writeMigratedCodexApp(t, home, migratedAppWithSubagents(project, updated)+
		codexAppRow(appExec, "", project, "One-shot", updated, 0)+
		`UPDATE threads SET source = 'exec' WHERE id = '`+appExec+`';`+
		codexAppRow(appOld, "", project, "CLI chat", updated, 0, "codex_cli_rs")+
		codexAppRow(unknownSource, "", project, "Unknown run", updated, 0)+
		`UPDATE threads SET source = 'unknown' WHERE id = '`+unknownSource+`';`)
	rows, err := readCodexAppThreads(context.Background(), codexAppDBPath(home), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{appMigrated, appSubagent, appGuardian, appReview} {
		if _, ok := rows[id]; !ok {
			t.Errorf("desktop chat or subagent %s is missing", id)
		}
	}
	for _, id := range []string{appExec, appOld, unknownSource} {
		if _, ok := rows[id]; ok {
			t.Errorf("non-sidebar run %s was included", id)
		}
	}
	visible := rowsByThread(n.codexSessions(map[string]bool{}))
	chat, sub := visible[appMigrated], visible[appSubagent]
	if chat.SessionID == "" || !sub.IsSubagent || sub.ParentSessionID != chat.SessionID || sub.Capabilities.Send {
		t.Fatalf("migrated metadata did not fold a read-only subagent under its chat: chat=%+v, sub=%+v", chat, sub)
	}
	for _, id := range []string{appGuardian, appExec, appOld, unknownSource} {
		if _, ok := visible[id]; ok {
			t.Errorf("guardian or program run %s appeared as a conversation", id)
		}
	}
}
