package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// A direction's lead conversation (#11392): kept listed however quiet, not deleted while it leads, still in the tree
// when its conversation is missing; notes saves that cannot overwrite each other and that reach the lead only when it
// is idle; an approval of assistant.leaders that compares the whole list. All ids, names and notes are placeholders.

// A current lead is kept listed on its direction record's computer (else the assistant's), on a fresh install with no
// setting touched, and leaves the set when its direction ends.
func TestProfileKeepsCurrentLeads(t *testing.T) {
	ctx := context.Background()
	f := newFakeHub().withMachine()
	s := newTestService(t, f)
	setSettings(t, s, func(v *Settings) { v.Main, v.Leaders = "main-1", []string{"main-1", "lead-a", "lead-b"} })
	b := &s.directions
	b.mu.Lock()
	if !s.directionsLocked(ctx) {
		t.Fatal("directions unreadable")
	}
	b.m["lead-a"] = &direction{MachineID: "box", By: directionByUser, At: time.Now()} // lead-b has no record yet
	b.mu.Unlock()
	s.publish(ctx)
	want := []SessionRef{{MachineID: "server", SessionID: "main-1"}, {MachineID: "box", SessionID: "lead-a"},
		{MachineID: "server", SessionID: "lead-b"}}
	if p := f.lastSnapshot().Profile; p == nil || !slices.Equal(p.Keep, want) {
		t.Fatalf("keep: %+v", p)
	}
	if ended, err := s.endDirection(ctx, "lead-a"); err != nil || !ended {
		t.Fatalf("end: %v %v", ended, err)
	}
	s.publish(ctx)
	want = []SessionRef{{MachineID: "server", SessionID: "main-1"}, {MachineID: "server", SessionID: "lead-b"}}
	if p := f.lastSnapshot().Profile; p == nil || !slices.Equal(p.Keep, want) {
		t.Fatalf("keep after the direction ended: %+v", p)
	}
}

// Deleting a current lead is refused with the protected-conversation code and a message, in the owner's language,
// that names the way out; once its direction ends the same delete goes through.
func TestDeleteRefusesACurrentLead(t *testing.T) {
	ctx := context.Background()
	f := healthHub()
	s := newTestService(t, f)
	lead := "work-0000-0000"
	setSettings(t, s, func(v *Settings) { v.MachineID, v.Main, v.Leaders = "server", "main-0000-0000", []string{lead} })
	path := "/api/v1/assistant/conversations/" + lead
	wayOut := map[i18n.Lang]string{i18n.En: "Unpin it", i18n.ZhHans: "取消常驻", i18n.Ja: "常駐をやめて"}
	for _, l := range []i18n.Lang{i18n.En, i18n.ZhHans, i18n.Ja} {
		f.mu.Lock()
		f.hello.Locale = string(l)
		f.mu.Unlock()
		w := serve(s, "DELETE", path, `{"machine_id":"server"}`)
		var body struct{ Code, Error string }
		_ = json.Unmarshal(w.Body.Bytes(), &body)
		if w.Code != http.StatusBadRequest || body.Code != "conversation_protected" ||
			body.Error != i18n.T(l, "assistant-conversation_delete.lead_protected") || !strings.Contains(body.Error, wayOut[l]) {
			t.Fatalf("%s: %d %s", l, w.Code, w.Body)
		}
	}
	if !slices.ContainsFunc(s.view(ctx, false).Conversations, func(c proto.Session) bool { return c.SessionID == lead }) {
		t.Fatal("the refused delete took the lead off the list")
	}
	if ended, err := s.endDirection(ctx, lead); err != nil || !ended {
		t.Fatalf("end: %v %v", ended, err)
	}
	if w := serve(s, "DELETE", path, `{"machine_id":"server"}`); w.Code != http.StatusOK {
		t.Fatalf("delete after the direction ended: %d %s", w.Code, w.Body)
	}
}

// The direction tree lists a direction whose conversation its computer does not list, and one whose conversation was
// deleted (while it was not a lead, then made one again, or before deleting a lead was refused), each with its name
// and tasks.
func TestDirectionTreeListsDirectionsWhoseConversationIsMissing(t *testing.T) {
	e := treeEnv(t) // leads conv_lead_a and conv_lead_b
	ctx := context.Background()
	e.leaders("conv_lead_a")
	e.f.setSessions("server", proto.Session{MachineID: "server", SessionID: "conv_lead_b", Assistant: true, Status: proto.StatusIdle})
	if err := e.s.deleteConversation(ctx, "server", "conv_lead_b"); err != nil {
		t.Fatal(err)
	}
	e.f.setSessions("server", proto.Session{MachineID: "server", SessionID: treeMain, Assistant: true, Status: proto.StatusIdle})
	e.leaders("conv_lead_a", "conv_lead_b")
	e.syncLeaders()
	a := e.task("A's task", supervisedBy("conv_lead_a"))
	b := e.task("B's task", supervisedBy("conv_lead_b"))

	rec := httptest.NewRecorder()
	e.s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/assistant/directions/tree", nil))
	var tree directionTree
	if err := json.Unmarshal(rec.Body.Bytes(), &tree); rec.Code != http.StatusOK || err != nil {
		t.Fatalf("tree: %d %s", rec.Code, rec.Body)
	}
	got := tree.Directions
	if len(got) != 3 || !got[2].Secretary {
		t.Fatalf("directions: %+v", got)
	}
	if got[0].ID != "conv_lead_a" || got[0].Name != "Android parity" || !slices.Equal(taskIDs(got[0]), []string{a}) {
		t.Fatalf("not listed: %+v", got[0])
	}
	if got[1].ID != "conv_lead_b" || got[1].Name != "Push relay" || !slices.Equal(taskIDs(got[1]), []string{b}) {
		t.Fatalf("deleted: %+v", got[1])
	}
	if _, listed := e.s.session(ctx, "server", "conv_lead_a"); listed {
		t.Fatal("conv_lead_a is listed")
	}
	if err := e.s.conversationIDAvailable(ctx, "conv_lead_b"); convCode(err) != "conversation_deleted" {
		t.Fatalf("conv_lead_b is not deleted: %v", err)
	}
}

// notesAnswer is a notes PUT's answer: the notes kept and their time, or the 409's code and what is stored.
type notesAnswer struct {
	Code    string     `json:"code"`
	Error   string     `json:"error"`
	Notes   string     `json:"notes"`
	NotesAt *time.Time `json:"notes_at"`
}

func putNotesAnswer(t *testing.T, s *Service, body string, status int) notesAnswer {
	t.Helper()
	rec := putNotes(t, s, "conv_lead", body, false)
	var out notesAnswer
	if err := json.Unmarshal(rec.Body.Bytes(), &out); rec.Code != status || err != nil {
		t.Fatalf("PUT %s: %d %s, want %d", body, rec.Code, rec.Body, status)
	}
	return out
}

// listedNotesAt is conv_lead's notes_at exactly as GET /api/v1/assistant/leaders writes it.
func listedNotesAt(t *testing.T, s *Service) string {
	t.Helper()
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/assistant/leaders", nil))
	var list struct {
		Leaders []struct {
			ID      string          `json:"id"`
			NotesAt json.RawMessage `json:"notes_at"`
		} `json:"leaders"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	for _, l := range list.Leaders {
		if l.ID == "conv_lead" {
			return strings.Trim(string(l.NotesAt), `"`)
		}
	}
	t.Fatalf("conv_lead not listed: %s", rec.Body)
	return ""
}

// A notes save that says which notes_at it started from is refused with what is stored when that is no longer it,
// and changes nothing; the current value is accepted as listed and as a client writes it again at millisecond
// precision; without notes_at a save is not checked.
func TestDirectionNotesSaveChecksNotesAt(t *testing.T) {
	s, _ := notesEnv(t)
	ctx := context.Background()
	// Two editors open the direction with no notes (null); the first save wins, the second is refused.
	first := putNotesAnswer(t, s, `{"notes":"Readings: the store build number.","notes_at":null}`, http.StatusOK)
	if first.NotesAt == nil || first.NotesAt.Nanosecond()%int(time.Millisecond) != 0 {
		t.Fatalf("first save's time: %+v", first)
	}
	stale := putNotesAnswer(t, s, `{"notes":"Never deploy on Fridays.","notes_at":null}`, http.StatusConflict)
	if stale.Code != codeNotesChanged || stale.Error != i18n.T(i18n.Default, "assistant-direction_notes.changed") ||
		stale.Notes != "Readings: the store build number." || stale.NotesAt == nil || !stale.NotesAt.Equal(*first.NotesAt) {
		t.Fatalf("stale null: %+v", stale)
	}
	if d := s.directionOf("conv_lead"); d.Notes != "Readings: the store build number." || !d.NotesAt.Equal(*first.NotesAt) {
		t.Fatalf("a refused save changed the notes: %+v", d)
	}
	// The value as listed, and the same instant written again at millisecond precision in another zone.
	second := putNotesAnswer(t, s, `{"notes":"Readings: v2.","notes_at":"`+listedNotesAt(t, s)+`"}`, http.StatusOK)
	again := second.NotesAt.In(time.FixedZone("", 8*3600)).Format("2006-01-02T15:04:05.000Z07:00")
	third := putNotesAnswer(t, s, `{"notes":"Readings: v3.","notes_at":"`+again+`"}`, http.StatusOK)
	if !third.NotesAt.After(*second.NotesAt) || !second.NotesAt.After(*first.NotesAt) {
		t.Fatalf("times: %v %v %v", first.NotesAt, second.NotesAt, third.NotesAt)
	}
	// An editor that started from the first value is refused with the third.
	old := putNotesAnswer(t, s, `{"notes":"Readings: old.","notes_at":"`+first.NotesAt.Format(time.RFC3339Nano)+`"}`, http.StatusConflict)
	if old.Notes != "Readings: v3." || !old.NotesAt.Equal(*third.NotesAt) {
		t.Fatalf("stale time: %+v", old)
	}
	// Without notes_at: saved as before. A notes_at that is not a time: a bad request.
	if got := putNotesAnswer(t, s, `{"notes":"Readings: v4."}`, http.StatusOK); got.Notes != "Readings: v4." {
		t.Fatalf("unchecked: %+v", got)
	}
	putNotesAnswer(t, s, `{"notes":"x","notes_at":"yesterday"}`, http.StatusBadRequest)
	// A record from before (a finer time) still matches the value a client wrote again at millisecond precision.
	fine := time.Date(2026, 11, 20, 1, 2, 3, 456789123, time.UTC)
	s.directions.mu.Lock()
	s.directions.m["conv_lead"].NotesAt = &fine
	s.directions.mu.Unlock()
	putNotesAnswer(t, s, `{"notes":"Readings: v5.","notes_at":"2026-11-20T01:02:03.456Z"}`, http.StatusOK)
	// Cleared: an editor that still holds a time is refused with no notes (null).
	cleared := putNotesAnswer(t, s, `{"notes":""}`, http.StatusOK)
	if cleared.NotesAt != nil {
		t.Fatalf("cleared: %+v", cleared)
	}
	if got := putNotesAnswer(t, s, `{"notes":"x","notes_at":"`+again+`"}`, http.StatusConflict); got.Notes != "" || got.NotesAt != nil {
		t.Fatalf("stale after a clear: %+v", got)
	}
	// Two saves in the same millisecond never share a time.
	at := time.Date(2026, 11, 20, 1, 2, 3, 0, time.UTC)
	if next := nextNotesAt(&at, at.Add(300*time.Microsecond)); !next.Equal(at.Add(time.Millisecond)) {
		t.Fatalf("next = %v", next)
	}
	if _, _, err := s.setDirectionNotes(ctx, "nope", "x", nil); err != errNoDirection {
		t.Fatalf("unknown: %v", err)
	}
}

// Notes saved while the lead works leave its policy as its process runs it, so the owner's next message brings the
// node no new role to refuse; the first send that finds the lead idle carries them, also after a manager restart.
func TestDirectionNotesWaitForAnIdleLead(t *testing.T) {
	s, f := notesEnv(t)
	ctx := context.Background()
	lead := func(c proto.Session) {
		c.SessionID, c.MachineID, c.Assistant = "conv_lead", "server", true
		f.setSessions("server", c)
	}
	policyOf := func(s *Service) string {
		t.Helper()
		p := map[string]any{"session_id": "conv_lead", "text": "go on"} // as the owner's message is dispatched
		if err := s.putSupervisionInstructions(ctx, "server", "conv_lead", p); err != nil {
			t.Fatal(err)
		}
		return p["supervision_instructions"].(string)
	}
	lead(proto.Session{Status: proto.StatusIdle})
	if _, _, err := s.setDirectionNotes(ctx, "conv_lead", "Readings: v1.", nil); err != nil {
		t.Fatal(err)
	}
	v1 := policyOf(s)
	if !strings.HasPrefix(v1, prompts.RenderSupervisionInstructions()+"\n\n") || !strings.Contains(v1, "Readings: v1.") {
		t.Fatalf("idle: %q", v1)
	}
	if _, _, err := s.setDirectionNotes(ctx, "conv_lead", "Readings: v2.", nil); err != nil {
		t.Fatal(err)
	}
	busy := []proto.Session{{Status: proto.StatusWorking}, {Status: proto.StatusBlocked}, {Status: proto.StatusIdle, Starting: true},
		{Status: proto.StatusIdle, PendingInteractions: 1}, {Status: proto.StatusIdle, Compaction: &proto.Compaction{State: proto.CompactionRunning}},
		{Status: proto.StatusIdle, Background: []proto.BackgroundTask{{ID: "bg1"}}}}
	for _, c := range busy {
		lead(c)
		if got := policyOf(s); got != v1 {
			t.Fatalf("busy %+v: the policy changed to %q", c, got)
		}
	}
	// A manager restart reads the block in effect from the direction's record.
	lead(proto.Session{Status: proto.StatusWorking})
	s2 := newServiceOn(t, f, s.store())
	if got := policyOf(s2); got != v1 {
		t.Fatalf("after a restart: %q", got)
	}
	lead(proto.Session{Status: proto.StatusIdle})
	v2 := policyOf(s2)
	if !strings.Contains(v2, "Readings: v2.") || strings.Contains(v2, "Readings: v1.") {
		t.Fatalf("first idle send: %q", v2)
	}
	lead(proto.Session{Status: proto.StatusWorking})
	if got := policyOf(s2); got != v2 {
		t.Fatalf("busy after the idle send: %q", got)
	}
	// Another leader's policy, and a leader without notes, are untouched.
	if other := mustSupervisionInstructions(t, s2, ctx, "server", "conv_other"); other != prompts.RenderSupervisionInstructions() {
		t.Fatal("another leader's policy changed")
	}
}

// leadID is the i-th of six 36-character conversation ids, as the launchers make them: the approval card shows only
// the first three of a list and the start of the fourth.
func leadID(i int) string {
	return fmt.Sprintf("%08d-0000-4000-8000-%012d", i+1, i+1)
}

// An assistant.leaders card raised against one list is refused as stale when the owner changed the list before
// approving, also where the card's clipped text reads the same; the list stays as the owner left it.
func TestLeadersApprovalComparesTheWholeList(t *testing.T) {
	ctx := context.Background()
	f := newFakeHub().withMachine()
	s := newTestService(t, f)
	ids := make([]string, 6)
	var listed []proto.Session
	for i := range ids {
		ids[i] = leadID(i)
		listed = append(listed, proto.Session{SessionID: ids[i], MachineID: "server", Assistant: true, Status: proto.StatusIdle})
	}
	f.setSessions("server", listed...)
	setSettings(t, s, func(v *Settings) { v.Main, v.Leaders = "main-1", slices.Clone(ids[:4]) })

	// propose has the assistant set value and, once the card is up, runs owner (the owner's own tap) and approves.
	propose := func(value []string, owner func()) string {
		t.Helper()
		raw, _ := json.Marshal(map[string]any{"action": "set", "key": "assistant.leaders", "value": value})
		out := make(chan string, 1)
		go func() {
			r := s.Tool(ctx, ToolCall{Name: "persona_settings", Arguments: raw, MachineID: "server", SessionID: "main-1"})
			out <- r.Content[0].Text
		}()
		q, ok := awaitCard(s)
		if !ok {
			t.Fatal("no card")
		}
		if owner != nil {
			owner()
			// The card's text alone does not show the change: it is what the same proposal reads now.
			v, _ := json.Marshal(value)
			if fresh, err := s.setLeaders(ctx, v); err != nil || fresh.apply == nil || fresh.card(s.lang(ctx)) != q.Summary {
				t.Fatalf("the card text differs, so this does not test the whole-list comparison: %v", err)
			}
		}
		allow := true
		answer, _ := json.Marshal(proto.Resolution{Allow: &allow})
		if _, err := s.Action(ctx, Action{Op: "answer", InteractionID: q.ID, Answer: answer}); err != nil {
			t.Fatal(err)
		}
		select {
		case text := <-out:
			return text
		case <-time.After(5 * time.Second):
			t.Fatal("the tool did not return")
			return ""
		}
	}
	leaders := func() []string { return s.settings(ctx).Leaders }

	// Four leaders; the owner pins a fifth while the card waits.
	out := propose(append(slices.Clone(ids[:4]), ids[5]), func() {
		if _, err := s.makeDirection(ctx, ids[4], "", "", ""); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "changed after this card was prepared") || !slices.Equal(leaders(), ids[:5]) {
		t.Fatalf("pinned meanwhile: %s %v", out, leaders())
	}
	// Five leaders; the owner unpins the fifth while the card waits.
	out = propose(slices.Clone(ids), func() {
		if ended, err := s.endDirection(ctx, ids[4]); err != nil || !ended {
			t.Fatalf("end: %v %v", ended, err)
		}
	})
	if !strings.Contains(out, "changed after this card was prepared") || !slices.Equal(leaders(), ids[:4]) {
		t.Fatalf("unpinned meanwhile: %s %v", out, leaders())
	}
	// Nothing changed meanwhile: the approval applies.
	want := append(slices.Clone(ids[:4]), ids[5])
	if out = propose(want, nil); !strings.HasPrefix(out, "assistant.leaders is") || !slices.Equal(leaders(), want) {
		t.Fatalf("approved: %s %v", out, leaders())
	}
}
