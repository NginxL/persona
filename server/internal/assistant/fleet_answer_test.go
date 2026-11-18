package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/proto"
)

// answerFleet is a manager with one agent session whose question waits for an answer; done is what its hook hears.
func answerFleet(t *testing.T) (*fleetTest, proto.Interaction, chan proto.Resolution) {
	t.Helper()
	ft := newFleetTest(t)
	ft.d.setSessions("mac", []proto.Session{{SessionID: "abcdef12-agent", Title: "修登录", Cwd: "/code/app", Status: proto.StatusWorking,
		UpdatedAt: time.Now().UTC()}})
	ix := proto.Interaction{
		ID: "ix_ask1", MachineID: "mac", SessionID: "abcdef12-agent", Kind: proto.IxQuestion,
		Questions: []proto.Question{{Question: "发吗？", Options: []proto.QuestionOption{{Label: "发"}, {Label: "不发"}}}},
		CreatedAt: time.Now().UTC(), ExpiresAt: time.Now().Add(time.Hour).UTC(),
	}
	done := make(chan proto.Resolution, 1)
	ft.s.fleet.mu.Lock()
	ft.s.ixMapLocked()[ix.ID] = &pending{ix: ix, done: done}
	ft.s.fleet.mu.Unlock()
	if err := ft.st.InsertInteraction(context.Background(), ix); err != nil {
		t.Fatal(err)
	}
	return ft, ix, done
}

// queueCall is app.call interactions.<method> as the assistant's own code makes it (what the hub answered before).
func queueCall(t *testing.T, s *Service, method string, params map[string]any) (map[string]any, error) {
	t.Helper()
	raw, _ := json.Marshal(params)
	res, ok, err := s.localFleetAppCall(context.Background(), AppCall{AppID: AppInteractions, Method: method, Params: raw})
	if !ok {
		t.Fatalf("%s is not the manager's", method)
	}
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(res, &out); err != nil {
		t.Fatal(err)
	}
	return out, nil
}

func TestHonestPlanApprovalUsesRecordedMode(t *testing.T) {
	yes, no := true, false
	for _, tc := range []struct {
		name, kind, recorded, clientMode, want string
		approve                                *bool
	}{
		{"recorded mode overrides the client", proto.IxPlan, "acceptEdits", proto.ModeBypass, "acceptEdits", &yes},
		{"recorded mode fills an omitted client mode", proto.IxPlan, proto.ModeBypass, "", proto.ModeBypass, &yes},
		{"legacy plan keeps the client mode", proto.IxPlan, "", "acceptEdits", "acceptEdits", &yes},
		{"legacy plan keeps an omitted mode omitted", proto.IxPlan, "", "", "", &yes},
		{"unknown recorded mode keeps the client's choice", proto.IxPlan, "futureMode", "default", "default", &yes},
		{"recorded plan keeps the client's choice", proto.IxPlan, "plan", "acceptEdits", "acceptEdits", &yes},
		{"rejection is not rewritten", proto.IxPlan, "acceptEdits", proto.ModeBypass, proto.ModeBypass, &no},
		{"an unanswered plan is not rewritten", proto.IxPlan, "acceptEdits", proto.ModeBypass, proto.ModeBypass, nil},
		{"another kind of approval is not rewritten", proto.IxPermission, "acceptEdits", proto.ModeBypass, proto.ModeBypass, &yes},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ix := proto.Interaction{Kind: tc.kind, PlanThen: tc.recorded}
			got := honest(ix, proto.Resolution{Approve: tc.approve, Mode: tc.clientMode})
			if got.Mode != tc.want {
				t.Fatalf("honest(%+v, mode %q) = mode %q, want %q", ix, tc.clientMode, got.Mode, tc.want)
			}
		})
	}
}

// interactions.get is the interaction while it is pending, else how it ended; interactions.answer resolves only a
// pending question of the session named, as the assistant's answer, and the history and the hub say so.
func TestInteractionsQueue(t *testing.T) {
	ft, ix, done := answerFleet(t)
	s := ft.s
	out, err := queueCall(t, s, assistantwire.InteractionsGet, map[string]any{"interaction_id": ix.ID})
	if err != nil || out["ok"] != true || out["interaction"].(map[string]any)["id"] != ix.ID {
		t.Fatalf("get, pending: %v %v", out, err)
	}
	ft.st.status["ix_gone"] = "dismissed"
	if out, err := queueCall(t, s, assistantwire.InteractionsGet, map[string]any{"interaction_id": "ix_gone"}); err != nil ||
		out["ok"] != false || out["status"] != "dismissed" {
		t.Fatalf("get, ended: %v %v", out, err)
	}
	perm := proto.Interaction{ID: "ix_perm", MachineID: "mac", SessionID: "abcdef12-agent", Kind: proto.IxPermission, ToolName: "Bash",
		CreatedAt: time.Now().UTC(), ExpiresAt: time.Now().Add(time.Hour).UTC()}
	s.fleet.mu.Lock()
	s.ixMapLocked()[perm.ID] = &pending{ix: perm, done: make(chan proto.Resolution, 1)}
	s.fleet.mu.Unlock()
	for _, params := range []map[string]any{
		{"machine_id": "mac", "session_id": "abcdef12-agent", "interaction_id": perm.ID, "answers": map[string]string{"x": "y"}},
		{"machine_id": "mac", "session_id": "99999999-agent", "interaction_id": ix.ID, "answers": map[string]string{"发吗？": "发"}},
		{"machine_id": "box", "session_id": "abcdef12-agent", "interaction_id": ix.ID, "answers": map[string]string{"发吗？": "发"}},
	} {
		if _, err := queueCall(t, s, assistantwire.InteractionsAnswer, params); err == nil {
			t.Fatalf("answered %v", params)
		}
	}
	if _, still := s.pendingIx(ix.ID); !still {
		t.Fatal("a refused answer resolved the interaction")
	}
	if out, err := queueCall(t, s, assistantwire.InteractionsAnswer, map[string]any{"machine_id": "mac", "session_id": "abcdef12-agent",
		"interaction_id": ix.ID, "answers": map[string]string{"发吗？": "发"}}); err != nil || out["ok"] != true {
		t.Fatalf("answer: %v %v", out, err)
	}
	select {
	case r := <-done:
		if r.Answers["发吗？"] != "发" || r.AnsweredBy != proto.AnsweredByAssistant {
			t.Fatalf("resolution: %+v", r)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the hook got no answer")
	}
	status, res, _ := ft.st.InteractionOutcome(context.Background(), ix.ID)
	if status != "resolved" || res == nil || res.AnsweredBy != proto.AnsweredByAssistant {
		t.Fatalf("history: %q %+v", status, res)
	}
	// The hub hears who answered: the app marks it.
	var saw bool
	for _, b := range ft.toldOf(assistantwire.EventFleetResolved) {
		var p assistantwire.FleetResolved
		_ = json.Unmarshal(b, &p)
		saw = saw || p.ID == ix.ID && p.AnsweredBy == proto.AnsweredByAssistant
	}
	if !saw {
		t.Fatal("the hub was not told the assistant answered")
	}
}

// The phone and the assistant answering the same hook-waited card at once: one answer wins, and the hook and the
// history get that same one.
func TestResolveTakesOneAnswerUnderRace(t *testing.T) {
	for range 50 {
		ft, ix, done := answerFleet(t)
		errs := make(chan error, 2)
		for _, l := range []string{"发", "不发"} {
			go func() { errs <- ft.s.resolve(ix.ID, proto.Resolution{Answers: map[string]string{"发吗？": l}}) }()
		}
		e1, e2 := <-errs, <-errs
		if (e1 == nil) == (e2 == nil) {
			t.Fatalf("want exactly one answer to win: %v / %v", e1, e2)
		}
		got := <-done
		_, res, _ := ft.st.InteractionOutcome(context.Background(), ix.ID)
		if res == nil || res.Answers["发吗？"] != got.Answers["发吗？"] {
			t.Fatalf("hook got %v, history %+v", got.Answers, res)
		}
	}
}

// The answer route the hub relays: an app's answer cannot say the assistant gave it; one over already is the same
// outcome again (200 already) when it was answered, else 410 with how it ended.
func TestResolveRoute(t *testing.T) {
	ft, ix, done := answerFleet(t)
	h := ft.s.fleetHandler(http.NotFoundHandler())
	post := func(id, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("POST", "/api/v1/interactions/"+id+"/resolve", strings.NewReader(body)))
		return w
	}
	if w := post(ix.ID, `{"answers":{"发吗？":"发"},"answered_by":"assistant","remembered":true}`); w.Code != http.StatusOK {
		t.Fatalf("status %d %s", w.Code, w.Body)
	}
	if got := <-done; got.AnsweredBy != "" || got.Remembered || got.Answers["发吗？"] != "发" {
		t.Fatalf("hook got %+v", got)
	}
	status, kept, err := ft.st.InteractionOutcome(context.Background(), ix.ID)
	if err != nil || status != "resolved" || kept == nil || kept.AnsweredBy != "" || kept.Answers["发吗？"] != "发" {
		t.Fatalf("an ordinary user's history answer changed: status %q, resolution %+v, err %v", status, kept, err)
	}
	if w := post(ix.ID, `{"answers":{"发吗？":"不发"}}`); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"already":true`) {
		t.Fatalf("again: %d %s", w.Code, w.Body)
	}
	ft.st.status["ix_old"] = "expired"
	if w := post("ix_old", `{"dismiss":true}`); w.Code != http.StatusGone || !strings.Contains(w.Body.String(), `"status":"expired"`) {
		t.Fatalf("expired: %d %s", w.Code, w.Body)
	}
}

// A second answer between the first one's claim and its outcome reaching the history (#1230) is the same outcome
// again, told from the claim: 200 already with the first answer, not 410. A card its end claimed (expired, its hook
// gone) has no answer to tell and stays 410.
func TestResolveRouteRightAfterAnotherAnswer(t *testing.T) {
	ft, ix, _ := answerFleet(t)
	h := ft.s.fleetHandler(http.NotFoundHandler())
	post := func() *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("POST", "/api/v1/interactions/"+ix.ID+"/resolve", strings.NewReader(`{"answers":{"发吗？":"不发"}}`)))
		return w
	}
	ft.s.fleet.mu.Lock()
	p := ft.s.fleet.interactions[ix.ID]
	p.claimed, p.resolution = true, &proto.Resolution{Answers: map[string]string{"发吗？": "发"}}
	ft.s.fleet.mu.Unlock()
	if status, _, _ := ft.st.InteractionOutcome(context.Background(), ix.ID); status != "pending" {
		t.Fatalf("the history has %q already", status)
	}
	w := post()
	var out struct {
		Already    bool              `json:"already"`
		Status     string            `json:"status"`
		Resolution *proto.Resolution `json:"resolution"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if w.Code != http.StatusOK || !out.Already || out.Status != "resolved" || out.Resolution == nil || out.Resolution.Answers["发吗？"] != "发" {
		t.Fatalf("right after another answer: %d %s", w.Code, w.Body)
	}
	ft.s.fleet.mu.Lock()
	p.resolution = nil // claimed by its end instead
	ft.s.fleet.mu.Unlock()
	if w := post(); w.Code != http.StatusGone {
		t.Fatalf("right after it ended: %d %s", w.Code, w.Body)
	}
}

// An MCP form or URL request (elicitation) is only shown while an app that takes one is connected (the hub says,
// fleet.clients); else it goes back to the terminal at once. Its answer must be one of its actions.
func TestElicitationNeedsAnApp(t *testing.T) {
	ft := newFleetTestNoStore(t)
	s := ft.s
	ix := proto.Interaction{ID: "ix_example", MachineID: "mac", SessionID: "test-session", Kind: proto.IxElicitation,
		Summary: "请选择", ExpiresAt: time.Now().Add(time.Minute), Elicitation: &proto.Elicitation{Mode: "form", Message: "请选择"}}
	if r, err := s.waitInteraction(context.Background(), ix); err != nil || !r.Dismiss || len(s.pendingInteractions()) != 0 {
		t.Fatalf("no app takes it: back to the terminal: %+v %v", r, err)
	}
	b, _ := json.Marshal(assistantwire.Clients{Elicitation: true})
	s.FleetEvent(context.Background(), assistantwire.EventClients, b)
	for _, action := range []string{"accept", "decline", "cancel", "dismiss"} {
		done := make(chan proto.Resolution, 1)
		go func() { r, _ := s.waitInteraction(context.Background(), ix); done <- r }()
		ft.waitPending(1)
		if err := s.resolve(ix.ID, proto.Resolution{Allow: new(bool)}); err == nil {
			t.Fatal("invalid action consumed request")
		}
		want := proto.Resolution{Action: action, Dismiss: action == "dismiss", Content: map[string]any{"value": true}}
		if err := s.resolve(ix.ID, want); err != nil {
			t.Fatal(err)
		}
		if got := <-done; got.Action != want.Action || got.Dismiss != want.Dismiss {
			t.Fatalf("resolution: %+v", got)
		}
		ft.waitPending(0)
	}
	// Its push goes only to what can show it (the hub picks): the kind says it is an elicitation.
	if got := ft.pushesOf(assistantwire.NotifyInput, 4, time.Second); len(got) != 4 || got[0].Interaction.Kind != proto.IxElicitation {
		t.Fatalf("pushes %+v", got)
	}
}

// A phone that registered for MCP forms and links by push (#2433, fleet.clients `elicitation_push`) is waited for as
// for any card: the request waits the usual card timeout, is pushed, and its hook coming back finds it still waiting.
// What an older hub says (no elicitation_push) hands it back at once as before, and so does a card that would not be
// pushed: a muted agent's, a quiet task's.
func TestElicitationWaitsForAPushedPhone(t *testing.T) {
	ft := newFleetTestNoStore(t)
	s := ft.s
	ctx := context.Background()
	ix := proto.Interaction{ID: "ix_example", MachineID: "mac", SessionID: "test-session", Kind: proto.IxElicitation,
		MCPServerName: "example-mcp", Summary: "Please choose", Resumable: true,
		Elicitation: &proto.Elicitation{Mode: "form", Message: "Please choose"}}
	clients := func(raw string) { s.FleetEvent(ctx, assistantwire.EventClients, json.RawMessage(raw)) }
	// hookFor is a hook that waits on ix until it goes away (its node's connection ends) after a moment.
	hookFor := func() proto.Resolution {
		hctx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
		defer cancel()
		r, _ := s.waitInteraction(hctx, ix)
		return r
	}

	clients(`{"elicitation":false}`) // an older hub's
	if r, err := s.waitInteraction(ctx, ix); err != nil || !r.Dismiss || len(s.pendingInteractions()) != 0 {
		t.Fatalf("an older hub, no app: back to the terminal: %+v %v", r, err)
	}

	clients(`{"elicitation":false,"elicitation_push":true}`)
	hookFor()
	got := ft.waitPending(1) // its hook went away: the card stays for the next one (resumeGrace)
	if wait := time.Until(got[0].ExpiresAt); wait < 29*time.Minute || wait > 31*time.Minute {
		t.Fatalf("waits %v, want the usual card timeout", wait)
	}
	if p := ft.pushesOf(assistantwire.NotifyInput, 1, time.Second); len(p) != 1 || p[0].Interaction.ID != ix.ID {
		t.Fatalf("pushes %+v", p)
	}
	// The hook comes back (a node restart): the same card waits on, it is not handed back.
	hookFor()
	if left := ft.waitPending(1); left[0].ID != ix.ID {
		t.Fatalf("pending %+v", left)
	}
	if err := s.resolve(ix.ID, proto.Resolution{Action: "accept", Content: map[string]any{"choice": "yes"}}); err != nil {
		t.Fatal(err)
	}
	ft.waitPending(0)
	// Answered on the phone, the waiting hook gets it.
	ix2 := ix
	ix2.ID, ix2.Resumable = "ix_example_2", false
	done := make(chan proto.Resolution, 1)
	go func() { r, _ := s.waitInteraction(ctx, ix2); done <- r }()
	ft.waitPending(1)
	if err := s.resolve(ix2.ID, proto.Resolution{Action: "accept", Content: map[string]any{"choice": "yes"}}); err != nil {
		t.Fatal(err)
	}
	if r := <-done; r.Action != "accept" || r.Dismiss {
		t.Fatalf("resolution %+v", r)
	}
	ft.waitPending(0)

	// Not pushed, not waited for: a muted agent's card, a quiet task's.
	if !s.elicitationReachable(ix) {
		t.Fatal("a phone to push")
	}
	b, _ := json.Marshal(assistantwire.FleetMutes{Mutes: &assistantwire.Mutes{Spaces: []string{}, Sessions: []string{"mac/test-session"}}})
	s.FleetEvent(ctx, assistantwire.EventMutes, b)
	if s.elicitationReachable(ix) {
		t.Fatal("a muted agent's card is not pushed")
	}
	b, _ = json.Marshal(assistantwire.FleetMutes{Mutes: &assistantwire.Mutes{Spaces: []string{}, Sessions: []string{}}})
	s.FleetEvent(ctx, assistantwire.EventMutes, b)
	s.fleet.rep.mu.Lock()
	s.fleet.rep.quiet = map[[2]string]bool{{"mac", "test-session"}: true}
	s.fleet.rep.mu.Unlock()
	if s.elicitationReachable(ix) {
		t.Fatal("a quiet task's card is not pushed")
	}
	clients(`{"elicitation":true,"elicitation_push":false}`)
	if !s.elicitationReachable(ix) {
		t.Fatal("an app is connected: it shows there")
	}
	clients(`{"elicitation":false,"elicitation_push":false}`)
	if s.elicitationReachable(ix) {
		t.Fatal("nothing can show it")
	}
}
