package hub

// The other paths around "a manager acts only on its own machines" (#5923): relayed Persona Links, ws.broadcast,
// notify.request, the extensions' events, app.call, and what the hub sends a machine's manager. Each test has two
// managers on a Postgres bus hub (newBusHub) unless it says otherwise.

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/bus"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/extwire"
	"github.com/nwverse/persona/server/internal/link"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// heard keeps the events of one kind a fake manager is told (its extensions' agent.*, the mutes…).
func heard(f *guardManager, event string) func() string {
	var mu sync.Mutex
	var evs []string
	f.mu.Lock()
	defer f.mu.Unlock()
	f.onEvent = func(method string, params json.RawMessage) { // not peer.OnEvent: Serve reads that as it runs
		if method != event {
			return
		}
		mu.Lock()
		evs = append(evs, string(params))
		mu.Unlock()
	}
	return func() string { mu.Lock(); defer mu.Unlock(); return strings.Join(evs, "\n") }
}

// appGot drains what app c was sent for a moment.
func appGot(c *client, within time.Duration) string {
	var got []string
	deadline := time.After(within)
	for {
		select {
		case b := <-c.out:
			got = append(got, string(b))
		case <-deadline:
			return strings.Join(got, "\n")
		}
	}
}

// pathsHub: the assistant (a) owns ma, with session s3 listed; m-0b0b0b (b) owns mb, with session b1 listed.
func pathsHub(t *testing.T, answerA, answerB func(string, json.RawMessage) (any, error)) (*Hub, *guardManager, *guardManager) {
	t.Helper()
	h, srv, _ := newBusHub(t)
	h.cfg.Machines = []MachineConfig{{ID: "ma", Name: "ma", Kind: "local"}, {ID: "mb", Name: "mb", Kind: "local"}}
	a := connectGuardManager(t, h, srv.URL, assistantwire.AssistantID, answerA)
	b := connectGuardManager(t, h, srv.URL, "m-0b0b0b", answerB)
	if _, err := h.moveMachine(context.Background(), "mb", "m-0b0b0b"); err != nil {
		t.Fatal(err)
	}
	a.report(t, devicewire.State{Machine: proto.Machine{ID: "ma", Name: "ma"}})
	b.report(t, devicewire.State{Machine: proto.Machine{ID: "mb", Name: "mb"}})
	eventually(t, 5*time.Second, "ma, mb", func() bool { return h.localMachine("ma") != nil && h.localMachine("mb") != nil })
	h.takeFleetSessions(assistantwire.FleetSessions{MachineID: "ma", Sessions: []proto.Session{{MachineID: "ma", SessionID: "s3", Title: "A's work"}}})
	h.takeFleetSessions(assistantwire.FleetSessions{MachineID: "mb", Sessions: []proto.Session{{MachineID: "mb", SessionID: "b1"}}})
	return h, a, b
}

func quiet(string, json.RawMessage) (any, error) { return map[string]any{}, nil }

// notify.request is only about the caller's own: a card's push for a pending card it reported (the hub pushes its own
// copy: what the request claims of the card is not used), a turn's push for a session of its own. The extensions'
// agent.needs_you and agent.finished go to that manager only.
func TestAPushOnlyAboutItsOwn(t *testing.T) {
	h, a, b := pathsHub(t, quiet, quiet)
	aHeard, bHeard := heard(a, extwire.EventEmit), heard(b, extwire.EventEmit)
	push := func(f *guardManager, n assistantwire.Notification) error {
		return f.call(t, assistantwire.MethodNotify, assistantwire.NotifyRequest{Notification: n}, nil)
	}
	tell := func(f *guardManager, ix proto.Interaction) {
		if err := f.peer.Notify(assistantwire.EventFleetInteraction, assistantwire.FleetInteraction{Interaction: ix}); err != nil {
			t.Fatal(err)
		}
	}
	tell(a, proto.Interaction{ID: "ix_a", MachineID: "ma", SessionID: "s3", Kind: proto.IxPermission, Summary: "A's summary"})
	tell(b, proto.Interaction{ID: "ix_b", MachineID: "mb", SessionID: "b1", Kind: proto.IxPermission, Summary: "B's summary"})
	eventually(t, 5*time.Second, "both cards", func() bool { return h.interactionOwner("ix_a") != "" && h.interactionOwner("ix_b") != "" })

	// B: A's card, a card it never reported, and its own card claiming A's session; a turn of A's session.
	for _, n := range []assistantwire.Notification{
		{ID: "n1", Kind: assistantwire.NotifyInput, Interaction: &proto.Interaction{ID: "ix_a", MachineID: "ma", SessionID: "s3", Summary: "x"}},
		{ID: "n2", Kind: assistantwire.NotifyInput, Interaction: &proto.Interaction{ID: "ix_new", MachineID: "ma", SessionID: "s3", Summary: "x"}},
		{ID: "n3", Kind: assistantwire.NotifyDone, DeviceID: "ma", SessionID: "s3", Title: "x"},
		{ID: "n4", Kind: assistantwire.NotifyReply, SessionID: "s3", Title: "x"},
		{ID: "n5", Kind: assistantwire.NotifyPush, DeviceID: "ma", Title: "x"},
	} {
		if err := push(b, n); rpcCode(err) != devicewire.CodeNoDevice {
			t.Fatalf("B's push %s: %v", n.ID, err)
		}
	}
	if err := push(b, assistantwire.Notification{ID: "n6", Kind: assistantwire.NotifyInput,
		Interaction: &proto.Interaction{ID: "ix_b", MachineID: "ma", SessionID: "s3", Summary: "FORGED SUMMARY"}}); err != nil {
		t.Fatalf("B's push of its own card: %v", err)
	}
	if err := push(b, assistantwire.Notification{ID: "n7", Kind: assistantwire.NotifyDone, DeviceID: "mb", SessionID: "b1", Title: "B's title"}); err != nil {
		t.Fatalf("B's push of its own session: %v", err)
	}
	// A: its own card and its own session's turn.
	if err := push(a, assistantwire.Notification{ID: "n8", Kind: assistantwire.NotifyInput, Interaction: &proto.Interaction{ID: "ix_a"}}); err != nil {
		t.Fatal(err)
	}
	if err := push(a, assistantwire.Notification{ID: "n9", Kind: assistantwire.NotifyDone, DeviceID: "ma", SessionID: "s3", Title: "A's title"}); err != nil {
		t.Fatal(err)
	}
	eventually(t, 5*time.Second, "the extensions' events", func() bool {
		return strings.Contains(aHeard(), "A's title") && strings.Contains(bHeard(), "B's title")
	})
	time.Sleep(200 * time.Millisecond)
	ah, bh := aHeard(), bHeard()
	if !strings.Contains(ah, "A's summary") || strings.Contains(ah, "B's") {
		t.Fatalf("A's extensions heard:\n%s", ah)
	}
	if !strings.Contains(bh, "B's summary") || strings.Contains(bh, "FORGED") || strings.Contains(bh, `"machine_id":"ma"`) || strings.Contains(bh, "A's") {
		t.Fatalf("B's extensions heard (its own card as the hub keeps it, nothing of A's):\n%s", bh)
	}
}

// A Persona Link relayed to a manager is cut when its machine moves to another (Persona Link is told to try again,
// 1013, and lands at the new manager), and every relay of a revoked manager is cut.
func TestALinkRelayFollowsItsMachine(t *testing.T) {
	h, srv, _ := newBusHub(t)
	h.cfg.Machines = []MachineConfig{{ID: "laptop", Name: "laptop", Kind: "link"}}
	a := connectGuardManager(t, h, srv.URL, assistantwire.AssistantID, quiet)
	a.report(t) // the carrier is up: a link of its own is handed to it
	eventually(t, 5*time.Second, "the carrier", h.devicesUp)
	bTok := busToken(t, h, "m-0b0b0b", "m-0b0b0b")
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	relayed := make(chan *websocket.Conn, 4)
	ws, err := bus.Dial(ctx, srv.URL, bTok, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ws.CloseNow() })
	conn := bus.NewConn(ctx, ws)
	peer := rpc.New("b", conn)
	peer.OnEvent = func(string, json.RawMessage) {}
	peer.OnRequest = func(_ context.Context, method string, params json.RawMessage) (any, error) {
		if method == devicewire.MethodAccept { // B takes the relay and keeps it
			var acc devicewire.Accept
			_ = json.Unmarshal(params, &acc)
			go func() {
				c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+devicewire.RelayPath+"?id="+acc.ID,
					&websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer " + bTok}}})
				if err == nil {
					relayed <- c
				}
			}()
		}
		return map[string]any{}, nil
	}
	go func() { _ = peer.Serve(ctx, conn) }()
	cctx, cc := context.WithTimeout(context.Background(), 5*time.Second)
	defer cc()
	if err := peer.Call(cctx, busHello, map[string]any{"id": "m-0b0b0b"}, nil); err != nil {
		t.Fatal(err)
	}
	tok, _ := linkToken(t, h, "laptop")
	dialLink := func() *websocket.Conn {
		t.Helper()
		lctx, lcancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer lcancel()
		lc, _, err := websocket.Dial(lctx, "ws"+strings.TrimPrefix(srv.URL, "http")+link.Path,
			&websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer " + tok}}})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = lc.CloseNow() })
		return lc
	}
	spliced := func(lc *websocket.Conn) *websocket.Conn {
		t.Helper()
		select {
		case bside := <-relayed:
			t.Cleanup(func() { _ = bside.CloseNow() })
			wctx, wc := context.WithTimeout(context.Background(), 2*time.Second)
			defer wc()
			if err := bside.Write(wctx, websocket.MessageText, []byte("hi")); err != nil {
				t.Fatal(err)
			}
			if _, got, err := lc.Read(wctx); err != nil || string(got) != "hi" {
				t.Fatalf("the splice: %q %v", got, err)
			}
			return bside
		case <-time.After(5 * time.Second):
			t.Fatal("B never took the link")
		}
		return nil
	}
	cutWith := func(lc *websocket.Conn) websocket.StatusCode {
		t.Helper()
		rctx, rc := context.WithTimeout(context.Background(), 5*time.Second)
		defer rc()
		_, _, err := lc.Read(rctx)
		return websocket.CloseStatus(err)
	}

	// Moved away: cut, and the link comes back to the new manager.
	if _, err := h.moveMachine(context.Background(), "laptop", "m-0b0b0b"); err != nil {
		t.Fatal(err)
	}
	lc := dialLink()
	spliced(lc)
	if _, err := h.moveMachine(context.Background(), "laptop", devicesOwner); err != nil {
		t.Fatal(err)
	}
	if code := cutWith(lc); code != websocket.StatusTryAgainLater {
		t.Fatalf("after the move the link was closed with %d, want 1013", code)
	}
	dialLink()
	eventually(t, 5*time.Second, "the link at the new manager", func() bool { return strings.Contains(a.log(), devicewire.MethodAccept) })

	// Revoked: every relay it has is cut.
	if _, err := h.moveMachine(context.Background(), "laptop", "m-0b0b0b"); err != nil {
		t.Fatal(err)
	}
	lc = dialLink()
	spliced(lc)
	if _, err := h.revokeBusToken(context.Background(), "m-0b0b0b"); err != nil {
		t.Fatal(err)
	}
	if code := cutWith(lc); code != websocket.StatusTryAgainLater {
		t.Fatalf("after the revoke the link was closed with %d, want 1013", code)
	}
}

// app.call naming a machine goes only to its manager, and what is a manager's (its settings, through its
// extensions) is the caller's: B reads neither A's machine's settings nor A's mail accounts.
func TestAppCallOnlyOnItsOwn(t *testing.T) {
	settings := func(who string) func(string, json.RawMessage) (any, error) {
		return func(method string, params json.RawMessage) (any, error) {
			if method == extwire.MethodHubCall {
				var c extwire.HubCall
				_ = json.Unmarshal(params, &c)
				switch c.Method {
				case extwire.HubSettingsList:
					return map[string]any{"settings": []map[string]any{{"key": "mail.accounts", "title": who + "'s mail"}}}, nil
				case extwire.HubSettingsTool:
					return map[string]any{"text": who + "'s accounts"}, nil
				}
			}
			return map[string]any{}, nil
		}
	}
	_, _, b := pathsHub(t, settings("A"), settings("B"))
	call := func(machine string) (json.RawMessage, error) {
		params, _ := json.Marshal(map[string]any{"name": "persona_settings", "machine_id": machine, "session_id": "s",
			"arguments": map[string]any{"action": "get", "key": "mail.accounts"}})
		var out json.RawMessage
		err := b.call(t, assistantwire.MethodApp, assistantwire.AppCall{AppID: assistantwire.AppSettings, Method: assistantwire.AppTool, Params: params}, &out)
		return out, err
	}
	if out, err := call("ma"); rpcCode(err) != devicewire.CodeNoDevice {
		t.Fatalf("B on A's machine: %s %v", out, err)
	}
	if out, err := call("mb"); err != nil || !strings.Contains(string(out), "B's accounts") {
		t.Fatalf("B on its own machine: %s %v", out, err)
	}
	var list struct {
		Settings []assistantwire.Setting `json:"settings"`
	}
	if err := b.call(t, assistantwire.MethodApp, assistantwire.AppCall{AppID: assistantwire.AppSettings, Method: assistantwire.SettingsList}, &list); err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(list.Settings, func(s assistantwire.Setting) bool { return s.Title == "B's mail" }) ||
		slices.ContainsFunc(list.Settings, func(s assistantwire.Setting) bool { return s.Title == "A's mail" }) {
		t.Fatalf("B's settings list: %+v", list.Settings)
	}
}

// An extension's push (hub.push_localized / hub.push_background) names only the manager's own machine, session and
// pending card: not A's card, nor a card id nobody has pending.
func TestAnExtensionPushOnlyAboutItsOwn(t *testing.T) {
	h, a, b := pathsHub(t, quiet, quiet)
	if err := a.peer.Notify(assistantwire.EventFleetInteraction, assistantwire.FleetInteraction{Interaction: proto.Interaction{ID: "ix_a", MachineID: "ma", SessionID: "s3", Kind: proto.IxPermission}}); err != nil {
		t.Fatal(err)
	}
	if err := b.peer.Notify(assistantwire.EventFleetInteraction, assistantwire.FleetInteraction{Interaction: proto.Interaction{ID: "ix_b", MachineID: "mb", SessionID: "b1", Kind: proto.IxPermission}}); err != nil {
		t.Fatal(err)
	}
	eventually(t, 5*time.Second, "both cards", func() bool { return h.interactionOwner("ix_a") != "" && h.interactionOwner("ix_b") != "" })
	push := func(payload map[string]any) error {
		params, _ := json.Marshal(map[string]any{"payloads": map[string]any{"zh-Hans": payload}})
		return b.call(t, assistantwire.MethodApp, assistantwire.AppCall{AppID: extwire.AppHub, Method: extwire.HubPushLocalized, Params: params}, nil)
	}
	for _, p := range []map[string]any{{"machine_id": "ma"}, {"session_id": "s3"}, {"interaction_id": "ix_a"}, {"interaction_id": "ix_nobodys"}} {
		if err := push(p); rpcCode(err) != devicewire.CodeNoDevice {
			t.Fatalf("B's push %v: %v", p, err)
		}
	}
	if err := push(map[string]any{"machine_id": "mb", "session_id": "b1", "interaction_id": "ix_b"}); err != nil {
		t.Fatalf("B's push about its own: %v", err)
	}
}

// What the hub tells or asks a machine's manager goes to that manager: the mutes (each its own machines'), the
// node updates, its claude-dirs (managerHTTP), a connector's pairing; and a machine's extras come from it only.
func TestTheHubAsksTheMachinesManager(t *testing.T) {
	h, a, b := pathsHub(t, quiet, func(method string, _ json.RawMessage) (any, error) {
		if method == assistantwire.MethodHTTP {
			return map[string]any{"status": 200, "body": []byte(`{"dirs":[],"blocked":["b-blocked"]}`)}, nil
		}
		return map[string]any{}, nil
	})
	aHeard, bHeard := heard(a, assistantwire.EventMutes), heard(b, assistantwire.EventMutes)
	h.mutesMu.Lock()
	h.mutes = &Pins{Spaces: []string{"ma/sa", "mb/sb"}, Sessions: []string{"ma/s3", "mb/b1"}}
	h.mutesMu.Unlock()
	h.mutesChanged()
	eventually(t, 5*time.Second, "the mutes", func() bool {
		return strings.Contains(aHeard(), "ma/s3") && strings.Contains(bHeard(), "mb/b1")
	})
	if ah, bh := aHeard(), bHeard(); strings.Contains(ah, "mb/") || strings.Contains(bh, "ma/") {
		t.Fatalf("mutes: A heard\n%s\nB heard\n%s", ah, bh)
	}
	h.updateNodes(context.Background())
	if !strings.Contains(a.log(), devicewire.MethodUpdateNodes) || !strings.Contains(b.log(), devicewire.MethodUpdateNodes) {
		t.Fatal("each manager redeploys its own machines")
	}
	if got := h.localMachine("mb").claudeDirsState(context.Background()).Blocked; !slices.Equal(got, []string{"b-blocked"}) ||
		strings.Contains(a.log(), "claude-dirs") {
		t.Fatalf("mb's claude dirs: %v", got)
	}
	addCfgMachines(h, MachineConfig{ID: "svc", Name: "svc", Kind: kindService})
	if _, err := h.moveMachine(context.Background(), "svc", "m-0b0b0b"); err != nil {
		t.Fatal(err)
	}
	_, _, _ = h.pairConnector(context.Background(), "link", "svc", "addr", "")
	if !strings.Contains(b.log(), devicewire.MethodPairConnector) || strings.Contains(a.log(), devicewire.MethodPairConnector) {
		t.Fatal("svc's connector was not paired by its manager")
	}
	h.keepExtReport("m-0b0b0b", &extwire.Report{Machines: map[string]extwire.MachineExtras{"ma": {}, "mb": {}}})
	if x := h.extReport().Machines; len(x) != 1 {
		t.Fatalf("extras: %v", x)
	}
	if _, ok := h.extReport().Machines["mb"]; !ok {
		t.Fatal("mb's extras from its manager")
	}
}

// A device.call checked before its machine moved does not reach the new manager's node; the request keys of one
// manager never end another's request; a kept move of a machine hub.json no longer has is the carrier's.
func TestOwnershipRacesAndLeftovers(t *testing.T) {
	h, a, b := pathsHub(t, answerWho("a"), answerWho("b"))
	var who map[string]any
	err := h.localMachine("ma").call(withCaller(context.Background(), "m-0b0b0b"), "panes.list", map[string]any{}, &who)
	if rpcCode(err) != devicewire.CodeNoDevice || strings.Contains(a.log(), "panes.list") {
		t.Fatalf("a call B checked before ma was A's: %v %v", who, err)
	}
	if err := h.localMachine("mb").call(withCaller(context.Background(), "m-0b0b0b"), "panes.list", map[string]any{}, &who); err != nil || who["who"] != "b" {
		t.Fatalf("B on its own machine: %v %v", who, err)
	}

	ca, cb := &busConn{ident: db.BusIdentity{ID: assistantwire.AssistantID}}, &busConn{ident: db.BusIdentity{ID: "m-0b0b0b"}}
	ctx, done := h.requestScope(context.Background(), requestKey(ca, "r1"))
	defer done()
	h.requestEnded(requestKey(cb, "r1"), true)
	if ctx.Err() != nil {
		t.Fatal("B's request_end ended A's request")
	}
	h.requestEnded(requestKey(ca, "r1"), true)
	if ctx.Err() == nil {
		t.Fatal("A's request_end did not end its request")
	}

	h.devices.mu.Lock()
	h.devices.assign["gone"] = "m-0b0b0b"
	h.devices.mu.Unlock()
	if o := h.machineOwner("gone"); o != devicesOwner {
		t.Fatalf("a machine hub.json no longer has is %s's", o)
	}
	_ = b
}

// Each manager's hello carries the mutes of its own machines only, as its fleet.mutes do.
func TestEachManagersHelloHasItsOwnMutes(t *testing.T) {
	h, _, _ := pathsHub(t, quiet, quiet)
	h.mutesMu.Lock()
	h.mutes = &Pins{Spaces: []string{"ma/sa", "mb/sb", "mz/gone"}, Sessions: []string{"ma/s3", "mb/b1"}, Allowed: []string{"ma/x", "mb/y"}}
	h.mutesMu.Unlock()
	for id, want := range map[string]assistantwire.Mutes{ // mz is nobody's: the carrier's (the built-in manager's)
		assistantwire.AssistantID: {Spaces: []string{"ma/sa", "mz/gone"}, Sessions: []string{"ma/s3"}, Allowed: []string{"ma/x"}},
		"m-0b0b0b":                {Spaces: []string{"mb/sb"}, Sessions: []string{"mb/b1"}, Allowed: []string{"mb/y"}},
	} {
		got := h.assistantHelloFor(context.Background(), id).Mutes
		if got == nil || !slices.Equal(got.Spaces, want.Spaces) || !slices.Equal(got.Sessions, want.Sessions) || !slices.Equal(got.Allowed, want.Allowed) {
			t.Errorf("%s's hello: %+v, want %+v", id, got, want)
		}
	}
}

// Each manager's hello places it on its own machines (#5760): the machines it owns, the hub's default machine when it
// owns that one, else its first own one, and only its own machines' launchers. A machine moved from one manager to
// the other moves in both hellos, and each manager is told (manager.updated).
func TestEachManagersHelloPlacesItOnItsOwnMachines(t *testing.T) {
	h, a, b := pathsHub(t, quiet, quiet)
	d := &h.devices
	d.mu.Lock()
	for i := range d.entries {
		d.entries[i].Launchers = []assistantwire.ConfiguredLauncher{{Launcher: proto.Launcher{Name: "work-" + d.entries[i].ID}}}
	}
	d.mu.Unlock()
	for _, m := range h.machinesNow() {
		m.mu.Lock()
		m.cfg.Launchers = []assistantwire.ConfiguredLauncher{{Launcher: proto.Launcher{Name: "work-" + m.cfg.ID}}}
		m.mu.Unlock()
	}
	place := func(id string) assistantwire.Hello { return h.assistantHelloFor(context.Background(), id) }
	check := func(id string, machines []string, machine string) {
		t.Helper()
		got := place(id)
		var launchers []string
		for mid := range got.Models.Launchers {
			launchers = append(launchers, mid)
		}
		slices.Sort(launchers)
		if !slices.Equal(got.Assistant.Machines, machines) || got.Assistant.MachineID != machine || !slices.Equal(launchers, machines) {
			t.Errorf("%s's hello: machines %v, default %q, launchers of %v; want %v, %q", id, got.Assistant.Machines,
				got.Assistant.MachineID, launchers, machines, machine)
		}
	}
	check(assistantwire.AssistantID, []string{"ma"}, "ma")
	check("m-0b0b0b", []string{"mb"}, "mb")

	// The setup's own updates (its move, the reports) are sent and done first: what is heard next is the move's.
	eventually(t, 5*time.Second, "the setup's updates", func() bool {
		return !h.assistantOf(assistantwire.AssistantID).updating.Load() && !h.assistantOf("m-0b0b0b").updating.Load()
	})
	check(assistantwire.AssistantID, []string{"ma"}, "ma") // the last answer each was sent, as it is now
	check("m-0b0b0b", []string{"mb"}, "mb")
	heardA, heardB := heard(a, assistantwire.EventUpdated), heard(b, assistantwire.EventUpdated)
	if _, err := h.moveMachine(context.Background(), "mb", assistantwire.AssistantID); err != nil {
		t.Fatal(err)
	}
	eventually(t, 5*time.Second, "both told", func() bool { return heardA() != "" && heardB() != "" })
	if !strings.Contains(heardA(), `"mb"`) || strings.Contains(heardB(), `"mb"`) {
		t.Errorf("told A %s, B %s", heardA(), heardB())
	}
	check(assistantwire.AssistantID, []string{"ma", "mb"}, "ma")
	check("m-0b0b0b", []string{}, "")
}
