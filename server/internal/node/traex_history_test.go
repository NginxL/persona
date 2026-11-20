package node

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/transcript"
)

func TestTraexOfflineTiedRowsKeepIDOrder(t *testing.T) {
	n := remoteTestNode(t)
	traexHistoryFixture(t, n, nil)
	writeCodexApp(t, n.traexHome(), "/work/app", `UPDATE threads SET archived = 0;
		UPDATE threads SET updated_at = 10, updated_at_ms = 10000;`)
	invalidateCodexPaginated(n.traexHome())
	want := []string{threadMain, threadOld}
	for range 32 {
		var ids []string
		for _, row := range n.traexSessions(map[string]bool{}) {
			ids = append(ids, row.SessionID)
		}
		if !slices.Equal(ids, want) {
			t.Fatalf("unchanged tied conversations changed order: %v, want %v", ids, want)
		}
	}
}

func traexHistoryFixture(t *testing.T, n *Node, turns []historyTurn) {
	t.Helper()
	home := n.traexHome()
	writeCodexApp(t, home, "/work/app", codexAppSchema+
		codexAppRow(threadMain, "", "/work/app", "Inspect the export", time.Now(), 0)+
		codexAppRow(threadGuardian, "", "/work/app", "Review a tool", time.Now(), 0)+
		codexAppRow(threadOld, "", "/work/app", "Archived", time.Now(), 1)+
		`ALTER TABLE threads ADD COLUMN history_mode TEXT DEFAULT 'paginated';
		UPDATE threads SET source = 'cli';
		UPDATE threads SET source = '{"subagent":{"other":"guardian"}}' WHERE id = '`+threadGuardian+`';`)
	writeCodexHistory(t, home, turns)
	invalidateCodexPaginated(home)
}

func TestTraexPaginatedHistoryListsAndReadsWithoutRollouts(t *testing.T) {
	n := remoteTestNode(t)
	traexHistoryFixture(t, n, []historyTurn{{thread: threadMain, id: "turn-1", status: "completed", ordinal: 1,
		items: [][2]string{userItem("u1", "Inspect the export"), answerItem("a1", "The export is correct.")}}})
	if got := rolloutPath(n.traexHome(), threadMain); got != "" {
		t.Fatalf("fixture unexpectedly has a rollout: %s", got)
	}
	rows := n.traexSessions(map[string]bool{})
	if len(rows) != 1 || rows[0].SessionID != threadMain || rows[0].Agent != traexAgent ||
		rows[0].Title != "Inspect the export" || rows[0].Status != proto.StatusOffline {
		t.Fatalf("paginated conversation = %+v", rows)
	}
	if fileExists(n.codexAppHistoryFile(codexSource{home: n.traexHome(), thread: threadMain})) {
		t.Fatal("listing a cold conversation synchronously projected its history")
	}
	if got := n.traexSessions(map[string]bool{threadMain: true}); len(got) != 0 {
		t.Fatalf("live conversation listed twice: %+v", got)
	}
	f, err := n.file(threadMain)
	if err != nil {
		t.Fatal(err)
	}
	items, err := f.Refresh()
	if err != nil {
		t.Fatal(err)
	}
	text := historyText(items)
	if !strings.Contains(text, "user:text:Inspect the export") || !strings.Contains(text, "assistant:text:The export is correct.") {
		t.Fatalf("conversation not readable: %s", text)
	}
	if rows := n.traexSessions(map[string]bool{}); len(rows) != 1 || rows[0].LastText != "The export is correct." {
		t.Fatalf("the summary did not use the already-read transcript: %+v", rows)
	}
	if p := n.transcriptPath(threadMain); !strings.HasPrefix(p, n.dir+string(filepath.Separator)) {
		t.Fatalf("history materialized outside the node state: %q", p)
	}
	// Reading Persona's copy never creates or modifies TRAE's legacy rollouts.
	if got := rolloutPath(n.traexHome(), threadMain); got != "" {
		t.Fatalf("reader wrote a CLI rollout: %s", got)
	}
}

func TestTraexPaginatedRunningTurnIsReadableAndFinishesWithoutDuplicates(t *testing.T) {
	n := remoteTestNode(t)
	turn := historyTurn{thread: threadMain, id: "turn-1", status: "inProgress", ordinal: 1,
		items: [][2]string{userItem("u1", "Inspect the export")}}
	traexHistoryFixture(t, n, []historyTurn{turn})
	f, err := n.file(threadMain)
	if err != nil {
		t.Fatal(err)
	}
	items, err := f.Refresh()
	if err != nil || !strings.Contains(historyText(items), "user:text:Inspect the export") {
		t.Fatalf("running turn: %s, %v", historyText(items), err)
	}
	mark, _ := readCodexHistoryMark(f.Where())
	if mark.Version != codexAppHistoryVersion || mark.Turn != 0 || mark.TurnID != turn.id || mark.Status != turn.status {
		t.Fatalf("running turn mark: %+v", mark)
	}
	turn.status = "completed"
	turn.items = append(turn.items, answerItem("a1", "The export is correct."))
	writeCodexHistory(t, n.traexHome(), []historyTurn{turn})
	if err := n.refreshCodexAppHistory(threadMain); err != nil {
		t.Fatal(err)
	}
	items, err = f.Refresh()
	text := historyText(items)
	if err != nil || strings.Count(text, "user:text:Inspect the export") != 1 || strings.Count(text, "assistant:text:The export is correct.") != 1 {
		t.Fatalf("finished turn: %s, %v", text, err)
	}
}

// Append to the running turn in the CLI database. Only the watch pump can project and push the reply.
func TestTraexWatchPushesRunningTurnItem(t *testing.T) {
	n := remoteTestNode(t)
	db := traexHistoryRevisionFixture(t, n, []historyTurn{{thread: threadMain, id: "turn-1", status: "inProgress",
		items: [][2]string{userItem("u1", "Inspect the export")}}})
	hub := withHub(t, n)
	if err := n.watch(threadMain, 0); err != nil {
		t.Fatal(err)
	}
	defer n.unwatch(threadMain)
	answer := answerItem("a1", "The export is correct.")
	traexHistoryRevisionSQL(t, db, fmt.Sprintf(`BEGIN;
		INSERT INTO thread_items VALUES ('%s', 'turn-1', 'a1', 1, 1, '%s', '%s');
		UPDATE thread_history_projection_state SET next_rollout_ordinal = next_rollout_ordinal + 1
			WHERE thread_id = '%s'; COMMIT;`, threadMain, answer[1], answer[0], threadMain))
	waitFor(t, "the watched TRAE reply", func() bool {
		return slices.ContainsFunc(hub.got(), func(it proto.Item) bool {
			return it.Role == "assistant" && it.Text == "The export is correct."
		})
	})
}

func TestTraexNonPaginatedRunningTurnCompletesInPlace(t *testing.T) {
	home := t.TempDir()
	turn := historyTurn{thread: threadMain, id: "turn-1", ordinal: 7, status: "inProgress",
		items: [][2]string{userItem("u1", "Inspect the export")}}
	writeCodexHistory(t, home, []historyTurn{turn})
	db := codexHistoryDBPath(home)
	path := filepath.Join(t.TempDir(), "history.jsonl")
	row := codexAppThread{ID: threadMain, includeRunning: true, Paginated: false}
	f := transcript.NewFile(path)
	project := func(status string) string {
		t.Helper()
		res, err := writeCodexAppHistory(context.Background(), db, row, path, codexHistoryMark{}, false)
		if err != nil {
			t.Fatal(err)
		}
		if res.turn != int64(turn.ordinal) || res.turnID != turn.id || res.status != status {
			t.Fatalf("watermark for %s: %+v", status, res)
		}
		items, err := f.Refresh()
		if err != nil {
			t.Fatal(err)
		}
		return historyText(items)
	}
	if got := project("inProgress"); !strings.Contains(got, "user:text:Inspect the export") {
		t.Fatalf("running transcript: %s", got)
	}
	answer := answerItem("a1", "The export is correct.")
	traexHistoryRevisionSQL(t, db, fmt.Sprintf(`BEGIN;
		INSERT INTO thread_items VALUES ('%s', 'turn-1', 'a1', 1, 1, '%s', '%s');
		UPDATE thread_turns SET status = 'completed' WHERE thread_id = '%s' AND turn_id = 'turn-1';
		COMMIT;`, threadMain, answer[1], answer[0], threadMain))
	if got := project("completed"); strings.Count(got, "user:text:Inspect the export") != 1 ||
		strings.Count(got, "assistant:text:The export is correct.") != 1 {
		t.Fatalf("completed transcript: %s", got)
	}
}

func TestTraexPaginatedOwnerReplacesStaleHostSession(t *testing.T) {
	n, _, _ := startNode(t)
	traexHistoryFixture(t, n, nil)
	writeOwner(t, n, threadMain, time.Now().Add(-time.Minute))
	writeOwner(t, n, threadGuardian, time.Now())
	if got := n.traexOwners()[os.Getpid()]; got != threadMain {
		t.Fatalf("picked guardian instead of paginated conversation: %q", got)
	}
	const pane = "w1:p2"
	traexCache.Lock()
	previous, had := traexCache.panes[os.Getpid()]
	traexCache.panes[os.Getpid()] = pane
	traexCache.Unlock()
	t.Cleanup(func() {
		traexCache.Lock()
		defer traexCache.Unlock()
		if had {
			traexCache.panes[os.Getpid()] = previous
		} else {
			delete(traexCache.panes, os.Getpid())
		}
	})
	agents := []host.Agent{{Agent: traexAgent, PaneID: pane, AgentSession: &host.AgentSession{Kind: "id", Value: threadOld}}}
	if added := n.traexAgents(context.Background(), []host.Agent{{PaneID: pane}}, agents); len(added) != 0 {
		t.Fatalf("one pane became two agents: %+v", added)
	}
	if got := agents[0].SessionID(); got != threadMain {
		t.Fatalf("host's stale session survived the exact writer lock: %q", got)
	}
}
