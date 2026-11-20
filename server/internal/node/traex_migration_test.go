package node

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/proto"
)

func TestTraexPaginatedMigrationReplacesCachedRollout(t *testing.T) {
	n := remoteTestNode(t)
	writeMetaRollout(t, n.traexHome(), threadMain, `{"id":"`+threadMain+`","cwd":"/work/app","source":"cli"}`, time.Now())
	legacy := rolloutPath(n.traexHome(), threadMain)
	f, err := os.OpenFile(legacy, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteString(`{"timestamp":"2026-11-01T10:00:00Z","type":"event_msg","payload":{"type":"item_completed","item":{"type":"AgentMessage","id":"old","content":[{"type":"text","text":"Old result."}],"phase":"final_answer"}}}` + "\n")
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	reader, err := n.file(threadMain)
	if err != nil {
		t.Fatal(err)
	}
	old, err := reader.Refresh()
	if err != nil || !strings.Contains(historyText(old), "Old result.") || reader.Where() != legacy {
		t.Fatalf("legacy reader: %s, %v, %s", historyText(old), err, reader.Where())
	}
	traexHistoryFixture(t, n, []historyTurn{{thread: threadMain, id: "new-turn", status: "completed", ordinal: 1,
		items: [][2]string{userItem("u1", "Inspect the export"), answerItem("a1", "New result.")}}})
	if err := n.refreshCodexAppHistory(threadMain); err != nil {
		t.Fatal(err)
	}
	if got := n.transcriptPath(threadMain); got == legacy || got == "" {
		t.Fatalf("cached rollout still hides paginated history: %s", got)
	}
	again, err := n.file(threadMain)
	if err != nil || again != reader {
		t.Fatalf("open reader was not retained: %p, %v", again, err)
	}
	items, err := reader.Refresh()
	text := historyText(items)
	if err != nil || !strings.Contains(text, "New result.") || strings.Contains(text, "Old result.") {
		t.Fatalf("migrated reader: %s, %v", text, err)
	}
	if !fileExists(legacy) {
		t.Fatal("migration reader removed the CLI's retained rollout")
	}
	n.agents[threadMain] = host.Agent{Agent: traexAgent, PaneID: "w1:p2", Cwd: "/work/app", AgentStatus: proto.StatusIdle}
	rows := n.sessionList(context.Background(), false)
	for _, s := range rows {
		if s.SessionID == threadMain {
			if s.LastText != "New result." {
				t.Fatalf("live row still uses the legacy reply: %+v", s)
			}
			return
		}
	}
	t.Fatal("migrated live conversation is missing")
}
