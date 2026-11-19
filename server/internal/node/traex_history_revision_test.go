package node

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// The shared history fixture predates TRAE 0.215's projection state. Add its revision columns here only.
func traexHistoryRevisionFixture(t *testing.T, n *Node, turns []historyTurn) string {
	t.Helper()
	traexHistoryFixture(t, n, turns)
	db := filepath.Join(n.traexHome(), "thread_history_1.sqlite")
	traexHistoryRevisionSQL(t, db, `CREATE TABLE thread_history_projection_state (
		thread_id TEXT PRIMARY KEY, next_rollout_ordinal INTEGER NOT NULL, active_turn_id TEXT,
		projection_context_json TEXT);`)
	for _, turn := range turns {
		active := "NULL"
		if turn.status == "inProgress" {
			active = "'" + turn.id + "'"
		}
		traexHistoryRevisionSQL(t, db, fmt.Sprintf(
			`INSERT INTO thread_history_projection_state (thread_id, next_rollout_ordinal, active_turn_id)
			VALUES ('%s', %d, %s);`, turn.thread, len(turn.items)+2, active))
	}
	return db
}

func traexHistoryRevisionSQL(t *testing.T, db, sql string) {
	t.Helper()
	if out, err := exec.Command(sqlite3WithJSON(t), db, sql).CombinedOutput(); err != nil {
		t.Fatalf("sqlite3: %v: %s", err, out)
	}
}

func traexHistoryRevisionSession(t *testing.T, n *Node, id, last string) proto.Session {
	t.Helper()
	// History is refreshed by a conversation read/watch, never the global list.
	if err := n.refreshCodexAppHistory(id); err != nil {
		t.Fatal(err)
	}
	// Model the next summary poll without sleeping through the list's existing metadata throttle.
	p := n.codexAppHistoryFile(codexSource{home: n.traexHome(), thread: id})
	n.mu.Lock()
	if m, ok := n.metas[p]; ok {
		m.readAt = time.Time{}
		n.metas[p] = m
	}
	n.mu.Unlock()
	for _, s := range n.traexSessions(map[string]bool{}) {
		if s.SessionID == id {
			if s.Agent != traexAgent || s.Title != "Inspect the export" || s.Status != proto.StatusOffline || s.LastText != last {
				t.Fatalf("conversation %s: agent %q, title %q, status %q, last %q; want last %q", id, s.Agent, s.Title, s.Status, s.LastText, last)
			}
			return s
		}
	}
	t.Fatalf("conversation %s was not listed", id)
	return proto.Session{}
}

func TestTraexHistoryRevisionKeepsUnchangedThreads(t *testing.T) {
	for _, status := range []string{"completed", "inProgress"} {
		t.Run(status, func(t *testing.T) {
			n := remoteTestNode(t)
			db := traexHistoryRevisionFixture(t, n, []historyTurn{
				{thread: threadMain, id: "turn-a", status: "inProgress", ordinal: 0,
					items: [][2]string{userItem("ua", "Inspect the export")}},
				{thread: threadOld, id: "turn-b", status: status, ordinal: 0,
					items: [][2]string{userItem("ub", "A separate question"), answerItem("ab", "A stable answer.")}},
			})
			traexHistoryRevisionSQL(t, filepath.Join(n.traexHome(), "state_5.sqlite"),
				fmt.Sprintf(`UPDATE threads SET archived = 0 WHERE id = '%s';`, threadOld))
			invalidateCodexPaginated(n.traexHome())
			for _, id := range []string{threadMain, threadOld} {
				if err := n.refreshCodexAppHistory(id); err != nil {
					t.Fatal(err)
				}
			}
			paths := map[string]string{}
			for _, id := range []string{threadMain, threadOld} {
				paths[id] = n.codexAppHistoryFile(codexSource{home: n.traexHome(), thread: id})
			}
			beforeA, beforeB := statOf(t, paths[threadMain]), statOf(t, paths[threadOld])
			markB, _ := readCodexHistoryMark(paths[threadOld])
			stamp := fileStamp(db) + fileStamp(db+"-wal")
			traexHistoryRevisionSQL(t, db, fmt.Sprintf(`BEGIN;
				UPDATE thread_items SET item_json = REPLACE(item_json, 'Inspect the export', 'Inspect the changed export')
					WHERE thread_id = '%s';
				UPDATE thread_history_projection_state SET next_rollout_ordinal = next_rollout_ordinal + 1
					WHERE thread_id = '%s'; COMMIT;`, threadMain, threadMain))
			if stamp == fileStamp(db)+fileStamp(db+"-wal") {
				t.Fatal("fixture did not change the history database stamp")
			}
			// Refresh B before A, then read A and its cached summary.
			if err := n.refreshCodexAppHistory(threadOld); err != nil {
				t.Fatal(err)
			}
			traexHistoryRevisionSession(t, n, threadMain, "")
			if os.SameFile(beforeA, statOf(t, paths[threadMain])) {
				t.Fatal("the changed running snapshot was not rebuilt")
			}
			if afterB := statOf(t, paths[threadOld]); !os.SameFile(beforeB, afterB) ||
				!beforeB.ModTime().Equal(afterB.ModTime()) || beforeB.Size() != afterB.Size() {
				t.Fatal("an unrelated thread write rebuilt B's transcript")
			}
			if after, _ := readCodexHistoryMark(paths[threadOld]); after != markB {
				t.Fatalf("an unrelated thread write changed B's mark: before %+v, after %+v", markB, after)
			}
		})
	}
}

func TestTraexListingDoesNotRebuildChangedHistory(t *testing.T) {
	n := remoteTestNode(t)
	db := traexHistoryRevisionFixture(t, n, []historyTurn{{thread: threadMain, id: "turn-a", status: "completed", ordinal: 0,
		items: [][2]string{userItem("ua", "Inspect the export"), answerItem("aa", "The export is correct.")}}})
	f, err := n.file(threadMain)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Refresh(); err != nil {
		t.Fatal(err)
	}
	p := n.codexAppHistoryFile(codexSource{home: n.traexHome(), thread: threadMain})
	before := statOf(t, p)
	traexHistoryRevisionSQL(t, db, `UPDATE thread_items SET item_json = REPLACE(item_json, 'correct', 'ready');
		UPDATE thread_history_projection_state SET next_rollout_ordinal = next_rollout_ordinal + 1;`)
	for range 4 {
		if rows := n.traexSessions(map[string]bool{}); len(rows) != 1 {
			t.Fatalf("listing lost the conversation: %+v", rows)
		}
		if !os.SameFile(before, statOf(t, p)) {
			t.Fatal("a global list rebuilt the changed history")
		}
	}
	f, err = n.file(threadMain)
	if err != nil {
		t.Fatal(err)
	}
	items, err := f.Refresh()
	if err != nil || !strings.Contains(historyText(items), "assistant:text:The export is ready.") {
		t.Fatalf("an explicit read did not refresh history: %s, %v", historyText(items), err)
	}
}

func TestTraexHistoryRevisionSkipsProjectionContext(t *testing.T) {
	n := remoteTestNode(t)
	db := traexHistoryRevisionFixture(t, n, []historyTurn{{thread: threadMain, id: "turn-a", status: "inProgress", ordinal: 0,
		items: [][2]string{userItem("ua", "Inspect the export")}}})
	// Selecting the context would fail, while the three revision columns remain readable.
	traexHistoryRevisionSQL(t, db, `ALTER TABLE thread_history_projection_state RENAME TO projection_markers;
		CREATE VIEW thread_history_projection_state AS
		SELECT thread_id, next_rollout_ordinal, active_turn_id,
			json_extract('synthetic invalid context', '$') AS projection_context_json FROM projection_markers;`)
	traexHistoryRevisionSession(t, n, threadMain, "")
	p := n.codexAppHistoryFile(codexSource{home: n.traexHome(), thread: threadMain})
	before := statOf(t, p)
	traexHistoryRevisionSQL(t, db, `CREATE TABLE unrelated (value INTEGER); INSERT INTO unrelated VALUES (1);`)
	traexHistoryRevisionSession(t, n, threadMain, "")
	if !os.SameFile(before, statOf(t, p)) {
		t.Fatal("reading projection context forced an unnecessary whole-database refresh")
	}
}

func TestTraexHistoryRevisionFollowsActiveChanges(t *testing.T) {
	n := remoteTestNode(t)
	db := traexHistoryRevisionFixture(t, n, []historyTurn{{thread: threadMain, id: "turn-a", status: "inProgress", ordinal: 0,
		items: [][2]string{userItem("ua", "Inspect the export")}}})
	traexHistoryRevisionSession(t, n, threadMain, "")
	f, err := n.file(threadMain)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Refresh(); err != nil {
		t.Fatal(err)
	}
	p := n.codexAppHistoryFile(codexSource{home: n.traexHome(), thread: threadMain})
	initial, _ := readCodexHistoryMark(p)
	for _, step := range []struct {
		sql, last string
		complete  bool
	}{
		{fmt.Sprintf(`UPDATE thread_items SET item_json = REPLACE(item_json, 'Inspect the export', 'Inspect the changed export')
			WHERE thread_id = '%s'; UPDATE thread_history_projection_state SET next_rollout_ordinal = next_rollout_ordinal + 1;`, threadMain),
			"", false},
		{fmt.Sprintf(`INSERT INTO thread_items VALUES ('%s', 'turn-a', 'answer', 10, 1,
			'{"type":"agentMessage","id":"answer","text":"The export is correct.","phase":"final_answer"}', 'agentMessage');
			UPDATE thread_history_projection_state SET next_rollout_ordinal = next_rollout_ordinal + 1;`, threadMain),
			"The export is correct.", false},
		// Finishing changes active state even if the next ordinal has not advanced yet.
		{`UPDATE thread_turns SET status = 'completed'; UPDATE thread_history_projection_state SET active_turn_id = NULL;`,
			"The export is correct.", true},
	} {
		traexHistoryRevisionSQL(t, db, "BEGIN;"+step.sql+"COMMIT;")
		traexHistoryRevisionSession(t, n, threadMain, step.last)
		items, err := f.Refresh()
		text := historyText(items)
		if err != nil || strings.Count(text, "user:text:Inspect the changed export") != 1 || strings.Contains(text, "user:text:Inspect the export") {
			t.Fatalf("updated running items = %s, %v", text, err)
		}
		if step.last == "The export is correct." && strings.Count(text, "assistant:text:The export is correct.") != 1 {
			t.Fatalf("answer missing or duplicated: %s", text)
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		completed := strings.Count(string(raw), `"type":"task_complete"`)
		if step.complete && completed != 1 || !step.complete && completed != 0 {
			t.Fatalf("completion events = %d, want completed %v", completed, step.complete)
		}
		if mark, _ := readCodexHistoryMark(p); mark.Updated != initial.Updated {
			t.Fatalf("fixture changed threads.updated_at_ms: %d -> %d", initial.Updated, mark.Updated)
		}
	}
}

func TestTraexHistoryRevisionFallsBackAfterProjectionReadFailure(t *testing.T) {
	for _, schema := range []struct{ name, sql string }{
		{"old schema", `DROP TABLE thread_history_projection_state;`},
		{"missing thread", `DELETE FROM thread_history_projection_state;`},
		{"unsupported columns", `ALTER TABLE thread_history_projection_state RENAME COLUMN next_rollout_ordinal TO old_ordinal;`},
		{"missing active state", `ALTER TABLE thread_history_projection_state RENAME COLUMN active_turn_id TO old_active_turn_id;`},
		{"failed read", `DROP TABLE thread_history_projection_state;
			CREATE VIEW thread_history_projection_state AS SELECT * FROM missing_projection_state;`},
	} {
		t.Run(schema.name, func(t *testing.T) {
			n := remoteTestNode(t)
			db := traexHistoryRevisionFixture(t, n, []historyTurn{{thread: threadMain, id: "turn-a", status: "inProgress", ordinal: 0,
				items: [][2]string{userItem("ua", "Inspect the export"), answerItem("aa", "The export is pending.")}}})
			traexHistoryRevisionSession(t, n, threadMain, "The export is pending.")
			traexHistoryRevisionSQL(t, db, schema.sql)
			traexHistoryRevisionSession(t, n, threadMain, "The export is pending.")
			for _, last := range []string{"The export is correct.", "The export is ready."} {
				traexHistoryRevisionSQL(t, db, fmt.Sprintf(`UPDATE thread_items SET item_json =
					'{"type":"agentMessage","id":"aa","text":"%s","phase":"final_answer"}'
					WHERE thread_id = '%s' AND item_type = 'agentMessage';`, last, threadMain))
				traexHistoryRevisionSession(t, n, threadMain, last)
				p := n.codexAppHistoryFile(codexSource{home: n.traexHome(), thread: threadMain})
				if mark, _ := readCodexHistoryMark(p); mark.History != fileStamp(db)+fileStamp(db+"-wal") {
					t.Fatalf("fallback did not use the whole database stamp: %+v", mark)
				}
			}
		})
	}
}

func TestTraexHistoryRevisionCachesProjectionReads(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sqlite3 query counter uses a POSIX shell wrapper")
	}
	for _, oldSchema := range []bool{false, true} {
		t.Run(fmt.Sprintf("old_schema_%v", oldSchema), func(t *testing.T) {
			n := remoteTestNode(t)
			db := traexHistoryRevisionFixture(t, n, []historyTurn{
				{thread: threadMain, id: "turn-a", status: "inProgress", ordinal: 0,
					items: [][2]string{userItem("ua", "Inspect the export")}},
				{thread: threadOld, id: "turn-b", status: "completed", ordinal: 0,
					items: [][2]string{userItem("ub", "A separate question")}},
			})
			traexHistoryRevisionSQL(t, filepath.Join(n.traexHome(), "state_5.sqlite"),
				fmt.Sprintf(`UPDATE threads SET archived = 0 WHERE id = '%s';`, threadOld))
			invalidateCodexPaginated(n.traexHome())
			n.traexThreads()
			if oldSchema {
				traexHistoryRevisionSQL(t, db, `DROP TABLE thread_history_projection_state;`)
			}
			sqlite := sqlite3WithJSON(t)
			dir := t.TempDir()
			log := filepath.Join(dir, "projection-reads")
			wrapper := `#!/bin/sh
readonly_query=no
projection_query=no
for arg do
	case "$arg" in
		-readonly) readonly_query=yes ;;
		*thread_history_projection_state*) projection_query=yes ;;
	esac
done
if [ "$readonly_query" = yes ] && [ "$projection_query" = yes ]; then
	printf 'read\n' >> "$TRAEX_REVISION_TEST_LOG"
fi
exec "$TRAEX_REVISION_TEST_SQLITE" "$@"
`
			if err := os.WriteFile(filepath.Join(dir, "sqlite3"), []byte(wrapper), 0o700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("TRAEX_REVISION_TEST_LOG", log)
			t.Setenv("TRAEX_REVISION_TEST_SQLITE", sqlite)
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			for pass := 1; pass <= 2; pass++ {
				// Concurrent readers and repeated lists share one projection query, including a cached failed read.
				errs := make(chan error, 4)
				for _, id := range []string{threadMain, threadOld, threadMain, threadOld} {
					go func() { errs <- n.refreshCodexAppHistory(id) }()
				}
				for range 4 {
					if err := <-errs; err != nil {
						t.Fatal(err)
					}
				}
				for range 3 {
					if rows := n.traexSessions(map[string]bool{}); len(rows) != 2 {
						t.Fatalf("listed %d conversations, want 2", len(rows))
					}
				}
				reads, err := os.ReadFile(log)
				if err != nil || strings.Count(string(reads), "read\n") != pass {
					t.Fatalf("projection reads after database change %d = %q, %v", pass, reads, err)
				}
				if pass == 1 {
					sql := fmt.Sprintf(`UPDATE thread_items SET item_json = REPLACE(item_json, 'Inspect the export', 'Inspect the changed export')
						WHERE thread_id = '%s';`, threadMain)
					if !oldSchema {
						sql += fmt.Sprintf(`UPDATE thread_history_projection_state SET next_rollout_ordinal = next_rollout_ordinal + 1
							WHERE thread_id = '%s';`, threadMain)
					}
					traexHistoryRevisionSQL(t, db, "BEGIN;"+sql+"COMMIT;")
				}
			}
		})
	}
}
