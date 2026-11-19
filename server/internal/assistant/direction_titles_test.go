package assistant

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// Seed the persisted records directly so setup has no asynchronous direction sync
// publishing a snapshot on behalf of the name-change operation under test.
func directionTitleEnv(t *testing.T) *todoEventEnv {
	t.Helper()
	e := directionEnv(t, &memStore{})
	e.s.titleSet("server", "lead0001", "1117 | 文档 | Handoff notes", titleByAssistant)
	e.s.titleSet("server", "side0001", "1117 | 研究 | A question", titleByAssistant)
	e.s.titleSet("server", "conv_main", "1117 | 探索 | Main history", titleByAssistant)
	setSettings(t, e.s, func(v *Settings) {
		v.MachineID, v.Main = "server", "conv_main"
		v.Leaders = []string{"lead0001", "conv_main"}
	})
	cfg := e.s.settings(t.Context())
	e.f.setSessions(cfg.MachineID,
		proto.Session{MachineID: cfg.MachineID, SessionID: "conv_main", Assistant: true, Agent: "codex"},
		proto.Session{MachineID: cfg.MachineID, SessionID: "lead0001", Assistant: true, Agent: "codex"},
		proto.Session{MachineID: cfg.MachineID, SessionID: "side0001", Assistant: true, Agent: "codex"})
	b := &e.s.directions
	b.mu.Lock()
	if !e.s.directionsLocked(t.Context()) {
		b.mu.Unlock()
		t.Fatal("direction records unavailable")
	}
	b.m["lead0001"] = &direction{MachineID: cfg.MachineID, By: directionByUser, At: time.Now(),
		Name: "Platform supervisor", NameBy: directionByAssistant}
	b.mu.Unlock()
	return e
}

func directionSessionTitle(t *testing.T, snap Snapshot, id string) string {
	t.Helper()
	for _, session := range snap.Sessions {
		if session.SessionID == id {
			return session.Title
		}
	}
	t.Fatalf("snapshot has no conversation %s", id)
	return ""
}

func TestDirectionSessionTitleIsItsNameAndRestoresTheOrdinaryTitle(t *testing.T) {
	e := directionTitleEnv(t)
	ctx := t.Context()
	raw, _ := e.s.conversationTitles()
	snap := e.s.snapshot(ctx)
	if got := directionSessionTitle(t, snap, "lead0001"); got != "Platform supervisor" {
		t.Fatalf("active direction title: %q", got)
	}
	if got := directionSessionTitle(t, snap, "conv_main"); got != "Persona" {
		t.Fatalf("main title changed: %q", got)
	}
	for _, id := range []string{"side0001"} {
		if got := directionSessionTitle(t, snap, id); got != raw[id].title {
			t.Fatalf("ordinary/main title changed: %s = %q", id, got)
		}
	}
	e.s.pubMu.Lock()
	before := e.s.seq
	e.s.pubMu.Unlock()
	if err := e.s.setDirectionName(ctx, "lead0001", "Release supervisor", directionByUser); err != nil {
		t.Fatal(err)
	}
	e.s.pubMu.Lock()
	after := e.s.seq
	e.s.pubMu.Unlock()
	if after <= before || directionSessionTitle(t, e.f.lastSnapshot(), "lead0001") != "Release supervisor" {
		t.Fatal("the name change did not publish its new session title immediately")
	}
	if err := e.s.setDirectionName(ctx, "lead0001", "", directionByUser); err != nil {
		t.Fatal(err)
	}
	if got := directionSessionTitle(t, e.s.snapshot(ctx), "lead0001"); got != "Handoff notes" {
		t.Fatalf("cleared name's fallback: %q", got)
	}
	stored, _ := e.s.conversationTitles()
	if stored["lead0001"] != raw["lead0001"] {
		t.Fatal("display projection rewrote the stored ordinary title")
	}
	e.leaders()
	if got := directionSessionTitle(t, e.s.snapshot(ctx), "lead0001"); got != raw["lead0001"].title {
		t.Fatalf("ended direction did not restore its ordinary title: %q", got)
	}
}

func TestDirectionSessionTitleKeepsTheCanonicalOfflineProjectName(t *testing.T) {
	e := directionTitleEnv(t)
	b := &e.s.directions
	b.mu.Lock()
	d := b.m["lead0001"]
	d.Name, d.NameBy, d.Project = "", "", "platform"
	d.Told = &leaderTold{Name: "Platform project", Project: "platform"}
	b.mu.Unlock()
	if got := directionSessionTitle(t, e.s.snapshot(t.Context()), "lead0001"); got != "Platform project" {
		t.Fatalf("offline project fallback differs from the direction tree: %q", got)
	}
}

func TestDirectionConversationRenameUsesAPlainName(t *testing.T) {
	e := directionTitleEnv(t)
	ctx := context.Background()
	for _, caller := range []string{"lead0001", "side0001"} {
		if _, err := runJSON(t, e.s, caller, "conversation_rename", map[string]any{"id": "lead0001", "name": "Other supervisor"}); err == nil || !strings.Contains(err.Error(), "only the main conversation") {
			t.Fatalf("direction-name permission from %s: %v", caller, err)
		}
	}
	if _, err := runJSON(t, e.s, "conv_main", "conversation_rename", map[string]any{"id": "lead0001", "name": "Release supervisor"}); err != nil {
		t.Fatalf("plain name without kind/topic: %v", err)
	}
	if got := directionSessionTitle(t, e.s.snapshot(ctx), "lead0001"); got != "Release supervisor" {
		t.Fatalf("plain rename gained a date or kind: %q", got)
	}
	if err := e.s.setDirectionName(ctx, "lead0001", "Owner project", directionByUser); err != nil {
		t.Fatal(err)
	}
	args := map[string]any{"id": "lead0001", "name": "New supervisor"}
	if _, err := runJSON(t, e.s, "conv_main", "conversation_rename", args); err == nil || !strings.Contains(err.Error(), "user_asked") {
		t.Fatalf("user-chosen name was not protected: %v", err)
	}
	args["user_asked"] = true
	if _, err := runJSON(t, e.s, "conv_main", "conversation_rename", args); err != nil {
		t.Fatal(err)
	}
	if _, err := runJSON(t, e.s, "conv_main", "conversation_rename", map[string]any{
		"id": "side0001", "kind": "研究", "topic": "A question", "user_asked": true}); err != nil {
		t.Fatalf("ordinary kind/topic rename: %v", err)
	}
	stored, _ := e.s.conversationTitles()
	if !strings.Contains(stored["side0001"].title, " | ") || stored["lead0001"].title != "1117 | 文档 | Handoff notes" {
		t.Fatal("ordinary-title storage or formatting changed")
	}
}

func TestDirectionSessionTitleDoesNotOpenTheKnowledgeBase(t *testing.T) {
	e := directionTitleEnv(t)
	e.s.kb.dir = t.TempDir()
	e.f.mu.Lock()
	e.f.hello.KB = &KBSettings{}
	e.f.mu.Unlock()
	t.Cleanup(e.s.kbEnd)
	if e.s.kb.cur.Load() != nil {
		t.Fatal("the test's knowledge base was already open")
	}
	if got := directionSessionTitle(t, e.s.snapshot(t.Context()), "lead0001"); got != "Platform supervisor" {
		t.Fatalf("name before the knowledge base opens: %q", got)
	}
	if e.s.kb.cur.Load() != nil {
		t.Fatal("title projection opened the knowledge base on the snapshot path")
	}
}

func TestDirectionConversationRenameFromTheAppUpdatesTheName(t *testing.T) {
	e := directionTitleEnv(t)
	before, _ := e.s.conversationTitles()
	if _, err := e.s.Rename(t.Context(), RenameParams{MachineID: "server", SessionID: "lead0001",
		Params: map[string]any{"title": "App-chosen supervisor"}}); err != nil {
		t.Fatal(err)
	}
	d := e.s.directionOf("lead0001")
	if d.Name != "App-chosen supervisor" || d.NameBy != directionByUser {
		t.Fatalf("App rename did not set the canonical user name: %+v", d)
	}
	if got := directionSessionTitle(t, e.f.lastSnapshot(), "lead0001"); got != d.Name {
		t.Fatalf("App rename did not publish its display name: %q", got)
	}
	after, _ := e.s.conversationTitles()
	if after["lead0001"] != before["lead0001"] {
		t.Fatal("App direction rename changed the ordinary stored title")
	}
}
