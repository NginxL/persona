package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/proto"
)

func TestCacheKeepaliveOnlyMainAndRegisteredLeads(t *testing.T) {
	s, f := mainChatFixture(t)
	ctx := context.Background()
	setModel(t, s, "assistant", ModelBinding{Provider: "launcher.claude", Model: "example-model"})
	setSettings(t, s, func(v *Settings) {
		v.Main = "main"
		v.Leaders = []string{"lead"}
		v.BackgroundSupervisor = "background"
	})
	var sessions []proto.Session
	for _, id := range []string{"main", "lead", "background", "worker", "child", "ordinary"} {
		sessions = append(sessions, proto.Session{SessionID: id, Launcher: "claude", Model: "example-model", Assistant: id != "worker", IsSubagent: id == "child"})
	}
	f.setSessions("server", sessions...)
	for _, id := range []string{"main", "lead", "background", "worker", "child", "ordinary", "unknown"} {
		t.Run(id, func(t *testing.T) {
			p, err := s.SendParams(ctx, SendParams{MachineID: "server", SessionID: id, Params: map[string]any{"text": "hello", "cache_keepalive": assistantwire.CacheKeepalive{Enabled: true}}})
			if err != nil {
				t.Fatal(err)
			}
			_, ok := p["cache_keepalive"]
			if ok != (id == "main" || id == "lead") {
				t.Fatalf("%s policy: %v", id, p["cache_keepalive"])
			}
		})
	}
	// A removed or archived lead loses authority even while its node retains a snapshot.
	setSettings(t, s, func(v *Settings) { v.Leaders = nil })
	p := map[string]any{}
	s.putCacheKeepalive(ctx, "server", "lead", false, p)
	if p["cache_keepalive"] != nil {
		t.Fatal("removed lead retained authority")
	}
}
func TestCacheKeepaliveSettingsDefaultsValidationAndPartialPUT(t *testing.T) {
	s := newTestService(t, newFakeHub().withMachine())
	ctx := context.Background()
	if p := cacheKeepaliveOf(s.settings(ctx)); !p.Enabled || p.IntervalMinutes != 55 || p.MaxRequests != 24 {
		t.Fatalf("defaults %+v", p)
	}
	put := func(body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPut, "/api/v1/assistant", strings.NewReader(body))
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		return w
	}
	for _, body := range []string{`{"cache_keepalive_minutes":0}`, `{"cache_keepalive_minutes":56}`, `{"cache_keepalive_max":0}`, `{"cache_keepalive_max":145}`, `{"cache_keepalive":"false"}`, `{"cache_keepalive_minutes":1.5}`} {
		if w := put(body); w.Code != 400 {
			t.Fatalf("accepted %s: %d", body, w.Code)
		}
	}
	w := put(`{"cache_keepalive":false,"cache_keepalive_minutes":30,"cache_keepalive_max":12}`)
	if w.Code != 200 {
		t.Fatalf("save %d %s", w.Code, w.Body.String())
	}
	var view map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view["cache_keepalive"] != false || view["cache_keepalive_minutes"] != float64(30) || view["cache_keepalive_max"] != float64(12) {
		t.Fatal(view)
	}
	if w := put(`{"notes":"example"}`); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if p := cacheKeepaliveOf(s.settings(ctx)); p.Enabled || p.IntervalMinutes != 30 || p.MaxRequests != 12 {
		t.Fatalf("partial reset %+v", p)
	}
}

func TestCacheKeepaliveEligibilityDoesNotReadTheNode(t *testing.T) {
	f := newFakeHub().withMachine()
	s := newTestService(t, f)
	d := newFleetDevs()
	d.add("server", "Computer")
	d.setSessions("server", []proto.Session{
		{SessionID: "main", Assistant: true}, {SessionID: "lead", Assistant: true},
		{SessionID: "worker"}, {SessionID: "child", Assistant: true, IsSubagent: true},
		{SessionID: "archived", Assistant: true, Archived: true}, {SessionID: "background", Assistant: true},
	})
	s.devs = d
	setSettings(t, s, func(v *Settings) {
		v.Main = "main"
		v.Leaders = []string{"lead", "worker", "child", "archived", "background"}
		v.BackgroundSupervisor = "background"
	})
	getsBefore, listsBefore := len(d.callsTo("server", DeviceSessionGet)), len(d.callsTo("server", DeviceSessionsList))
	for _, id := range []string{"main", "lead", "worker", "child", "archived", "background"} {
		p := map[string]any{}
		s.putCacheKeepalive(context.Background(), "server", id, id == "main", p)
		if (p["cache_keepalive"] != nil) != (id == "main" || id == "lead") {
			t.Fatalf("wrong policy for %s", id)
		}
	}
	if got := len(d.callsTo("server", DeviceSessionGet)); got != getsBefore {
		t.Fatalf("eligibility added %d session.get calls", got-getsBefore)
	}
	if got := len(d.callsTo("server", DeviceSessionsList)); got != listsBefore {
		t.Fatalf("eligibility added %d sessions.list calls", got-listsBefore)
	}
}

// The manager's own sends (a leader's supervision digest, a relay, an A2A delivery, an Inbox continuation) are often
// what starts a main or lead process, and the node mounts the refresh proxy only at a start. Each carries the role as
// sendModel's messages do; other conversations' sends carry none, and a value a caller set is never passed on (#12786).
func TestCacheKeepaliveGoesWithEveryManagerSend(t *testing.T) {
	ctx := context.Background()
	f := newFakeHub().withMachine()
	s := newTestService(t, f)
	sessions := []proto.Session{
		{SessionID: "main", Assistant: true}, {SessionID: "lead", Assistant: true},
		{SessionID: "background", Assistant: true}, {SessionID: "side", Assistant: true},
		{SessionID: "worker"}, {SessionID: "child", Assistant: true, IsSubagent: true},
	}
	f.setSessions("server", sessions...)
	// The node's retained inventory, which revokes a registered id that is not a root assistant conversation.
	d := newFleetDevs()
	d.add("server", "Computer")
	d.setSessions("server", sessions)
	s.devs = d
	setSettings(t, s, func(v *Settings) {
		v.Main = "main"
		v.Leaders = []string{"lead", "worker", "child", "background"}
		v.BackgroundSupervisor = "background"
	})
	last := func(method string) map[string]any {
		t.Helper()
		if raw := d.callsTo("server", method); len(raw) > 0 { // the call went to the devices
			var p map[string]any
			if err := json.Unmarshal(raw[len(raw)-1], &p); err != nil {
				t.Fatal(err)
			}
			return p
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		for i := len(f.calls) - 1; i >= 0; i-- {
			if f.calls[i].Method == method {
				var p map[string]any
				if err := json.Unmarshal(f.calls[i].Params, &p); err != nil {
					t.Fatal(err)
				}
				return p
			}
		}
		t.Fatalf("no %s", method)
		return nil
	}
	forged := assistantwire.CacheKeepalive{Enabled: true, IntervalMinutes: 1, MaxRequests: 144}
	for _, method := range []string{DeviceSessionSend, DeviceSessionSendOnce, DeviceAssistantSend} {
		for _, id := range []string{"main", "lead", "background", "side", "worker", "child"} {
			t.Run(method+"/"+id, func(t *testing.T) {
				if err := s.call(ctx, "server", method, map[string]any{"session_id": id, "text": "A task finished.", "cache_keepalive": forged}, nil); err != nil {
					t.Fatal(err)
				}
				got, ok := last(method)["cache_keepalive"].(map[string]any)
				if want := id == "main" || id == "lead"; ok != want {
					t.Fatalf("policy %v, want one: %v", got, want)
				}
				if ok && (got["enabled"] != true || got["interval_minutes"] != float64(55) || got["max_requests"] != float64(24)) {
					t.Fatalf("caller's value passed on: %v", got)
				}
			})
		}
	}
	// Switched off: the role still goes, saying off (the node then mounts nothing and stops refreshing).
	setSettings(t, s, func(v *Settings) { v.CacheKeepalive = ptr(false) })
	if err := s.call(ctx, "server", DeviceSessionSend, map[string]any{"session_id": "lead", "text": "x"}, nil); err != nil {
		t.Fatal(err)
	}
	if p, _ := last(DeviceSessionSend)["cache_keepalive"].(map[string]any); p == nil || p["enabled"] != false {
		t.Fatalf("switched off: %v", p)
	}
}

// A node never marks its own rows Assistant: only the hub's decorated list does. The retained inventory the eligibility
// check reads is the node's, so a main or lead running in the assistant directory must count as an assistant row there,
// or every start lost its refresh role and no proxy was ever mounted.
func TestCacheKeepaliveAcceptsNodeListedAssistantRows(t *testing.T) {
	f := newFakeHub().withMachine()
	s := newTestService(t, f)
	d := newFleetDevs()
	d.add("server", "Computer")
	s.devs = d
	m, ok := s.fleetMachine("server")
	if !ok {
		t.Fatal("no fleet machine")
	}
	dir := m.assistantCwd()
	d.setSessions("server", []proto.Session{
		{SessionID: "main", Agent: "claude", Cwd: dir},
		{SessionID: "lead", Agent: "claude", Cwd: dir},
		{SessionID: "worker", Agent: "claude", Cwd: "/home/u/project"},
		{SessionID: "child", Agent: "claude", Cwd: dir, IsSubagent: true},
		{SessionID: "archived", Agent: "claude", Cwd: dir, Archived: true},
	})
	setSettings(t, s, func(v *Settings) {
		v.Main = "main"
		v.Leaders = []string{"lead", "worker", "child", "archived"}
	})
	for _, id := range []string{"main", "lead", "worker", "child", "archived"} {
		p := map[string]any{}
		s.putCacheKeepalive(context.Background(), "server", id, id == "main", p)
		if (p["cache_keepalive"] != nil) != (id == "main" || id == "lead") {
			t.Fatalf("%s policy: %v", id, p["cache_keepalive"])
		}
	}
}
