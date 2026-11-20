package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// The owner's message to an Agent whose computer is away (#10661): the hub marks it holdable (NodeCall.Hold) and the
// machine's manager, here played on the real bus, holds it and answers devicewire.CodeHeld.

// holdManager is the devices manager of machine "box", whose computer is away: a session.send with Hold is held
// (CodeHeld, as internal/devices held.go answers), any other one is refused machine_offline as before. ignoresHold
// plays a manager from before holding, holdsAll a wrong one that answers held to any send; heldAhead: older messages
// of the session are held, so an ordered send without Hold is refused held_ahead, nothing typed. calls are the node
// calls it got, relayed the apps' requests the hub relayed to it.
type holdManager struct {
	mu          sync.Mutex
	calls       []devicewire.NodeCall
	relayed     []assistantwire.HTTPRequest
	ignoresHold bool
	holdsAll    bool
	heldAhead   bool
}

func (k *holdManager) sends() []devicewire.NodeCall {
	k.mu.Lock()
	defer k.mu.Unlock()
	var out []devicewire.NodeCall
	for _, c := range k.calls {
		if c.Method == "session.send" {
			out = append(out, c)
		}
	}
	return out
}

// holdHub is a hub with machine "box" (shown as "Studio Mac"), carried by its manager, listing row; its manager is k.
func holdHub(t *testing.T, row proto.Session) (*Hub, *Machine, *holdManager) {
	t.Helper()
	h := newTestHub(t)
	h.store = newSentStore()
	m := newMachine(h, MachineConfig{ID: "box", Name: "Studio Mac"})
	m.managed, m.peer = true, &busNode{h: h, id: "box", done: make(chan struct{})}
	registerMachine(h, m)
	editSessions(m, func([]proto.Session) []proto.Session { return []proto.Session{row} })
	h.bus.idents = &memIdents{}
	mux := http.NewServeMux()
	h.busRoutes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	token, err := h.createBusToken(context.Background(), assistantwire.AssistantID, "Test manager")
	if err != nil {
		t.Fatal(err)
	}
	k := &holdManager{}
	connectGuardManagerWith(t, srv.URL, assistantwire.AssistantID, token, func(method string, raw json.RawMessage) (any, error) {
		switch method {
		case assistantwire.MethodSendParams: // an assistant conversation's message runs as it came
			var p assistantwire.SendParams
			_ = json.Unmarshal(raw, &p)
			return map[string]any{"params": p.Params}, nil
		case assistantwire.MethodHTTP:
			var req assistantwire.HTTPRequest
			if err := json.Unmarshal(raw, &req); err != nil {
				return nil, err
			}
			k.mu.Lock()
			k.relayed = append(k.relayed, req)
			k.mu.Unlock()
			return assistantwire.HTTPResponse{Status: http.StatusOK, Body: []byte(`{"ok":true}`)}, nil
		case devicewire.MethodCall:
		default:
			return map[string]any{}, nil
		}
		var c devicewire.NodeCall
		if err := json.Unmarshal(raw, &c); err != nil {
			return nil, err
		}
		k.mu.Lock()
		k.calls = append(k.calls, c)
		ignores, all, ahead := k.ignoresHold, k.holdsAll, k.heldAhead
		k.mu.Unlock()
		if c.Method != "session.send" {
			return map[string]any{}, nil
		}
		if ahead && c.Ordered && c.Hold == nil {
			return nil, &rpc.Error{Code: "held_ahead", Message: "Earlier messages to this Agent are waiting to be sent.",
				Data: json.RawMessage(`{"delivered":"none"}`)}
		}
		if (c.Hold == nil && !all) || ignores {
			return nil, &rpc.Error{Code: devicewire.CodeMachineOffline, Message: "Studio Mac is offline"}
		}
		var p struct {
			ID   string `json:"client_msg_id"`
			Text string `json:"text"`
		}
		_ = json.Unmarshal(c.Params, &p)
		data, _ := json.Marshal(devicewire.HeldAnswer{Delivered: "later", Undelivered: &proto.UndeliveredMessage{ID: p.ID,
			Text: p.Text, At: time.Now().UTC(), State: proto.UndeliveredWaiting, Computer: "Studio Mac",
			Error: "Will send when Studio Mac is back"}})
		return nil, &rpc.Error{Code: devicewire.CodeHeld, Message: "Will send when Studio Mac is back", Data: data}
	})
	h.devices.mu.Lock()
	h.devices.upOpen = map[string]bool{devicesOwner: true}
	h.devices.mu.Unlock()
	return h, m, k
}

// agentRow is an Agent the hub lists while its computer is away.
var agentRow = proto.Session{SessionID: "s1", Agent: "claude", Launcher: "claude", Cwd: "/home/me/project", Status: proto.StatusUnknown}

// ownerPhone is a person's own app: a full device's credential.
var ownerPhone = principal{ID: "dev-phone", Name: "Phone", Role: roleFull}

// sendTo sends body to box's session sid from the owner's phone, whose app draws held messages (it says so in
// X-Persona-Features).
func sendTo(h *Hub, m *Machine, sid, lang, body string) *httptest.ResponseRecorder {
	return sendAsWho(h, m, ownerPhone, sid, lang, body)
}

// sendAsWho is sendTo from who's credential.
func sendAsWho(h *Hub, m *Machine, who principal, sid, lang, body string) *httptest.ResponseRecorder {
	return sendFrom(h, m, who, "", featureAgentMessageHold, sid, lang, body)
}

// sendFrom is sendTo from who's credential, its app naming itself client ("platform/version", "": none) and
// declaring features ("": none) on the request.
func sendFrom(h *Hub, m *Machine, who principal, client, features, sid, lang, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/box/"+sid+"/messages", strings.NewReader(body))
	ctx := context.WithValue(r.Context(), principalKey{}, who)
	if client != "" {
		ctx = context.WithValue(ctx, requestClientKey{}, parseRequestClient(client))
	}
	r = r.WithContext(ctx)
	if features != "" {
		r.Header.Set(featuresHeader, features)
	}
	r.SetPathValue("sid", sid)
	if lang != "" {
		r.Header.Set("Accept-Language", lang)
	}
	w := httptest.NewRecorder()
	h.sendMessage(w, r, m)
	return w
}

// The owner's message to an Agent whose computer is away goes to the manager holdable, in the request's language, and
// the manager's held answer is the app's: 202 delivered "later" with the message as its row shows it, the chat told it
// is kept, the send record settled later. A retry of the id, in this hub or after a restart, is answered as kept and
// never sent again.
func TestAgentMessageHeldWhileTheComputerIsAway(t *testing.T) {
	h, m, k := holdHub(t, agentRow)
	c := sendSubscriber(h, "box", "s1")
	w := sendTo(h, m, "s1", "ja", `{"text":"run the tests","client_msg_id":"held-1"}`)
	var out struct {
		Delivered   string                    `json:"delivered"`
		Undelivered *proto.UndeliveredMessage `json:"undelivered"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code != http.StatusAccepted || out.Delivered != sendLater ||
		out.Undelivered == nil || out.Undelivered.ID != "held-1" || out.Undelivered.Computer != "Studio Mac" {
		t.Fatalf("answer: %d %s", w.Code, w.Body)
	}
	sends := k.sends()
	if len(sends) != 1 || sends[0].Hold == nil || sends[0].Hold.Lang != "ja" || !sends[0].Ordered || sends[0].Caller == nil ||
		sends[0].MachineID != "box" {
		t.Fatalf("node calls %+v, want one holdable, ordered session.send in ja with its caller", sends)
	}
	var params map[string]any
	if err := json.Unmarshal(sends[0].Params, &params); err != nil || params["text"] != "run the tests" ||
		params["client_msg_id"] != "held-1" || params["session_id"] != "s1" {
		t.Fatalf("held params %s", sends[0].Params)
	}
	if event := nextSendState(t, c); event.State != proto.MessageSendProcessing {
		t.Fatalf("first state %+v", event)
	}
	if event := nextSendState(t, c); event.State != proto.MessageSendKept {
		t.Fatalf("final state %+v", event)
	}
	if st, _ := h.store.ClaimSend(context.Background(), "held-1", "box", "s1"); st != sendLater {
		t.Fatalf("send record %q, want %q", st, sendLater)
	}
	if again := sendTo(h, m, "s1", "ja", `{"text":"run the tests","client_msg_id":"held-1"}`); again.Code != http.StatusAccepted ||
		!strings.Contains(again.Body.String(), `"delivered":"later"`) {
		t.Fatalf("retry: %d %s", again.Code, again.Body)
	}
	h.sends.mu.Lock()
	clear(h.sends.flights) // a hub that restarted remembers only its Postgres record
	h.sends.mu.Unlock()
	if again := sendTo(h, m, "s1", "ja", `{"text":"run the tests","client_msg_id":"held-1"}`); again.Code != http.StatusAccepted ||
		!strings.Contains(again.Body.String(), `"delivered":"later"`) {
		t.Fatalf("retry after a restart: %d %s", again.Code, again.Body)
	}
	if n := len(k.sends()); n != 1 {
		t.Fatalf("sent %d times, want once", n)
	}
}

// Only a person's plain message to an Agent the hub lists may be held. A message without an id cannot go once later; a
// to-do's hand-off, an Agent's first prompt and a launcher switch are decided now; an assistant conversation keeps its
// own (keepUndelivered); a session the hub does not list may be one; an extension's, the hub token's or an answer-only
// device's message is no owner's bubble a full device could Retry or Delete. None carries Hold or Ordered, and each
// fails as before.
func TestOnlyAPlainAgentMessageIsHoldable(t *testing.T) {
	for _, tc := range []struct {
		name  string
		row   proto.Session
		sid   string
		body  string
		fresh bool
		who   *principal // nil: the owner's phone
	}{
		{name: "no client_msg_id", row: agentRow, sid: "s1", body: `{"text":"hi"}`},
		{name: "a launcher switch", row: agentRow, sid: "s1", body: `{"text":"hi","client_msg_id":"x1","apply_switch":true}`},
		{name: "a to-do's hand-off", row: agentRow, sid: "s1", body: `{"text":"hi","client_msg_id":"x2","todo_id":"t1"}`},
		{name: "an Agent's first prompt", row: agentRow, sid: "s1", body: `{"text":"hi","client_msg_id":"x3"}`, fresh: true},
		{name: "an assistant conversation", row: proto.Session{SessionID: "s1", Assistant: true, Status: proto.StatusUnknown},
			sid: "s1", body: `{"text":"hi","client_msg_id":"x4"}`},
		{name: "a session not listed", row: agentRow, sid: "s9", body: `{"text":"hi","client_msg_id":"x5"}`},
		{name: "an extension's token", row: agentRow, sid: "s1", body: `{"text":"hi","client_msg_id":"x6"}`,
			who: &principal{ID: "ext-token", Role: roleExtension, Extension: "example", Scopes: []string{"agents.control"}}},
		{name: "an answer-only device", row: agentRow, sid: "s1", body: `{"text":"hi","client_msg_id":"x7"}`,
			who: &principal{ID: "dev-watch", Name: "Watch", Role: roleAnswer}},
		{name: "the hub's own token", row: agentRow, sid: "s1", body: `{"text":"hi","client_msg_id":"x8"}`,
			who: &principal{Role: roleFull}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, m, k := holdHub(t, tc.row)
			if tc.fresh {
				h.fresh.add("box", tc.sid, "t1", "/home/me/project")
			}
			who := ownerPhone
			if tc.who != nil {
				who = *tc.who
			}
			w := sendAsWho(h, m, who, tc.sid, "en", tc.body)
			sends := k.sends()
			if len(sends) != 1 || sends[0].Hold != nil || sends[0].Ordered {
				t.Fatalf("node calls %+v, want one session.send without Hold or Ordered", sends)
			}
			if w.Code < http.StatusBadRequest {
				t.Fatalf("answer %d %s, want the failure as before", w.Code, w.Body)
			}
		})
	}
}

// A manager from before holding ignores Hold: the app gets machine_offline as before, and a retry may send it.
func TestHoldableSendToAnOlderManagerFailsAsBefore(t *testing.T) {
	h, m, k := holdHub(t, agentRow)
	k.mu.Lock()
	k.ignoresHold = true
	k.mu.Unlock()
	if w := sendTo(h, m, "s1", "en", `{"text":"hi","client_msg_id":"old-1"}`); w.Code != http.StatusServiceUnavailable ||
		strings.Contains(w.Body.String(), `"later"`) {
		t.Fatalf("answer: %d %s", w.Code, w.Body)
	}
	if st, _ := h.store.ClaimSend(context.Background(), "old-1", "box", "s1"); st != "" {
		t.Fatalf("send record %q: a retry could not send it", st)
	}
}

// Hold and Ordered go on a session.send only: any other call under the same context goes as before. withHold's send
// carries both, withOrdered's Ordered alone, an unmarked one neither.
func TestHoldOnlyOnSessionSend(t *testing.T) {
	h, _, k := holdHub(t, agentRow)
	ctx := withHold(context.Background(), "en")
	node := &busNode{h: h, id: "box", done: make(chan struct{})}
	for _, method := range []string{"session.get", "session.interrupt", "session.send"} {
		if err := node.Call(ctx, method, map[string]any{"session_id": "s1"}, nil); err != nil && method != "session.send" {
			t.Fatalf("%s: %v", method, err)
		}
	}
	if err := node.Call(context.Background(), "session.send", map[string]any{"session_id": "s1"}, nil); err == nil {
		t.Fatal("an unmarked send was held")
	}
	if err := node.Call(withOrdered(context.Background()), "session.send", map[string]any{"session_id": "s1"}, nil); err == nil {
		t.Fatal("an ordered send was held")
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if len(k.calls) != 5 {
		t.Fatalf("calls %+v", k.calls)
	}
	for i, c := range k.calls {
		if (c.Hold != nil) != (i == 2) || c.Ordered != (i == 2 || i == 4) {
			t.Fatalf("call %d (%s) carried Hold %+v, Ordered %t", i, c.Method, c.Hold, c.Ordered)
		}
	}
}

// Only a client that draws a held message gets one held: it says so on the request (X-Persona-Features), or a
// connection of the same device, platform and version said so in client_hello. Any other (an Android or HarmonyOS app
// that would take 202 "later" for sent) gets the failure as before, and a retry may send it.
func TestHoldOnlyForAClientThatDrawsIt(t *testing.T) {
	const body = `{"text":"run the tests","client_msg_id":"%s"}`
	for _, tc := range []struct {
		name, client, features string
		connected              bool // a connection of the phone's app, this version, declared the feature
		held                   bool
	}{
		{name: "declared on the request", features: "elicitation, " + featureAgentMessageHold, held: true},
		{name: "declared by its connection", client: "ios-app/2.0+300", connected: true, held: true},
		{name: "another version's connection declared it", client: "ios-app/1.9+250", connected: true},
		{name: "not declared", client: "android/1.0", features: "elicitation"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, m, k := holdHub(t, agentRow)
			if tc.connected {
				c := &client{device: ownerPhone.ID, out: make(chan []byte, 8), subs: map[[2]string]bool{}}
				c.meta.Platform, c.meta.Version = "ios-app", "2.0+300"
				c.meta.features = map[string]bool{featureAgentMessageHold: true}
				h.mu.Lock()
				h.clients[c] = struct{}{}
				h.mu.Unlock()
			}
			id := strings.ReplaceAll(tc.name, " ", "-")
			w := sendFrom(h, m, ownerPhone, tc.client, tc.features, "s1", "en", fmt.Sprintf(body, id))
			sends := k.sends()
			if len(sends) != 1 || (sends[0].Hold != nil) != tc.held || !sends[0].Ordered {
				t.Fatalf("node calls %+v, want one ordered session.send, held %t", sends, tc.held)
			}
			if held := w.Code == http.StatusAccepted && strings.Contains(w.Body.String(), `"delivered":"later"`); held != tc.held {
				t.Fatalf("answer %d %s, held %t", w.Code, w.Body, tc.held)
			}
			if !tc.held {
				if w.Code != http.StatusServiceUnavailable {
					t.Fatalf("answer %d %s, want machine_offline as before", w.Code, w.Body)
				}
				if st, _ := h.store.ClaimSend(context.Background(), id, "box", "s1"); st != "" {
					t.Fatalf("send record %q: a retry could not send it", st)
				}
			}
		})
	}
}

// A held answer to a send that did not ask to be held (a manager that holds what it should not) is an ordinary
// refusal: never 202 "later", never kept in the send record, never reported kept to the chat.
func TestHeldAnswerToASendThatDidNotAskIsARefusal(t *testing.T) {
	h, m, k := holdHub(t, agentRow)
	k.mu.Lock()
	k.holdsAll = true
	k.mu.Unlock()
	c := sendSubscriber(h, "box", "s1")
	w := sendFrom(h, m, ownerPhone, "android/1.0", "", "s1", "en", `{"text":"hi","client_msg_id":"plain-1"}`)
	if sends := k.sends(); len(sends) != 1 || sends[0].Hold != nil || !sends[0].Ordered {
		t.Fatalf("node calls %+v, want one ordered session.send without Hold", sends)
	}
	if w.Code == http.StatusAccepted || w.Code < http.StatusBadRequest || strings.Contains(w.Body.String(), `"delivered":"later"`) {
		t.Fatalf("answer %d %s, want a refusal", w.Code, w.Body)
	}
	if st, _ := h.store.ClaimSend(context.Background(), "plain-1", "box", "s1"); st != "" {
		t.Fatalf("send record %q, want none", st)
	}
	if event := nextSendState(t, c); event.State != proto.MessageSendProcessing {
		t.Fatalf("first state %+v", event)
	}
	if event := nextSendState(t, c); event.State != proto.MessageSendFailed {
		t.Fatalf("final state %+v, want failed", event)
	}
}

// What the hub relays to a manager says whether a person's own app asks (assistantwire.PersonHeader): "1" for a full
// device's own credential only, whatever the client sent under that name.
func TestRelayTellsWhetherAPersonAsks(t *testing.T) {
	h, _, k := holdHub(t, agentRow)
	for _, tc := range []struct {
		name   string
		who    principal
		person bool
	}{
		{name: "a person's phone", who: ownerPhone, person: true},
		{name: "the hub's own token", who: principal{Role: roleFull}},
		{name: "an extension's token", who: principal{ID: "ext-token", Role: roleExtension, Extension: "example"}},
		{name: "an answer-only device", who: principal{ID: "dev-watch", Name: "Watch", Role: roleAnswer}},
	} {
		for _, forged := range []string{"", "1", "0"} {
			r := httptest.NewRequest(http.MethodDelete, "/api/v1/assistant/conversations/s1/undelivered/m1", nil)
			r = r.WithContext(context.WithValue(r.Context(), principalKey{}, tc.who))
			if forged != "" {
				r.Header.Set(assistantwire.PersonHeader, forged)
			}
			w := httptest.NewRecorder()
			h.relayHTTP(w, r, assistantwire.AssistantID, false, false)
			if w.Code != http.StatusOK {
				t.Fatalf("%s: relayed %d %s", tc.name, w.Code, w.Body)
			}
			k.mu.Lock()
			got := k.relayed[len(k.relayed)-1].Header[assistantwire.PersonHeader]
			k.mu.Unlock()
			want := []string(nil)
			if tc.person {
				want = []string{"1"}
			}
			if !slices.Equal(got, want) {
				t.Fatalf("%s (client sent %q): %s = %q, want %q", tc.name, forged, assistantwire.PersonHeader, got, want)
			}
		}
	}
}

// An Agent whose row shows messages held for it cannot be deleted: its computer's manager would send them once the
// computer is back and the first would start it again. The owner deletes those first; an Agent with none deletes as
// before, and so does one whose row shows only other undelivered messages.
func TestDeletingAnAgentWithHeldMessagesIsRefused(t *testing.T) {
	h := newTestHub(t)
	h.store = &settingsStore{vals: map[string][]byte{}}
	m := newMachine(h, MachineConfig{ID: "box", Name: "Studio Mac"})
	registerMachine(h, m)
	held := []proto.UndeliveredMessage{{ID: "held-1", Text: "run the tests", State: proto.UndeliveredFailed, Computer: "Studio Mac",
		Error: "Not sent: Studio Mac was away for 24 hours"}}
	other := []proto.UndeliveredMessage{{ID: "kept-1", Text: "hello", State: proto.UndeliveredWaiting}}
	editSessions(m, func([]proto.Session) []proto.Session {
		return []proto.Session{
			{SessionID: "held-0001", Cwd: "/code/app", Status: proto.StatusOffline, Undelivered: &held},
			{SessionID: "plain-0002", Cwd: "/code/app", Status: proto.StatusOffline},
			{SessionID: "other-0003", Cwd: "/code/app", Status: proto.StatusOffline, Undelivered: &other},
		}
	})
	mux := http.NewServeMux()
	mux.HandleFunc("DELETE /api/v1/sessions/{mid}/{sid}", h.withMachine(h.deleteSession))
	del := func(sid, lang string) *httptest.ResponseRecorder {
		w := inboxLocaleWriter{ResponseRecorder: httptest.NewRecorder(), lang: i18n.Lang(lang)} // as the request logger sets it
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodDelete, "/api/v1/sessions/box/"+sid, nil))
		return w.ResponseRecorder
	}
	for lang, words := range map[string]string{"en": "Delete the messages waiting to be sent first", "zh-Hans": "先删除等待发送的消息",
		"ja": "送信待ちのメッセージを先に削除してください"} {
		if w := del("held-0001", lang); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), words) {
			t.Fatalf("%s: delete with held messages: %d %s", lang, w.Code, w.Body)
		}
	}
	if _, gone := h.deletedAt("box")["held-0001"]; gone {
		t.Fatal("deleted all the same")
	}
	for _, sid := range []string{"plain-0002", "other-0003"} {
		if w := del(sid, "en"); w.Code != http.StatusOK {
			t.Fatalf("delete %s: %d %s", sid, w.Code, w.Body)
		}
	}
}

// A person's message from an app that does not draw held messages never overtakes the ones held for its Agent: it goes
// ordered (without Hold), and the manager's held_ahead refusal, nothing typed, reaches the app as an ordinary refusal:
// the send record forgotten, the chat told it failed, and the app's retry of the id goes to the manager again.
func TestOrderedSendBehindHeldMessagesIsRefused(t *testing.T) {
	h, m, k := holdHub(t, agentRow)
	k.mu.Lock()
	k.heldAhead = true
	k.mu.Unlock()
	c := sendSubscriber(h, "box", "s1")
	w := sendFrom(h, m, ownerPhone, "android/1.0", "", "s1", "en", `{"text":"one more thing","client_msg_id":"after-1"}`)
	if sends := k.sends(); len(sends) != 1 || sends[0].Hold != nil || !sends[0].Ordered {
		t.Fatalf("node calls %+v, want one ordered session.send without Hold", sends)
	}
	var out struct {
		Code      string `json:"code"`
		Delivered string `json:"delivered"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code != http.StatusConflict || out.Code != "held_ahead" ||
		out.Delivered != "none" {
		t.Fatalf("answer %d %s, want the held_ahead refusal", w.Code, w.Body)
	}
	if st, _ := h.store.ClaimSend(context.Background(), "after-1", "box", "s1"); st != "" {
		t.Fatalf("send record %q: a retry could not send it", st)
	}
	_ = h.store.SettleSend(context.Background(), "after-1", "") // the claim just made to look
	if event := nextSendState(t, c); event.State != proto.MessageSendProcessing {
		t.Fatalf("first state %+v", event)
	}
	if event := nextSendState(t, c); event.State != proto.MessageSendFailed {
		t.Fatalf("final state %+v, want failed", event)
	}
	k.mu.Lock()
	k.heldAhead = false
	k.mu.Unlock()
	if again := sendFrom(h, m, ownerPhone, "android/1.0", "", "s1", "en", `{"text":"one more thing","client_msg_id":"after-1"}`); again.Code == http.StatusConflict {
		t.Fatalf("retry: %d %s", again.Code, again.Body)
	}
	if n := len(k.sends()); n != 2 {
		t.Fatalf("the retry reached the manager %d times in all, want twice", n)
	}
}
