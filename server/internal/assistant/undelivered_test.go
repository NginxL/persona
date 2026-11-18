package assistant

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// keepFor keeps the owner's message id with text for conv1 on "server", as the hub does after a failed send.
func keepFor(t *testing.T, s *Service, id, text string) proto.UndeliveredMessage {
	t.Helper()
	kept, err := s.KeepUndelivered(context.Background(), assistantwire.KeepUndeliveredParams{MachineID: "server", SessionID: "conv1",
		Params: map[string]any{"text": text, "client_msg_id": id, "session_id": "conv1", "live_snapshot": "not kept"}, Human: true})
	if err != nil {
		t.Fatalf("keep %s: %v", id, err)
	}
	return kept
}

func undeliveredOf(t *testing.T, s *Service, sid string) []keptMessage {
	t.Helper()
	views, ok := s.undeliveredViews(context.Background())
	if !ok {
		t.Fatal("the kept messages could not be read")
	}
	return views[sid]
}

// ownerSends are the session.send calls that carried an owner's kept message, by client_msg_id.
func ownerSends(f *fakeHub) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, a := range f.agentSent {
		if id, _ := a["client_msg_id"].(string); id != "" {
			out = append(out, id)
		}
	}
	return out
}

// The conversation cannot start: the owner's message is kept (with what the app sent, not what the hub added), shown on
// its row as waiting, tried again with backoff while it still cannot start, and sent once it can, exactly once.
func TestUndeliveredIsKeptAndSentOnceWhenTheConversationCanStart(t *testing.T) {
	s, f := newQuietServiceOn(t, &memStore{})
	kept := keepFor(t, s, "msg-1", "check the backup job")
	if kept.State != proto.UndeliveredWaiting || kept.Text != "check the backup job" || kept.NextAt == nil || kept.Error == "" {
		t.Fatalf("kept as %+v", kept)
	}
	if q := undeliveredOf(t, s, "conv1"); len(q) != 1 || q[0].Params["live_snapshot"] != nil || q[0].Params["session_id"] != nil {
		t.Fatalf("kept %+v: want the app's own fields only", q)
	}
	snap := s.snapshot(context.Background())
	shown := false
	for _, v := range snap.Sessions {
		if v.SessionID == "conv1" && len(v.Undelivered) == 1 && v.Undelivered[0].ID == "msg-1" {
			shown = true
		}
	}
	if !shown {
		t.Fatalf("the row does not show the kept message: %+v", snap.Sessions)
	}

	failSends(f, errors.New("the launcher did not start (16.6s), the message was not delivered"))
	s.undeliveredPass(context.Background(), time.Now()) // not due yet: no try
	if got := len(ownerSends(f)); got != 0 {
		t.Fatalf("%d sends before the first backoff step", got)
	}
	s.undeliveredPass(context.Background(), time.Now().Add(time.Minute))
	q := undeliveredOf(t, s, "conv1")
	if len(q) != 1 || q[0].Attempts != 2 || !q[0].NextAt.After(time.Now()) || !q[0].TriedAt.IsZero() {
		t.Fatalf("after a failed try: %+v", q)
	}

	failSends(f, nil)
	s.undeliveredPass(context.Background(), time.Now().Add(time.Hour))
	if got := ownerSends(f); len(got) != 1 || got[0] != "msg-1" {
		t.Fatalf("sent %q, want msg-1 once", got)
	}
	f.mu.Lock()
	sent := f.agentSent[len(f.agentSent)-1]
	f.mu.Unlock()
	if sent["session_id"] != "conv1" || sent["text"] == nil {
		t.Fatalf("sent %+v", sent)
	}
	if q := undeliveredOf(t, s, "conv1"); len(q) != 0 {
		t.Fatalf("still kept after it went in: %+v", q)
	}
	s.undeliveredPass(context.Background(), time.Now().Add(2*time.Hour))
	if got := ownerSends(f); len(got) != 1 {
		t.Fatalf("sent again: %q", got)
	}
}

// The app retries the same id while it is kept: one kept message, never two.
func TestUndeliveredKeptTwiceIsKeptOnce(t *testing.T) {
	s, _ := newQuietServiceOn(t, &memStore{})
	keepFor(t, s, "msg-1", "hello")
	keepFor(t, s, "msg-1", "hello")
	if q := undeliveredOf(t, s, "conv1"); len(q) != 1 {
		t.Fatalf("kept %d copies", len(q))
	}
}

// The kept messages live in the database: a manager that starts again still has them and still sends them.
func TestUndeliveredSurvivesARestart(t *testing.T) {
	st := &memStore{}
	s, _ := newQuietServiceOn(t, st)
	keepFor(t, s, "msg-1", "hello")
	again, f := newQuietServiceOn(t, st)
	if q := undeliveredOf(t, again, "conv1"); len(q) != 1 || q[0].ID != "msg-1" {
		t.Fatalf("after a restart: %+v", q)
	}
	again.undeliveredPass(context.Background(), time.Now().Add(time.Hour))
	if got := ownerSends(f); len(got) != 1 {
		t.Fatalf("sent %q after a restart", got)
	}
}

// A try may have gone in (the manager stopped during it, or no answer came): the conversation's latest turns are looked
// at first, and a line tied to the message's id means it went in. Nothing is sent again.
func TestUndeliveredTryInDoubtIsNotSentAgainWhenItLanded(t *testing.T) {
	s, f := newQuietServiceOn(t, &memStore{})
	keepFor(t, s, "msg-1", "hello")
	s.undelivered.mu.Lock()
	s.updateKeptLocked("conv1", "msg-1", func(m *keptMessage) { m.TriedAt = time.Now().Add(-time.Minute) })
	s.undelivered.mu.Unlock()
	f.mu.Lock()
	f.items = []proto.Item{{ID: "u1", Seq: 1, Role: "user", Kind: proto.KindText, Text: "hello", ClientMsgID: "msg-1", TS: time.Now()}}
	f.mu.Unlock()
	s.undeliveredPass(context.Background(), time.Now().Add(time.Hour))
	if got := ownerSends(f); len(got) != 0 {
		t.Fatalf("sent again: %q", got)
	}
	if q := undeliveredOf(t, s, "conv1"); len(q) != 0 {
		t.Fatalf("still kept: %+v", q)
	}
}

// The first transport-unknown attempt keeps its start time. A later manager pass finds the same words in the latest
// turns, even after the node forgot their client_msg_id, and does not type them again.
func TestUndeliveredUnknownFirstAttemptChecksTranscriptBeforeRetry(t *testing.T) {
	s, f := newQuietServiceOn(t, &memStore{})
	started := time.Now().Add(-time.Minute)
	_, err := s.KeepUndelivered(context.Background(), assistantwire.KeepUndeliveredParams{MachineID: "server", SessionID: "conv1",
		Params: map[string]any{"text": "check the backup job", "client_msg_id": "msg-unknown"}, Human: true,
		Code: assistantwire.CodeDeliveryUnknown, TriedAt: started})
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.items = []proto.Item{{ID: "u1", Seq: 1, Role: "user", Kind: proto.KindText, Text: "check the backup job", TS: started.Add(time.Second)}}
	f.mu.Unlock()
	s.undeliveredPass(context.Background(), time.Now().Add(time.Hour))
	if got := ownerSends(f); len(got) != 0 {
		t.Fatalf("sent again before checking the first attempt: %q", got)
	}
	if q := undeliveredOf(t, s, "conv1"); len(q) != 0 {
		t.Fatalf("still kept after the landed check: %+v", q)
	}
}

// Two passes at once (a tick and a wake) send a kept message once: one pass per conversation at a time.
func TestUndeliveredConcurrentPassesSendOnce(t *testing.T) {
	s, f := newQuietServiceOn(t, &memStore{})
	keepFor(t, s, "msg-1", "hello")
	release := make(chan struct{})
	entered := make(chan struct{})
	f.mu.Lock()
	f.beforeSend = func() { close(entered); <-release }
	f.mu.Unlock()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); s.undeliveredPass(context.Background(), time.Now().Add(time.Hour)) }()
	<-entered
	s.undeliveredPass(context.Background(), time.Now().Add(time.Hour)) // the first is still sending
	close(release)
	wg.Wait()
	s.undeliveredPass(context.Background(), time.Now().Add(time.Hour))
	if got := ownerSends(f); len(got) != 1 {
		t.Fatalf("sent %q, want once", got)
	}
}

// A newer owner message to a conversation with kept ones waits behind them; they go in the order they were sent.
func TestUndeliveredNewerMessageGoesBehindAndInOrder(t *testing.T) {
	s, f := newQuietServiceOn(t, &memStore{})
	keepFor(t, s, "msg-1", "first")
	p := SendParams{MachineID: "server", SessionID: "conv1", Params: map[string]any{"text": "second", "client_msg_id": "msg-2"}, Human: true}
	if !s.keepsBehind(context.Background(), p) {
		t.Fatal("a newer message would overtake the kept one")
	}
	if s.keepsBehind(context.Background(), SendParams{MachineID: "server", SessionID: "conv1", Params: map[string]any{}}) {
		t.Fatal("the node's own question (no words) was held")
	}
	if _, err := s.KeepUndelivered(context.Background(), assistantwire.KeepUndeliveredParams{MachineID: "server", SessionID: "conv1",
		Params: p.Params, Human: true, Code: assistantwire.CodeUndeliveredAhead}); err != nil {
		t.Fatal(err)
	}
	if q := undeliveredOf(t, s, "conv1"); len(q) != 2 || q[1].Error == "" || q[1].Error == q[0].Error {
		t.Fatalf("the one behind says why it waits: %+v", q)
	}
	s.undeliveredPass(context.Background(), time.Now().Add(time.Hour))
	if got := ownerSends(f); len(got) != 2 || got[0] != "msg-1" || got[1] != "msg-2" {
		t.Fatalf("sent %q, want msg-1 then msg-2", got)
	}
}

// A conversation seen running again gets its kept messages at once, not at the next backoff step.
func TestUndeliveredGoesWhenTheConversationRunsAgain(t *testing.T) {
	s, f := newQuietServiceOn(t, &memStore{})
	keepFor(t, s, "msg-1", "hello")
	s.undeliveredSeen(DeviceSessions{MachineID: "server", Sessions: []proto.Session{{SessionID: "conv1", Status: proto.StatusIdle}}})
	s.undeliveredPass(context.Background(), time.Now())
	if got := ownerSends(f); len(got) != 1 {
		t.Fatalf("sent %q once it ran again", got)
	}
}

// After a day of tries it gives up: the row says failed, nothing more is tried; Retry starts again, Delete removes it.
func TestUndeliveredGivesUpThenRetryOrDelete(t *testing.T) {
	s, f := newQuietServiceOn(t, &memStore{})
	keepFor(t, s, "msg-1", "hello")
	s.undelivered.mu.Lock()
	s.updateKeptLocked("conv1", "msg-1", func(m *keptMessage) { m.At = time.Now().Add(-undeliveredGiveUp - time.Minute) })
	s.undelivered.mu.Unlock()
	s.undeliveredPass(context.Background(), time.Now().Add(time.Hour))
	if q := undeliveredOf(t, s, "conv1"); len(q) != 1 || q[0].State != proto.UndeliveredFailed {
		t.Fatalf("after the give-up time: %+v", q)
	}
	if got := ownerSends(f); len(got) != 0 {
		t.Fatalf("sent %q after giving up", got)
	}
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/assistant/conversations/conv1/undelivered/msg-1/retry", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("retry: %d %s", rec.Code, rec.Body)
	}
	if q := undeliveredOf(t, s, "conv1"); len(q) != 1 || q[0].State != proto.UndeliveredWaiting {
		t.Fatalf("after Retry: %+v", q)
	}
	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/assistant/conversations/conv1/undelivered/msg-1", nil)
	req.Header.Set(RoleHeader, "readonly")
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("a read-only device deleted it: %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/v1/assistant/conversations/conv1/undelivered/msg-1", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body)
	}
	if q := undeliveredOf(t, s, "conv1"); len(q) != 0 {
		t.Fatalf("after Delete: %+v", q)
	}
}

// A deleted conversation will never take it: it is marked failed at once, not tried for a day.
func TestUndeliveredToAGoneConversationFails(t *testing.T) {
	s, f := newQuietServiceOn(t, &memStore{})
	keepFor(t, s, "msg-1", "hello")
	failSends(f, &rpc.Error{Code: "conversation_deleted", Message: "deleted"})
	s.undeliveredPass(context.Background(), time.Now().Add(time.Hour))
	if q := undeliveredOf(t, s, "conv1"); len(q) != 1 || q[0].State != proto.UndeliveredFailed {
		t.Fatalf("after a refusal for good: %+v", q)
	}
}

// conversation_send to a conversation that cannot start keeps the message in its queue instead of failing.
func TestConversationSendToOneThatCannotStartIsQueued(t *testing.T) {
	s, f := newQuietService(t)
	f.mu.Lock()
	f.failAssistant = &rpc.Error{Message: "the launcher did not start, the message was not delivered"} // as the node answers it
	f.mu.Unlock()
	f.known["conv2"] = true
	f.setSessions("server", proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle},
		proto.Session{SessionID: "conv2", MachineID: "server", Assistant: true, Status: proto.StatusIdle})
	out, err := s.toolConversationSend(context.Background(), "conv2", []byte(`{"id":"conv1","text":"status?"}`))
	if err != nil {
		t.Fatalf("conversation_send failed instead of queueing: %v", err)
	}
	if s.queuedFor("conv1") != 1 || !strings.Contains(out, "could not take the message now") {
		t.Fatalf("not queued as one that could not go in (%q)", out)
	}
}

// The kept messages reach the rows the hub hands the apps (decorateInventory), and leave them once none is left.
func TestUndeliveredReachesTheRow(t *testing.T) {
	ft := newFleetTest(t)
	s := ft.s
	setSettings(t, s, func(v *Settings) { v.MachineID = "mac" })
	row := proto.Session{SessionID: "conv9", MachineID: "mac", Assistant: true, Cwd: "/home/me/.persona/assistant", Status: proto.StatusOffline}
	ft.d.setSessions("mac", []proto.Session{row})
	if _, err := s.KeepUndelivered(context.Background(), assistantwire.KeepUndeliveredParams{MachineID: "mac", SessionID: "conv9",
		Params: map[string]any{"text": "hello", "client_msg_id": "msg-9"}}); err != nil {
		t.Fatal(err)
	}
	s.fleetTakeSnapshot(s.snapshot(context.Background()))
	rows := s.publishList("mac")
	if len(rows) != 1 || rows[0].Undelivered == nil || len(*rows[0].Undelivered) != 1 || (*rows[0].Undelivered)[0].ID != "msg-9" {
		t.Fatalf("rows %+v", rows)
	}
	s.undeliveredDrop("conv9", "msg-9")
	s.fleetTakeSnapshot(s.snapshot(context.Background()))
	if rows := s.publishList("mac"); len(rows) != 1 || rows[0].Undelivered != nil {
		t.Fatalf("still on the row: %+v", rows)
	}
}

// A refusal for good (the manager's own, or a node's with a code) is no passing failure: conversation_send fails and
// nothing is queued.
func TestConversationSendRefusedForGoodIsNotQueued(t *testing.T) {
	for _, refusal := range []error{errors.New("the picked model is gone"), &rpc.Error{Code: "launcher_missing", Message: "no launcher"}} {
		s, f := newQuietService(t)
		f.mu.Lock()
		f.failAssistant = refusal
		f.mu.Unlock()
		f.known["conv2"] = true
		f.setSessions("server", proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle},
			proto.Session{SessionID: "conv2", MachineID: "server", Assistant: true, Status: proto.StatusIdle})
		if out, err := s.toolConversationSend(context.Background(), "conv2", []byte(`{"id":"conv1","text":"status?"}`)); err == nil {
			t.Fatalf("%v: reported %q", refusal, out)
		}
		if s.queuedFor("conv1") != 0 {
			t.Fatalf("%v: queued", refusal)
		}
	}
}
