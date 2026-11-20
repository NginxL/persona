package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// directionRouteEnv is the assistant with a database, its main conversation conv1 and side conversations on its machine.
func directionRouteEnv(t *testing.T, sides ...string) (*Service, *fakeHub) {
	t.Helper()
	s, f := newQuietServiceOn(t, &memStore{})
	list := []proto.Session{{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle}}
	for _, id := range sides {
		f.known[id] = true
		list = append(list, proto.Session{SessionID: id, MachineID: "server", Assistant: true, Status: proto.StatusIdle})
	}
	f.setSessions("server", list...)
	return s, f
}

func (s *Service) offerOf(id string) *routeOffer {
	b := &s.offers
	b.mu.Lock()
	defer b.mu.Unlock()
	if !s.routeOffersLocked(context.Background()) || b.m[id] == nil {
		return nil
	}
	cp := *b.m[id]
	return &cp
}

func countEvents(f *fakeHub, typ string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, e := range f.events {
		if e == typ {
			n++
		}
	}
	return n
}

func TestNewChatDoesNotAskForADirection(t *testing.T) {
	for _, name := range []string{"no directions", "existing direction"} {
		t.Run(name, func(t *testing.T) {
			s, f := directionRouteEnv(t, "lead1")
			if name == "existing direction" {
				setSettings(t, s, func(v *Settings) { v.Leaders = []string{"lead1"} })
			}
			var calls atomic.Int32
			called := make(chan string, 1)
			s.offers.ask = func(_ context.Context, _, input string, out any) error {
				calls.Add(1)
				select {
				case called <- input:
				default:
				}
				return json.Unmarshal([]byte(`{"kind":"direction","title":"Community garden"}`), out)
			}
			const sentence = "Plan the community garden."
			code, out := doRoute(t, s, http.MethodPost, "/api/v1/assistant/conversations", `{"prompt":"Plan the community garden."}`, "")
			var session proto.Session
			if code != http.StatusOK || json.Unmarshal(out["session"], &session) != nil || session.SessionID == "" {
				t.Fatalf("new chat: %d %s", code, out)
			}
			f.mu.Lock()
			sent := slices.ContainsFunc(f.sends, func(p map[string]any) bool { return p["text"] == sentence })
			f.mu.Unlock()
			if !sent {
				t.Fatal("the conversation did not receive the first message")
			}

			// The old handler started the classifier in a goroutine after sending the first message.
			var input string
			select {
			case input = <-called:
			case <-time.After(350 * time.Millisecond):
			}
			if n := calls.Load(); n != 0 {
				t.Errorf("new chat called assistant.route %d times with %q", n, input)
			}
			if n := countEvents(f, "direction_offer"); n != 0 {
				t.Errorf("new chat broadcast %d direction offers", n)
			}
			var offers map[string]*routeOffer
			if stored, err := s.store().Setting(context.Background(), routeOffersKey, &offers); err != nil || stored {
				t.Errorf("new chat wrote %s: stored=%v, offers=%d, err=%v", routeOffersKey, stored, len(offers), err)
			}
		})
	}
}

func doRoute(t *testing.T, s *Service, method, path, body string, role string) (int, map[string]json.RawMessage) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if role != "" {
		req.Header.Set(RoleHeader, role)
	}
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	var out map[string]json.RawMessage
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func TestDirectionOffersWithoutAnActiveOffer(t *testing.T) {
	const offers = "/api/v1/assistant/directions/offers"
	for _, action := range []struct {
		name, method, path string
		wantStatus         int
		key, want          string
	}{
		{"start", http.MethodPost, "/api/v1/assistant/directions/side1/start", http.StatusNotFound, "error", `"no direction is offered for this conversation: make it one with …/make"`},
		{"drop", http.MethodDelete, offers + "/side1", http.StatusOK, "dropped", "false"},
	} {
		for _, state := range []string{"empty", "expired"} {
			t.Run(action.name+"/"+state, func(t *testing.T) {
				s, f := directionRouteEnv(t, "side1")
				if state == "expired" {
					if err := s.store().PutSetting(context.Background(), routeOffersKey, map[string]*routeOffer{
						"side1": {MachineID: "server", Title: "Old project", At: time.Now().Add(-8 * 24 * time.Hour)},
					}); err != nil {
						t.Fatal(err)
					}
				}
				if code, out := doRoute(t, s, http.MethodGet, offers, "", "readonly"); code != http.StatusOK || string(out["offers"]) != "[]" {
					t.Errorf("GET offers: %d %s", code, out["offers"])
				}
				if code, _ := doRoute(t, s, action.method, action.path, "", "readonly"); code != http.StatusForbidden {
					t.Errorf("read-only %s: %d", action.path, code)
				}
				code, out := doRoute(t, s, action.method, action.path, "", "")
				if code != action.wantStatus || string(out[action.key]) != action.want {
					t.Errorf("%s without an active offer: %d %s", action.name, code, out[action.key])
				}
				if events := countEvents(f, "direction_offer"); events != 0 || len(s.settings(context.Background()).Leaders) != 0 {
					t.Errorf("a missing offer changed directions or broadcast an event: %d", events)
				}
			})
		}
	}
}

// The owner's taps: Start direction takes the offer, Make it a direction a quick chat, End direction takes it out; each
// is recorded as the owner's, with no phone approval; Just a quick chat drops the offer.
func TestDirectionEndpoints(t *testing.T) {
	s, f := directionRouteEnv(t, "side1", "side2", "side3")
	hub := &navigationHub{fakeHub: f}
	setHub(s, hub)
	// An upgraded manager loads the old manager's offers from its durable settings.
	now := time.Now().UTC()
	if err := s.store().PutSetting(context.Background(), routeOffersKey, map[string]*routeOffer{
		"side1": {MachineID: "server", Title: "Community garden", At: now},
		"side3": {MachineID: "server", Title: "Launch", At: now.Add(-time.Hour)},
	}); err != nil {
		t.Fatal(err)
	}
	code, out := doRoute(t, s, http.MethodGet, "/api/v1/assistant/directions/offers", "", "readonly")
	var listed []struct{ Conversation, Title string }
	if code != http.StatusOK || json.Unmarshal(out["offers"], &listed) != nil || len(listed) != 2 ||
		listed[0].Conversation != "side1" || listed[0].Title != "Community garden" || listed[1].Conversation != "side3" {
		t.Fatalf("stored offers: %d %s", code, out["offers"])
	}

	if code, _ := doRoute(t, s, http.MethodDelete, "/api/v1/assistant/directions/offers/side3", "", "readonly"); code != http.StatusForbidden {
		t.Fatalf("a read-only device dropped a current offer: %d", code)
	}
	if code, _ := doRoute(t, s, http.MethodPost, "/api/v1/assistant/directions/side1/start", "", "readonly"); code != http.StatusForbidden {
		t.Fatalf("a read-only device started a direction: %d", code)
	}
	if code, _ := doRoute(t, s, http.MethodPost, "/api/v1/assistant/directions/side2/start", "", ""); code != http.StatusNotFound {
		t.Fatalf("start without an offer: %d", code)
	}
	code, out = doRoute(t, s, http.MethodPost, "/api/v1/assistant/directions/side1/start", "", "")
	if code != 200 {
		t.Fatalf("start: %d %s", code, out["error"])
	}
	if cfg := s.settings(context.Background()); !slices.Equal(cfg.Leaders, []string{"side1"}) {
		t.Fatalf("leaders after start: %q", cfg.Leaders)
	}
	if d := s.directionOf("side1"); d == nil || d.By != directionByUser || d.EndedAt != nil {
		t.Fatalf("record after start: %+v", d)
	}
	if s.offerOf("side1") != nil {
		t.Fatal("the offer stayed after start")
	}
	if by, _ := s.titleOf("side1"); by != titleByAssistant {
		t.Fatalf("the direction's name was not given: by %q", by)
	}
	if len(f.pushes) != 0 {
		t.Fatalf("a phone approval was asked: %+v", f.pushes)
	}

	// Just a quick chat: the offer goes, nothing else changes.
	if code, out := doRoute(t, s, http.MethodDelete, "/api/v1/assistant/directions/offers/side3", "", ""); code != 200 || string(out["dropped"]) != "true" {
		t.Fatalf("drop: %d %s", code, out["dropped"])
	}
	if s.offerOf("side3") != nil || slices.Contains(s.settings(context.Background()).Leaders, "side3") {
		t.Fatal("the quick chat is still offered or became a direction")
	}

	var removed []string
	for _, event := range hub.take() {
		if event["type"] == "direction_offer" {
			if offer, present := event["offer"]; !present || offer != nil {
				t.Fatalf("taking a legacy offer must send an explicit null: %v", event)
			}
			removed = append(removed, fmt.Sprint(event["conversation"]))
		}
	}
	if !slices.Equal(removed, []string{"side1", "side3"}) {
		t.Fatalf("offer removals: %v", removed)
	}

	// Make it a direction: a quick chat, no offer needed.
	if code, out := doRoute(t, s, http.MethodPost, "/api/v1/assistant/directions/side2/make", "", ""); code != 200 {
		t.Fatalf("make: %d %s", code, out["error"])
	}
	if cfg := s.settings(context.Background()); !slices.Equal(cfg.Leaders, []string{"side1", "side2"}) {
		t.Fatalf("leaders after make: %q", cfg.Leaders)
	}
	if code, _ := doRoute(t, s, http.MethodPost, "/api/v1/assistant/directions/conv1/make", "", ""); code != http.StatusConflict {
		t.Fatalf("the main conversation was made a direction: %d", code)
	}
	if code, _ := doRoute(t, s, http.MethodPost, "/api/v1/assistant/directions/nope/make", "", ""); code != http.StatusConflict {
		t.Fatalf("an unknown conversation was made a direction: %d", code)
	}

	// End direction: out of the leaders, the record stays and is ended once the leaders are compared.
	code, out = doRoute(t, s, http.MethodPost, "/api/v1/assistant/directions/side1/end", "", "")
	if code != 200 || string(out["ended"]) != "true" {
		t.Fatalf("end: %d %s", code, out["ended"])
	}
	if cfg := s.settings(context.Background()); !slices.Equal(cfg.Leaders, []string{"side2"}) {
		t.Fatalf("leaders after end: %q", cfg.Leaders)
	}
	s.directionsSync(context.Background())
	if d := s.directionOf("side1"); d == nil || d.EndedAt == nil || d.By != directionByUser {
		t.Fatalf("record after end: %+v", d)
	}
	if code, out := doRoute(t, s, http.MethodPost, "/api/v1/assistant/directions/side1/end", "", ""); code != 200 || string(out["ended"]) != "false" {
		t.Fatalf("ending again: %d %s", code, out["ended"])
	}
	// Made a direction again: no longer ended.
	if code, _ := doRoute(t, s, http.MethodPost, "/api/v1/assistant/directions/side1/make", "", ""); code != 200 {
		t.Fatalf("make again: %d", code)
	}
	if d := s.directionOf("side1"); d == nil || d.EndedAt != nil {
		t.Fatalf("record after make again: %+v", d)
	}
}

// The 20-leader limit holds for the owner's taps too.
func TestDirectionLimit(t *testing.T) {
	var ids []string
	for i := range leadersMost + 1 {
		ids = append(ids, fmt.Sprintf("side%02d", i))
	}
	s, _ := directionRouteEnv(t, ids...)
	setSettings(t, s, func(v *Settings) { v.Leaders = ids[:leadersMost] })
	if code, out := doRoute(t, s, http.MethodPost, "/api/v1/assistant/directions/"+ids[leadersMost]+"/make", "", ""); code != http.StatusConflict ||
		!strings.Contains(string(out["error"]), "at most 20") {
		t.Fatalf("the 21st direction: %d %s", code, out["error"])
	}
	// One already a leader is fine.
	if code, _ := doRoute(t, s, http.MethodPost, "/api/v1/assistant/directions/"+ids[0]+"/make", "", ""); code != 200 {
		t.Fatalf("an existing leader again: %d", code)
	}
}

func TestGoldenRoute(t *testing.T) {
	golden(t, "route_input", prompts.RenderRouteInput(prompts.RouteInput{Sentence: "Bring the Android app up to the phone's features",
		Leaders:  []prompts.RouteLeader{{ID: "lead_a", Title: "Infra", Project: "infra"}, {ID: "lead_b", Title: "Docs"}},
		Projects: []string{"Example app", "Infra"}}))
	golden(t, "route_input.empty", prompts.RenderRouteInput(prompts.RouteInput{Sentence: "What's on my calendar today?"}))
}

// A direction made again keeps the name it was given; Start's offered name never replaces one given before.
func TestDirectionMadeAgainKeepsItsName(t *testing.T) {
	s, _ := directionRouteEnv(t, "side1")
	if code, _ := doRoute(t, s, http.MethodPost, "/api/v1/assistant/directions/side1/make", `{"title":"Infra"}`, ""); code != 200 {
		t.Fatalf("make: %d", code)
	}
	if err := s.setDirectionName(context.Background(), "side1", "基建", directionByUser); err != nil {
		t.Fatal(err)
	}
	doRoute(t, s, http.MethodPost, "/api/v1/assistant/directions/side1/end", "", "")
	s.directionsSync(context.Background())
	code, out := doRoute(t, s, http.MethodPost, "/api/v1/assistant/directions/side1/make", `{"title":"Other"}`, "")
	if code != 200 || !strings.Contains(string(out["leader"]), `"name":"基建"`) {
		t.Fatalf("made again: %d %s", code, out["leader"])
	}
	if d := s.directionOf("side1"); d == nil || d.Name != "基建" || d.EndedAt != nil {
		t.Fatalf("record: %+v", d)
	}
}
