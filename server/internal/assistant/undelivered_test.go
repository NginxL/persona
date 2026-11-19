package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/bus"
	"github.com/nwverse/persona/server/internal/i18n"
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

func TestUndeliveredFromSignedOutDeviceIsDeleteOnly(t *testing.T) {
	for _, tc := range []struct {
		name    string
		event   bool
		inDoubt bool
	}{
		{name: "received_event", event: true},
		{name: "missed_event"},
		{name: "missed_event_with_a_try_in_doubt", inDoubt: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			st := &memStore{}
			s, f := newQuietServiceOn(t, st)
			for _, id := range []string{"signed-out", "other-device"} {
				code := "machine_offline"
				if tc.inDoubt && id == "signed-out" {
					code = assistantwire.CodeDeliveryUnknown
				}
				row, err := s.KeepUndelivered(ctx, assistantwire.KeepUndeliveredParams{
					MachineID: "server", SessionID: "conv1", CredentialID: id, Human: true, Lang: "en", Code: code,
					Params: map[string]any{"text": id, "client_msg_id": id},
				})
				if err != nil || row.State != proto.UndeliveredWaiting {
					t.Fatalf("initial row: %+v, %v", row, err)
				}
			}
			assertFailed := func(service *Service) []proto.UndeliveredMessage {
				t.Helper()
				for _, v := range service.snapshot(ctx).Sessions {
					if v.SessionID == "conv1" {
						rows := v.Undelivered
						if len(rows) > 0 && rows[0].ID == "signed-out" && rows[0].State == proto.UndeliveredFailed &&
							rows[0].DeleteOnly && rows[0].NextAt == nil && rows[0].Error == "Not sent: the device that sent it was signed out" {
							return rows
						}
						t.Fatalf("conversation's rows: %+v", rows)
					}
				}
				t.Fatal("conversation missing from snapshot")
				return nil
			}
			if tc.event {
				b := &BusHub{svc: s}
				raw, err := json.Marshal(bus.DeviceCredential{ID: "signed-out"})
				if err != nil {
					t.Fatal(err)
				}
				if err := b.event(ctx, busEvent{name: bus.EventCredentialRevoked, params: raw}); err != nil {
					t.Fatal(err)
				}
				rows := assertFailed(s)
				if len(rows) != 2 || rows[1].ID != "other-device" || rows[1].State != proto.UndeliveredWaiting || rows[1].DeleteOnly {
					t.Fatalf("other device's row: %+v", rows)
				}
			} else {
				// The original sender survives reload, even though this manager missed the event.
				s, f = newQuietServiceOn(t, st)
				f.mu.Lock()
				f.revokedDevices = map[string]bool{"signed-out": true}
				f.credentialCheckErr = errors.New("hub temporarily unavailable")
				f.mu.Unlock()
				s.undeliveredPass(ctx, time.Now().Add(time.Minute))
				if got := ownerSends(f); len(got) != 0 {
					t.Fatalf("sent while the credential could not be checked: %q", got)
				}
				f.mu.Lock()
				f.credentialCheckErr = nil
				f.mu.Unlock()
			}

			s.undeliveredSeen(DeviceSessions{MachineID: "server", Sessions: []proto.Session{{SessionID: "conv1", Status: proto.StatusIdle}}})
			now := time.Now()
			if tc.inDoubt {
				// An uncertain try is checked on its own schedule after the computer returns.
				now = now.Add(time.Minute)
			}
			s.undeliveredPass(ctx, now)
			if got := ownerSends(f); !slices.Equal(got, []string{"other-device"}) {
				t.Fatalf("sent %q", got)
			}
			if rows := assertFailed(s); len(rows) != 1 {
				t.Fatalf("still kept after delivery: %+v", rows)
			}
			again, nextHub := newQuietServiceOn(t, st)
			assertFailed(again)
			path := "/api/v1/assistant/conversations/conv1/undelivered/signed-out"
			rec := httptest.NewRecorder()
			again.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path+"/retry", nil))
			if rec.Code != http.StatusConflict {
				t.Fatalf("retry: %d %s", rec.Code, rec.Body)
			}
			again.undeliveredPass(ctx, time.Now().Add(time.Hour))
			if got := ownerSends(nextHub); len(got) != 0 {
				t.Fatalf("sent after reload and Retry: %q", got)
			}
			rec = httptest.NewRecorder()
			again.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, path, nil))
			if rec.Code != http.StatusOK || len(undeliveredOf(t, again, "conv1")) != 0 {
				t.Fatalf("delete: %d %s", rec.Code, rec.Body)
			}
		})
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

// --- a try whose outcome is in doubt is never sent again by itself (#10669) ---

// sendCalls counts the session.send calls the node got for the owner's message id, answered or not.
func sendCalls(f *fakeHub, id string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		var a map[string]any
		_ = json.Unmarshal(c.Params, &a)
		if c.Method == DeviceSessionSend && a["client_msg_id"] == id {
			n++
		}
	}
	return n
}

// copyOf is what m holds now, as a database left behind by a manager that stopped.
func (m *memStore) copyOf() *memStore {
	m.mu.Lock()
	defer m.mu.Unlock()
	c := &memStore{vals: map[string][]byte{}}
	for k, v := range m.vals {
		c.vals[k] = slices.Clone(v)
	}
	return c
}

func failPuts(st *memStore, err error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.failPut = err
}

// tryInDoubt makes one try of kept message id whose send ends without an answer (the link dropped while it went) and
// whose line is not in the conversation when looked for right after: the message is then in doubt.
func tryInDoubt(t *testing.T, s *Service, f *fakeHub, id string) {
	t.Helper()
	failSends(f, rpc.ErrDropped)
	s.undeliveredPass(context.Background(), time.Now().Add(time.Hour))
	failSends(f, nil)
	q := undeliveredOf(t, s, "conv1")
	if len(q) != 1 || q[0].ID != id || q[0].State != proto.UndeliveredWaiting || !q[0].inDoubt() || q[0].LookUntil.IsZero() {
		t.Fatalf("after a try in doubt: %+v", q)
	}
}

// keepUnknown keeps the owner's message id as the hub does after its own first try ended with the outcome unknown.
func keepUnknown(t *testing.T, s *Service, id, text string, started time.Time) {
	t.Helper()
	if _, err := s.KeepUndelivered(context.Background(), assistantwire.KeepUndeliveredParams{MachineID: "server", SessionID: "conv1",
		Params: map[string]any{"text": text, "client_msg_id": id}, Human: true, Code: assistantwire.CodeDeliveryUnknown, TriedAt: started}); err != nil {
		t.Fatal(err)
	}
}

// endLook has the look for kept message id run out: its next look is its last.
func endLook(s *Service, id string) {
	s.undelivered.mu.Lock()
	defer s.undelivered.mu.Unlock()
	s.updateKeptLocked("conv1", id, func(m *keptMessage) { m.LookUntil = time.Now().Add(-time.Second) })
}

// stillLooking checks kept message id is waiting to be looked for again, later, never to be sent by itself.
func stillLooking(t *testing.T, s *Service, id string) {
	t.Helper()
	q := undeliveredOf(t, s, "conv1")
	if len(q) != 1 || q[0].ID != id || q[0].State != proto.UndeliveredWaiting || !q[0].inDoubt() || !q[0].NextAt.After(time.Now()) {
		t.Fatalf("not waiting for the next look: %+v", q)
	}
}

// failedMaybeIn checks kept message id gave up for the owner to decide, saying it may already have gone in.
func failedMaybeIn(t *testing.T, s *Service, id string) {
	t.Helper()
	q := undeliveredOf(t, s, "conv1")
	if len(q) != 1 || q[0].ID != id || q[0].State != proto.UndeliveredFailed ||
		q[0].Error != i18n.T(i18n.Lang(q[0].Lang), "assistant-undelivered.may_have_gone_in") {
		t.Fatalf("want failed, may have gone in: %+v", q)
	}
}

// ownerRetry is the owner's Retry on kept message id; its status.
func ownerRetry(s *Service, id string) int {
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/assistant/conversations/conv1/undelivered/"+id+"/retry", nil))
	return rec.Code
}

// The manager stops during a try. The try's mark was saved before it, so the manager that starts again only looks:
// the line tied to the message's id means it went in; none by the end of the look, the owner decides. The node gets
// the message once either way.
func TestUndeliveredManagerStoppedMidTryOnlyLooks(t *testing.T) {
	for _, landed := range []bool{true, false} {
		st := &memStore{}
		s, f := newQuietServiceOn(t, st)
		keepFor(t, s, "msg-1", "check the backup job")
		var left *memStore
		beforeSend(f, func() { left = st.copyOf() }) // the manager stops while the send is out
		s.undeliveredPass(context.Background(), time.Now().Add(time.Hour))
		if n := sendCalls(f, "msg-1"); n != 1 || left == nil {
			t.Fatalf("landed=%v: %d sends before the stop", landed, n)
		}
		again, node := newQuietServiceOn(t, left)
		if q := undeliveredOf(t, again, "conv1"); len(q) != 1 || !q[0].inDoubt() {
			t.Fatalf("landed=%v: the try's mark was not saved before it: %+v", landed, q)
		}
		if landed {
			node.mu.Lock()
			node.items = []proto.Item{{ID: "u1", Seq: 1, Role: "user", Kind: proto.KindText, Text: "check the backup job", ClientMsgID: "msg-1", TS: time.Now()}}
			node.mu.Unlock()
		}
		again.undeliveredPass(context.Background(), time.Now().Add(time.Hour))
		if landed {
			if q := undeliveredOf(t, again, "conv1"); len(q) != 0 {
				t.Fatalf("found in the conversation but still kept: %+v", q)
			}
		} else {
			stillLooking(t, again, "msg-1")
			endLook(again, "msg-1")
			again.undeliveredPass(context.Background(), time.Now().Add(time.Hour))
			failedMaybeIn(t, again, "msg-1")
		}
		if n := sendCalls(node, "msg-1"); n != 0 {
			t.Fatalf("landed=%v: sent again %d times after the restart", landed, n)
		}
	}
}

// The manager shuts down during a try (its context ends while the send is out): that is no refusal, the try may have
// gone in. Its mark stays, and the manager that starts again only looks.
func TestUndeliveredShutdownMidTryKeepsTheMark(t *testing.T) {
	st := &memStore{}
	s, f := newQuietServiceOn(t, st)
	keepFor(t, s, "msg-1", "hello")
	ctx, cancel := context.WithCancel(context.Background())
	f.mu.Lock()
	f.sendHangs = true
	f.mu.Unlock()
	beforeSend(f, cancel)
	s.undeliveredPass(ctx, time.Now().Add(time.Hour))
	again, node := newQuietServiceOn(t, st)
	if q := undeliveredOf(t, again, "conv1"); len(q) != 1 || !q[0].inDoubt() {
		t.Fatalf("a try cut short by the shutdown reads as refused: %+v", q)
	}
	again.undeliveredPass(context.Background(), time.Now().Add(time.Hour))
	if n := sendCalls(node, "msg-1"); n != 0 {
		t.Fatalf("sent %d times after the restart", n)
	}
}

// The node restarted after a try in doubt and forgot the message's id: its line carries none. The owner's words
// written since the try mean it went in; other words are not it, and by the end of the look the owner decides. The
// node gets the message once either way.
func TestUndeliveredNodeRestartedOnlyTheWordsTell(t *testing.T) {
	for _, line := range []string{"check the backup job", "something else"} {
		s, f := newQuietServiceOn(t, &memStore{})
		keepFor(t, s, "msg-1", "check the backup job")
		tryInDoubt(t, s, f, "msg-1")
		f.mu.Lock()
		f.items = []proto.Item{{ID: "u1", Seq: 1, Role: "user", Kind: proto.KindText, Text: line, TS: time.Now()}}
		f.mu.Unlock()
		s.undeliveredPass(context.Background(), time.Now().Add(time.Hour))
		if line == "check the backup job" {
			if q := undeliveredOf(t, s, "conv1"); len(q) != 0 {
				t.Fatalf("its words are in the conversation but it is still kept: %+v", q)
			}
		} else {
			stillLooking(t, s, "msg-1")
			endLook(s, "msg-1")
			s.undeliveredPass(context.Background(), time.Now().Add(time.Hour))
			failedMaybeIn(t, s, "msg-1")
		}
		if n := sendCalls(f, "msg-1"); n != 1 {
			t.Fatalf("line %q: sent %d times", line, n)
		}
	}
}

// The transcript cannot be read after the hub's own first try was in doubt: a read that fails says nothing about
// whether it went in. The message is not sent; it is looked for while the look lasts, then the owner decides.
func TestUndeliveredUnreadableTranscriptIsNotSentAgain(t *testing.T) {
	s, f := newQuietServiceOn(t, &memStore{})
	keepUnknown(t, s, "msg-1", "check the backup job", time.Now().Add(-time.Minute))
	f.mu.Lock()
	f.itemsFail = 1000
	f.mu.Unlock()
	s.undeliveredPass(context.Background(), time.Now().Add(time.Hour))
	stillLooking(t, s, "msg-1")
	endLook(s, "msg-1")
	s.undeliveredPass(context.Background(), time.Now().Add(time.Hour))
	failedMaybeIn(t, s, "msg-1")
	if n := sendCalls(f, "msg-1"); n != 0 {
		t.Fatalf("sent %d times though the transcript could not be read", n)
	}
}

// The line comes late: the try in doubt is not in the conversation right after it, but is at the next look, within
// the look's time. It went in, and is sent no second time. A conversation seen running meanwhile does not hurry the
// look.
func TestUndeliveredLateLineIsFoundWithinTheLook(t *testing.T) {
	s, f := newQuietServiceOn(t, &memStore{})
	keepFor(t, s, "msg-1", "hello")
	tryInDoubt(t, s, f, "msg-1")
	next := undeliveredOf(t, s, "conv1")[0].NextAt
	s.undeliveredSeen(DeviceSessions{MachineID: "server", Sessions: []proto.Session{{SessionID: "conv1", Status: proto.StatusIdle}}})
	if q := undeliveredOf(t, s, "conv1"); !q[0].NextAt.Equal(next) {
		t.Fatalf("a try in doubt was moved up to %v from %v", q[0].NextAt, next)
	}
	f.mu.Lock()
	f.items = []proto.Item{{ID: "u1", Seq: 1, Role: "user", Kind: proto.KindText, Text: "hello", ClientMsgID: "msg-1", TS: time.Now()}}
	f.mu.Unlock()
	s.undeliveredPass(context.Background(), time.Now().Add(time.Hour))
	if q := undeliveredOf(t, s, "conv1"); len(q) != 0 {
		t.Fatalf("still kept after its line came: %+v", q)
	}
	if n := sendCalls(f, "msg-1"); n != 1 {
		t.Fatalf("sent %d times", n)
	}
}

// Pictures only, no words: once the node forgot the id there is nothing to know its line by, and a line of the
// owner's without words is not taken for it. It is not sent again; by the end of the look the owner decides.
func TestUndeliveredPicturesOnlyInDoubtIsNotSentAgain(t *testing.T) {
	s, f := newQuietServiceOn(t, &memStore{})
	pictures := []any{map[string]any{"mime": "image/png", "data": "iVBORw0KGgo="}}
	if _, err := s.KeepUndelivered(context.Background(), assistantwire.KeepUndeliveredParams{MachineID: "server", SessionID: "conv1",
		Params: map[string]any{"images": pictures, "client_msg_id": "msg-1"}, Human: true}); err != nil {
		t.Fatal(err)
	}
	tryInDoubt(t, s, f, "msg-1")
	f.mu.Lock()
	f.items = []proto.Item{{ID: "u1", Seq: 1, Role: "user", Kind: proto.KindImage, TS: time.Now()},
		{ID: "u2", Seq: 2, Role: "user", Kind: proto.KindText, Text: "", TS: time.Now()}}
	f.mu.Unlock()
	s.undeliveredPass(context.Background(), time.Now().Add(time.Hour))
	stillLooking(t, s, "msg-1")
	endLook(s, "msg-1")
	s.undeliveredPass(context.Background(), time.Now().Add(time.Hour))
	failedMaybeIn(t, s, "msg-1")
	if n := sendCalls(f, "msg-1"); n != 1 {
		t.Fatalf("sent %d times", n)
	}
}

// The try's mark cannot be saved (the database is away): no try is made, since a manager stopped during it would not
// know to look. Once saving works again, the message goes, once.
func TestUndeliveredTryNotMarkedIsNotMade(t *testing.T) {
	st := &memStore{}
	s, f := newQuietServiceOn(t, st)
	keepFor(t, s, "msg-1", "hello")
	failPuts(st, errors.New("the database is away"))
	s.undeliveredPass(context.Background(), time.Now().Add(time.Hour))
	if n := sendCalls(f, "msg-1"); n != 0 {
		t.Fatalf("sent %d times without its mark saved", n)
	}
	if q := undeliveredOf(t, s, "conv1"); len(q) != 1 || q[0].State != proto.UndeliveredWaiting || !q[0].TriedAt.IsZero() || !q[0].NextAt.After(time.Now()) {
		t.Fatalf("after a try that was not made: %+v", q)
	}
	failPuts(st, nil)
	s.undeliveredPass(context.Background(), time.Now().Add(time.Hour))
	if n := sendCalls(f, "msg-1"); n != 1 {
		t.Fatalf("sent %d times once the mark saved", n)
	}
	if q := undeliveredOf(t, s, "conv1"); len(q) != 0 {
		t.Fatalf("still kept: %+v", q)
	}
}

// After a try in doubt the owner looked at the conversation and pressed Retry: it is sent, not failed again at once.
// A Retry that could not be saved says so and changes nothing. A try of the Retry's own in doubt is again only looked
// for, then the owner decides again.
func TestUndeliveredOwnerRetryAfterDoubtSends(t *testing.T) {
	st := &memStore{}
	s, f := newQuietServiceOn(t, st)
	keepFor(t, s, "msg-1", "hello")
	tryInDoubt(t, s, f, "msg-1")
	endLook(s, "msg-1")
	s.undeliveredPass(context.Background(), time.Now().Add(time.Hour))
	failedMaybeIn(t, s, "msg-1")

	failPuts(st, errors.New("the database is away"))
	if code := ownerRetry(s, "msg-1"); code != http.StatusInternalServerError {
		t.Fatalf("a Retry that was not saved answered %d", code)
	}
	failedMaybeIn(t, s, "msg-1")
	failPuts(st, nil)

	if code := ownerRetry(s, "msg-1"); code != http.StatusOK {
		t.Fatalf("retry: %d", code)
	}
	failSends(f, rpc.ErrDropped) // the Retry's own try is in doubt too
	s.undeliveredPass(context.Background(), time.Now().Add(time.Hour))
	failSends(f, nil)
	if n := sendCalls(f, "msg-1"); n != 2 {
		t.Fatalf("the owner's Retry sent %d times in all, want 2", n)
	}
	stillLooking(t, s, "msg-1")
	s.undeliveredPass(context.Background(), time.Now().Add(time.Hour))
	endLook(s, "msg-1")
	s.undeliveredPass(context.Background(), time.Now().Add(time.Hour))
	failedMaybeIn(t, s, "msg-1")
	if n := sendCalls(f, "msg-1"); n != 2 {
		t.Fatalf("the Retry's try in doubt was sent again: %d sends", n)
	}

	if code := ownerRetry(s, "msg-1"); code != http.StatusOK {
		t.Fatalf("retry: %d", code)
	}
	s.undeliveredPass(context.Background(), time.Now().Add(time.Hour))
	if n := sendCalls(f, "msg-1"); n != 3 {
		t.Fatalf("the second Retry sent %d times in all, want 3", n)
	}
	if q := undeliveredOf(t, s, "conv1"); len(q) != 0 {
		t.Fatalf("still kept after it went in: %+v", q)
	}
}

// A try refused before anything was typed (the launcher did not start) is no doubt: it is tried again by itself and
// goes in at the next try.
func TestUndeliveredNothingTypedIsTriedAgainByItself(t *testing.T) {
	s, f := newQuietServiceOn(t, &memStore{})
	keepFor(t, s, "msg-1", "hello")
	failSends(f, errors.New("the launcher did not start (16.6s), the message was not delivered"))
	s.undeliveredPass(context.Background(), time.Now().Add(time.Hour))
	if q := undeliveredOf(t, s, "conv1"); len(q) != 1 || q[0].State != proto.UndeliveredWaiting || q[0].inDoubt() {
		t.Fatalf("after a refused try: %+v", q)
	}
	failSends(f, nil)
	s.undeliveredPass(context.Background(), time.Now().Add(time.Hour))
	if n := sendCalls(f, "msg-1"); n != 2 {
		t.Fatalf("sent %d times, want the refused try and the one that went in", n)
	}
	if q := undeliveredOf(t, s, "conv1"); len(q) != 0 {
		t.Fatalf("still kept: %+v", q)
	}
}

// A try that fails before session.send (SendParams cannot read the deletion ledger in time) typed nothing, even with an
// error that reads like a timeout: it is no doubt. Its mark is cleared, it is tried again by itself, and the node gets
// the message once.
func TestUndeliveredFailureBeforeTheSendIsNoDoubt(t *testing.T) {
	st := &memStore{}
	s, f := newQuietServiceOn(t, st)
	keepFor(t, s, "msg-1", "hello")
	// A non-durable store caches the ledger's first read: make the next one fail.
	s.deletedConversations.mu.Lock()
	s.deletedConversations.loaded, s.deletedConversations.rows = false, nil
	s.deletedConversations.mu.Unlock()
	st.failKey(deletedConversationsKey, context.DeadlineExceeded)
	s.undeliveredPass(context.Background(), time.Now().Add(time.Hour))
	if n := sendCalls(f, "msg-1"); n != 0 {
		t.Fatalf("session.send was reached %d times", n)
	}
	if q := undeliveredOf(t, s, "conv1"); len(q) != 1 || q[0].State != proto.UndeliveredWaiting || q[0].inDoubt() || !q[0].TriedAt.IsZero() {
		t.Fatalf("a failure before the send was taken as a try in doubt: %+v", q)
	}
	st.failKey(deletedConversationsKey, nil)
	s.undeliveredPass(context.Background(), time.Now().Add(time.Hour))
	if n := sendCalls(f, "msg-1"); n != 1 {
		t.Fatalf("sent %d times, want once", n)
	}
	if q := undeliveredOf(t, s, "conv1"); len(q) != 0 {
		t.Fatalf("still kept: %+v", q)
	}
}

// failBeforeSend has every try fail in SendParams, before session.send, with err (the computer away, as the deletion
// ledger cannot be read in time); nil lets tries through again. A non-durable store caches the ledger's first read,
// so the cache is dropped first.
func failBeforeSend(s *Service, st *memStore, err error) {
	s.deletedConversations.mu.Lock()
	s.deletedConversations.loaded, s.deletedConversations.rows = false, nil
	s.deletedConversations.mu.Unlock()
	st.failKey(deletedConversationsKey, err)
}

// failedAfterDoubt leaves kept message id failed after a try in doubt that began at started, for the owner's Retry.
func failedAfterDoubt(t *testing.T, s *Service, f *fakeHub, id string, started time.Time) {
	t.Helper()
	tryInDoubt(t, s, f, id)
	s.undelivered.mu.Lock()
	s.updateKeptLocked("conv1", id, func(m *keptMessage) { m.TriedAt = started })
	s.undelivered.mu.Unlock()
	endLook(s, id)
	s.undeliveredPass(context.Background(), time.Now().Add(time.Hour))
	failedMaybeIn(t, s, id)
}

// The owner pressed Retry after a try in doubt while the computer was still away: the Retry's try failed before
// session.send. Once the computer is back, the earlier try is still looked for first: its line there (the node kept
// the id) means it went in and nothing is sent; no line, and the Retry sends it.
func TestUndeliveredRetryWhileAwayLooksFirstWhenBack(t *testing.T) {
	for _, landed := range []bool{true, false} {
		st := &memStore{}
		s, f := newQuietServiceOn(t, st)
		keepFor(t, s, "msg-1", "check the backup job")
		failedAfterDoubt(t, s, f, "msg-1", time.Now().Add(-time.Minute))
		if code := ownerRetry(s, "msg-1"); code != http.StatusOK {
			t.Fatalf("retry: %d", code)
		}
		failBeforeSend(s, st, errors.New("the computer is not connected"))
		f.mu.Lock()
		f.itemsFail = 1000 // away: nothing can be read either
		f.mu.Unlock()
		s.undeliveredPass(context.Background(), time.Now().Add(time.Hour))
		if q := undeliveredOf(t, s, "conv1"); len(q) != 1 || q[0].State != proto.UndeliveredWaiting || q[0].inDoubt() || q[0].TriedAt.IsZero() {
			t.Fatalf("landed=%v: after the Retry's try failed before the send: %+v", landed, q)
		}
		failBeforeSend(s, st, nil) // the computer is back
		f.mu.Lock()
		f.itemsFail = 0
		if landed {
			f.items = []proto.Item{{ID: "u1", Seq: 1, Role: "user", Kind: proto.KindText, Text: "check the backup job", ClientMsgID: "msg-1", TS: time.Now().Add(-time.Minute)}}
		}
		f.mu.Unlock()
		s.undeliveredPass(context.Background(), time.Now().Add(time.Hour))
		if q := undeliveredOf(t, s, "conv1"); len(q) != 0 {
			t.Fatalf("landed=%v: still kept once the computer was back: %+v", landed, q)
		}
		want := 2 // the try in doubt, then the Retry's
		if landed {
			want = 1
		}
		if n := sendCalls(f, "msg-1"); n != want {
			t.Fatalf("landed=%v: sent %d times, want %d", landed, n, want)
		}
	}
}

// The Retry's own try is in doubt too, or the manager stops during it: the look still goes from the first try in
// doubt, so that try's line, written minutes before the Retry and no longer tied to its id (the node restarted), is
// found by its words and nothing more is sent.
func TestUndeliveredRetryInDoubtLooksFromTheFirstTry(t *testing.T) {
	st := &memStore{}
	s, f := newQuietServiceOn(t, st)
	keepFor(t, s, "msg-1", "check the backup job")
	first := time.Now().Add(-5 * time.Minute).UTC()
	failedAfterDoubt(t, s, f, "msg-1", first)
	if code := ownerRetry(s, "msg-1"); code != http.StatusOK {
		t.Fatalf("retry: %d", code)
	}
	var left *memStore
	beforeSend(f, func() { left = st.copyOf() })
	failSends(f, rpc.ErrDropped)
	s.undeliveredPass(context.Background(), time.Now().Add(time.Hour))
	failSends(f, nil)
	if n := sendCalls(f, "msg-1"); n != 2 {
		t.Fatalf("the Retry sent %d times in all, want 2", n)
	}
	if left == nil {
		t.Fatal("the Retry's try did not reach the send")
	}
	stopped, _ := newQuietServiceOn(t, left)
	if q := undeliveredOf(t, stopped, "conv1"); len(q) != 1 || !q[0].inDoubt() || !q[0].TriedAt.Equal(first) {
		t.Fatalf("a manager stopped during the Retry's try finds %+v, want it in doubt since %v", q, first)
	}
	if q := undeliveredOf(t, s, "conv1"); len(q) != 1 || !q[0].inDoubt() || !q[0].TriedAt.Equal(first) {
		t.Fatalf("after the Retry's try in doubt: %+v, want it looked for since %v", q, first)
	}
	f.mu.Lock()
	f.items = []proto.Item{{ID: "u1", Seq: 1, Role: "user", Kind: proto.KindText, Text: "check the backup job", TS: first.Add(time.Second)}}
	f.mu.Unlock()
	s.undeliveredPass(context.Background(), time.Now().Add(time.Hour))
	if q := undeliveredOf(t, s, "conv1"); len(q) != 0 {
		t.Fatalf("the first try's line is there but it is still kept: %+v", q)
	}
	if n := sendCalls(f, "msg-1"); n != 2 {
		t.Fatalf("sent %d times, want 2", n)
	}
}
